package subagent

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

// centPricing makes every input token cost exactly $0.01.
func centPricing() *core.Pricing { return &core.Pricing{Input: 10_000} }

// usageToolCall is a tool call whose request reports input tokens of usage.
func usageToolCall(toolID, toolName string, args map[string]any, input int) func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
	return func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 2)
		msg := core.Message{
			Role: "assistant", Content: []core.Content{core.ToolCallContent(toolID, toolName, args)},
			StopReason: "tool_use", Timestamp: time.Now().Unix(), Usage: &core.Usage{Input: input},
		}
		ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
		ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		close(ch)
		return ch, nil
	}
}

func callCount(p *mockProvider) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

type childEnd struct {
	status, result, err string
	goalActivation      uint64
}

type budgetFixture struct {
	mu    sync.Mutex
	ends  []childEnd
	child *mockProvider
}

func (f *budgetFixture) ended() []childEnd {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]childEnd(nil), f.ends...)
}

// newBudgetParent wires the subagent tool into a real parent agent with
// MaxBudget, so children are launched from a budgeted run's tool call.
func newBudgetParent(t *testing.T, maxBudget float64, childModel core.Model, parentProv, childProv *mockProvider, loader func(string) (ResumedTranscript, error)) (*agent.Agent, *budgetFixture) {
	t.Helper()
	f := &budgetFixture{child: childProv}
	cfg := Config{
		DefaultModel:     childModel,
		AppCtx:           context.Background(),
		ParentTools:      core.NewRegistry(),
		ProviderFactory:  func(core.Model) (core.Provider, error) { return childProv, nil },
		TranscriptLoader: loader,
		OnChildEnd: func(_, _ string, _ bool, status, result, resultErr string, _ time.Time, _ *core.Usage, _, _ float64, goalActivation uint64) {
			f.mu.Lock()
			f.ends = append(f.ends, childEnd{status, result, resultErr, goalActivation})
			f.mu.Unlock()
		},
	}
	reg := core.NewRegistry()
	if err := reg.Register(newSubagent(cfg, newJobStore())); err != nil {
		t.Fatal(err)
	}
	parent, err := agent.New(agent.AgentConfig{
		Provider:     parentProv,
		Model:        core.Model{ID: "parent", Provider: "mock", Pricing: centPricing()},
		SystemPrompt: "parent",
		Tools:        reg,
		MaxBudget:    maxBudget,
	})
	if err != nil {
		t.Fatal(err)
	}
	return parent, f
}

func wantBudgetStop(t *testing.T, end childEnd, jobHint string) {
	t.Helper()
	if end.status != statusFailed || !strings.Contains(end.err, "budget") || !strings.Contains(end.err, "resume=") {
		t.Fatalf("%s child ended %q with %q, want failed by the run's budget with a resume hint", jobHint, end.status, end.err)
	}
}

func wantParentBudgetError(t *testing.T, err error, spent, limit float64) {
	t.Helper()
	var be *agent.BudgetExceededError
	if !errors.As(err, &be) {
		t.Fatalf("parent error = %v, want BudgetExceededError", err)
	}
	if math.Abs(be.Spent-spent) > 1e-9 || math.Abs(be.Limit-limit) > 1e-9 {
		t.Fatalf("parent BudgetExceededError = %+v, want Spent %v Limit %v", be, spent, limit)
	}
}

func TestSubagentChargesParentRunBudget(t *testing.T) {
	childModel := core.Model{ID: "child", Provider: "mock", Pricing: centPricing()}
	childProv := newMockProvider(
		usageToolCall("c1", "nothing", map[string]any{}, 300), // $3
		textResponse("child done"),
	)
	parentProv := newMockProvider(
		usageToolCall("p1", "subagent", map[string]any{"task": "work"}, 1), // $0.01
		textResponse("parent done"),
	)
	parent, f := newBudgetParent(t, 0.10, childModel, parentProv, childProv, func(string) (ResumedTranscript, error) {
		return ResumedTranscript{}, nil
	})

	_, err := parent.Run(context.Background(), "go")

	wantParentBudgetError(t, err, 3.01, 0.10)
	ends := f.ended()
	if len(ends) != 1 {
		t.Fatalf("child ends = %+v, want 1", ends)
	}
	wantBudgetStop(t, ends[0], "spawned")
	if callCount(childProv) != 1 {
		t.Fatalf("child made %d calls, want 1", callCount(childProv))
	}
}

