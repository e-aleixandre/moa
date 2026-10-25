package serve

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/e-aleixandre/moa/pkg/voice"
)

func testAudioDiagnostics() *audioCallDiagnostics {
	return &audioCallDiagnostics{metrics: &voice.AudioDiagnostics{}, sink: &audioDiagnosticSink{queue: make(chan audioDiagnosticRecord, 8)}, id: 1, start: time.Now()}
}
func TestAudioDiagnosticsQueueHooks(t *testing.T) {
	d := testAudioDiagnostics()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cl := &voiceClient{limit: 100, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1)}
	cl.audioDiagnostics.Store(d)
	if !cl.sendAudio(make([]byte, 12)) || cl.sendAudio(make([]byte, 100)) {
		t.Fatal("admission policy")
	}
	f, ok := cl.next()
	if !ok {
		t.Fatal("missing frame")
	}
	if f.diagnostics == nil || f.diagnostics.enqueuedNS == 0 {
		t.Error("audio frame lacks relative enqueue timestamp")
	}
	got := d.metrics.Snapshot()
	if got.Counts[voice.EnqueueAttempts] != 2 || got.Counts[voice.EnqueueAccepted] != 1 || got.Counts[voice.EnqueueRejected] != 1 || got.Stats[voice.OccupancyBytes].Max != 12 {
		t.Fatal(got)
	}
	f.diagnostics = &voiceFrameDiagnostics{epoch: 1, enqueuedNS: 100000000}
	cl.audioWriteStart(d, f, d.metrics.Epoch(), 135000000)
	cl.audioWriteEnd(d, f, 135000000, 142000000, true)
	cl.audioWriteStart(d, f, d.metrics.Epoch(), 150000000)
	cl.audioWriteEnd(d, f, 150000000, 151000000, false)
	got = d.metrics.Snapshot()
	if got.Stats[voice.QueueAgeUS].Min != 35000 || got.Stats[voice.WriteUS].Max != 7000 || got.Counts[voice.WriteOK] != 1 || got.Counts[voice.WriteError] != 1 || got.Counts[voice.WrittenBytes] != 12 {
		t.Fatal(got)
	}
	cl.release(f)
}
func TestAudioDiagnosticsWriterWSParity(t *testing.T) {
	run := func(enabled bool) ([][]byte, voice.AudioSnapshot) {
		d := testAudioDiagnostics()
		d.metrics.Gate = &voice.AudioDiagnosticGate{}
		d.metrics.Gate.SetEnabled(enabled)
		ready := make(chan *voiceClient, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			cl := newVoiceClient(conn, 48000)
			cl.audioDiagnostics.Store(d)
			ready <- cl
			<-cl.done
		}))
		defer server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		conn, _, err := websocket.Dial(ctx, server.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.CloseNow() }()
		cl := <-ready
		var want, out, wire [][]byte
		for i, size := range []int{2, 94, 960, 4096, 12, 1822} {
			b := bytes.Repeat([]byte{byte(i)}, size)
			want = append(want, b)
			if !cl.sendAudio(b) || !cl.sendJSON(map[string]any{"type": "state", "state": "listening", "text": "CANARY-CONTENT"}) {
				t.Fatal("enqueue")
			}
		}
		cl.finish(map[string]any{"type": "ended"})
		for {
			kind, b, err := conn.Read(ctx)
			if err != nil {
				break
			}
			wire = append(wire, append([]byte{byte(kind)}, b...))
			if kind == websocket.MessageBinary {
				out = append(out, b)
			}
		}
		<-cl.done
		if !reflect.DeepEqual(want, out) {
			t.Fatal("WS changed chunks/bytes/order")
		}
		return wire, d.metrics.Snapshot()
	}
	off, disabled := run(false)
	if disabled != (voice.AudioSnapshot{}) {
		t.Fatal("OFF WS observed diagnostics")
	}
	on, got := run(true)
	if !reflect.DeepEqual(off, on) {
		t.Fatal("off/on PCM/control bytes differ")
	}
	if got.Counts[voice.WriteOK] != 6 || got.Counts[voice.EnqueueAccepted] != 6 || got.Counts[voice.WrittenBytes] != 6986 || got.Stats[voice.WriteUS].N != 6 {
		t.Fatal(got)
	}
}

type audioBlockedHandler struct {
	entered, release chan struct{}
	once             sync.Once
}

