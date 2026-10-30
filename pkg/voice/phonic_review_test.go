package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Adversarial probes from the independent review (Sol, 30/10), kept as
// regression tests: loopback only, no external services.
func TestPhonicReviewStartupOverflowMustFail(t *testing.T) {
	f := newPhonicFake(t, func(c *websocket.Conn, _ map[string]any) {
		for i := 0; i < phonicStartupEvents+1; i++ {
			phonicSend(t, c, map[string]any{"type": "input_text", "text": fmt.Sprint(i)})
		}
		phonicSend(t, c, map[string]any{"type": "conversation_created", "conversation_id": "review"})
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	up, err := NewPhonic(PhonicConfig{URL: f.url, Credentials: fixedCred{}}).Open(ctx, CallSpec{})
	if up != nil {
		closeCtx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		defer func() { _ = up.Close(closeCtx, "") }()
	}
	if err == nil {
		t.Fatal("Open succeeded after 65 early transcripts; the 65th was silently discarded instead of failing startup")
	}
}

func TestPhonicReviewTranscriptOverflowMustFail(t *testing.T) {
	u := &phonicUpstream{createdOnce: true}
	raw, _ := json.Marshal(map[string]any{"type": "audio_chunk", "text": strings.Repeat("x", MaxTranscriptBytes+1), "audio": ""})
	events := u.parse("audio_chunk", raw)
	for _, e := range events {
		// Passed on whole, the common transcript limit ends the call.
		if e.Kind == EventFailed || e.Kind == EventTranscript && len(e.Text) == MaxTranscriptBytes+1 {
			var tr Transcript
			if e.Kind == EventTranscript && tr.Add(e) != ErrTranscriptFull {
				t.Fatal("the common transcript accepted an oversized segment")
			}
			return
		}
	}
	t.Fatalf("oversized spoken segment discarded with no failure: events=%d spoken=%d", len(events), u.spoken.Len())
}

func TestPhonicReviewErrorNamesAreNotAnAllowlist(t *testing.T) {
	var b bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&b, nil)))
	defer slog.SetDefault(old)
	const canary = "syntheticcredentialcanary1234567890"
	raw, _ := json.Marshal(map[string]any{"type": "error", "param_errors": map[string]any{canary: "ignored"}})
	phonicErrorFatal(raw)
	if strings.Contains(b.String(), canary) {
		t.Fatal("untrusted provider param_errors key reached logs verbatim (alphanumeric credential canary)")
	}
}

func TestPhonicReviewCancelledOpenIsBounded(t *testing.T) {
	configured := make(chan struct{})
	gate := make(chan struct{})
	f := newPhonicFake(t, func(_ *websocket.Conn, _ map[string]any) { close(configured); <-gate })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := NewPhonic(PhonicConfig{URL: f.url, Credentials: fixedCred{}}).Open(ctx, CallSpec{})
		done <- err
	}()
	select {
	case <-configured:
	case <-time.After(time.Second):
		close(gate)
		t.Fatal("config not received")
	}
	cancel()
	timely := false
	select {
	case <-done:
		timely = true
	case <-time.After(500 * time.Millisecond):
	}
	close(gate) // let the test server read and acknowledge Close; no worker survives the probe
	if !timely {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Open still stuck after releasing peer")
		}
		t.Fatal("cancelled Open stayed blocked >500ms until the peer was allowed to ACK: cleanup uses context.Background")
	}
}

type phonicReviewBody struct {
	io.ReadWriteCloser
	closeWritten chan struct{}
	streamClosed chan struct{}
	once         sync.Once
	closeOnce    sync.Once
}

func (b *phonicReviewBody) Write(p []byte) (int, error) {
	n, err := b.ReadWriteCloser.Write(p)
	if len(p) > 0 && p[0]&0x0f == 8 {
		b.once.Do(func() { close(b.closeWritten) })
	}
	return n, err
}

func (b *phonicReviewBody) Close() error {
	b.closeOnce.Do(func() { close(b.streamClosed) })
	return b.ReadWriteCloser.Close()
}

