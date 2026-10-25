package serve

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/e-aleixandre/moa/pkg/voice"
)

// One bounded consumer per enabled registry. A stuck Handler can strand this
// worker, but cannot strand media, teardown, or one worker per call.
type audioDiagnosticSink struct {
	queue   chan audioDiagnosticRecord
	done    chan struct{}
	dropped atomic.Int64
	next    atomic.Int64
}
type audioDiagnosticRecord struct {
	id, socket, elapsed, dropped  int64
	occupancy                     int64
	final, writerComplete, client bool
	server                        voice.AudioSnapshot
	report                        audioClientReport
	source, seq                   int64
	observedUS                    int64
	trace                         [8]int64
}

func newAudioDiagnosticSink(ctx context.Context) *audioDiagnosticSink {
	s := &audioDiagnosticSink{queue: make(chan audioDiagnosticRecord, 8), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		for {
			select {
			case <-ctx.Done():
				return
			case r := <-s.queue:
				s.log(r)
			}
		}
	}()
	return s
}
func (s *audioDiagnosticSink) offer(r audioDiagnosticRecord) {
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
	attrs := []slog.Attr{slog.Int("v", 1), slog.String("provider", "chatgpt-plan"), slog.Int64("diag", r.id), slog.Int64("socket_index", r.socket), slog.Int64("server_elapsed_ms", r.elapsed), slog.Int64("diag_log_dropped", r.dropped)}
	message := "voice audio diagnostics"
	if r.source != 0 {
		message = "voice state trace"
		attrs = append(attrs, slog.Int64("source", r.source), slog.Int64("source_seq", r.seq), slog.Int64("server_elapsed_us", r.observedUS), slog.Any("trace", r.trace))
	} else if r.client {
		message = "voice audio client metrics"
		for i, n := range audioClientNumbers {
			if i != 0 && r.report.present[i] {
				attrs = append(attrs, slog.Int64(n.name, r.report.values[i]))
			}
		}
		for i, name := range audioClientGroups {
			if !r.report.groups[i].present {
				continue
			}
			g := r.report.groups[i]
			var children []slog.Attr
			for j, key := range audioGroupKeys(i) {
				if g.has[j] {
					children = append(children, slog.Int64(key, g.values[j]))
				}
			}
			attrs = append(attrs, slog.Attr{Key: name, Value: slog.GroupValue(children...)})
		}
		attrs = append(attrs, slog.Int64("source", r.report.values[1]), slog.Any("trace", r.report.trace[:r.report.traceN]), slog.Int64("server_elapsed_us", r.observedUS))
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
	lastEvent time.Time
}

// Sources have separate clocks/sequences. Client times are relative to their
// activation, not synchronized with the server or an audio sample timeline.
func (d *audioCallDiagnostics) traceEvent(source, kind, cause, before, after, aux int64) {
	seq := d.traceSeq[source-3].Add(1)
	us := time.Since(d.start).Microseconds()
	d.sink.offer(audioDiagnosticRecord{id: d.id, socket: d.sockets.Load(), elapsed: us / 1000, observedUS: us, source: source, seq: seq, trace: [8]int64{seq, us, kind, cause, 0, before, after, aux}})
}

func (c *voiceCall) traceServerState(v any, origin, admission int64) {
	d := c.audioDiagnostics
	if d == nil {
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
	complete := false
	var occupancy int64
	if cl != nil {
		occupancy = cl.diagOccupancy.Load()
		select {
		case <-cl.done:
			complete = true
		default:
		}
	}
	d.sink.offer(audioDiagnosticRecord{id: d.id, socket: d.sockets.Load(), elapsed: time.Since(d.start).Milliseconds(), final: final, writerComplete: complete, occupancy: occupancy, server: d.metrics.Snapshot()})
}
func (d *audioCallDiagnostics) event(size int, client bool) {
	now := time.Now()
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
func (cl *voiceClient) audioWriteStart(d *audioCallDiagnostics, f voiceFrame, now int64) {
	d.metrics.Add(voice.WriteAttempts, 1)
	d.metrics.Observe(voice.WriteBytes, f.audio)
	d.metrics.Observe(voice.QueueAgeUS, (now-f.enqueuedNS)/1000)
	if cl.diagLastWrite != 0 {
		d.metrics.Observe(voice.WriteGapUS, (now-cl.diagLastWrite)/1000)
	}
	cl.diagLastWrite = now
}
func (cl *voiceClient) audioWriteEnd(d *audioCallDiagnostics, f voiceFrame, start, end int64, ok bool) {
	d.metrics.Observe(voice.WriteUS, (end-start)/1000)
	if ok {
		d.metrics.Add(voice.WriteOK, 1)
		d.metrics.Add(voice.WrittenBytes, f.audio)
	} else {
		d.metrics.Add(voice.WriteError, 1)
	}
}

// Report storage contains numbers and presence bits only, never client strings
// or JSON. Bounds are protocol limits, not limits on the media pipeline.
var audioClientNumbers = [...]struct {
	name string
	max  int64
}{
	{"v", 1}, {"client", 2}, {"seq", 2147483647}, {"window_start_ms", 86400000}, {"window_ms", 60000},
	{"rx_frames", 1000000}, {"rx_bytes", 67108864}, {"rx_odd_frames", 1000000},
	{"scheduled_frames", 1000000}, {"scheduled_samples", 33554432}, {"renderer_generation", 2147483647},
	{"main_hitches", 1000000}, {"restarts", 1000000}, {"interrupts", 1000000},
	{"pending_drop_frames", 1000000}, {"pending_drop_bytes", 67108864}, {"playback_drop_frames", 1000000}, {"playback_drop_samples", 33554432}, {"reports_skipped", 1000000},
	{"queued_samples", 33554432}, {"queued_samples_min", 33554432}, {"queued_samples_max", 33554432}, {"render_progress_samples", 33554432}, {"render_observations", 1000000}, {"render_invalid", 1000000},
	{"trace_dropped", 2147483647},
}
var audioClientGroups = [...]string{"rx_dt_us", "schedule_us", "main_timer_dt_us", "completion_delay_us", "margin"}
var audioUSKeys = []string{"n", "min", "max", "sum"}
var audioMarginKeys = []string{"n", "invalid_n", "last_valid", "invalid_reason", "min_us", "max_us", "last_us"}

func audioGroupKeys(i int) []string {
	if i == 4 {
		return audioMarginKeys
	}
	return audioUSKeys
}

type audioClientGroup struct {
	present bool
	values  [7]int64
	has     [7]bool
}
type audioClientReport struct {
	values       [len(audioClientNumbers)]int64
	present      [len(audioClientNumbers)]bool
	groups       [len(audioClientGroups)]audioClientGroup
	trace        [24][8]int64
	traceN       int
	tracePresent bool
}

// Rows contain sequence, monotonic microseconds, kind, cause, generation,
// before, after, aux. Native uses kinds 1..8 and sample depths; UI uses 20..22
// and phase enums. No unvalidated strings survive parsing.
func audioTrace(dec *json.Decoder, r *audioClientReport) bool {
	if r.tracePresent {
		return false
	}
	r.tracePresent = true
	t, err := dec.Token()
	if err != nil || t != json.Delim('[') {
		return false
	}
	for dec.More() {
		if r.traceN == len(r.trace) {
			return false
		}
		t, err = dec.Token()
		if err != nil || t != json.Delim('[') {
			return false
		}
		for j := range r.trace[r.traceN] {
			v, ok := audioInteger(dec)
			if !ok {
				return false
			}
			r.trace[r.traceN][j] = v
		}
		t, err = dec.Token()
		if err != nil || t != json.Delim(']') {
			return false
		}
		r.traceN++
	}
	t, err = dec.Token()
	return err == nil && t == json.Delim(']')
}

func audioInteger(dec *json.Decoder) (int64, bool) {
	t, err := dec.Token()
	if err != nil {
		return 0, false
	}
	n, ok := t.(json.Number)
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseInt(string(n), 10, 64)
	return v, err == nil
}
func audioObject(dec *json.Decoder, field func(string) bool) bool {
	t, err := dec.Token()
	if err != nil || t != json.Delim('{') {
		return false
	}
	for dec.More() {
		t, err = dec.Token()
		key, ok := t.(string)
		if err != nil || !ok || !field(key) {
			return false
		}
	}
	t, err = dec.Token()
	return err == nil && t == json.Delim('}')
}
func parseAudioClientReport(data []byte) (r audioClientReport, reason voice.AudioCounter) {
	reason = voice.ClientSchema
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	typed := false
	ok := audioObject(dec, func(key string) bool {
		if key == "trace" {
			return audioTrace(dec, &r)
		}
		if key == "type" {
			if typed {
				return false
			}
			typed = true
			t, err := dec.Token()
			return err == nil && t == "audio_metrics"
		}
		for i, n := range audioClientNumbers {
			if key == n.name {
				if r.present[i] {
					return false
				}
				v, ok := audioInteger(dec)
				r.present[i] = ok
				r.values[i] = v
				return ok
			}
		}
		for i, name := range audioClientGroups {
			if key == name {
				g := &r.groups[i]
				if g.present {
					return false
				}
				g.present = true
				return audioObject(dec, func(key string) bool {
					for j, k := range audioGroupKeys(i) {
						if key == k {
							if g.has[j] {
								return false
							}
							v, ok := audioInteger(dec)
							g.has[j] = ok
							g.values[j] = v
							return ok
						}
					}
					return false
				})
			}
		}
		return false
	})
	if !ok || !typed {
		return
	}
	if _, err := dec.Token(); err != io.EOF {
		return
	}
	for i := 0; i < 5; i++ {
		if !r.present[i] {
			return
		}
	}
	reason = voice.ClientRange
	for i, n := range audioClientNumbers {
		if r.present[i] && (r.values[i] < 0 || r.values[i] > n.max) {
			return
		}
	}
	if r.values[0] != 1 || r.values[1] < 1 || r.values[2] < 1 || r.values[4] < 1 || r.values[3]+r.values[4] > 86400000 {
		return
	}
	for i, row := range r.trace[:r.traceN] {
		for j, v := range row {
			max := int64(2147483647)
			if j == 1 {
				max = (r.values[3] + r.values[4]) * 1000
			}
			if v < 0 || v > max {
				return
			}
		}
		if row[0] == 0 || (i > 0 && (row[0] <= r.trace[i-1][0] || row[1] < r.trace[i-1][1])) {
			return
		}
		if r.values[1] == 1 {
			if row[2] < 1 || row[2] > 8 || row[3] != 0 || row[5] > 33554432 || row[6] > 33554432 || row[7] > 1000000 {
				return
			}
		} else if row[2] < 20 || row[2] > 22 || row[3] < 1 || row[3] > 7 || row[5] > 8 || row[6] > 8 || row[7] != 0 {
			return
		}
	}
	if r.present[7] && (!r.present[5] || r.values[7] > r.values[5]) {
		return
	}
	for i, g := range r.groups {
		if !g.present {
			continue
		}
		if !g.has[0] || g.values[0] < 0 || g.values[0] > 1000000 {
			return
		}
		n := g.values[0]
		if i != 4 {
			if n == 0 {
				if g.has[1] || g.has[2] || g.has[3] {
					return
				}
			} else {
				for j := 1; j < 4; j++ {
					if !g.has[j] || g.values[j] < 0 {
						return
					}
				}
				lo, hi, sum := g.values[1], g.values[2], g.values[3]
				if lo > hi || hi > 3600000000 || sum > 3600000000000000 || sum < n*lo || sum > n*hi {
					return
				}
			}
		} else {
			for j := 1; j < 4; j++ {
				if !g.has[j] || g.values[j] < 0 {
					return
				}
			}
			if g.values[1] > 1000000 || g.values[2] > 1 || g.values[3] > 6 {
				return
			}
			if n == 0 {
				if g.has[4] || g.has[5] || g.has[6] {
					return
				}
			} else {
				for j := 4; j < 7; j++ {
					if !g.has[j] || g.values[j] < -60000000 || g.values[j] > 60000000 {
						return
					}
				}
				if g.values[4] > g.values[6] || g.values[6] > g.values[5] {
					return
				}
			}
			if g.values[2] == 1 && (n == 0 || g.values[3] != 0) {
				return
			}
		}
	}
	return r, 0
}
func (c *voiceCall) receiveAudioMetrics(cl *voiceClient, data []byte, now time.Time) {
	d := c.audioDiagnostics
	if d == nil {
		return
	}
	reject := func(k voice.AudioCounter) { d.metrics.Add(k, 1) }
	select {
	case <-c.endCh:
		reject(voice.ClientStale)
		return
	default:
	}
	if c.ending.Load() || d.current.Load() != cl {
		reject(voice.ClientStale)
		return
	}
	if len(data) > 4096 {
		reject(voice.ClientSize)
		return
	}
	var header struct {
		Client int `json:"client"`
	}
	source := 0
	if json.Unmarshal(data, &header) == nil && header.Client >= 1 && header.Client <= 2 {
		source = header.Client
	}
	gate := &cl.diagReports[source]
	if !gate.last.IsZero() && now.Sub(gate.last) < 500*time.Millisecond {
		reject(voice.ClientRate)
		return
	}
	// Limit parse attempts as well as accepted reports.
	gate.last = now
	r, reason := parseAudioClientReport(data)
	if reason != 0 {
		reject(reason)
		return
	}
	seq, start, end := r.values[2], r.values[3], r.values[3]+r.values[4]
	if seq <= gate.seq || start < gate.windowEnd || (r.traceN > 0 && (r.trace[0][0] <= gate.traceSeq || r.trace[0][1] < gate.traceUS)) {
		reject(voice.ClientOrder)
		return
	}
	if c.ending.Load() || d.current.Load() != cl {
		reject(voice.ClientStale)
		return
	}
	gate.seq = seq
	gate.windowEnd = end
	if r.traceN > 0 {
		gate.traceSeq = r.trace[r.traceN-1][0]
		gate.traceUS = r.trace[r.traceN-1][1]
	}
	d.metrics.Add(voice.ClientAccepted, 1)
	us := time.Since(d.start).Microseconds()
	d.sink.offer(audioDiagnosticRecord{id: d.id, socket: cl.diagSocket, elapsed: us / 1000, observedUS: us, client: true, report: r})
}

// Recognize the reserved type before validating the rest, so a malformed tail
// or duplicate type cannot turn a diagnostic into a mute/liveness/end control.
func isAudioMetricsReport(data []byte) bool {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	t, err := dec.Token()
	if err != nil || t != json.Delim('{') {
		return false
	}
	for dec.More() {
		t, err = dec.Token()
		if err != nil {
			return false
		}
		if t == "type" {
			v, err := dec.Token()
			if err != nil {
				return false
			}
			if v == "audio_metrics" {
				return true
			}
		} else {
			var ignored json.RawMessage
			if dec.Decode(&ignored) != nil {
				return false
			}
		}
	}
	return false
}

type audioReportGate struct {
	last                              time.Time
	seq, windowEnd, traceSeq, traceUS int64
}
