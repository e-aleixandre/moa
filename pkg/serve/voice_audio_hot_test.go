package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/voice"
)

func audioSentinel(t *testing.T, on bool) {
	t.Helper()
	path := core.ConfigSubdir("voice-audio-diagnostics")
	if on {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("CANARY-CONTENT: contents must not be read"), 0600); err != nil {
			t.Fatal(err)
		}
	} else if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestAudioDiagnosticsHotSentinelExistingRoute(t *testing.T) {
	t.Setenv("MOA_VOICE_AUDIO_DIAGNOSTICS", "")
	f := vpNewFixture(t, "chatgpt-plan")
	client := f.call()
	state := client.await("state", func(v map[string]any) bool { return v["type"] == "state" })
	if state["audio_diagnostics"] != nil {
		t.Fatal("server-only diagnostics activated client")
	}
	c, _, _ := f.mgr.voiceCalls[0].lookup(client.c.ID)
	if c.audioDiagnostics == nil {
		t.Fatal("OFF-born call has no stable hot diagnostic handle")
	}
	d := c.audioDiagnostics
	_, peers, _ := f.up.snapshot()
	pcmFrames := func() [][]byte {
		var out [][]byte
		for _, fr := range client.snapshot() {
			if fr.kind == websocket.MessageBinary {
				out = append(out, fr.data)
			}
		}
		return out
	}
	send := func(n int) {
		peers[0].emit(t, map[string]any{"type": "response.done", "response": map[string]any{"status": "cancelled", "error": "CANARY-CONTENT"}})
		peers[0].audio(t, bytes.Repeat(vpPCM(), 3))
		pollUntil(t, vpDeadline, "PCM barrier", func() bool { return len(pcmFrames()) == n })
	}
	send(3)
	if d.metrics.Snapshot() != (voice.AudioSnapshot{}) || d.traceSeq[0].Load() != 0 {
		t.Fatal("OFF observed diagnostics")
	}
	audioSentinel(t, true)
	// Poll is global and independent of media; wait for its periodic snapshot.
	pollUntil(t, 3*time.Second, "hot ON snapshot", func() bool { return strings.Contains(f.log.String(), "final=false") })
	send(6)
	pollUntil(t, vpDeadline, "hot ON metrics and drained writes", func() bool {
		got := d.metrics.Snapshot()
		return got.Counts[voice.WriteOK] == 3 && got.Stats[voice.OccupancyBytes].N == 6 && d.traceSeq[0].Load() == 1
	})
	before := d.metrics.Snapshot()
	audioSentinel(t, false)
	pollUntil(t, 3*time.Second, "hot OFF", func() bool { return d.metrics.Epoch() == 0 })
	send(9)
	if got := d.metrics.Snapshot(); got != before {
		t.Fatalf("OFF counters changed: before=%+v after=%+v", before, got)
	}
	audioSentinel(t, true)
	pollUntil(t, 3*time.Second, "second hot ON", func() bool { return d.metrics.Epoch() != 0 })
	send(12)
	pollUntil(t, vpDeadline, "second epoch", func() bool { return d.metrics.Snapshot().Counts[voice.WriteOK] == 6 })
	got := d.metrics.Snapshot()
	if got.Counts[voice.RTPGapSlots] != 0 || got.Stats[voice.EventGapUS].Max > 1000000 || got.Stats[voice.RTPArrivalUS].Max > 1000000 || got.Stats[voice.WriteGapUS].Max > 1000000 || got.Stats[voice.AdmissionGapUS].Max > 1000000 {
		t.Fatal("OFF interval fabricated a gap", got)
	}
	for _, chunk := range pcmFrames() {
		if len(chunk) != 960 {
			t.Fatal("hot toggle changed PCM framing")
		}
	}
	client.hangup()
	if strings.Contains(f.log.String(), "CANARY-CONTENT") || strings.Contains(f.log.String(), "voice audio client metrics") {
		t.Fatal("client/raw diagnostics retained")
	}
}

