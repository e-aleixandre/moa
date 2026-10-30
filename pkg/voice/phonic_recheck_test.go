package voice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Probes from the independent recheck (Sol, 30/10), kept as regression tests.
func TestPhonicRecheckStartupBytesMustFail(t *testing.T) {
	for _, kind := range []string{"text", "audio", "tool"} {
		t.Run(kind, func(t *testing.T) {
			var frame map[string]any
			switch kind {
			case "text":
				frame = map[string]any{"type": "input_text", "text": strings.Repeat("x", phonicStartupBytes+1)}
			case "audio":
				frame = map[string]any{"type": "audio_chunk", "audio": base64.StdEncoding.EncodeToString(voiced(phonicStartupBytes + 2))}
			case "tool":
				frame = map[string]any{"type": "tool_call", "tool_call_id": "recheck", "tool_name": "ask_session", "parameters": map[string]any{"question": strings.Repeat("x", phonicStartupBytes+1)}}
			}
			f := newPhonicFake(t, func(c *websocket.Conn, _ map[string]any) {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				b, _ := json.Marshal(frame)
				if err := c.Write(ctx, websocket.MessageText, b); err != nil {
					return
				}
				_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"conversation_created","conversation_id":"recheck"}`))
			})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			up, err := NewPhonic(PhonicConfig{URL: f.url, Credentials: fixedCred{}}).Open(ctx, CallSpec{Opening: OpenWait})
			if up != nil {
				closeCtx, stop := context.WithTimeout(context.Background(), time.Second)
				defer stop()
				defer func() { _ = up.Close(closeCtx, "") }()
			}
			if err == nil {
				ev, _ := nextEvent(t, up)
				n := len(ev.PCM) + len(ev.Text)
				for _, tool := range ev.Tools {
					n += len(tool.ID) + len(tool.Name) + len(tool.Arguments)
				}
				t.Fatalf("Open succeeded retaining %d %s bytes, above startup budget %d", n, kind, phonicStartupBytes)
			}
		})
	}
}

func TestPhonicRecheckCumulativeTranscriptLimit(t *testing.T) {
	u := &phonicUpstream{createdOnce: true}
	var tr Transcript
	for i, text := range []string{strings.Repeat("x", MaxTranscriptBytes), "y"} {
		raw, _ := json.Marshal(map[string]any{"type": "audio_chunk", "text": text})
		events := u.parse("audio_chunk", raw)
		if len(events) != 1 || events[0].Kind != EventTranscript || events[0].Text != text {
			t.Fatalf("delta %d was not preserved whole", i)
		}
		err := tr.Add(events[0])
		if i == 0 && err != nil || i == 1 && err != ErrTranscriptFull {
			t.Fatalf("delta %d: transcript error=%v", i, err)
		}
		if u.spoken.Len() > MaxTranscriptBytes {
			t.Fatal("spoken grew above its cap")
		}
	}
}
