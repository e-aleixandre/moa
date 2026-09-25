package subagent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

// A child's quick call batched with a slow one ends first, but its result
// reaches the child's history only with the whole batch. A client that joins
// in between rebuilds the child's transcript from that history, so the job
// has to keep the ended call, result included, until the batch lands.
func TestChildCallEndedAheadOfItsBatchIsKeptUntilItsResultLands(t *testing.T) {
	release := make(chan struct{})
	quick := core.Tool{
		Name:       "quick",
		Effect:     core.EffectReadOnly,
		Parameters: json.RawMessage(`{"type":"object"}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			return core.TextResult("quick out"), nil
		},
	}
	slow := core.Tool{
		Name:       "slow",
		Effect:     core.EffectReadOnly,
		Parameters: json.RawMessage(`{"type":"object"}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return core.TextResult("slow out"), nil
		},
	}
	batch := func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 5)
		go func() {
			defer close(ch)
			msg := core.Message{
				Role: "assistant",
				Content: []core.Content{
					core.ToolCallContent("call-quick", "quick", map[string]any{}),
					core.ToolCallContent("call-slow", "slow", map[string]any{}),
				},
				StopReason: "tool_use",
				Timestamp:  time.Now().Unix(),
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		}()
		return ch, nil
	}
	provider := newMockProvider(batch, textResponse("child done"))
	sub, _, _, jobs := newSubagentToolsWithStore(t, Config{
		DefaultModel:    core.Model{ID: "default", Provider: "mock"},
		ProviderFactory: func(model core.Model) (core.Provider, error) { return provider, nil },
	}, quick, slow)

	res, err := sub.Execute(context.Background(), map[string]any{
		"task": "run both", "async": true, "tools": []any{"quick", "slow"},
	}, nil)
	if err != nil || res.IsError {
		t.Fatalf("start child = %+v, %v", res, err)
	}
	jobID := jobIDFromResult(t, res)
	j := &Jobs{store: jobs}

	waitFor(t, 2*time.Second, func() bool { return len(j.EndedTools(jobID)) == 1 })
	got := j.EndedTools(jobID)[0]
	if got.ToolCallID != "call-quick" || got.ToolName != "quick" || got.Phase != bus.LiveToolPhaseDone || got.Result != "quick out" {
		t.Fatalf("ended call while its batch runs = %+v, want call-quick done with its result", got)
	}

	close(release)
	waitFor(t, 2*time.Second, func() bool {
		info, ok := jobs.get(jobID)
		if !ok {
			return false
		}
		info.mu.Lock()
		defer info.mu.Unlock()
		return info.status == statusCompleted
	})
	if left := j.EndedTools(jobID); len(left) != 0 {
		t.Fatalf("ended calls after their batch landed = %+v, want none: the history holds them", left)
	}
}
