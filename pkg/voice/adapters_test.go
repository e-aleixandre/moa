package voice

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/pion/opus"
	"github.com/pion/webrtc/v4/pkg/media"
)

type fixedCred struct{}

func (fixedCred) Resolve(context.Context) (Credential, error) {
	return Credential{Token: "SYNTHETIC", AccountID: "SYNTHETIC"}, nil
}
func (fixedCred) Refresh(context.Context, Credential) (Credential, error) { return Credential{}, nil }

// wsPair opens a real xAI (live=false) or Live (live=true) upstream against a
// local WebSocket and returns the provider side of the socket, which stays
// open until the test ends.
func wsPair(t *testing.T, live bool) (Upstream, *websocket.Conn) {
	t.Helper()
	remote := make(chan *websocket.Conn, 1)
	serverDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		if _, _, err := c.Read(context.Background()); err != nil {
			_ = c.CloseNow()
			return
		}
		typ := "session.updated"
		if live {
			typ = "session.started"
		}
		b, _ := json.Marshal(map[string]any{"type": typ})
		if c.Write(context.Background(), websocket.MessageText, b) != nil {
			_ = c.CloseNow()
			return
		}
		remote <- c
		<-serverDone
	}))
	t.Cleanup(func() { close(serverDone); srv.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	a := Adapter(NewRealtimeWS(RealtimeWSConfig{URL: url, ID: GrokPlan, Credentials: fixedCred{}}))
	if live {
		a = NewLiveWS(LiveWSConfig{URL: url, Credentials: fixedCred{}})
	}
	up, err := a.Open(ctx, CallSpec{Voice: "eve", BackendModel: core.Model{ID: "gpt-6-luna"}, Instructions: CommonInstructions, Tools: ToolSpecs()})
	if err != nil {
		t.Fatal(err)
	}
	c := <-remote
	t.Cleanup(func() {
		_ = c.CloseNow()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = up.Close(ctx, "")
	})
	return up, c
}

func wsSend(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timeout waiting for " + what)
}

func nextEvent(t *testing.T, up Upstream) (Event, bool) {
	t.Helper()
	select {
	case e, ok := <-up.Events():
		return e, ok
	case <-time.After(time.Second):
		t.Fatal("no event")
		return Event{}, false
	}
}

// drainUntilClosed reads every remaining event and fails if Events does not
// end within a second.
func drainUntilClosed(t *testing.T, ch <-chan Event) []Event {
	t.Helper()
	var out []Event
	timeout := time.After(time.Second)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, e)
		case <-timeout:
			t.Fatalf("Events did not end; got %d events", len(out))
		}
	}
}

func wsOf(up Upstream) *wsUpstream {
	if l, ok := up.(*liveUpstream); ok {
		return l.wsUpstream
	}
	return up.(*realtimeUpstream).wsUpstream
}

type usageReporter interface{ Usage() *Usage }

func reportedUsage(t *testing.T, up Upstream) *Usage {
	t.Helper()
	r, ok := up.(usageReporter)
	if !ok {
		t.Fatal("upstream does not report the usage it received")
	}
	return r.Usage()
}

func TestAdapterWSInboundAudioBoundInBytes(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "xai", true: "live"}[live], func(t *testing.T) {
			up, c := wsPair(t, live)
			u := wsOf(up)
			typ := "response.output_audio.delta"
			if live {
				typ = "session.output_audio.delta"
			}
			// One chunk above the 60 s output budget fails the upstream.
			wsSend(t, c, map[string]any{"type": typ, "delta": base64.StdEncoding.EncodeToString(make([]byte, MaxPendingOutputAudio+2))})
			eventually(t, "reader to take the chunk", func() bool { return len(u.events) >= 1 || u.ctx.Err() != nil })
			queued, failed := 0, false
			for _, e := range drainUntilClosed(t, up.Events()) {
				queued += len(e.PCM)
				failed = failed || e.Kind == EventFailed
			}
			if queued > MaxPendingOutputAudio || !failed {
				t.Fatalf("queued_audio_bytes=%d max=%d failed=%v", queued, MaxPendingOutputAudio, failed)
			}
		})
	}
}

