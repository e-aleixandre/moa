package bus

import (
	"math"
	"testing"
	"time"
)

func approxEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// querySessionCost is a small helper to read the accumulated session cost.
func querySessionCost(t *testing.T, b EventBus) float64 {
	t.Helper()
	cost, err := QueryTyped[GetSessionCost, float64](b, GetSessionCost{})
	if err != nil {
		t.Fatalf("GetSessionCost: %v", err)
	}
	return cost
}

func TestSessionCost_AccumulatesRunAndSubagents(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	got := make(chan SessionCostUpdated, 8)
	b.Subscribe(func(e SessionCostUpdated) { got <- e })

	// A run's cost accumulates.
	b.Publish(RunEnded{SessionID: "test-session", Cost: 0.10})
	// A subagent's cost adds on top.
	b.Publish(SubagentEnded{SessionID: "test-session", CostUSD: 0.05})
	b.Drain(time.Second)

	if total := querySessionCost(t, b); !approxEqual(total, 0.15) {
		t.Fatalf("session cost = %v, want 0.15", total)
	}

	// The last published update should carry the running total.
	var last SessionCostUpdated
	drained := false
	for !drained {
		select {
		case e := <-got:
			last = e
		default:
			drained = true
		}
	}
	if !approxEqual(last.TotalUSD, 0.15) {
		t.Fatalf("last SessionCostUpdated.TotalUSD = %v, want 0.15", last.TotalUSD)
	}
}

func TestSessionCost_ZeroCostRunDoesNotPublish(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	got := make(chan SessionCostUpdated, 4)
	b.Subscribe(func(e SessionCostUpdated) { got <- e })

	b.Publish(RunEnded{SessionID: "test-session", Cost: 0})
	b.Drain(time.Second)

	select {
	case e := <-got:
		t.Fatalf("unexpected SessionCostUpdated for a zero-cost run: %+v", e)
	default:
	}
	if total := querySessionCost(t, b); total != 0 {
		t.Fatalf("session cost = %v, want 0", total)
	}
}

func TestSessionCost_ResetOnClear(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	b.Publish(RunEnded{SessionID: "test-session", Cost: 0.20})
	b.Drain(time.Second)
	if total := querySessionCost(t, b); !approxEqual(total, 0.20) {
		t.Fatalf("session cost before clear = %v, want 0.20", total)
	}

	if err := b.Execute(ClearSession{}); err != nil {
		t.Fatalf("ClearSession: %v", err)
	}
	b.Drain(time.Second)
	if total := querySessionCost(t, b); total != 0 {
		t.Fatalf("session cost after clear = %v, want 0", total)
	}
}