func TestAudioDiagnosticsServerOnlyProtocolMatchesBase(t *testing.T) {
	t.Setenv("MOA_VOICE_AUDIO_DIAGNOSTICS", "1")
	f := vpNewFixture(t, "chatgpt-plan")
	audioSentinel(t, true)
	pollUntil(t, 3*time.Second, "server-only call starts ON", func() bool { return f.mgr.voiceCalls[0].audioDiagnostics.epoch() != 0 })
	client := f.call()
	state := client.await("state", func(v map[string]any) bool { return v["type"] == "state" })
	if state["audio_diagnostics"] != nil {
		t.Error("server must never activate client diagnostics")
	}
	client.send(map[string]any{"type": "audio_metrics"})
	got := client.await("base rejects unknown control", func(v map[string]any) bool { return v["type"] == "ended" })
	if got["reason"] != "protocol_violation" {
		t.Fatal(got)
	}
}

func TestAudioDiagnosticsSentinelInitialReadAndErrors(t *testing.T) {
	for _, kind := range []string{"absent", "regular", "directory", "missing-parent"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("MOA_CONFIG_DIR", t.TempDir())
			t.Setenv("MOA_VOICE_AUDIO_DIAGNOSTICS", "1") // retired gate has no authority
			path := core.ConfigSubdir("voice-audio-diagnostics")
			switch kind {
			case "regular":
				audioSentinel(t, true)
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "missing-parent":
				t.Setenv("MOA_CONFIG_DIR", filepath.Join(t.TempDir(), "absent"))
			}
			r := newVoiceCallRegistry(nil, nil, VoiceCallOptions{})
			defer r.cancel()
			if got := r.audioDiagnostics.epoch() != 0; got != (kind == "regular") {
				t.Fatalf("initial flag state = %v", got)
			}
		})
	}
}

func TestAudioDiagnosticsQueuedAndInflightWritesAcrossEpochs(t *testing.T) {
	d := testAudioDiagnostics()
	gate := &voice.AudioDiagnosticGate{}
	d.metrics.Gate = gate
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cl := &voiceClient{limit: 100, ctx: ctx, wake: make(chan struct{}, 1)}
	cl.audioDiagnostics.Store(d)
	if !cl.sendAudio([]byte{1, 2}) {
		t.Fatal("OFF enqueue")
	}
	offFrame, _ := cl.next()
	if offFrame.diagnostics != nil {
		t.Fatal("OFF timestamped frame")
	}
	gate.SetEnabled(true)
	cl.audioWriteStart(d, offFrame, d.metrics.Epoch(), 1000000)
	cl.audioWriteEnd(d, offFrame, 1000000, 2000000, true)
	cl.release(offFrame)
	if !cl.sendAudio([]byte{3, 4}) {
		t.Fatal("ON enqueue")
	}
	old, _ := cl.next()
	cl.audioWriteStart(d, old, d.metrics.Epoch(), 3000000)
	before := d.metrics.Snapshot()
	gate.SetEnabled(false)
	gate.SetEnabled(true)
	cl.audioWriteEnd(d, old, 3000000, 900000000000, true)
	if d.metrics.Snapshot() != before {
		t.Fatal("cross-epoch inflight write fabricated duration/completion")
	}
	cl.audioWriteStart(d, old, d.metrics.Epoch(), 900000000000)
	cl.audioWriteStart(d, offFrame, d.metrics.Epoch(), 900000001000)
	got := d.metrics.Snapshot()
	if got.Stats[voice.QueueAgeUS].N != before.Stats[voice.QueueAgeUS].N || got.Stats[voice.WriteGapUS].Max > 1000000 {
		t.Fatal("cross-epoch frame fabricated queue/write age", got)
	}
	cl.release(old)
	if !cl.sendAudio([]byte{5, 6}) {
		t.Fatal("new epoch enqueue")
	}
	if got := d.metrics.Snapshot().Stats[voice.AdmissionGapUS]; got.N != 0 {
		t.Fatal("admission baseline crossed epoch", got)
	}
}