func TestAdapterProviderErrorsAfterOpen(t *testing.T) {
	for _, live := range []bool{false, true} {
		barrier := "response.output_audio_transcript.delta"
		if live {
			barrier = "session.output_transcript.delta"
		}
		name := map[bool]string{false: "xai", true: "live"}[live]
		t.Run(name+"/fatal_ends_call", func(t *testing.T) {
			up, c := wsPair(t, live)
			wsSend(t, c, map[string]any{"type": "error", "error": map[string]any{"type": "invalid_request_error", "code": "responses_validation_failed", "message": "SYNTHETIC secret must never cross"}})
			wsSend(t, c, map[string]any{"type": barrier, "delta": "barrier"})
			e, _ := nextEvent(t, up)
			if e.Kind != EventFailed {
				t.Fatalf("fatal provider error disappeared; first event is %s %q", e.Kind, e.Text)
			}
			if strings.Contains(e.Reason+e.Text, "SYNTHETIC") || e.Reason == "" {
				t.Fatalf("unsafe or empty reason %q", e.Reason)
			}
			for _, e := range drainUntilClosed(t, up.Events()) {
				if e.Kind == EventTranscript {
					t.Fatal("events kept flowing after a fatal error")
				}
			}
		})
		t.Run(name+"/rejected_command_is_ignored", func(t *testing.T) {
			up, c := wsPair(t, live)
			wsSend(t, c, map[string]any{"type": "error", "error": map[string]any{"type": "invalid_request_error", "code": "response_cancel_not_active", "message": "x", "client_event_id": "evt_1"}})
			wsSend(t, c, map[string]any{"type": barrier, "delta": "barrier"})
			if e, _ := nextEvent(t, up); e.Kind != EventTranscript || e.Text != "barrier" {
				t.Fatalf("a rejected single command ended the call: %+v", e)
			}
		})
	}
	t.Run("chatgpt/fatal", func(t *testing.T) {
		e := parseChatGPT([]byte(`{"type":"error","error":{"code":"forbidden","message":"Voice session access denied"}}`))
		if len(e) != 1 || e[0].Kind != EventFailed || strings.Contains(e[0].Reason, "denied") {
			t.Fatalf("ChatGPT access denied not normalized: %+v", e)
		}
	})
	t.Run("chatgpt/rejected_command", func(t *testing.T) {
		if e := parseChatGPT([]byte(`{"type":"error","error":{"type":"invalid_request_error","code":"invalid_value","client_event_id":"c1"}}`)); len(e) != 0 {
			t.Fatalf("rejected command should not end the call: %+v", e)
		}
	})
}

func TestAdapterLiveSessionClosedEndsEvents(t *testing.T) {
	up, c := wsPair(t, true)
	wsSend(t, c, map[string]any{"type": "session.closed", "reason": "expired", "usage": map[string]any{"seconds": 12}})
	closed := false
	for _, e := range drainUntilClosed(t, up.Events()) {
		closed = closed || e.Kind == EventClosed
	}
	if !closed {
		t.Fatal("session.closed did not emit EventClosed")
	}
}

func TestAdapterLiveUsageUpdatedSurvivesDrop(t *testing.T) {
	up, c := wsPair(t, true)
	wsSend(t, c, map[string]any{"type": "session.usage.updated", "usage": map[string]any{"seconds": 12}, "context_window": map[string]any{"usage_ratio": 0.4}})
	wsSend(t, c, map[string]any{"type": "session.usage.updated", "usage": map[string]any{"seconds": 15}})
	e, _ := nextEvent(t, up)
	if e.Kind != EventUsage || e.Usage == nil || e.Usage.Seconds == nil || *e.Usage.Seconds != 12 || e.Usage.Final {
		t.Fatalf("cumulative usage update dropped: %+v", e)
	}
	// The transport drops before session.closed: the latest cumulative value
	// survives, unconfirmed.
	_ = c.CloseNow()
	drainUntilClosed(t, up.Events())
	u := reportedUsage(t, up)
	if u == nil || u.Seconds == nil || *u.Seconds != 15 || u.Final {
		t.Fatalf("latest usage lost on drop: %+v", u)
	}
}