func (h *audioBlockedHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *audioBlockedHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Message == "voice audio diagnostics" {
		h.once.Do(func() { close(h.entered) })
		<-h.release
	}
	return nil
}
func (h *audioBlockedHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *audioBlockedHandler) WithGroup(string) slog.Handler      { return h }
func TestAudioDiagnosticsBlockedLogger(t *testing.T) {
	h := &audioBlockedHandler{entered: make(chan struct{}), release: make(chan struct{})}
	old := slog.Default()
	slog.SetDefault(slog.New(h))
	defer slog.SetDefault(old)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newAudioDiagnosticSink(ctx, nil)
	defer func() {
		cancel()
		close(h.release)
		select {
		case <-s.done:
		case <-time.After(time.Second):
			t.Error("diagnostic worker did not exit after logger release")
		}
	}()
	s.offer(audioDiagnosticRecord{epoch: 1})
	select {
	case <-h.entered:
	case <-time.After(time.Second):
		t.Fatal("handler not entered")
	}
	done := make(chan struct{})
	go func() {
		for i := 0; i < 108; i++ {
			s.offer(audioDiagnosticRecord{epoch: 1})
		}
		cancel()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("blocked offer/close")
	}
	if cap(s.queue) != 8 || len(s.queue) != 8 || s.dropped.Load() != 100 {
		t.Fatalf("queue=%d drops=%d", len(s.queue), s.dropped.Load())
	}
}

func audioFakeController(t *testing.T, limits vpLimits) (*vpFixture, *voiceCall, vpCreated, *vaControllerUpstream) {
	t.Helper()
	f := vpNewFixtureWithLimits(t, "chatgpt-plan", limits)
	audioSentinel(t, true)
	pollUntil(t, 3*time.Second, "diagnostics ON", func() bool { return f.mgr.voiceCalls[0].audioDiagnostics.epoch() != 0 })
	f.harnessOnly = true
	r := f.mgr.voiceCalls[0]
	slot, closed := r.reserve("token")
	if closed || slot == nil {
		t.Fatal("reservation")
	}
	u := vaNewControllerUpstream()
	id := newVoiceCallID()
	c := newVoiceCall(r, id, "token", f.sess.ID, voice.ChatGPTPlan, voice.Billing{Kind: "plan_unverified"}, u)
	c.slot = slot
	c.audioDiagnostics = &audioCallDiagnostics{metrics: &voice.AudioDiagnostics{Gate: r.audioDiagnostics.gate}, sink: r.audioDiagnostics, id: r.audioDiagnostics.next.Add(1), start: time.Now()}
	if !r.register(c, nil, voiceModel()) {
		t.Fatal("register")
	}
	go c.run()
	return f, c, vpCreated{ID: id, Provider: "chatgpt-plan", WSURL: "/api/voice/calls/" + id + "/ws"}, u
}

func TestAudioDiagnosticsControllerPreattachReplaceFinal(t *testing.T) {
	limits := vpDefaultLimits()
	limits.grace = time.Second
	limits.bufferedBytes = 1024
	f, c, created, u := audioFakeController(t, limits)
	d := c.audioDiagnostics
	u.events <- voice.Event{Kind: voice.EventAudio, PCM: make([]byte, 960)}
	u.events <- voice.Event{Kind: voice.EventAudio, PCM: make([]byte, 200)}
	pollUntil(t, vpDeadline, "preattach observed", func() bool { return d.metrics.Snapshot().Counts[voice.EventFrames] == 2 })
	first := f.dial(created, f.owner(), true)
	first.await("attach one", func(v map[string]any) bool { return v["type"] == "state" })
	pollUntil(t, vpDeadline, "early PCM", func() bool {
		for _, fr := range first.snapshot() {
			if fr.kind == websocket.MessageBinary {
				return len(fr.data) == 960
			}
		}
		return false
	})
	second := f.dial(created, f.owner(), true)
	second.await("attach two", func(v map[string]any) bool { return v["type"] == "state" })
	got := d.metrics.Snapshot()
	for k, w := range map[voice.AudioCounter]int64{voice.EarlyRetained: 1, voice.EarlyDiscarded: 1, voice.EarlyFlushed: 1, voice.EventNoClient: 2} {
		if got.Counts[k] != w {
			t.Errorf("%s=%d want%d", voice.AudioCounterNames[k], got.Counts[k], w)
		}
	}
	if d.sockets.Load() != 2 {
		t.Fatal("socket correlation")
	}
	second.hangup()
	pollUntil(t, vpDeadline, "one final summary", func() bool { return strings.Count(f.log.String(), "final=true") == 1 })
	if strings.Count(f.log.String(), "final=true") != 1 || d.current.Load() != nil {
		t.Fatal("final/current socket")
	}
}