func TestSubagentResumeChargesResumingRunBudget(t *testing.T) {
	childModel := core.Model{ID: "child", Provider: "mock", Pricing: centPricing()}
	inherited := core.Message{
		Role: "assistant", Content: []core.Content{core.TextContent("earlier")},
		StopReason: "end_turn", Usage: &core.Usage{Input: 200}, Model: "child", Provider: "mock", // $2 paid earlier
	}
	seed := []core.AgentMessage{core.WrapMessage(core.NewUserMessage("earlier task")), core.WrapMessage(inherited)}
	childProv := newMockProvider(
		usageToolCall("c1", "nothing", map[string]any{}, 2),   // $0.02: under the limit
		usageToolCall("c2", "nothing", map[string]any{}, 300), // $3
		textResponse("child done"),
	)
	parentProv := newMockProvider(
		usageToolCall("p1", "subagent", map[string]any{"task": "more", "resume": "sa-old"}, 1),
		textResponse("parent done"),
	)
	parent, f := newBudgetParent(t, 0.10, childModel, parentProv, childProv, func(string) (ResumedTranscript, error) {
		return ResumedTranscript{Messages: seed}, nil
	})

	_, err := parent.Run(context.Background(), "go")

	wantParentBudgetError(t, err, 3.03, 0.10)
	ends := f.ended()
	if len(ends) != 1 {
		t.Fatalf("child ends = %+v, want 1", ends)
	}
	wantBudgetStop(t, ends[0], "resumed")
	if callCount(childProv) != 2 {
		t.Fatalf("resumed child made %d calls, want 2 (inherited spend must not count)", callCount(childProv))
	}
}

func TestAsyncSubagentChargesLaunchingRunBudget(t *testing.T) {
	childModel := core.Model{ID: "child", Provider: "mock", Pricing: centPricing()}
	parentEnded := make(chan struct{})
	childProv := newMockProvider(
		func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
			<-parentEnded
			return usageToolCall("c1", "nothing", map[string]any{}, 300)(ctx, req)
		},
		textResponse("child done"),
	)
	parentProv := newMockProvider(
		usageToolCall("p1", "subagent", map[string]any{"task": "work", "async": true}, 1),
		textResponse("parent done"),
	)
	parent, f := newBudgetParent(t, 0.10, childModel, parentProv, childProv, func(string) (ResumedTranscript, error) {
		return ResumedTranscript{}, nil
	})

	if _, err := parent.Run(context.Background(), "go"); err != nil {
		t.Fatalf("parent run: %v", err)
	}
	close(parentEnded)
	waitFor(t, 5*time.Second, func() bool { return len(f.ended()) == 1 })
	wantBudgetStop(t, f.ended()[0], "async")
}

// A child on a model without pricing cannot be counted: it runs and adds
// nothing, but it still stops when the rest of the run has used the budget.
func TestUnpricedSubagentReadsButDoesNotFeedRunBudget(t *testing.T) {
	var limit atomic.Value
	limit.Store(1.0)
	childProv := newMockProvider(
		func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
			// The run's live cap drops below what the parent already spent
			// (as a goal budget does when other spend lands).
			limit.Store(0.30)
			return usageToolCall("c1", "nothing", map[string]any{}, 100_000)(ctx, req)
		},
		textResponse("child done"),
	)
	// A tool-call turn is charged once its tools return, so the parent's
	// $0.50 is spent in a turn before the one that launches the child.
	parentProv := newMockProvider(
		usageToolCall("p0", "nothing", map[string]any{}, 50), // $0.50
		toolCallResponse("p1", "subagent", map[string]any{"task": "work"}),
		textResponse("parent done"),
	)
	parent, f := newBudgetParent(t, 0, core.Model{ID: "free", Provider: "mock"}, parentProv, childProv, nil)
	parent.SetBudgetCap(func(context.Context) (float64, bool) { return limit.Load().(float64), true })

	_, err := parent.Run(context.Background(), "go")

	ends := f.ended()
	if len(ends) != 1 {
		t.Fatalf("child ends = %+v, want 1", ends)
	}
	if ends[0].status != statusFailed || !strings.Contains(ends[0].err, "budget") {
		t.Fatalf("unpriced child ended %q with %q, want stopped by the exhausted run budget", ends[0].status, ends[0].err)
	}
	if callCount(childProv) != 1 {
		t.Fatalf("unpriced child made %d calls, want 1", callCount(childProv))
	}
	// Exactly the parent's own $0.50: the unpriced child added nothing.
	wantParentBudgetError(t, err, 0.50, 0.30)
}

// The goal activation of the launching run travels with the child to its end,
// for both lanes: the reactor charges the child's spend to it.
func TestSubagentCarriesLaunchingGoalActivation(t *testing.T) {
	for _, async := range []bool{false, true} {
		var mu sync.Mutex
		var got []uint64
		cfg := Config{
			DefaultModel:    core.Model{ID: "child", Provider: "mock"},
			AppCtx:          context.Background(),
			ParentTools:     core.NewRegistry(),
			ProviderFactory: func(core.Model) (core.Provider, error) { return newMockProvider(textResponse("done")), nil },
			OnChildEnd: func(_, _ string, _ bool, _, _, _ string, _ time.Time, _ *core.Usage, _, _ float64, goalActivation uint64) {
				mu.Lock()
				got = append(got, goalActivation)
				mu.Unlock()
			},
		}
		sub := newSubagent(cfg, newJobStore())
		ctx := bus.WithGoalActivation(context.Background(), 7)
		if res, err := sub.Execute(ctx, map[string]any{"task": "x", "async": async}, nil); err != nil || res.IsError {
			t.Fatalf("async=%v: %v %s", async, err, textOf(res))
		}
		waitFor(t, 2*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return len(got) == 1 })
		mu.Lock()
		if got[0] != 7 {
			t.Errorf("async=%v: OnChildEnd goal activation = %d, want 7", async, got[0])
		}
		mu.Unlock()
	}
}
