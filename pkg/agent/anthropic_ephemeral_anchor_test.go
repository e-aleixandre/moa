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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
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

// messageBreakpoints lists "message.block" for every cache_control in the
// messages and whether any sits on a block carrying the inventory text.
func messageBreakpoints(t *testing.T, body []byte, inventory string) (at []string, onInventory bool) {
	t.Helper()
	var req struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	for i, m := range req.Messages {
		for j, b := range m.Content {
			if _, ok := b["cache_control"]; !ok {
				continue
			}
			at = append(at, fmt.Sprintf("%d.%d", i, j))
			if text, _ := b["text"].(string); inventory != "" && strings.Contains(text, inventory) {
				onInventory = true
			}
		}
	}
	return at, onInventory
}

// lastDurableBlock is "message.block" of the last block of the last user
// message that is not the inventory: where the conversation breakpoint belongs.
func lastDurableBlock(t *testing.T, body []byte, inventory string) string {
	t.Helper()
	var req struct {
		Messages []struct {
			Role    string           `json:"role"`
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role != "user" {
			continue
		}
		for j := len(req.Messages[i].Content) - 1; j >= 0; j-- {
			if text, _ := req.Messages[i].Content[j]["text"].(string); inventory == "" || !strings.Contains(text, inventory) {
				return fmt.Sprintf("%d.%d", i, j)
			}
		}
	}
	return ""
}

func countIn(body []byte, s string) int { return bytes.Count(body, []byte(s)) }

func newAnthropicAnchorAgent(t *testing.T, prov core.Provider, inventory func() string) *Agent {
	t.Helper()
	reg := core.NewRegistry()
	if err := reg.Register(core.Tool{Name: "noop", Description: "noop", Parameters: []byte(`{"type":"object"}`), Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
		return core.TextResult("ok"), nil
	}}); err != nil {
		t.Fatal(err)
	}
	ag, err := New(AgentConfig{Provider: prov, Model: anthropicTailModel(t), ThinkingLevel: "high", SystemPrompt: "s", Tools: reg,
		MaxTurns: 10, MaxToolCallsPerTurn: 5, MaxRunDuration: 10 * time.Second,
		EphemeralContext: func() *core.Message {
			text := inventory()
			if text == "" {
				return nil
			}
			m := core.NewUserMessage(text)
			return &m
		}})
	if err != nil {
		t.Fatal(err)
	}
	return ag
}

func sendAnchor(t *testing.T, ag *Agent, text string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := ag.Send(ctx, text); err != nil {
		t.Fatal(err)
	}
}

// Anthropic binds Opus's signed thinking to the exact prefix before it and,
// with drop_block, discards it when that prefix changes. An unchanged
// inventory must therefore leave every earlier turn byte-identical, inside a
// tool loop and across runs, with the conversation breakpoint still on the
// last durable block and never on the inventory.
func TestAnthropicUnchangedInventoryKeepsPrefix(t *testing.T) {
	const inv = "OPEN-QUESTIONS-INVENTORY"
	prov, bodies := anthropicAnchorServer(t, "tool", "tool", "text", "tool", "text")
	ag := newAnthropicAnchorAgent(t, prov, func() string { return inv })
	sendAnchor(t, ag, "first task")
	sendAnchor(t, ag, "second task")
	got := bodies()
	if len(got) != 5 {
		t.Fatalf("got %d requests, want 5", len(got))
	}
	for i := 1; i < len(got); i++ {
		if at := firstRewrite(wireMessages(t, got[i-1]), wireMessages(t, got[i])); at >= 0 {
			t.Errorf("request %d rewrote message %d of request %d:\nbefore %s\nafter  %s", i+1, at, i, wireMessages(t, got[i-1])[at], wireMessages(t, got[i])[at])
		}
	}
	for i, b := range got {
		if n := countIn(b, inv); n != 1 {
			t.Errorf("request %d carries the inventory %d times, want 1", i+1, n)
		}
		at, onInv := messageBreakpoints(t, b, inv)
		if onInv {
			t.Errorf("request %d put a breakpoint on the inventory", i+1)
		}
		if want := lastDurableBlock(t, b, inv); len(at) != 1 || at[0] != want {
			t.Errorf("request %d message breakpoints = %v, want [%s]", i+1, at, want)
		}
	}
}

// A changed inventory is new information: it is sent, once, and the prefix
// breaks from where the old one sat (accepted by the owner). From then on the
// new block is anchored again.
func TestAnthropicChangedInventoryReanchors(t *testing.T) {
	prov, bodies := anthropicAnchorServer(t, "tool", "tool", "tool", "text")
	// The inventory changes before the third request leaves.
	ag := newAnthropicAnchorAgent(t, prov, func() string {
		if len(bodies()) >= 2 {
			return "INVENTORY-B"
		}
		return "INVENTORY-A"
	})
	sendAnchor(t, ag, "task")
	got := bodies()
	if len(got) != 4 {
		t.Fatalf("got %d requests, want 4", len(got))
	}
	msgs := make([][][]byte, len(got))
	for i := range got {
		msgs[i] = wireMessages(t, got[i])
	}
	if at := firstRewrite(msgs[0], msgs[1]); at >= 0 {
		t.Errorf("unchanged inventory: request 2 rewrote message %d", at)
	}
	if at := firstRewrite(msgs[1], msgs[2]); at < 0 {
		t.Error("changed inventory: request 3 still carries the old block in place")
	}
	if countIn(got[2], "INVENTORY-A") != 0 || countIn(got[2], "INVENTORY-B") != 1 {
		t.Errorf("request 3 must carry only the new inventory: %s", got[2])
	}
	if at := firstRewrite(msgs[2], msgs[3]); at >= 0 {
		t.Errorf("re-anchored inventory: request 4 rewrote message %d", at)
	}
	for i, b := range got {
		if _, onInv := messageBreakpoints(t, b, "INVENTORY-"); onInv {
			t.Errorf("request %d put a breakpoint on the inventory", i+1)
		}
	}
}

// An inventory that empties (the last question answered) leaves no stale copy
// behind, and what follows extends the request without it.
func TestAnthropicVanishedInventoryLeavesNoCopy(t *testing.T) {
	inv := "INVENTORY-A"
	prov, bodies := anthropicAnchorServer(t, "tool", "tool", "tool", "text")
	// The inventory empties before the third request leaves.
	ag := newAnthropicAnchorAgent(t, prov, func() string {
		if len(bodies()) >= 2 {
			return ""
		}
		return inv
	})
	sendAnchor(t, ag, "task")
	got := bodies()
	if len(got) != 4 {
		t.Fatalf("got %d requests, want 4", len(got))
	}
	if at := firstRewrite(wireMessages(t, got[0]), wireMessages(t, got[1])); at >= 0 {
		t.Errorf("unchanged inventory: request 2 rewrote message %d", at)
	}
	for _, i := range []int{2, 3} {
		if countIn(got[i], inv) != 0 {
			t.Errorf("request %d still carries the vanished inventory", i+1)
		}
	}
	if at := firstRewrite(wireMessages(t, got[2]), wireMessages(t, got[3])); at >= 0 {
		t.Errorf("request 4 rewrote message %d of request 3", at)
	}
}
