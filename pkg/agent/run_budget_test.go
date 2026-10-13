package agent

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// usageOf returns a usage that costs exactly usd at testPricing ($10/M input).
func usageOf(usd float64) *core.Usage {
	n := int(math.Round(usd * 100_000))
	return &core.Usage{Input: n, TotalTokens: n}
}

func noopTool() core.Tool {
	return core.Tool{
		Name: "noop", Description: "does nothing", Effect: core.EffectReadOnly,
		Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
			return core.TextResult("ok"), nil
		},
	}
}

func budgetNoopRegistry(t *testing.T) *core.Registry {
	t.Helper()
	reg := core.NewRegistry()
	if err := reg.Register(noopTool()); err != nil {
		t.Fatal(err)
	}
	return reg
}

// newBudgetChild builds a child the way a delegating tool must: with the
// budget of the run whose tool call launched it.
func newBudgetChild(t *testing.T, ctx context.Context, pricing *core.Pricing, handlers ...func(core.Request) (<-chan core.AssistantEvent, error)) (*Agent, *MockProvider) {
	t.Helper()
	prov := NewMockProvider(handlers...)
	child, err := New(AgentConfig{
		Provider:     prov,
		Model:        core.Model{ID: "child", Provider: "mock", Pricing: pricing},
		SystemPrompt: "child",
		Tools:        budgetNoopRegistry(t),
		ParentBudget: RunBudgetFrom(ctx),
	})
	if err != nil {
		t.Fatal(err)
	}
	return child, prov
}

// spawnTool runs fn as the body of a read-only tool named name.
func spawnTool(name string, fn func(ctx context.Context) string) core.Tool {
	return core.Tool{
		Name: name, Description: "spawns a child", Effect: core.EffectReadOnly,
		Execute: func(ctx context.Context, _ map[string]any, _ func(core.Result)) (core.Result, error) {
			return core.TextResult(fn(ctx)), nil
		},
	}
}

func newBudgetParent(t *testing.T, maxBudget float64, prov core.Provider, tools ...core.Tool) *Agent {
	t.Helper()
	reg := core.NewRegistry()
	for _, tl := range tools {
		if err := reg.Register(tl); err != nil {
			t.Fatal(err)
		}
	}
	parent, err := New(AgentConfig{
		Provider:     prov,
		Model:        core.Model{ID: "parent", Provider: "mock", Pricing: testPricing()},
		SystemPrompt: "parent",
		Tools:        reg,
		MaxBudget:    maxBudget,
	})
	if err != nil {
		t.Fatal(err)
	}
	return parent
}

func budgetError(err error) *BudgetExceededError {
	var be *BudgetExceededError
	if errors.As(err, &be) {
		return be
	}
	return nil
}

// The review's probe: a parent limited to $0.10 whose child spends $3 used to
// finish without any error.
func TestRunBudgetCountsChildSpend(t *testing.T) {
	var childErr error
	var childCalls int
	spawn := spawnTool("spawn", func(ctx context.Context) string {
		child, prov := newBudgetChild(t, ctx, testPricing(),
			toolCallWithUsage("c1", "noop", map[string]any{}, usageOf(3)),
			textResponseWithUsage("child done", usageOf(0.01)),
		)
		_, childErr = child.Run(context.Background(), "work")
		childCalls = prov.calls
		return "child returned"
	})
	parentProv := NewMockProvider(
		toolCallWithUsage("p1", "spawn", map[string]any{}, usageOf(0.01)),
		textResponseWithUsage("parent done", usageOf(0.01)),
	)
	parent := newBudgetParent(t, 0.10, parentProv, spawn)

	_, err := parent.Run(context.Background(), "go")

	if be := budgetError(childErr); be == nil {
		t.Fatalf("child error = %v, want BudgetExceededError (its $3 exceeds the run's $0.10)", childErr)
	}
	if childCalls != 1 {
		t.Errorf("child made %d provider calls, want 1 (it must stop at the checkpoint after the $3 call)", childCalls)
	}
	be := budgetError(err)
	if be == nil {
		t.Fatalf("parent error = %v, want BudgetExceededError at its next checkpoint", err)
	}
	if math.Abs(be.Spent-3.01) > 1e-9 || be.Limit != 0.10 {
		t.Errorf("parent BudgetExceededError = %+v, want Spent 3.01 (own + child) and Limit 0.10", be)
	}
	if parentProv.calls != 1 {
		t.Errorf("parent made %d provider calls, want 1 (no call after the child exhausted the budget)", parentProv.calls)
	}
}

