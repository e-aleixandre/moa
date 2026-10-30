package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

type providerHandler = func(core.Request) (<-chan core.AssistantEvent, error)

func finalTextResponse(text string) providerHandler {
	return func(core.Request) (<-chan core.AssistantEvent, error) {
		msg := core.Message{Role: "assistant", Content: []core.Content{core.TextContent(text)}, StopReason: "end_turn"}
		ch := make(chan core.AssistantEvent, 1)
		ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		close(ch)
		return ch, nil
	}
}

func callN(id int, tool, arg string) providerHandler {
	return toolBatchResponse(core.ToolCallContent(fmt.Sprintf("call-%d", id), tool, map[string]any{"x": arg}))
}

func requestText(m core.Message) string {
	var text string
	for _, c := range m.Content {
		text += c.Text
	}
	return text
}

func nudgeIndexes(msgs []core.AgentMessage) []int {
	var idx []int
	for i, m := range msgs {
		if m.Custom["source"] == "doom_loop_nudge" {
			idx = append(idx, i)
		}
	}
	return idx
}

func constTool(name string, runs *atomic.Int32, result func(n int32) string) core.Tool {
	return core.Tool{
		Name: name, Parameters: []byte(`{"type":"object"}`),
		Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
			n := runs.Add(1)
			return core.TextResult(result(n)), nil
		},
	}
}

func TestLoop_DoomLoopNudge_WarnsAtThirdStopsAtSixth(t *testing.T) {
	var runs atomic.Int32
	var handlers []providerHandler
	for i := 1; i <= 8; i++ {
		h := callN(i, "noop", "same")
		if i == 4 { // the request after the 3rd result must carry the nudge
			inner := h
			h = func(req core.Request) (<-chan core.AssistantEvent, error) {
				found := false
				for _, m := range req.Messages {
					if strings.Contains(requestText(m), "same tool call") {
						found = true
					}
				}
				if !found {
					t.Error("4th request lacks the doom-loop nudge")
				}
				return inner(req)
			}
		}
		if i == 3 {
			inner := h
			h = func(req core.Request) (<-chan core.AssistantEvent, error) {
				for _, m := range req.Messages {
					if strings.Contains(requestText(m), "same tool call") {
						t.Error("nudge arrived before the 3rd repetition finished")
					}
				}
				return inner(req)
			}
		}
		handlers = append(handlers, h)
	}
	provider := NewMockProvider(handlers...)
	ag := newTestAgent(provider, constTool("noop", &runs, func(int32) string { return "ok" }))
	ag.config.MaxTurns = 100

	_, err := ag.Run(context.Background(), "loop")
	if !errors.Is(err, ErrDoomLoop) {
		t.Fatalf("err=%v, want ErrDoomLoop", err)
	}
	if runs.Load() != 6 || providerCalls(provider) != 6 {
		t.Errorf("runs=%d provider calls=%d, want 6/6", runs.Load(), providerCalls(provider))
	}
	msgs := ag.Messages()
	idx := nudgeIndexes(msgs)
	if len(idx) != 1 {
		t.Fatalf("nudges=%d, want exactly 1", len(idx))
	}
	if prev := msgs[idx[0]-1]; prev.Role != "tool_result" || prev.ToolCallID != "call-3" {
		t.Errorf("nudge must follow the 3rd result, follows %s %s", prev.Role, prev.ToolCallID)
	}
	if text := messageText(msgs[idx[0]]); !strings.Contains(text, "3 times") || !strings.Contains(text, "change your approach") {
		t.Errorf("unexpected nudge text: %q", text)
	}
	// Every call, including the sixth, has its result: the history stays valid.
	toolResultsInOrder(t, msgs, "call-1", "call-2", "call-3", "call-4", "call-5", "call-6")
}

