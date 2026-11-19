package serve

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/voice"
)

// One bounded consumer per registry. A stuck Handler can strand this
// worker, but cannot strand media, teardown, or the independent control poll.
// An already running slog Handler may finish after OFF; queued old epochs cannot.
type audioDiagnosticSink struct {
	gate        *voice.AudioDiagnosticGate
	controlDone chan struct{}
	queue       chan audioDiagnosticRecord
	done        chan struct{}
	dropped     atomic.Int64
	next        atomic.Int64
}
type audioDiagnosticRecord struct {
	id, socket, elapsed, dropped int64
	occupancy                    int64
	final, writerComplete        bool
	epoch                        uint64
	server                       voice.AudioSnapshot
	source, seq                  int64
	observedUS                   int64
	trace                        [8]int64
}

func newAudioDiagnosticSink(ctx context.Context, gate *voice.AudioDiagnosticGate) *audioDiagnosticSink {
	s := &audioDiagnosticSink{gate: gate, queue: make(chan audioDiagnosticRecord, 8), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		for {
			select {
			case <-ctx.Done():
				return
			case r := <-s.queue:
				if s.epoch() == r.epoch {
					s.log(r)
				}
			}
		}
	}()
	return s
}
func (s *audioDiagnosticSink) epoch() uint64 {
	if s.gate == nil {
		return 1
	}
	return s.gate.Epoch()
}
func (s *audioDiagnosticSink) offer(r audioDiagnosticRecord) {
	if r.epoch == 0 || s.epoch() != r.epoch {
		return
	}
	r.dropped = s.dropped.Load()
	select {
	case s.queue <- r:
	default:
		s.dropped.Add(1)
	}
}
func aggregateAttr(name string, a voice.AudioAggregate) slog.Attr {
	if a.N == 0 {
		return slog.Group(name, "n", int64(0))
	}
	return slog.Group(name, "n", a.N, "min", a.Min, "max", a.Max, "sum", a.Sum)
}
func (s *audioDiagnosticSink) log(r audioDiagnosticRecord) {
	attrs := []slog.Attr{slog.Int("v", 1), slog.Uint64("epoch", r.epoch), slog.Bool("cumulative", true), slog.String("provider", "chatgpt-plan"), slog.Int64("diag", r.id), slog.Int64("socket_index", r.socket), slog.Int64("server_elapsed_ms", r.elapsed), slog.Int64("diag_log_dropped", r.dropped)}
	message := "voice audio diagnostics"
	if r.source != 0 {
		message = "voice state trace"
		attrs = append(attrs, slog.Int64("source", r.source), slog.Int64("source_seq", r.seq), slog.Int64("server_elapsed_us", r.observedUS), slog.Any("trace", r.trace))
	} else {
		attrs = append(attrs, slog.Bool("final", r.final), slog.Bool("writer_complete", r.writerComplete), slog.Int64("audio_inflight_bytes", r.occupancy))
		for i, name := range voice.AudioCounterNames {
			attrs = append(attrs, slog.Int64(name, r.server.Counts[i]))
		}
		for i, name := range voice.AudioStatNames {
			attrs = append(attrs, aggregateAttr(name, r.server.Stats[i]))
		}
	}
	slog.LogAttrs(context.Background(), slog.LevelInfo, message, attrs...)
}

type audioCallDiagnostics struct {
	metrics  *voice.AudioDiagnostics
	sink     *audioDiagnosticSink
	id       int64
	start    time.Time
	current  atomic.Pointer[voiceClient]
	sockets  atomic.Int64
	traceSeq [2]atomic.Int64 // provider (3), server control (4)
	// Controller-owned; admission and writer gaps are client-owned below.
	lastEvent  time.Time
	eventEpoch uint64
}

// Counters and clocks are call-cumulative; observation gaps never cross epochs.
func (d *audioCallDiagnostics) traceEvent(source, kind, cause, before, after, aux int64) {
	epoch := d.metrics.Epoch()
	if epoch == 0 {
		return
	}
	seq := d.traceSeq[source-3].Add(1)
	us := time.Since(d.start).Microseconds()
	d.sink.offer(audioDiagnosticRecord{epoch: epoch, id: d.id, socket: d.sockets.Load(), elapsed: us / 1000, observedUS: us, source: source, seq: seq, trace: [8]int64{seq, us, kind, cause, 0, before, after, aux}})
}

