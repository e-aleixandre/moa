package subagent

import (
	"context"
	"sync"
	"testing"
	"time"

	agentcontext "github.com/e-aleixandre/moa/pkg/context"
	"github.com/e-aleixandre/moa/pkg/core"
)

// A resumed child sends the prompt it ran with, byte for byte, even when the
// parent's context has moved since (a reload, a compaction, a restart): its
// signed thinking is bound to that prefix. A child without a saved prompt
// (an older transcript) is built as before.
func TestResumedChildKeepsItsOwnSystemPrompt(t *testing.T) {
	model := core.Model{ID: "default", Provider: "mock"}
	var mu sync.Mutex
	var systems []string
	record := func(text string) func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
		return func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
			mu.Lock()
			systems = append(systems, req.System)
			mu.Unlock()
			return textResponse(text)(ctx, req)
		}
	}
	provider := newMockProvider(record("first"), record("second"), record("third"))

	agentsMD := "rule v1"
	jobs := newJobStore()
	var saved ResumedTranscript
	var firstID string
	cfg := Config{
		DefaultModel: model,
		AppCtx:       context.Background(),
		ParentTools:  core.NewRegistry(),
		PromptBuilder: func(opts agentcontext.SystemPromptOptions) string {
			mu.Lock()
			defer mu.Unlock()
			return "parent context: " + agentsMD
		},
		ProviderFactory: func(core.Model) (core.Provider, error) { return provider, nil },
		TranscriptLoader: func(string) (ResumedTranscript, error) {
			mu.Lock()
			defer mu.Unlock()
			return saved, nil
		},
		OnChildEnd: func(jobID, _ string, _ bool, _, _, _ string, _ time.Time, _ *core.Usage, _, _ float64, _ uint64) {
			mu.Lock()
			defer mu.Unlock()
			if firstID == "" {
				firstID = jobID
				saved = ResumedTranscript{Messages: jobs.messages(jobID), SystemPrompt: jobs.systemPrompt(jobID)}
			}
		},
	}
	sub := newSubagent(cfg, jobs)
	if _, err := sub.Execute(context.Background(), map[string]any{"task": "one"}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return firstID != "" })

	mu.Lock()
	agentsMD = "rule v2"
	id := firstID
	if saved.SystemPrompt == "" {
		mu.Unlock()
		t.Fatal("the job did not record the prompt it ran with")
	}
	mu.Unlock()
	if res, err := sub.Execute(context.Background(), map[string]any{"task": "two", "resume": id}, nil); err != nil || res.IsError {
		t.Fatalf("resume: %v %s", err, textOf(res))
	}

	// An older transcript, with no prompt saved, gets today's.
	mu.Lock()
	saved.SystemPrompt = ""
	mu.Unlock()
	if res, err := sub.Execute(context.Background(), map[string]any{"task": "three", "resume": id}, nil); err != nil || res.IsError {
		t.Fatalf("resume without a saved prompt: %v %s", err, textOf(res))
	}

	mu.Lock()
	defer mu.Unlock()
	if len(systems) != 3 {
		t.Fatalf("requests = %d, want 3", len(systems))
	}
	if systems[1] != systems[0] {
		t.Errorf("the resumed child got a new prompt:\n--- ran with\n%s\n--- resumed with\n%s", systems[0], systems[1])
	}
	if systems[2] == systems[0] {
		t.Error("a child with no saved prompt did not get the current one")
	}
}