func TestLoop_DoomLoopNudge_DifferentResultsDoNotCount(t *testing.T) {
	var runs atomic.Int32
	var handlers []providerHandler
	for i := 1; i <= 12; i++ {
		handlers = append(handlers, callN(i, "poll", "same"))
	}
	handlers = append(handlers, finalTextResponse("done"))
	provider := NewMockProvider(handlers...)
	ag := newTestAgent(provider, constTool("poll", &runs, func(n int32) string { return fmt.Sprintf("progress %d", n) }))
	ag.config.MaxTurns = 100

	if _, err := ag.Run(context.Background(), "poll"); err != nil {
		t.Fatalf("changing results must stay free, got %v", err)
	}
	if n := len(nudgeIndexes(ag.Messages())); n != 0 {
		t.Errorf("nudges=%d, want 0", n)
	}
}

func TestLoop_DoomLoopNudge_ChangedResultResetsStreak(t *testing.T) {
	var runs atomic.Int32
	var handlers []providerHandler
	for i := 1; i <= 8; i++ {
		handlers = append(handlers, callN(i, "t", "same"))
	}
	handlers = append(handlers, finalTextResponse("done"))
	provider := NewMockProvider(handlers...)
	// Results: A A B B A A B B -> never three equal in a row.
	ag := newTestAgent(provider, constTool("t", &runs, func(n int32) string { return []string{"A", "A", "B", "B"}[(n-1)%4] }))
	ag.config.MaxTurns = 100
	if _, err := ag.Run(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if n := len(nudgeIndexes(ag.Messages())); n != 0 {
		t.Errorf("nudges=%d, want 0", n)
	}
}

func TestLoop_DoomLoopNudge_ExemptToolsNeverNudgeOrStop(t *testing.T) {
	var runs atomic.Int32
	var handlers []providerHandler
	for i := 1; i <= 10; i++ {
		handlers = append(handlers, callN(i, "bash_status", "j1"))
	}
	handlers = append(handlers, finalTextResponse("done"))
	provider := NewMockProvider(handlers...)
	ag := newTestAgent(provider, constTool("bash_status", &runs, func(int32) string { return "running" }))
	ag.config.MaxTurns = 100
	if _, err := ag.Run(context.Background(), "wait"); err != nil {
		t.Fatalf("exempt polling must not stop: %v", err)
	}
	if n := len(nudgeIndexes(ag.Messages())); n != 0 {
		t.Errorf("nudges=%d, want 0", n)
	}
}

func TestLoop_DoomLoopNudge_DifferentCallResetsStreak(t *testing.T) {
	var runs atomic.Int32
	var handlers []providerHandler
	id := 0
	add := func(arg string, n int) {
		for i := 0; i < n; i++ {
			id++
			handlers = append(handlers, callN(id, "noop", arg))
		}
	}
	add("a", 2)
	add("b", 1)
	add("a", 2) // 2+1+2: no streak reaches 3
	add("a", 1) // third in a row -> nudge
	handlers = append(handlers, finalTextResponse("done"))
	provider := NewMockProvider(handlers...)
	ag := newTestAgent(provider, constTool("noop", &runs, func(int32) string { return "ok" }))
	ag.config.MaxTurns = 100
	if _, err := ag.Run(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	msgs := ag.Messages()
	idx := nudgeIndexes(msgs)
	if len(idx) != 1 || msgs[idx[0]-1].ToolCallID != "call-6" {
		t.Fatalf("want one nudge after call-6, got %v", idx)
	}
}

func TestResultSignature_IgnoresAttachmentOccurrenceID(t *testing.T) {
	slot := func(id string) toolExecSlot {
		return toolExecSlot{tc: core.ToolCallContent("c", "read", nil), result: core.Result{Content: []core.Content{
			{Type: "image", MimeType: "image/png", AttachmentID: id, AttachmentSize: 42},
		}}}
	}
	if resultSignature([]toolExecSlot{slot("att_a")}) != resultSignature([]toolExecSlot{slot("att_b")}) {
		t.Error("same image under different occurrence IDs must have the same signature")
	}
	other := slot("att_a")
	other.result.Content[0].AttachmentSize = 43
	if resultSignature([]toolExecSlot{slot("att_a")}) == resultSignature([]toolExecSlot{other}) {
		t.Error("different image size must change the signature")
	}
}
