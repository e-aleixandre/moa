package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/provider/anthropic"
)

// anthropicAnchorServer answers each request with the next scripted turn and
// keeps every request body as sent. A turn is "tool" (signed thinking plus a
// noop call) or "text" (signed thinking plus a final answer).
func anthropicAnchorServer(t *testing.T, script ...string) (*anthropic.Anthropic, func() [][]byte) {
	t.Helper()
	var mu sync.Mutex
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		n := len(bodies)
		bodies = append(bodies, b)
		mu.Unlock()
		kind := "text"
		if n < len(script) {
			kind = script[n]
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, anthropicAnchorSSE(n, kind == "tool"))
	}))
	t.Cleanup(server.Close)
	return anthropic.NewWithBaseURL("sk-ant-oat-synthetic-not-a-credential", server.URL), func() [][]byte {
		mu.Lock()
		defer mu.Unlock()
		return append([][]byte(nil), bodies...)
	}
}

func anthropicAnchorSSE(n int, tool bool) string {
	ev := func(name, data string) string { return "event: " + name + "\ndata: " + data + "\n\n" }
	s := ev("message_start", `{"type":"message_start","message":{"id":"msg_`+fmt.Sprint(n)+`","role":"assistant","model":"claude-opus-5-5","usage":{"input_tokens":5,"output_tokens":1}}}`)
	s += ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`)
	s += ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-`+fmt.Sprint(n)+`"}}`)
	s += ev("content_block_stop", `{"type":"content_block_stop","index":0}`)
	reason := "end_turn"
	if tool {
		reason = "tool_use"
		s += ev("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_`+fmt.Sprint(n)+`","name":"noop","input":{}}}`)
		s += ev("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"n\":`+fmt.Sprint(n)+`}"}}`)
	} else {
		s += ev("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`)
		s += ev("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"done"}}`)
	}
	s += ev("content_block_stop", `{"type":"content_block_stop","index":1}`)
	s += ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"`+reason+`"},"usage":{"output_tokens":2}}`)
	return s + ev("message_stop", `{"type":"message_stop"}`)
}

// wireMessages returns each message of a request body exactly as sent, minus
// the blocks' own cache_control, which is the only part of a turn that may
// legitimately move between requests.
func wireMessages(t *testing.T, body []byte) [][]byte {
	t.Helper()
	var req struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	out := make([][]byte, len(req.Messages))
	for i, m := range req.Messages {
		out[i] = cacheControlLast.ReplaceAll(cacheControlFirst.ReplaceAll(m, []byte("{")), nil)
	}
	return out
}

var (
	cacheControlFirst = regexp.MustCompile(`\{"cache_control":\{[^{}]*\},?`)
	cacheControlLast  = regexp.MustCompile(`,"cache_control":\{[^{}]*\}`)
)

// firstRewrite is the index of the first message of prev that next does not
// carry byte for byte, or -1 when next extends prev.
func firstRewrite(prev, next [][]byte) int {
	for i := range prev {
		if i >= len(next) || !bytes.Equal(prev[i], next[i]) {
			return i
		}
	}
	return -1
}

func sendAnchor(t *testing.T, ag *Agent, text string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := ag.Send(ctx, text); err != nil {
		t.Fatal(err)
	}
}
