package voice

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func reviewWireFrame(t *testing.T, c *websocket.Conn, d time.Duration) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	_, raw, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func reviewNoWireFrame(t *testing.T, c *websocket.Conn, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	_, raw, err := c.Read(ctx)
	if err == nil {
		t.Fatalf("unexpected frame: %s", raw)
	}
	if ctx.Err() == nil {
		t.Fatalf("unexpected read failure: %v", err)
	}
}

func TestReviewVoice_TimerCannotSpeakAfterCloseBegins(t *testing.T) {
	prev := SetOpenSilence(100 * time.Millisecond)
	defer SetOpenSilence(prev)
	up, remote := wsPair(t, true)
	if err := wsOf(up).open(OpenOnSilence); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- up.Close(ctx, "hangup") }()
	first := reviewWireFrame(t, remote, 500*time.Millisecond)
	if first["type"] != "session.close" {
		t.Fatalf("first frame = %v", first)
	}
	// The provider has accepted the close frame, but confirmation has not
	// arrived yet. The greeting timer must not create a fresh response now.
	ctxRead, cancelRead := context.WithTimeout(context.Background(), 300*time.Millisecond)
	_, raw, err := remote.Read(ctxRead)
	cancelRead()
	if err == nil {
		var second map[string]any
		_ = json.Unmarshal(raw, &second)
		wsSend(t, remote, map[string]any{"type": "session.closed"})
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		t.Fatalf("RED: frame was sent AFTER session.close: %v -> %v", first, second)
	}
	if ctxRead.Err() == nil {
		t.Fatalf("unexpected provider read error: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestReviewVoice_HeardBeforeDeadlineSuppressesOpening(t *testing.T) {
	prev := SetOpenSilence(80 * time.Millisecond)
	defer SetOpenSilence(prev)
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "xai", true: "live"}[live], func(t *testing.T) {
			up, remote := wsPair(t, live)
			u := wsOf(up)
			if err := u.open(OpenOnSilence); err != nil {
				t.Fatal(err)
			}
			frame := map[string]any{"type": "input_audio_buffer.speech_started"}
			if live {
				frame = map[string]any{"type": "session.input_transcript.delta", "delta": "hola"}
			}
			wsSend(t, remote, frame)
			e, _ := nextEvent(t, up)
			if !u.heard.Load() {
				t.Fatalf("heard did not complete before the deadline: %+v", e)
			}
			reviewNoWireFrame(t, remote, 3*OpenSilence())
		})
	}
}

func TestReviewVoice_SilentTimerOnlyOnce(t *testing.T) {
	prev := SetOpenSilence(60 * time.Millisecond)
	defer SetOpenSilence(prev)
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "xai", true: "live"}[live], func(t *testing.T) {
			up, remote := wsPair(t, live)
			u := wsOf(up)
			if err := u.open(OpenOnSilence); err != nil {
				t.Fatal(err)
			}
			first := reviewWireFrame(t, remote, 500*time.Millisecond)
			if first["type"] != "response.create" {
				t.Fatalf("opening = %v", first)
			}
			if u.heard.Load() {
				t.Fatal("silent first turn falsely marked as user speech")
			}
			reviewNoWireFrame(t, remote, 3*OpenSilence())
		})
	}
}