// Children running at the same time charge one pool: together they exceed the
// limit even though each one alone is under it.
func TestRunBudgetParallelChildren(t *testing.T) {
	var arrived sync.WaitGroup
	arrived.Add(2)
	// Each child's first call waits until both are in flight, so the two
	// really charge the pool concurrently (under -race).
	barrier := func(h func(core.Request) (<-chan core.AssistantEvent, error)) func(core.Request) (<-chan core.AssistantEvent, error) {
		return func(req core.Request) (<-chan core.AssistantEvent, error) {
			arrived.Done()
			done := make(chan struct{})
			go func() { arrived.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
			}
			return h(req)
		}
	}
	var mu sync.Mutex
	var childErrs []error
	spawn := func(name string) core.Tool {
		return spawnTool(name, func(ctx context.Context) string {
			child, _ := newBudgetChild(t, ctx, testPricing(),
				barrier(toolCallWithUsage("c1", "noop", map[string]any{}, usageOf(0.06))),
				textResponseWithUsage("child done", nil),
			)
			_, err := child.Run(context.Background(), "work")
			mu.Lock()
			childErrs = append(childErrs, err)
			mu.Unlock()
			return "child returned"
		})
	}
	parentProv := NewMockProvider(
		func(req core.Request) (<-chan core.AssistantEvent, error) {
			ch := make(chan core.AssistantEvent, 2)
			msg := core.Message{
				Role: "assistant",
				Content: []core.Content{
					core.ToolCallContent("p1", "spawn_a", map[string]any{}),
					core.ToolCallContent("p2", "spawn_b", map[string]any{}),
				},
				StopReason: "tool_use", Timestamp: time.Now().Unix(),
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
			close(ch)
			return ch, nil
		},
		textResponseWithUsage("parent done", nil),
	)
	parent := newBudgetParent(t, 0.10, parentProv, spawn("spawn_a"), spawn("spawn_b"))

	_, err := parent.Run(context.Background(), "go")

	mu.Lock()
	defer mu.Unlock()
	cut := 0
	for _, e := range childErrs {
		if budgetError(e) != nil {
			cut++
		}
	}
	if len(childErrs) != 2 || cut == 0 {
		t.Fatalf("child errors = %v, want at least one BudgetExceededError ($0.06 + $0.06 > $0.10)", childErrs)
	}
	if be := budgetError(err); be == nil || math.Abs(be.Spent-0.12) > 1e-9 {
		t.Fatalf("parent error = %v, want BudgetExceededError with Spent 0.12", err)
	}
}

// An async child keeps charging the run that launched it after that run has
// ended; the parent's next run starts with a fresh pool.
func TestRunBudgetAsyncChildOutlivesRun(t *testing.T) {
	parentDone := make(chan struct{})
	childResult := make(chan error, 1)
	spawn := spawnTool("spawn", func(ctx context.Context) string {
		child, _ := newBudgetChild(t, ctx, testPricing(),
			func(req core.Request) (<-chan core.AssistantEvent, error) {
				<-parentDone
				return toolCallWithUsage("c1", "noop", map[string]any{}, usageOf(3))(req)
			},
			textResponseWithUsage("child done", nil),
		)
		go func() {
			_, err := child.Run(context.Background(), "work")
			childResult <- err
		}()
		return "child started"
	})
	parentProv := NewMockProvider(
		toolCallWithUsage("p1", "spawn", map[string]any{}, usageOf(0.01)),
		textResponseWithUsage("first run done", usageOf(0.01)),
		textResponseWithUsage("second run done", usageOf(0.01)),
	)
	parent := newBudgetParent(t, 0.10, parentProv, spawn)

	if _, err := parent.Run(context.Background(), "first"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	close(parentDone)
	select {
	case err := <-childResult:
		if be := budgetError(err); be == nil || math.Abs(be.Spent-3.02) > 1e-9 {
			t.Fatalf("async child error = %v, want BudgetExceededError charging the first run's pool (3.02)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("async child did not finish")
	}
	if _, err := parent.Run(context.Background(), "second"); err != nil {
		t.Fatalf("second run blocked by the old child's spend: %v", err)
	}
}

// A resumed child replays history paid in earlier runs: only what it spends
// now counts against the run that resumed it.
func TestRunBudgetResumedChildChargesOnlyNewSpend(t *testing.T) {
	inherited := core.Message{
		Role: "assistant", Content: []core.Content{core.TextContent("earlier work")},
		StopReason: "end_turn", Usage: usageOf(2), Model: "child", Provider: "mock",
	}
	var childErr error
	var childCalls int
	spawn := spawnTool("resume", func(ctx context.Context) string {
		child, prov := newBudgetChild(t, ctx, testPricing(),
			toolCallWithUsage("c1", "noop", map[string]any{}, usageOf(0.02)),
			toolCallWithUsage("c2", "noop", map[string]any{}, usageOf(3)),
			textResponseWithUsage("child done", nil),
		)
		if err := child.LoadState([]core.AgentMessage{
			core.WrapMessage(core.NewUserMessage("earlier task")), core.WrapMessage(inherited),
		}, 0); err != nil {
			t.Error(err)
		}
		_, childErr = child.Run(context.Background(), "continue")
		childCalls = prov.calls
		return "child returned"
	})
	parentProv := NewMockProvider(
		toolCallWithUsage("p1", "resume", map[string]any{}, usageOf(0.01)),
		textResponseWithUsage("parent done", nil),
	)
	parent := newBudgetParent(t, 0.10, parentProv, spawn)

	_, _ = parent.Run(context.Background(), "go")

	if childCalls != 2 {
		t.Errorf("resumed child made %d calls, want 2 (the $2 inherited must not stop it, the new $3 must)", childCalls)
	}
	// The parent's $0.01 for the turn that called the tool is charged before
	// its tools run, so the child sees it plus its own new spend: 0.01 + 0.02 + 3.
	if be := budgetError(childErr); be == nil || math.Abs(be.Spent-3.03) > 1e-9 {
		t.Fatalf("resumed child error = %v, want BudgetExceededError with Spent 3.03 (no inherited $2)", childErr)
	}
}

// The pool is for enforcement only: the parent's own run cost (what RunEnded
// reports and the session total adds) never includes its children, who are
// charged to the session separately.
func TestRunEndedCostStaysOwnSpend(t *testing.T) {
	spawn := spawnTool("spawn", func(ctx context.Context) string {
		child, _ := newBudgetChild(t, ctx, testPricing(),
			textResponseWithUsage("child done", usageOf(3)),
		)
		_, _ = child.Run(context.Background(), "work")
		return "child returned"
	})
	parentProv := NewMockProvider(
		toolCallWithUsage("p1", "spawn", map[string]any{}, usageOf(0.01)),
		textResponseWithUsage("parent done", usageOf(0.01)),
	)
	parent := newBudgetParent(t, 1, parentProv, spawn)

	_, err := parent.Run(context.Background(), "go")

	if be := budgetError(err); be == nil || math.Abs(be.Spent-3.01) > 1e-9 {
		t.Fatalf("parent error = %v, want BudgetExceededError with Spent 3.01 (the child's $3 counts)", err)
	}
	if got := parent.RunCost(); math.Abs(got-0.01) > 1e-9 {
		t.Fatalf("parent RunCost = %v, want 0.01 (own spend only)", got)
	}
}