func TestAdapterLiveClosePreservesFinalUsageUnderBackpressure(t *testing.T) {
	up, c := wsPair(t, true)
	u := wsOf(up)
	for i := 0; i < upstreamEventQueue; i++ {
		wsSend(t, c, map[string]any{"type": "session.input_transcript.delta", "delta": "x"})
	}
	eventually(t, "a full event queue", func() bool { return len(u.events) == upstreamEventQueue })
	remoteDone := make(chan struct{})
	go func() {
		defer close(remoteDone)
		if _, _, err := c.Read(context.Background()); err != nil {
			return
		}
		_ = c.Write(context.Background(), websocket.MessageText, []byte(`{"type":"session.closed","reason":"close_requested","usage":{"seconds":17}}`))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := up.Close(ctx, ""); err != nil {
		t.Fatal(err)
	}
	<-remoteDone
	for range up.Events() {
	}
	got := reportedUsage(t, up)
	if got == nil || got.Seconds == nil || *got.Seconds != 17 || !got.Final {
		t.Fatalf("final usage lost under backpressure: %+v", got)
	}
}

func TestAdapterConcurrentCloseRespectsEachContext(t *testing.T) {
	up, c := wsPair(t, true)
	firstCtx, firstCancel := context.WithCancel(context.Background())
	defer firstCancel()
	first := make(chan error, 1)
	go func() { first <- up.Close(firstCtx, "") }()
	// Receiving session.close proves the first caller is waiting for the
	// provider's confirmation.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, err := c.Read(ctx); err != nil {
		t.Fatal(err)
	}
	short, shortCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer shortCancel()
	second := make(chan error, 1)
	go func() { second <- up.Close(short, "") }()
	select {
	case <-second:
	case <-time.After(200 * time.Millisecond):
		firstCancel()
		<-first
		<-second
		t.Fatal("second Close stayed blocked after its own 10ms context expired")
	}
	firstCancel()
	<-first
}

func TestPeerInboundPCMBoundInBytes(t *testing.T) {
	p, a, _ := connectedPair(t)
	enc, err := opus.NewEncoder(opus.WithVBR(false))
	if err != nil {
		t.Fatal(err)
	}
	packet := make([]byte, 1500)
	n, err := enc.Encode(make([]byte, 1920), packet)
	if err != nil {
		t.Fatal(err)
	}
	// RFC 6716 code 3 CBR packet: six 20 ms frames, 120 ms (5760 bytes of
	// PCM24k). The packet backlog (50) fills long before the 60 s byte budget.
	multi := []byte{packet[0]&0xfc | 3, 6}
	for i := 0; i < 6; i++ {
		multi = append(multi, packet[1:n]...)
	}
	for i := 0; i < 200; i++ {
		if err := a.track.WriteSample(media.Sample{Data: bytes.Clone(multi), Duration: 120 * time.Millisecond}); err != nil {
			t.Fatal(err)
		}
	}
	done := func() bool {
		select {
		case <-p.Done():
			return true
		default:
			return false
		}
	}
	eventually(t, "the peer to take all packets", done)
	queued := 0
	timeout := time.After(2 * time.Second)
drain:
	for {
		select {
		case b, ok := <-p.PCM():
			if !ok {
				break drain
			}
			queued += len(b)
		case <-timeout:
			break drain
		}
	}
	if queued > MaxPendingOutputAudio || p.Err() == nil {
		t.Fatalf("queued_audio_bytes=%d max=%d err=%v", queued, MaxPendingOutputAudio, p.Err())
	}
}

func TestProviderErrorClassification(t *testing.T) {
	for raw, fatal := range map[string]bool{
		`{"error":{"type":"invalid_request_error","code":"immutable_field_update","client_event_id":"e1"}}`:      false,
		`{"error":{"type":"invalid_request_error","code":"conversation_already_has_active_response"}}`:           false,
		`{"error":{"code":"some_rejection","client_event_id":"e2"}}`:                                             false,
		`{"error":{"type":"invalid_request_error","code":"responses_validation_failed","client_event_id":"e3"}}`: true,
		`{"error":{"type":"authentication_error","client_event_id":"e4"}}`:                                       true,
		`{"error":{"type":"server_error"}}`:                                                                      true,
		`{"error":{"type":"moderation","code":null}}`:                                                            true,
		`{"error":"not an object"}`:                                                                              true,
	} {
		if got, _ := providerErrorFatal([]byte(raw)); got != fatal {
			t.Errorf("%s: fatal=%v, want %v", raw, got, fatal)
		}
	}
}