func TestAudioDiagnosticsWriteCapturedClockAcrossEpoch(t *testing.T) {
	d := testAudioDiagnostics()
	gate := &voice.AudioDiagnosticGate{}
	d.metrics.Gate = gate
	cl := &voiceClient{}
	frame := voiceFrame{kind: websocket.MessageBinary, data: make([]byte, 960)}
	gate.SetEnabled(true)
	epoch := d.metrics.Epoch()
	capturedWriteStart := d.elapsed()
	// Pause between write's clock capture and its start helper.
	gate.SetEnabled(false)
	time.Sleep(50 * time.Millisecond)
	gate.SetEnabled(true)
	cl.audioWriteStart(d, frame, epoch, capturedWriteStart)
	if d.metrics.Snapshot() != (voice.AudioSnapshot{}) || cl.diagWriteEpoch != 0 || cl.diagLastWrite != 0 {
		t.Fatal("stale captured write changed counters/baseline")
	}
	if d.metrics.Epoch() == epoch {
		t.Fatal("invalid test epoch")
	}
	// The production caller already drops the stale write's completion.
	now := d.elapsed()
	cl.audioWriteStart(d, frame, d.metrics.Epoch(), now)
	if got := d.metrics.Snapshot().Stats[voice.WriteGapUS]; got.Max >= 40000 {
		t.Fatalf("OFF interval became new-epoch WS write gap: %+v", got)
	}
	cl.audioWriteStart(d, frame, d.metrics.Epoch(), now+20000)
	if got := d.metrics.Snapshot().Stats[voice.WriteGapUS]; got.N != 1 || got.Max != 20 {
		t.Fatalf("new-epoch write baseline not measured: %+v", got)
	}
}

type audioEpochHandler struct {
	entered, release chan struct{}
	records          chan int64
	once             sync.Once
}

func (h *audioEpochHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *audioEpochHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Message != "voice audio diagnostics" {
		return nil
	}
	h.once.Do(func() { close(h.entered); <-h.release })
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "diag" {
			h.records <- a.Value.Int64()
		}
		return true
	})
	return nil
}
func (h *audioEpochHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *audioEpochHandler) WithGroup(string) slog.Handler      { return h }

func TestAudioDiagnosticsHotPollIndependentAndStaleQueueDiscarded(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	audioSentinel(t, true)
	r := newVoiceCallRegistry(nil, nil, VoiceCallOptions{})
	defer r.cancel()
	s := r.audioDiagnostics
	h := &audioEpochHandler{entered: make(chan struct{}), release: make(chan struct{}), records: make(chan int64, 16)}
	previous := slog.Default()
	slog.SetDefault(slog.New(h))
	defer slog.SetDefault(previous)
	var release sync.Once
	defer func() { release.Do(func() { close(h.release) }); r.cancel(); <-s.done; <-s.controlDone }()
	old := s.epoch()
	s.offer(audioDiagnosticRecord{epoch: old, id: 1})
	select {
	case <-h.entered:
	case <-time.After(time.Second):
		t.Fatal("handler not blocked")
	}
	for i := 0; i < 108; i++ {
		s.offer(audioDiagnosticRecord{epoch: old, id: 2})
	}
	if len(s.queue) != 8 || s.dropped.Load() != 100 {
		t.Fatal("sink not bounded")
	}
	audioSentinel(t, false)
	pollUntil(t, 3*time.Second, "OFF despite blocked handler", func() bool { return s.epoch() == 0 })
	s.offer(audioDiagnosticRecord{epoch: old, id: 3})
	audioSentinel(t, true)
	pollUntil(t, 3*time.Second, "ON despite blocked handler", func() bool { return s.epoch() != 0 && s.epoch() != old })
	release.Do(func() { close(h.release) })
	pollUntil(t, time.Second, "stale queue consumed", func() bool { return len(s.queue) == 0 })
	s.offer(audioDiagnosticRecord{epoch: s.epoch(), id: 4})
	var ids []int64
	for len(ids) < 2 {
		select {
		case id := <-h.records:
			ids = append(ids, id)
		case <-time.After(time.Second):
			t.Fatal("fresh log not emitted", ids)
		}
	}
	if !reflect.DeepEqual(ids, []int64{1, 4}) {
		t.Fatal("stale queued epoch logged", ids)
	}
}

