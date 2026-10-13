package bus_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

// A run the user starts while a goal is active (paused after a failed run, or
// interrupting the verifier) is limited by what the goal has left, not by the
// MaxBudget the driver set for an earlier iteration.
func TestUserRunUnderGoalLimitedByGoalRemaining(t *testing.T) {
	b := bus.NewLocalBus()
	g := poolBudgetGoal(t) // total .10
	g.AddSpent(.07)
	provider := &poolBudgetProvider{steps: []func(context.Context) core.Message{
		func(context.Context) core.Message { return poolBudgetText("done", 4) }, // $.04 > .03 left
	}}
	model := core.Model{ID: "pool", Provider: "mock", Pricing: &core.Pricing{Input: 10_000}}
	a, err := agent.New(agent.AgentConfig{Provider: provider, Model: model, MaxBudget: .10})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := bus.NewSessionRuntime(bus.RuntimeConfig{Bus: b, SessionID: "pool", Agent: a, Goal: g})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)

	ended := make(chan bus.RunEnded, 1)
	b.Subscribe(func(e bus.RunEnded) { ended <- e })
	if err := b.Execute(bus.SendPrompt{Text: "user prompt"}); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-ended:
		var be *agent.BudgetExceededError
		if !errors.As(e.Err, &be) {
			t.Fatalf("run error = %v, want BudgetExceededError against the goal's .03 left", e.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not end")
	}
}
