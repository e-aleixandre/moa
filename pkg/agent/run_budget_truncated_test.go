package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// A max_tokens response stopped by the run budget still pairs its truncated
// tool call with an error result, without running the tool.
func TestTruncatedToolCallPairedOnBudgetStop(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(release) }) }
	defer open()
	type outcome struct {
		msgs []core.AgentMessage
		err  error
	}
	done := make(chan outcome, 1)
	spawn := spawnTool("spawn", func(ctx context.Context) string {
		child, _ := newBudgetChild(t, ctx, nil, func(core.Request) (<-chan core.AssistantEvent, error) {
			close(started)
			<-release
			m := core.Message{Role: "assistant", StopReason: "max_tokens", Content: []core.Content{core.ToolCallContent("truncated-tool", "noop", map[string]any{})}}
			ch := make(chan core.AssistantEvent, 2)
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &m}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &m}
			close(ch)
			return ch, nil
		})
		go func() { msgs, err := child.Run(context.Background(), "work"); done <- outcome{msgs, err} }()
		return "launched"
	})
	parent := newBudgetParent(t, .10, NewMockProvider(
		toolCallWithUsage("p1", "spawn", map[string]any{}, nil),
		func(req core.Request) (<-chan core.AssistantEvent, error) {
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Error("child never entered provider")
			}
			return textResponseWithUsage("expensive caller", usageOf(.20))(req)
		},
	), spawn)
	_, err := parent.Run(context.Background(), "go")
	open()
	if budgetError(err) == nil {
		t.Fatalf("caller error=%v, want budget error", err)
	}
	select {
	case out := <-done:
		calls, results := 0, 0
		for _, m := range out.msgs {
			for _, c := range m.Content {
				if c.Type == "tool_call" {
					calls++
				}
			}
			if m.Role == "tool_result" {
				results++
			}
		}
		if budgetError(out.err) == nil {
			t.Fatalf("child error=%v, want budget error", out.err)
		}
		if calls != 1 || results != 1 {
			t.Fatalf("child stopped with %v but saved %d tool calls / %d tool results; the max_tokens budget checkpoint returned before pairing the truncated call", out.err, calls, results)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("child did not finish")
	}
}
