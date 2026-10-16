package voice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/e-aleixandre/moa/pkg/core"
)

type nestedUsageCred struct{}

func (nestedUsageCred) Resolve(context.Context) (Credential, error) {
	return Credential{Token: "SYNTHETIC", AccountID: "SYNTHETIC"}, nil
}
func (nestedUsageCred) Refresh(context.Context, Credential) (Credential, error) {
	return Credential{}, nil
}

func nestedUsageLive(t *testing.T) (Upstream, *websocket.Conn) {
	t.Helper()
	remote := make(chan *websocket.Conn, 1)
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		if _, _, err = c.Read(context.Background()); err != nil {
			_ = c.CloseNow()
			return
		}
		b, _ := json.Marshal(map[string]any{"type": "session.started"})
		if c.Write(context.Background(), websocket.MessageText, b) != nil {
			_ = c.CloseNow()
			return
		}
		remote <- c
		<-done
	}))
	t.Cleanup(func() { close(done); srv.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	a := NewLiveWS(LiveWSConfig{URL: "ws" + strings.TrimPrefix(srv.URL, "http"), Credentials: nestedUsageCred{}})
	up, err := a.Open(ctx, CallSpec{Voice: "marin", BackendModel: core.Model{ID: "gpt-6-luna"}, Instructions: CommonInstructions, Tools: ToolSpecs()})
	if err != nil {
		t.Fatal(err)
	}
	c := <-remote
	t.Cleanup(func() {
		_ = c.CloseNow()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = up.Close(ctx, "")
	})
	return up, c
}

func nestedUsageEmit(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
}

func nestedCompleted(in, out, total int) map[string]any {
	return map[string]any{"type": "response.event", "delegation_id": "d", "event": map[string]any{"type": "response.completed", "response": map[string]any{"id": "r", "usage": map[string]any{"input_tokens": in, "output_tokens": out, "total_tokens": total}}}}
}

// Delegated Responses usage is billed apart from session seconds: it must be
// summed across responses, survive Close, and not erase the seconds snapshot.
func TestLiveNestedResponseUsage(t *testing.T) {
	up, c := nestedUsageLive(t)
	nestedUsageEmit(t, c, map[string]any{"type": "session.usage.updated", "usage": map[string]any{"seconds": 42.0}})
	nestedUsageEmit(t, c, nestedCompleted(7, 11, 18))
	nestedUsageEmit(t, c, nestedCompleted(3, 4, 7))
	nestedUsageEmit(t, c, map[string]any{"type": "session.output_transcript.delta", "delta": "barrier"})
	var last *Usage
	for {
		select {
		case e := <-up.Events():
			if e.Usage != nil {
				last = e.Usage
			}
			if e.Kind == EventTranscript && e.Text == "barrier" {
				goto done
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no barrier")
		}
	}
done:
	_ = c.CloseNow()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := up.Close(ctx, ""); err != nil {
		t.Fatal(err)
	}
	check := func(what string, u *Usage) {
		t.Helper()
		if u == nil || u.InputTokens == nil || u.OutputTokens == nil || u.TotalTokens == nil || u.Seconds == nil ||
			*u.InputTokens != 10 || *u.OutputTokens != 15 || *u.TotalTokens != 25 || *u.Seconds != 42 {
			t.Fatalf("%s: want tokens 10/15/25 and 42s, got %+v", what, u)
		}
	}
	check("last event", last)
	check("retained after Close", up.(UsageReporter).Usage())
}
