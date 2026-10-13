package agent

import (
	"context"
	"sync"
)

// RunBudget is the spend ledger of one run of a budgeted agent, shared with
// every child that run launches (and their children). A child's spend is real
// spend of the run that asked for it, so the run's limit holds for all of them
// together, not for each one alone.
//
// It only enforces: each member's own cost (RunCost, RunEnded) stays its own,
// so ledgers that add members separately never count a child twice.
type RunBudget struct {
	mu    sync.Mutex
	spent float64
	// limit is the owning run's effective limit (MaxBudget narrowed by its
	// live cap), evaluated against the owner's context.
	limit func() (float64, bool)
}

// charge adds delta to the pool and reports the pool's spend against the
// owner's limit. A member without pricing charges 0 and still sees what the
// others spent.
func (b *RunBudget) charge(delta float64) (spent, limit float64, over bool) {
	limit, ok := b.limit()
	b.mu.Lock()
	defer b.mu.Unlock()
	if delta > 0 {
		b.spent += delta
	}
	return b.spent, limit, ok && b.spent > limit
}

type runBudgetKey struct{}

// RunBudgetFrom returns the budget of the run a tool call belongs to, or nil
// when that run has no limit. A tool that starts another agent on the run's
// behalf passes it explicitly as that agent's AgentConfig.ParentBudget.
func RunBudgetFrom(ctx context.Context) *RunBudget {
	b, _ := ctx.Value(runBudgetKey{}).(*RunBudget)
	return b
}

// withRunBudget publishes a run's budget to its tools. Written even when nil,
// so an agent run on a caller's context never charges that caller's pool
// unless it was handed it explicitly.
func withRunBudget(ctx context.Context, b *RunBudget) context.Context {
	return context.WithValue(ctx, runBudgetKey{}, b)
}

// runBudgetFor returns the pool a run charges: the launcher's when this agent
// was started on its behalf, otherwise a fresh one when the run has a limit.
func runBudgetFor(ctx context.Context, parent *RunBudget, maxBudget float64, budgetCap func(context.Context) (float64, bool)) *RunBudget {
	if parent != nil {
		return parent
	}
	if maxBudget <= 0 && budgetCap == nil {
		return nil
	}
	return &RunBudget{limit: func() (float64, bool) {
		return effectiveLimit(ctx, maxBudget, budgetCap)
	}}
}

// effectiveLimit is MaxBudget narrowed by the live cap when one applies.
func effectiveLimit(ctx context.Context, maxBudget float64, budgetCap func(context.Context) (float64, bool)) (float64, bool) {
	limit, ok := maxBudget, maxBudget > 0
	if budgetCap != nil {
		if c, capped := budgetCap(ctx); capped && (!ok || c < limit) {
			limit, ok = c, true
		}
	}
	return limit, ok
}
