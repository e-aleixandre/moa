package subagent

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// usageTextResponse replies with text and a fixed Input usage so the child's
// cost is deterministic.
func usageTextResponse(text string, input int) func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
	return func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 3)
		msg := core.Message{
			Role: "assistant", Content: []core.Content{core.TextContent(text)},
			StopReason: "end_turn", Timestamp: time.Now().Unix(),
			Usage: &core.Usage{Input: input},
		}
		ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
		ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		close(ch)
		return ch, nil
	}
}

// A resumed child's persisted/cumulative cost includes what was already paid
// before the resume. Whatever consumes terminal costs to build a ledger must be
// able to tell the new spend apart, or every resume re-charges the old spend.
func TestResumedChildReportsOnlyNewSpendToLedgers(t *testing.T) {
	// $1 per input token, so costs are exact integers.
	model := core.Model{ID: "default", Provider: "mock", Pricing: &core.Pricing{Input: 1e6}}
	provider := newMockProvider(usageTextResponse("first", 3), usageTextResponse("second", 2))

	jobs := newJobStore()
	var mu sync.Mutex
	var cumulative, ledger float64
	var firstID string
	var store ResumedTranscript
	cfg := Config{
		DefaultModel:    model,
		AppCtx:          context.Background(),
		ParentTools:     core.NewRegistry(),
		ProviderFactory: func(core.Model) (core.Provider, error) { return provider, nil },
		TranscriptLoader: func(string) (ResumedTranscript, error) {
			mu.Lock()
			defer mu.Unlock()
			return store, nil
		},
		OnChildEnd: func(jobID, _ string, _ bool, _, _, _ string, _ time.Time, _ *core.Usage, costUSD, priorCostUSD float64) {
			mu.Lock()
			if firstID == "" {
				firstID = jobID
				store = ResumedTranscript{Messages: jobs.messages(jobID)}
			}
			cumulative = costUSD
			ledger += costUSD - priorCostUSD
			mu.Unlock()
		},
	}
	sub := newSubagent(cfg, jobs)

	if _, err := sub.Execute(context.Background(), map[string]any{"task": "one"}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return firstID != "" })
	mu.Lock()
	id := firstID
	mu.Unlock()

	res, err := sub.Execute(context.Background(), map[string]any{"task": "two", "resume": id}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("resume failed: %s", textOf(res))
	}

	waitFor(t, 2*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return cumulative == 5 })
	mu.Lock()
	defer mu.Unlock()
	if math.Abs(cumulative-5) > 1e-9 {
		t.Errorf("cumulative cost of the resumed child = %v, want 5 (3 before + 2 after)", cumulative)
	}
	if math.Abs(ledger-5) > 1e-9 {
		t.Errorf("sum of ledger costs across both runs = %v, want 5 (real spend), not 8", ledger)
	}
}