type phonicReviewTransport struct{ closeWritten, streamClosed chan struct{} }

func (rt phonicReviewTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err == nil && resp.StatusCode == http.StatusSwitchingProtocols {
		resp.Body = &phonicReviewBody{ReadWriteCloser: resp.Body.(io.ReadWriteCloser), closeWritten: rt.closeWritten, streamClosed: rt.streamClosed}
	}
	return resp, err
}

func phonicReviewPair(t *testing.T, parse func(string, []byte) []Event) (*wsUpstream, *websocket.Conn, <-chan struct{}, <-chan struct{}) {
	t.Helper()
	remote := make(chan *websocket.Conn, 1)
	stop := make(chan struct{})
	sent := make(chan struct{})
	streamClosed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		remote <- c
		<-stop
		_ = c.CloseNow()
	}))
	t.Cleanup(func() { close(stop); srv.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, abort, err := dialWSAbortable(ctx, &http.Client{Transport: phonicReviewTransport{sent, streamClosed}}, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	u := newWSUpstream(c, parse)
	u.closeWS = true
	u.abort = abort
	u.start()
	peer := <-remote
	t.Cleanup(func() {
		_ = peer.CloseNow()
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = u.close(closeCtx)
	})
	return u, peer, sent, streamClosed
}

func TestPhonicReviewBlockedEmitMustNotLeaveWriterAfterTeardown(t *testing.T) {
	var parsed atomic.Int64
	u, peer, closeWritten, streamClosed := phonicReviewPair(t, func(string, []byte) []Event {
		parsed.Add(1)
		return []Event{{Kind: EventTranscript, Role: "user", Text: "x", Final: true}}
	})
	for i := 0; i < upstreamEventQueue+1; i++ {
		wsSend(t, peer, map[string]any{"type": "input_text"})
	}
	eventually(t, "reader blocked in emit", func() bool { return parsed.Load() == upstreamEventQueue+1 })
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	returned := make(chan error, 1)
	go func() { returned <- u.close(ctx) }()
	select {
	case <-closeWritten:
	case <-time.After(time.Second):
		t.Fatal("no close frame")
	}
	<-returned
	joined := make(chan struct{})
	go func() { u.workers.Wait(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(300 * time.Millisecond):
		streamStillOpen := true
		select {
		case <-streamClosed:
			streamStillOpen = false
		default:
		}
		_ = peer.CloseNow()
		<-joined
		t.Fatalf("Close returned at its deadline but writer remained alive >300ms; underlying stream still open=%v: cancelled emit does not cancel the library's background close-handshake Read", streamStillOpen)
	}
}

func TestPhonicReviewNoAdmissionsAfterCloseFrame(t *testing.T) {
	u, peer, closeWritten, _ := phonicReviewPair(t, func(string, []byte) []Event { return nil })
	p := &phonicUpstream{wsUpstream: u}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	returned := make(chan error, 1)
	go func() { returned <- u.close(ctx) }()
	select {
	case <-closeWritten:
	case <-time.After(time.Second):
		t.Fatal("no close frame")
	}
	audioErr := p.SendAudio(context.Background(), voiced(960))
	contextErr := p.InjectContext(context.Background(), "too late")
	_ = peer.CloseNow()
	<-returned
	if audioErr == nil || contextErr == nil {
		t.Fatalf("new data accepted after close was on the wire: audio=%v context=%v pending_audio=%d queued=%d", audioErr, contextErr, u.pendingAudio.Load(), len(u.out))
	}
}

func TestPhonicReviewPartialEnqueueBudget(t *testing.T) {
	u := newWSUpstream(nil, nil)
	defer u.cancel()
	p := &phonicUpstream{wsUpstream: u}
	for i := 0; i < upstreamWriteQueue-2; i++ {
		if err := u.send(map[string]any{"type": "filler"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.SendAudio(context.Background(), voiced(MaxPCMFrameBytes)); err != errQueueFull {
		t.Fatalf("partial send: %v", err)
	}
	if n := u.pendingAudio.Load(); n != 2*phonicChunkBytes {
		t.Fatalf("pending=%d want=%d", n, 2*phonicChunkBytes)
	}
	var bytes int64
	for len(u.out) > 0 {
		f := <-u.out
		bytes += f.audio
		u.pendingAudio.Add(-f.audio)
	}
	if bytes != 1920 || u.pendingAudio.Load() != 0 {
		t.Fatalf("reservation leaked: bytes=%d pending=%d", bytes, u.pendingAudio.Load())
	}
	if err := p.SendAudio(context.Background(), voiced(MaxPCMFrameBytes)); err != nil {
		t.Fatal(err)
	}
}

func TestPhonicReviewNullableArgumentsPreserveCurrentToolSemantics(t *testing.T) {
	for _, raw := range []string{`{"path":null}`, `{"session_id":null}`, `{"ask_id":"id","answer":null,"answers":null}`, `{"decided":null,"passed":null,"open":null}`} {
		var before, after map[string]any
		_ = json.Unmarshal([]byte(raw), &before)
		_ = json.Unmarshal(dropNulls([]byte(raw)), &after)
		for k, v := range before {
			if v == nil {
				if got := after[k]; got != nil {
					t.Fatalf("%s: %v", k, got)
				}
			}
		}
	}
	var specs = ToolSpecs()
	for _, s := range specs {
		var schema map[string]any
		_ = json.Unmarshal(s.Parameters, &schema)
		for name, p := range schema["properties"].(map[string]any) {
			typ := p.(map[string]any)["type"]
			if typ != "string" && typ != "array" {
				t.Fatalf("review null assumptions for %s.%s (%v)", s.Name, name, typ)
			}
		}
	}
}

func TestPhonicReviewNormalCloseWaitsForDelayedACK(t *testing.T) {
	u, peer, closeWritten, _ := phonicReviewPair(t, func(string, []byte) []Event { return nil })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- u.close(ctx) }()
	select {
	case <-closeWritten:
	case <-time.After(time.Second):
		t.Fatal("close not written")
	}
	select {
	case err := <-done:
		t.Fatalf("close returned without ACK: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if u.ctx.Err() != nil {
		t.Fatal("reader was cancelled before ACK")
	}
	// Reading the peer's Close makes coder/websocket send the automatic ACK.
	_, _, _ = peer.Read(ctx)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPhonicReviewOnSilenceUsesPhonicOpeningAndHeardSuppressesIt(t *testing.T) {
	old := SetOpenSilence(70 * time.Millisecond)
	defer SetOpenSilence(old)
	for _, heard := range []bool{false, true} {
		t.Run(fmt.Sprint(heard), func(t *testing.T) {
			f := newPhonicFake(t, created(t))
			up := openPhonic(t, f, OpenOnSilence)
			f.next(t)
			if heard {
				p := up.(*phonicUpstream)
				phonicSend(t, <-f.conn, map[string]any{"type": "input_text", "text": "hola"})
				nextEvent(t, up)
				if !p.heard.Load() {
					t.Fatal("speech not marked heard")
				}
			}
			select {
			case m := <-f.frames:
				if heard || m["type"] != "generate_reply" {
					t.Fatalf("unexpected opening=%v heard=%v", m, heard)
				}
			case <-time.After(250 * time.Millisecond):
				if !heard {
					t.Fatal("no opening")
				}
			}
		})
	}
}

func TestPhonicReviewConcurrentAudioReservationStaysExact(t *testing.T) {
	for range 100 {
		u := newWSUpstream(nil, nil)
		p := &phonicUpstream{wsUpstream: u}
		var wg sync.WaitGroup
		for range 16 {
			wg.Add(1)
			go func() { defer wg.Done(); _ = p.SendAudio(context.Background(), voiced(MaxPCMFrameBytes)) }()
		}
		wg.Wait()
		pending := u.pendingAudio.Load()
		var queued int64
		for len(u.out) > 0 {
			queued += (<-u.out).audio
		}
		u.cancel()
		if pending != queued || pending < 0 || pending > MaxPendingAudio {
			t.Fatalf("budget mismatch: pending=%d queued=%d", pending, queued)
		}
	}
}
