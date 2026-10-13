package subagent

import (
	"context"
	"math"
	"strings"
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
		OnChildEnd: func(jobID, _ string, _ bool, _, _, _ string, _ time.Time, _ *core.Usage, costUSD, runCostUSD float64) {
			mu.Lock()
			if firstID == "" {
				firstID = jobID
				store = ResumedTranscript{Messages: jobs.messages(jobID)}
			}
			cumulative = costUSD
			ledger += runCostUSD
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

// Compaction rewrites a resumed child's history, so the cost of the final
// history can fall below what was inherited. The run's own spend must still be
// exact, and never negative.
func TestResumedChildRunCostSurvivesCompaction(t *testing.T) {
	model := core.Model{ID: "default", Provider: "mock", MaxInput: 200000, Pricing: &core.Pricing{Input: 1e3}}
	big := strings.Repeat("old context ", 4000)
	var seed []core.AgentMessage
	for i := range 6 {
		assistant := core.Message{Role: "assistant", Content: []core.Content{core.TextContent(big)},
			StopReason: "end_turn", Model: "default", Provider: "mock"}
		if i == 1 {
			assistant.Usage = &core.Usage{Input: 60000} // the whole inherited bill: $60
		}
		seed = append(seed, core.WrapMessage(core.NewUserMessage(big)), core.WrapMessage(assistant))
	}
	provider := newMockProvider(textResponse("summary of old work"), usageTextResponse("done", 2000), usageTextResponse("done", 2000))

	var mu sync.Mutex
	var cumulative, run float64
	var ended bool
	cfg := Config{
		DefaultModel:       model,
		AppCtx:             context.Background(),
		ParentTools:        core.NewRegistry(),
		ProviderFactory:    func(core.Model) (core.Provider, error) { return provider, nil },
		InheritedCompactAt: func() int { return 50000 },
		TranscriptLoader:   func(string) (ResumedTranscript, error) { return ResumedTranscript{Messages: seed}, nil },
		OnChildEnd: func(_, _ string, _ bool, _, _, _ string, _ time.Time, _ *core.Usage, costUSD, runCostUSD float64) {
			mu.Lock()
			cumulative, run, ended = costUSD, runCostUSD, true
			mu.Unlock()
		},
	}
	sub := newSubagent(cfg, newJobStore())
	res, err := sub.Execute(context.Background(), map[string]any{"task": "go", "resume": "sa-old"}, nil)
	if err != nil || res.IsError {
		t.Fatalf("resume: %v %s", err, textOf(res))
	}
	waitFor(t, 2*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return ended })
	mu.Lock()
	defer mu.Unlock()
	if cumulative >= 60 {
		t.Fatalf("compaction did not rewrite the history (cumulative=%v); the setup no longer exercises it", cumulative)
	}
	if math.Abs(run-2) > 1e-9 {
		t.Errorf("run cost = %v, want 2 (history cost fell to %v after compaction)", run, cumulative)
	}
}