func TestAudioDiagnosticsBlockedLoggerCannotBlockAudioOrHangup(t *testing.T) {
	f, c, created, u := audioFakeController(t, vpDefaultLimits())
	client := f.dial(created, f.owner(), true)
	client.await("attach", func(v map[string]any) bool { return v["type"] == "state" })
	h := &audioBlockedHandler{entered: make(chan struct{}), release: make(chan struct{})}
	old := slog.Default()
	slog.SetDefault(slog.New(h))
	defer slog.SetDefault(old)
	d := c.audioDiagnostics
	defer func() {
		c.reg.cancel()
		close(h.release)
		select {
		case <-d.sink.done:
		case <-time.After(time.Second):
			t.Error("diagnostic worker did not exit after logger release")
		}
	}()
	d.snapshot(false, d.current.Load())
	select {
	case <-h.entered:
	case <-time.After(time.Second):
		t.Fatal("diagnostic worker not blocked")
	}
	for i := 0; i < 108; i++ {
		d.snapshot(false, d.current.Load())
	}
	for i := 0; i < 3; i++ {
		u.events <- voice.Event{Kind: voice.EventAudio, PCM: bytes.Repeat([]byte{byte(i)}, 12)}
	}
	pollUntil(t, vpDeadline, "audio despite blocked logger", func() bool {
		n := 0
		for _, fr := range client.snapshot() {
			if fr.kind == websocket.MessageBinary {
				if !bytes.Equal(fr.data, bytes.Repeat([]byte{byte(n)}, 12)) {
					t.Error("PCM changed")
				}
				n++
			}
		}
		return n == 3
	})
	start := time.Now()
	client.hangup()
	if time.Since(start) > time.Second {
		t.Fatal("hangup waited on logger")
	}
	if d.sink.dropped.Load() < 101 {
		t.Fatal("missing overflow/final drop")
	}
}

func TestAudioDiagnosticsDoesNotAuthorizeOtherControls(t *testing.T) {
	for _, kind := range []string{"transcript", "tool_call", "credential"} {
		t.Run(kind, func(t *testing.T) {
			f, _, created, _ := audioFakeController(t, vpDefaultLimits())
			cl := f.dial(created, f.owner(), true)
			cl.await("attach", func(v map[string]any) bool { return v["type"] == "state" })
			cl.send(map[string]any{"type": kind})
			got := cl.await("forbidden control", func(v map[string]any) bool { return v["type"] == "ended" })
			if got["reason"] != "protocol_violation" {
				t.Fatal(got)
			}
		})
	}
}

func TestAudioDiagnosticsRealWriteError(t *testing.T) {
	d := testAudioDiagnostics()
	ready := make(chan *voiceClient, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		cl := newVoiceClient(conn, 48000)
		cl.audioDiagnostics.Store(d)
		ready <- cl
		<-cl.done
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	cl := <-ready
	_ = cl.conn.CloseNow()
	if !cl.sendAudio(make([]byte, 12)) {
		t.Fatal("admission changed")
	}
	select {
	case <-cl.done:
	case <-ctx.Done():
		t.Fatal("writer not finished")
	}
	got := d.metrics.Snapshot()
	if got.Counts[voice.WriteAttempts] != 1 || got.Counts[voice.WriteError] != 1 || got.Counts[voice.WriteOK] != 0 || got.Counts[voice.WrittenBytes] != 0 {
		t.Fatal(got)
	}
}

func TestAudioDiagnosticsChatGPTRealRouteRTPToWS(t *testing.T) {
	f := vpNewFixture(t, "chatgpt-plan")
	audioSentinel(t, true)
	pollUntil(t, 3*time.Second, "diagnostics ON", func() bool { return f.mgr.voiceCalls[0].audioDiagnostics.epoch() != 0 })
	client := f.call()
	r := f.mgr.voiceCalls[0]
	c, _, found := r.lookup(client.c.ID)
	if !found || c == nil || c.audioDiagnostics == nil {
		t.Fatal("diagnostic call not registered")
	}
	_, peers, _ := f.up.snapshot()
	if len(peers) != 1 {
		t.Fatal("synthetic provider missing")
	}
	peers[0].audio(t, bytes.Repeat(vpPCM(), 3))
	pollUntil(t, vpDeadline, "three real RTP->WS writes", func() bool { return c.audioDiagnostics.metrics.Snapshot().Counts[voice.WriteOK] == 3 })
	got := c.audioDiagnostics.metrics.Snapshot()
	for k, w := range map[voice.AudioCounter]int64{voice.RTPPackets: 3, voice.RTPDecoded: 3, voice.RTPSamples: 1440, voice.RTPPCMBytes: 2880, voice.EventFrames: 3, voice.EventBytes: 2880, voice.EnqueueAccepted: 3, voice.WriteOK: 3, voice.WrittenBytes: 2880} {
		if got.Counts[k] != w {
			t.Errorf("%s=%d want%d", voice.AudioCounterNames[k], got.Counts[k], w)
		}
	}
	pollUntil(t, vpDeadline, "periodic cumulative summary", func() bool { return strings.Contains(f.log.String(), "final=false") })
	client.hangup()
	pollUntil(t, vpDeadline, "final summary", func() bool { return strings.Count(f.log.String(), "final=true") == 1 })
}

func TestAudioDiagnosticsFinalDoesNotWaitForWriter(t *testing.T) {
	d := testAudioDiagnostics()
	cl := &voiceClient{done: make(chan struct{})}
	d.snapshot(true, cl)
	r := <-d.sink.queue
	if !r.final || r.writerComplete {
		t.Fatal("pending writer incorrectly marked complete")
	}
	close(cl.done)
	d.snapshot(false, cl)
	r = <-d.sink.queue
	if !r.writerComplete {
		t.Fatal("completed writer not observed")
	}
}
