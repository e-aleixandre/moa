package serve

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/e-aleixandre/moa/pkg/voice"
)

const audioValidJSON = `{"type":"audio_metrics","v":1,"client":1,"seq":1,"window_start_ms":0,"window_ms":1000,"rx_frames":3,"rx_bytes":2880,"rx_odd_frames":0,"rx_dt_us":{"n":2,"min":20000,"max":20000,"sum":40000},"schedule_us":{"n":0},"scheduled_frames":3,"scheduled_samples":1440,"renderer_generation":1,"margin":{"n":0,"invalid_n":10,"last_valid":0,"invalid_reason":3},"main_timer_dt_us":{"n":0},"main_hitches":0,"restarts":0,"interrupts":0,"pending_drop_frames":0,"pending_drop_bytes":0,"playback_drop_frames":0,"playback_drop_samples":0,"reports_skipped":0,"queued_samples":1440,"queued_samples_min":480,"queued_samples_max":1440,"render_progress_samples":0,"render_observations":10,"render_invalid":0,"completion_delay_us":{"n":1,"min":30,"max":30,"sum":30}}`

func TestAudioDiagnosticsClientJSONContract(t *testing.T) {
	for _, data := range []string{audioValidJSON, `{"type":"audio_metrics","v":1,"client":1,"seq":1,"window_start_ms":0,"window_ms":1}`, strings.Replace(audioValidJSON, `"n":0,"invalid_n":10,"last_valid":0,"invalid_reason":3`, `"n":1,"invalid_n":0,"last_valid":1,"invalid_reason":0,"min_us":-10,"max_us":10,"last_us":0`, 1)} {
		if _, reason := parseAudioClientReport([]byte(data)); reason != 0 {
			t.Fatalf("valid JSON reason %s", voice.AudioCounterNames[reason])
		}
	}
	cases := map[string]string{
		"version": strings.Replace(audioValidJSON, `"v":1`, `"v":2`, 1), "float": strings.Replace(audioValidJSON, `"v":1`, `"v":1.0`, 1), "string": strings.Replace(audioValidJSON, `"seq":1`, `"seq":"1"`, 1), "null": strings.Replace(audioValidJSON, `"seq":1`, `"seq":null`, 1),
		"unknown": strings.Replace(audioValidJSON, `"v":1`, `"CANARY-CONTENT":"VP-ACCESS-OLD","v":1`, 1), "negative": strings.Replace(audioValidJSON, `"rx_frames":3`, `"rx_frames":-1`, 1), "count": strings.Replace(audioValidJSON, `"rx_frames":3`, `"rx_frames":1000001`, 1),
		"sum": strings.Replace(audioValidJSON, `"sum":40000`, `"sum":39999`, 1), "minmax": strings.Replace(audioValidJSON, `"min":20000`, `"min":20001`, 1), "nzero": strings.Replace(audioValidJSON, `"schedule_us":{"n":0}`, `"schedule_us":{"n":0,"min":0}`, 1), "nfloat": strings.Replace(audioValidJSON, `"n":2`, `"n":2e0`, 1),
		"windowzero": strings.Replace(audioValidJSON, `"window_ms":1000`, `"window_ms":0`, 1), "windowbig": strings.Replace(audioValidJSON, `"window_ms":1000`, `"window_ms":60001`, 1), "windowend": strings.Replace(audioValidJSON, `"window_start_ms":0`, `"window_start_ms":86400000`, 1),
		"margin": strings.Replace(audioValidJSON, `"last_valid":0`, `"last_valid":1`, 1), "queue": strings.Replace(audioValidJSON, `"queued_samples":1440`, `"queued_samples":33554433`, 1), "render": strings.Replace(audioValidJSON, `"render_invalid":0`, `"render_invalid":1000001`, 1), "odd": strings.Replace(audioValidJSON, `"rx_odd_frames":0`, `"rx_odd_frames":4`, 1),
		"duplicate": strings.Replace(audioValidJSON, `"seq":1`, `"seq":1,"seq":2`, 1), "extraGroup": strings.Replace(audioValidJSON, `"schedule_us":{"n":0}`, `"schedule_us":{"n":0,"error":"CANARY-CONTENT"}`, 1), "trailing": audioValidJSON + `{}`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, reason := parseAudioClientReport([]byte(data)); reason == 0 {
				t.Fatal("invalid report accepted")
			}
		})
	}
}
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
	if f.enqueuedNS == 0 {
		t.Error("audio frame lacks relative enqueue timestamp")
	}
	got := d.metrics.Snapshot()
	if got.Counts[voice.EnqueueAttempts] != 2 || got.Counts[voice.EnqueueAccepted] != 1 || got.Counts[voice.EnqueueRejected] != 1 || got.Stats[voice.OccupancyBytes].Max != 12 {
		t.Fatal(got)
	}
	f.enqueuedNS = 100000000
	cl.audioWriteStart(d, f, 135000000)
	cl.audioWriteEnd(d, f, 135000000, 142000000, true)
	cl.audioWriteStart(d, f, 150000000)
	cl.audioWriteEnd(d, f, 150000000, 151000000, false)
	got = d.metrics.Snapshot()
	if got.Stats[voice.QueueAgeUS].Min != 35000 || got.Stats[voice.WriteUS].Max != 7000 || got.Counts[voice.WriteOK] != 1 || got.Counts[voice.WriteError] != 1 || got.Counts[voice.WrittenBytes] != 12 {
		t.Fatal(got)
	}
	cl.release(f)
}
func TestAudioDiagnosticsActivationGateAndDisabledReport(t *testing.T) {
	for _, value := range []string{"", "0", "true", "01", "1 ", "1"} {
		t.Run(fmt.Sprintf("env=%q", value), func(t *testing.T) {
			t.Setenv("MOA_VOICE_AUDIO_DIAGNOSTICS", value)
			f := vpNewFixture(t, "chatgpt-plan")
			c := f.call()
			state := c.await("state", func(v map[string]any) bool { return v["type"] == "state" })
			_, active := state["audio_diagnostics"]
			if active != (value == "1") {
				t.Errorf("activation %v value %q", state, value)
			}
			c.send(map[string]any{"type": "audio_metrics", "v": 2, "CANARY-CONTENT": "VP-ACCESS-OLD"})
			c.send(map[string]any{"type": "ping"})
			got := c.await("diagnostic followed by ping", func(v map[string]any) bool { return v["type"] == "pong" || v["type"] == "ended" })
			if got["type"] != "pong" {
				t.Fatalf("diagnostic ended call: %v", got)
			}
			c.hangup()
		})
	}
	t.Run("other provider", func(t *testing.T) {
		t.Setenv("MOA_VOICE_AUDIO_DIAGNOSTICS", "1")
		f := vpNewFixture(t, "grok-plan")
		c := f.call()
		state := c.await("state", func(v map[string]any) bool { return v["type"] == "state" })
		if _, ok := state["audio_diagnostics"]; ok {
			t.Fatal(state)
		}
		c.hangup()
	})
}

