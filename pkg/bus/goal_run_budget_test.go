package bus_test

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/goal"
	"github.com/e-aleixandre/moa/pkg/subagent"
)

type poolBudgetProvider struct {
	mu    sync.Mutex
	calls int
	steps []func(context.Context) core.Message
}

func (p *poolBudgetProvider) Stream(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
	p.mu.Lock()
	i := p.calls
	p.calls++
	p.mu.Unlock()
	if i >= len(p.steps) {
		return nil, errors.New("unexpected extra provider call")
	}
	msg := p.steps[i](ctx)
	ch := make(chan core.AssistantEvent, 2)
	ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
	close(ch)
	return ch, nil
}

func (p *poolBudgetProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func poolBudgetText(text string, input int) core.Message {
	return core.Message{Role: "assistant", Content: []core.Content{core.TextContent(text)}, StopReason: "end_turn", Usage: &core.Usage{Input: input}}
}

func poolBudgetSpawn(async bool, input int) core.Message {
	return core.Message{Role: "assistant", Content: []core.Content{core.ToolCallContent("spawn", "subagent", map[string]any{"task": "child", "async": async})}, StopReason: "tool_use", Usage: &core.Usage{Input: input}}
}

func poolBudgetGoal(t *testing.T) *goal.Goal {
	t.Helper()
	g := goal.New()
	if err := g.Enter(goal.Options{Objective: "x", TotalBudget: .10, StatePath: filepath.Join(t.TempDir(), "STATE.md")}); err != nil {
		t.Fatal(err)
	}
	return g
}

func poolBudgetRuntime(t *testing.T, b bus.EventBus, g *goal.Goal, parentProvider, childProvider *poolBudgetProvider, ended func(bus.SubagentEnded)) (*agent.Agent, *bus.SessionRuntime) {
	t.Helper()
	model := core.Model{ID: "pool", Provider: "mock", Pricing: &core.Pricing{Input: 10_000}}
	reg := core.NewRegistry()
	if _, err := subagent.RegisterAll(reg, subagent.Config{
		DefaultModel:    model,
		ParentTools:     core.NewRegistry(),
		AppCtx:          context.Background(),
		ProviderFactory: func(core.Model) (core.Provider, error) { return childProvider, nil },
		OnChildEnd: func(id, task string, async bool, status, result, resultErr string, finished time.Time, usage *core.Usage, cost, newCost float64, act uint64) {
			e := bus.SubagentEnded{SessionID: "pool", JobID: id, Task: task, Async: async, Status: status, Result: result, Error: resultErr, FinishedAt: finished, Usage: usage, CostUSD: cost, RunCostUSD: &newCost, GoalActivation: act}
			b.Publish(e)
			// This is a valid schedule of the async reactor: let its accounting
			// land before the launching tool returns to the parent's checkpoint.
			b.Drain(time.Second)
			if ended != nil {
				ended(e)
			}
		},
	}); err != nil {
		t.Fatal(err)
	}
	parent, err := agent.New(agent.AgentConfig{Provider: parentProvider, Model: model, Tools: reg, MaxBudget: .10})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := bus.NewSessionRuntime(bus.RuntimeConfig{Bus: b, SessionID: "pool", Agent: parent, Goal: g})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	return parent, rt
}

// A settled synchronous child is in both the root pool and the goal total;
// the run's limit stays what the goal had left when it started.
func TestGoalSyncChildNotCountedTwice(t *testing.T) {
	b := bus.NewLocalBus()
	g := poolBudgetGoal(t)
	parentProvider := &poolBudgetProvider{steps: []func(context.Context) core.Message{
		func(context.Context) core.Message { return poolBudgetSpawn(false, 1) },
		func(context.Context) core.Message { return poolBudgetText("parent done", 1) },
	}}
	childProvider := &poolBudgetProvider{steps: []func(context.Context) core.Message{
		func(context.Context) core.Message { return poolBudgetText("child done", 6) },
	}}
	parent, _ := poolBudgetRuntime(t, b, g, parentProvider, childProvider, nil)
	_, err := parent.Run(bus.WithGoalActivation(context.Background(), g.Activation()), "go")
	if err != nil {
		t.Fatalf("premature goal cutoff: %v; parent calls=%d, own cost=%.2f, goal settled cost=%.2f; real spend .07 (< .10), expected final call and .08 total", err, parentProvider.count(), parent.RunCost(), g.Spent())
	}
	if parentProvider.count() != 2 || math.Abs(parent.RunCost()-.02) > 1e-9 {
		t.Fatalf("parent calls=%d own cost=%v, want 2 and .02", parentProvider.count(), parent.RunCost())
	}
}

// An async child keeps charging its launching run's pool after that run
// settled, and its spend lands once in the goal total.
func TestGoalAsyncChildDoesNotRecountSettledParent(t *testing.T) {
	b := bus.NewLocalBus()
	g := poolBudgetGoal(t)
	childStarted := make(chan struct{})
	childRelease := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(childRelease) }) }
	t.Cleanup(release)
	childEnd := make(chan bus.SubagentEnded, 1)
	parentProvider := &poolBudgetProvider{steps: []func(context.Context) core.Message{
		func(context.Context) core.Message { return poolBudgetSpawn(true, 3) },
		func(context.Context) core.Message {
			select {
			case <-childStarted:
			case <-time.After(time.Second):
				t.Error("child did not enter provider")
			}
			return poolBudgetText("parent done", 3)
		},
	}}
	childProvider := &poolBudgetProvider{steps: []func(context.Context) core.Message{
		func(ctx context.Context) core.Message {
			close(childStarted)
			select {
			case <-childRelease:
			case <-ctx.Done():
			}
			return core.Message{Role: "assistant", Content: []core.Content{core.ToolCallContent("noop", "noop", map[string]any{})}, StopReason: "tool_use", Usage: &core.Usage{Input: 1}}
		},
		func(context.Context) core.Message { return poolBudgetText("child done", 1) },
	}}
	parent, _ := poolBudgetRuntime(t, b, g, parentProvider, childProvider, func(e bus.SubagentEnded) { childEnd <- e })
	_, err := parent.Run(bus.WithGoalActivation(context.Background(), g.Activation()), "go")
	if err != nil {
		t.Fatal(err)
	}
	// Pause the goal driver but charge the parent's normal RunEnded.Cost. The
	// run was driven outside the bus, so its generation is 0, as RunEnded's.
	b.Publish(bus.RunEnded{SessionID: "pool", Cost: parent.RunCost(), Err: errors.New("pause verifier for probe")})
	b.Drain(time.Second)
	if math.Abs(g.Spent()-.06) > 1e-9 {
		t.Fatalf("goal spent=%v, want .06 before releasing child", g.Spent())
	}
	release()
	select {
	case e := <-childEnd:
		if e.Status != "completed" {
			t.Fatalf("premature async goal cutoff: status=%q error=%q child calls=%d new cost=%.2f, goal spent=%.2f; real spend .07 (< .10), expected completed at .08", e.Status, e.Error, childProvider.count(), *e.RunCostUSD, g.Spent())
		}
		if math.Abs(g.Spent()-.08) > 1e-9 {
			t.Fatalf("goal spent=%v, want .08: parent .06 and child .02 once each", g.Spent())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("child did not settle")
	}
}