func TestAudioDiagnosticsOffHooksNoObservationsOrAllocations(t *testing.T) {
	d := testAudioDiagnostics()
	d.metrics.Gate = &voice.AudioDiagnosticGate{}
	c := &voiceCall{audioDiagnostics: d}
	state := map[string]any{"type": "state", "state": "listening", "text": "CANARY-CONTENT"}
	if n := testing.AllocsPerRun(1000, func() {
		d.event(960, true)
		c.traceServerState(state, 1, 1)
		d.snapshot(false, nil)
	}); n != 0 {
		t.Fatal("OFF diagnostic allocations", n)
	}
	if d.metrics.Snapshot() != (voice.AudioSnapshot{}) || !d.lastEvent.IsZero() || d.eventEpoch != 0 || len(d.sink.queue) != 0 || d.traceSeq[1].Load() != 0 {
		t.Fatal("OFF observation work")
	}
}

func BenchmarkAudioDiagnosticsOffHooks(b *testing.B) {
	d := testAudioDiagnostics()
	d.metrics.Gate = &voice.AudioDiagnosticGate{}
	c := &voiceCall{audioDiagnostics: d}
	state := map[string]any{"type": "state", "state": "listening"}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		d.event(960, true)
		c.traceServerState(state, 1, 1)
		d.snapshot(false, nil)
	}
}

func TestAudioDiagnosticsServerLogNumericPrivacySchema(t *testing.T) {
	d := testAudioDiagnostics()
	c := &voiceCall{audioDiagnostics: d}
	d.traceEvent(3, 10, 0, 0, 0, 0)
	for _, state := range []string{"listening", "speaking", "consulting", "CANARY-CONTENT"} {
		c.traceServerState(map[string]any{"type": "state", "state": state, "interrupt": true, "text": "CANARY-CONTENT", "token": "CANARY-CONTENT"}, 2, 1)
	}
	d.snapshot(false, nil)
	if len(d.sink.queue) != 5 {
		t.Fatal("unknown state observed or fixed state omitted")
	}
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	defer slog.SetDefault(previous)
	for len(d.sink.queue) != 0 {
		d.sink.log(<-d.sink.queue)
	}
	if strings.Contains(output.String(), "CANARY-CONTENT") || strings.Contains(output.String(), "client_accepted") {
		t.Fatal("raw/client data in server logs")
	}
	dec := json.NewDecoder(&output)
	for i := 0; i < 5; i++ {
		var row map[string]any
		if err := dec.Decode(&row); err != nil {
			t.Fatal(err)
		}
		if row["provider"] != "chatgpt-plan" || row["epoch"] != float64(1) || row["cumulative"] != true {
			t.Fatal("missing fixed schema/window semantics", row)
		}
		if i < 4 {
			if row["msg"] != "voice state trace" || (row["source"] != float64(3) && row["source"] != float64(4)) {
				t.Fatal("non-server trace source", row)
			}
			trace, ok := row["trace"].([]any)
			if !ok || len(trace) != 8 {
				t.Fatal("trace schema", row)
			}
			for _, v := range trace {
				if _, ok := v.(float64); !ok {
					t.Fatal("non-numeric trace")
				}
			}
		}
		for key, value := range row {
			if _, ok := value.(string); ok && key != "time" && key != "level" && key != "msg" && key != "provider" {
				t.Fatalf("unapproved string field %q", key)
			}
		}
	}
}