func TestAudioDiagnosticsWriterWSParity(t *testing.T) {
	run := func(enabled bool) ([][]byte, voice.AudioSnapshot) {
		d := testAudioDiagnostics()
		ready := make(chan *voiceClient, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			cl := newVoiceClient(conn, 48000)
			if enabled {
				cl.audioDiagnostics.Store(d)
			}
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
		var want, out [][]byte
		for i, size := range []int{2, 94, 960, 4096, 12, 1822} {
			b := bytes.Repeat([]byte{byte(i)}, size)
			want = append(want, b)
			if !cl.sendAudio(b) || !cl.sendJSON(map[string]any{"type": "state", "state": "listening"}) {
				t.Fatal("enqueue")
			}
		}
		cl.finish(map[string]any{"type": "ended"})
		for {
			kind, b, err := conn.Read(ctx)
			if err != nil {
				break
			}
			if kind == websocket.MessageBinary {
				out = append(out, b)
			}
		}
		<-cl.done
		if !reflect.DeepEqual(want, out) {
			t.Fatal("WS changed chunks/bytes/order")
		}
		return out, d.metrics.Snapshot()
	}
	off, _ := run(false)
	on, got := run(true)
	if !reflect.DeepEqual(off, on) {
		t.Fatal("off/on PCM differs")
	}
	if got.Counts[voice.WriteOK] != 6 || got.Counts[voice.EnqueueAccepted] != 6 || got.Counts[voice.WrittenBytes] != 6986 || got.Stats[voice.WriteUS].N != 6 {
		t.Fatal(got)
	}
}
func TestAudioDiagnosticsClientIsolation(t *testing.T) {
	d := testAudioDiagnostics()
	cl := &voiceClient{diagSocket: 1}
	d.current.Store(cl)
	c := &voiceCall{audioDiagnostics: d}
	now := time.Now()
	cl.heard.Store(17)
	c.receiveAudioMetrics(cl, []byte(audioValidJSON), now)
	c.receiveAudioMetrics(cl, []byte(audioValidJSON), now.Add(time.Millisecond))
	c.receiveAudioMetrics(cl, []byte(audioValidJSON), now.Add(time.Second))
	c.receiveAudioMetrics(cl, []byte(strings.Replace(audioValidJSON, `"seq":1`, `"seq":2`, 1)), now.Add(2*time.Second))
	c.receiveAudioMetrics(cl, bytes.Repeat([]byte{' '}, 4097), now.Add(3*time.Second))
	d.current.Store(&voiceClient{})
	c.receiveAudioMetrics(cl, []byte(audioValidJSON), now.Add(4*time.Second))
	got := d.metrics.Snapshot()
	for k, w := range map[voice.AudioCounter]int64{voice.ClientAccepted: 1, voice.ClientRate: 1, voice.ClientOrder: 2, voice.ClientSize: 1, voice.ClientStale: 1} {
		if got.Counts[k] != w {
			t.Errorf("%s=%d want%d", voice.AudioCounterNames[k], got.Counts[k], w)
		}
	}
	if cl.heard.Load() != 17 || !cl.playedBy.IsZero() || c.muted.Load() {
		t.Fatal("diagnostics changed liveness/media")
	}
}

type audioBlockedHandler struct {
	entered, release chan struct{}
	once             sync.Once
}

func (h *audioBlockedHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *audioBlockedHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Message == "voice audio diagnostics" || r.Message == "voice audio client metrics" {
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
	s := newAudioDiagnosticSink(ctx)
	defer func() {
		cancel()
		close(h.release)
		select {
		case <-s.done:
		case <-time.After(time.Second):
			t.Error("diagnostic worker did not exit after logger release")
		}
	}()
	s.offer(audioDiagnosticRecord{})
	select {
	case <-h.entered:
	case <-time.After(time.Second):
		t.Fatal("handler not entered")
	}
	done := make(chan struct{})
	go func() {
		for i := 0; i < 108; i++ {
			s.offer(audioDiagnosticRecord{})
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
	t.Setenv("MOA_VOICE_AUDIO_DIAGNOSTICS", "1")
	f := vpNewFixtureWithLimits(t, "chatgpt-plan", limits)
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
	c.audioDiagnostics = &audioCallDiagnostics{metrics: &voice.AudioDiagnostics{}, sink: r.audioDiagnostics, id: r.audioDiagnostics.next.Add(1), start: time.Now()}
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
	old := d.current.Load()
	second := f.dial(created, f.owner(), true)
	second.await("attach two", func(v map[string]any) bool { return v["type"] == "state" })
	c.receiveAudioMetrics(old, []byte(audioValidJSON), time.Now())
	got := d.metrics.Snapshot()
	for k, w := range map[voice.AudioCounter]int64{voice.EarlyRetained: 1, voice.EarlyDiscarded: 1, voice.EarlyFlushed: 1, voice.EventNoClient: 2, voice.ClientStale: 1} {
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
		t.Fatal("final/permission")
	}
}

func TestAudioDiagnosticsWSInvalidReportsPreserveMediaAndPrivacy(t *testing.T) {
	f, c, created, u := audioFakeController(t, vpDefaultLimits())
	client := f.dial(created, f.owner(), true)
	client.await("activation", func(v map[string]any) bool { return v["audio_diagnostics"] != nil })
	cl := c.audioDiagnostics.current.Load()
	base := time.Now()
	bad := []string{strings.Replace(audioValidJSON, `"v":1`, `"v":2`, 1), strings.Replace(audioValidJSON, `"seq":1`, `"seq":"1"`, 1), strings.Replace(audioValidJSON, `"v":1`, `"v":1,"CANARY-CONTENT":"VP-ACCESS-OLD"`, 1), strings.Replace(audioValidJSON, `"sum":40000`, `"sum":1`, 1), strings.Replace(audioValidJSON, `"window_ms":1000`, `"window_ms":0`, 1)}
	large := `{"type":"audio_metrics","pad":"` + strings.Repeat("x", 4064) + `"}`
	if len(large) != 4097 {
		t.Fatalf("size fixture %d", len(large))
	}
	bad = append(bad, large, audioValidJSON, audioValidJSON)
	for i, data := range bad {
		// Directly validate every malformed report with an admissible clock;
		// then traverse the real readClient path (whose rate gate may discard).
		c.receiveAudioMetrics(cl, []byte(data), base.Add(time.Duration(i+1)*time.Second))
		ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
		if err := client.conn.Write(ctx, websocket.MessageText, []byte(data)); err != nil {
			t.Fatal(err)
		}
		if err := client.conn.Write(ctx, websocket.MessageBinary, []byte{1, 2}); err != nil {
			t.Fatal(err)
		}
		cancel()
		select {
		case pcm := <-u.audio:
			if !bytes.Equal(pcm, []byte{1, 2}) {
				t.Fatal("upstream PCM changed")
			}
		case <-time.After(vpDeadline):
			t.Fatal("report prevented microphone audio")
		}
		client.send(map[string]any{"type": "ping"})
		pollUntil(t, vpDeadline, "fresh pong barrier", func() bool {
			n := 0
			for _, fr := range client.snapshot() {
				if strings.Contains(string(fr.data), `"pong"`) {
					n++
				}
			}
			return n == i+1
		})
	}
	client.hangup()
	got := c.audioDiagnostics.metrics.Snapshot()
	for _, k := range []voice.AudioCounter{voice.ClientRange, voice.ClientSchema, voice.ClientSize, voice.ClientRate, voice.ClientOrder, voice.ClientAccepted} {
		if got.Counts[k] == 0 {
			t.Errorf("unexercised %s", voice.AudioCounterNames[k])
		}
	}
	pollUntil(t, vpDeadline, "final privacy barrier", func() bool { return strings.Contains(f.log.String(), "final=true") })
	if strings.Contains(f.log.String(), "CANARY-CONTENT") {
		t.Fatal("payload in logs")
	}
	vpNoSecrets(t, f.log.String())
	if err := filepath.WalkDir(f.root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err == nil && bytes.Contains(b, []byte("CANARY-CONTENT")) {
			t.Errorf("diagnostic persisted in %s", path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
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

func TestAudioDiagnosticsReportsCannotRenewLiveness(t *testing.T) {
	d := testAudioDiagnostics()
	ready := make(chan *voiceClient, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		cl := newVoiceClient(conn, 48000)
		cl.audioDiagnostics.Store(d)
		d.current.Store(cl)
		c := &voiceCall{audioDiagnostics: d, endCh: make(chan struct{})}
		go cl.keepalive(5*time.Millisecond, 5*time.Millisecond, 60*time.Millisecond)
		ready <- cl
		_ = c.readClient(cl)
		cl.kill()
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
	pollUntil(t, time.Second, "keepalive initialized", func() bool { return cl.heard.Load() != 0 })
	initial := cl.heard.Load()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	// Do not read: the synthetic muted client cannot answer transport pings.
	for {
		select {
		case <-cl.ctx.Done():
			if cl.heard.Load() != initial {
				t.Fatal("metrics renewed liveness")
			}
			return
		case <-ctx.Done():
			t.Fatal("metrics-only socket remained alive")
		case <-tick.C:
			_ = conn.Write(ctx, websocket.MessageText, []byte(audioValidJSON))
		}
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

func TestAudioDiagnosticsGateCapturedAtRegistryConstruction(t *testing.T) {
	for _, value := range []string{"", "0", "true", "01", "1 ", "1"} {
		t.Run(fmt.Sprintf("gate=%q", value), func(t *testing.T) {
			t.Setenv("MOA_VOICE_AUDIO_DIAGNOSTICS", value)
			r := newVoiceCallRegistry(nil, nil, VoiceCallOptions{})
			defer r.cancel()
			t.Setenv("MOA_VOICE_AUDIO_DIAGNOSTICS", "changed")
			if (r.audioDiagnostics != nil) != (value == "1") {
				t.Fatal("gate is not exact/captured")
			}
		})
	}
}

func TestAudioDiagnosticsChatGPTRealRouteRTPToWS(t *testing.T) {
	t.Setenv("MOA_VOICE_AUDIO_DIAGNOSTICS", "1")
	f := vpNewFixture(t, "chatgpt-plan")
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
	if !r.final || r.writerComplete || r.server.Counts[voice.ClientAccepted] != 0 {
		t.Fatal("pending writer/missing client coverage hidden")
	}
	close(cl.done)
	d.snapshot(false, cl)
	r = <-d.sink.queue
	if !r.writerComplete {
		t.Fatal("completed writer not observed")
	}
}