func (c *voiceCall) traceServerState(v any, origin, admission int64) {
	d := c.audioDiagnostics
	if d == nil || d.metrics.Epoch() == 0 {
		return
	}
	m, ok := v.(map[string]any)
	if !ok || m["type"] != "state" {
		return
	}
	var state int64
	switch m["state"] {
	case "listening":
		state = 3
	case "speaking":
		state = 4
	case "consulting":
		state = 5
	default:
		return
	}
	var interrupt int64
	if m["interrupt"] == true {
		interrupt = 1
	}
	d.traceEvent(4, 30, origin, state, interrupt, admission)
}

func (d *audioCallDiagnostics) elapsed() int64 { return time.Since(d.start).Nanoseconds() }
func (d *audioCallDiagnostics) snapshot(final bool, cl *voiceClient) {
	epoch := d.metrics.Epoch()
	if epoch == 0 {
		return
	}
	complete := false
	var occupancy int64
	if cl != nil {
		cl.mu.Lock()
		occupancy = cl.audio
		cl.mu.Unlock()
		select {
		case <-cl.done:
			complete = true
		default:
		}
	}
	d.sink.offer(audioDiagnosticRecord{epoch: epoch, id: d.id, socket: d.sockets.Load(), elapsed: time.Since(d.start).Milliseconds(), final: final, writerComplete: complete, occupancy: occupancy, server: d.metrics.Snapshot()})
}
func (d *audioCallDiagnostics) event(size int, client bool) {
	epoch := d.metrics.Epoch()
	if epoch == 0 {
		return
	}
	now := time.Now()
	if d.metrics.Epoch() != epoch {
		return
	}
	if d.eventEpoch != epoch {
		d.eventEpoch = epoch
		d.lastEvent = time.Time{}
	}
	d.metrics.Add(voice.EventFrames, 1)
	d.metrics.Add(voice.EventBytes, int64(size))
	d.metrics.Add(voice.EventSamples, int64(size/2))
	if !client {
		d.metrics.Add(voice.EventNoClient, 1)
	}
	if !d.lastEvent.IsZero() {
		d.metrics.Observe(voice.EventGapUS, now.Sub(d.lastEvent).Microseconds())
	}
	d.lastEvent = now
}
func (cl *voiceClient) audioWriteStart(d *audioCallDiagnostics, f voiceFrame, epoch uint64, now int64) {
	if epoch == 0 || d.metrics.Epoch() != epoch {
		return
	}
	d.metrics.Add(voice.WriteAttempts, 1)
	d.metrics.Observe(voice.WriteBytes, int64(len(f.data)))
	if cl.diagWriteEpoch != epoch {
		cl.diagWriteEpoch = epoch
		cl.diagLastWrite = 0
	}
	if meta := f.diagnostics; meta != nil && meta.epoch == epoch && meta.enqueuedNS != 0 {
		d.metrics.Observe(voice.QueueAgeUS, (now-meta.enqueuedNS)/1000)
	}
	if cl.diagLastWrite != 0 {
		d.metrics.Observe(voice.WriteGapUS, (now-cl.diagLastWrite)/1000)
	}
	cl.diagLastWrite = now
}
func (cl *voiceClient) audioWriteEnd(d *audioCallDiagnostics, f voiceFrame, start, end int64, ok bool) {
	if d.metrics.Epoch() == 0 || cl.diagWriteEpoch != d.metrics.Epoch() {
		return
	}
	d.metrics.Observe(voice.WriteUS, (end-start)/1000)
	if ok {
		d.metrics.Add(voice.WriteOK, 1)
		d.metrics.Add(voice.WrittenBytes, int64(len(f.data)))
	} else {
		d.metrics.Add(voice.WriteError, 1)
	}
}

// The one control ticker also requests periodic snapshots: OFF calls own no
// diagnostic timer. Filesystem latency cannot block media or the log consumer.
func (r *voiceCallRegistry) startAudioDiagnostics() {
	gate := &voice.AudioDiagnosticGate{}
	s := newAudioDiagnosticSink(r.ctx, gate)
	r.audioDiagnostics = s
	path := core.ConfigSubdir("voice-audio-diagnostics")
	poll := func() {
		info, err := os.Stat(path)
		gate.SetEnabled(err == nil && info.Mode().IsRegular())
	}
	poll()
	s.controlDone = make(chan struct{})
	go func() {
		defer close(s.controlDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-r.ctx.Done():
				return
			case <-ticker.C:
				poll()
				if gate.Epoch() == 0 {
					continue
				}
				r.mu.Lock()
				for _, c := range r.calls {
					if d := c.audioDiagnostics; d != nil {
						d.snapshot(false, d.current.Load())
					}
				}
				r.mu.Unlock()
			}
		}
	}()
}
