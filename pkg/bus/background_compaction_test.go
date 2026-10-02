package bus

// STEP2 edge tests for background compaction on the real runtime, TreeSyncer
// and FileStore (bgFix from background_compaction_red_test.go): a run reserved
// while an idle cut is being saved waits for that save alone and is refused if
// it failed; Close settles an accepted cut and discards an unaccepted one; an
// idle Stop discards a pending summary without a run; usage is charged once
// and only to the goal activation that started it; late or background events
// never resurrect a boundary or clear foreground state.

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/goal"
	"github.com/e-aleixandre/moa/pkg/session"
)

// bgHoldSave holds the first save carrying a compaction until hold closes,
// then fails it when fail is set.
func bgHoldSave(t *testing.T, f *bgFix, fail bool) (saving, hold chan struct{}) {
	t.Helper()
	saving, hold = make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.p.before = func(entries []session.Entry) error {
		if !catomicHasCompaction(entries) {
			return nil
		}
		first := false
		once.Do(func() { first = true; close(saving) })
		if !first {
			return nil
		}
		<-hold
		if fail {
			return errors.New("injected disk failure")
		}
		return nil
	}
	return saving, hold
}

// bgIdleWithHeldSummary ends the originating run while its summary is held.
func bgIdleWithHeldSummary(t *testing.T) *bgFix {
	t.Helper()
	f := newBGSoftBelowHard(t, nil)
	f.send(t, bgText("go ", 40))
	f.request(t, "ordinary request while the summary is held")
	f.waitSummaryEntered(t)
	f.waitEnded(t, "originating run")
	f.rt.Bus.Drain(bgWait)
	return f
}

func bgWaitClosed(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(bgWait):
		t.Fatal(what)
	}
}

// A human send reserved while an accepted idle cut is being saved waits for
// that save only, and when it fails is refused with the storage error before
// anything runs; the next human attempt reconciles and runs.
func TestBackgroundCompaction_AdmissionWaitsForAcceptedIdleSaveFailure(t *testing.T) {
	f := bgIdleWithHeldSummary(t)
	saving, hold := bgHoldSave(t, f, true)
	before := catomicMsgs(f.ag.Messages())
	f.sum.open()
	bgWaitClosed(t, saving, "the idle cut was never saved")

	res := make(chan error, 1)
	go func() { res <- f.rt.Bus.Execute(SendPrompt{Text: "next prompt"}) }()
	select {
	case err := <-res:
		t.Fatalf("admission returned (%v) while the accepted save was undecided", err)
	case <-time.After(bgNegative):
	}
	if r, quiet := f.noRequest(); !quiet {
		t.Fatalf("a request left during the accepted save: %v", bgShapesLLM(r.req.Messages))
	}
	close(hold)
	select {
	case err := <-res:
		if !errors.Is(err, ErrSessionNotSaved) {
			t.Fatalf("admission after a failed cut: %v, want ErrSessionNotSaved", err)
		}
	case <-time.After(bgWait):
		t.Fatal("admission never returned after the save failed")
	}
	if r, quiet := f.noRequest(); !quiet {
		t.Fatalf("refused send still reached the provider: %v", bgShapesLLM(r.req.Messages))
	}
	if f.runs.Load() != 1 {
		t.Fatalf("refused send started a run: runs=%d", f.runs.Load())
	}
	if got := catomicMsgs(f.ag.Messages()); !equalMsgs(got, before) {
		t.Fatalf("agent changed by an unsaved cut:\n got %v\nwant %v", got, before)
	}
	if s := f.rt.State.Current(); s != StateIdle {
		t.Fatalf("state=%s after a refused admission, want idle", s)
	}
	// A human attempt reconciles the previous state and runs.
	f.send(t, "after recovery")
	f.request(t, "human attempt after reconciliation")
	f.waitEnded(t, "recovered run")
}

// Close waits for an accepted cut and leaves both sides adopted.
func TestBackgroundCompaction_CloseSettlesAcceptedCut(t *testing.T) {
	f := bgIdleWithHeldSummary(t)
	saving, hold := bgHoldSave(t, f, false)
	originals := catomicMsgs(f.ag.Messages())
	f.sum.open()
	bgWaitClosed(t, saving, "the idle cut was never saved")

	closed := make(chan struct{})
	go func() { f.rt.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("Close returned while an accepted cut was being saved")
	case <-time.After(bgNegative):
	}
	close(hold)
	bgWaitClosed(t, closed, "Close never returned")

	got := f.ag.Messages()
	if len(got) == 0 || got[0].Role != "compaction_summary" {
		t.Fatalf("agent did not adopt the saved cut: %v", catomicMsgs(got))
	}
	entries, leaf := f.savedRaw(t)
	if _, err := catomicCheckNewSnapshot(entries, leaf, originals); err != nil {
		t.Fatalf("durable cut: %v", err)
	}
}

// Close before acceptance discards the summary: it is never saved or adopted.
func TestBackgroundCompaction_CloseDiscardsUnacceptedSummary(t *testing.T) {
	f := bgIdleWithHeldSummary(t)
	f.rt.Close()
	f.sum.open()
	f.ag.WaitBackgroundCompaction()
	if f.durable.Load() {
		t.Fatal("a summary released after Close was saved")
	}
	if got := f.ag.Messages(); len(got) > 0 && got[0].Role == "compaction_summary" {
		t.Fatal("a summary released after Close was adopted")
	}
}

// An idle Stop discards the pending summary without starting a run.
func TestBackgroundCompaction_IdleStopDiscardsWithoutRun(t *testing.T) {
	f := bgIdleWithHeldSummary(t)
	if s := f.rt.Context().BackgroundCompaction(); !s.Active {
		t.Fatalf("no active background state after the run: %+v", s)
	}
	cancelled := false
	if err := f.rt.Bus.Execute(CancelBackgroundCompaction{Cancelled: &cancelled}); err != nil || !cancelled {
		t.Fatalf("idle cancel: %v cancelled=%v", err, cancelled)
	}
	f.sum.open()
	f.ag.WaitBackgroundCompaction()
	f.ag.Drain(bgWait) // the state travels through the agent's emitter first
	f.rt.Bus.Drain(bgWait)
	if f.durable.Load() {
		t.Fatal("cancelled summary was saved")
	}
	if s := f.rt.Context().BackgroundCompaction(); s.Active {
		t.Fatalf("background state still active after cancel: %+v", s)
	}
	if f.runs.Load() != 1 || f.rt.State.Current() != StateIdle {
		t.Fatalf("idle cancel started a run: runs=%d state=%s", f.runs.Load(), f.rt.State.Current())
	}
	again := true
	if err := f.rt.Bus.Execute(CancelBackgroundCompaction{Cancelled: &again}); err != nil || again {
		t.Fatalf("second cancel: %v cancelled=%v", err, again)
	}
}

// Background usage is charged to the session once and to the goal activation
// that started it, never to a replacement; its completion is not charged.
func TestBackgroundCompaction_UsageOnceAndGoalActivation(t *testing.T) {
	f := newBGFix(t, bgCfg{window: 100000, reserve: 1000, keep: 1000})
	sctx := f.rt.Context()
	g := goal.New()
	sctx.Goal = g
	if err := g.Enter(goal.Options{Objective: "x", StatePath: t.TempDir() + "/STATE.md"}); err != nil {
		t.Fatal(err)
	}
	first := g.Activation()
	pricing := &core.Pricing{Input: 1000, Output: 1000}
	usage := core.Usage{Input: 1000, Output: 100}
	cost := pricing.Cost(usage)
	// An ended run's context: not the current run generation.
	origin := context.WithValue(context.WithValue(context.Background(), runGenKey{}, uint64(99)), goalActivationKey{}, first)

	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventCompactionUsage, BackgroundJobID: 1, Usage: &usage, Pricing: pricing, Origin: origin})
	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventCompactionEnd, BackgroundJobID: 1, Origin: origin,
		Compaction: &core.CompactionPayload{Summary: "s", Usage: &usage, Pricing: pricing, FirstKeptMsgID: "x", SummaryMsgID: "y", BoundaryID: "b"}})
	f.rt.Bus.Drain(bgWait)
	total, _ := QueryTyped[GetSessionCost, float64](f.rt.Bus, GetSessionCost{})
	if math.Abs(total-cost) > 1e-9 || math.Abs(g.Spent()-cost) > 1e-9 {
		t.Fatalf("session=%v goal=%v, want each charged %v once", total, g.Spent(), cost)
	}
	// A replacement activation never pays for the old job.
	if err := g.Enter(goal.Options{Objective: "y", StatePath: t.TempDir() + "/STATE.md"}); err != nil {
		t.Fatal(err)
	}
	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventCompactionUsage, BackgroundJobID: 2, Usage: &usage, Pricing: pricing, Origin: origin})
	f.rt.Bus.Drain(bgWait)
	total, _ = QueryTyped[GetSessionCost, float64](f.rt.Bus, GetSessionCost{})
	if math.Abs(total-2*cost) > 1e-9 || g.Spent() != 0 {
		t.Fatalf("session=%v goal=%v after replacement, want session %v and goal 0", total, g.Spent(), 2*cost)
	}
}

// Background events are session-level: revisions only move forward, a
// background completion neither clears the foreground compacting flag nor
// appends a boundary of its own (so a late one cannot resurrect a cleared
// cut), and RunEnded does not clear the background state.
func TestBackgroundCompaction_SessionStateAndLateEvents(t *testing.T) {
	f := newBGFix(t, bgCfg{window: 100000, reserve: 1000, keep: 1000})
	sctx := f.rt.Context()
	changes := make(chan BackgroundCompactionChanged, 8)
	f.rt.Bus.Subscribe(func(e BackgroundCompactionChanged) { changes <- e })

	newer := core.BackgroundCompactionState{JobID: 3, Revision: 5, Active: true, Waiting: true}
	older := core.BackgroundCompactionState{JobID: 2, Revision: 4, Active: false}
	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventBackgroundCompaction, BackgroundCompaction: &newer})
	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventBackgroundCompaction, BackgroundCompaction: &older})
	sctx.setCompacting(true)
	payload := &core.CompactionPayload{Summary: "late", FirstKeptMsgID: "gone", SummaryMsgID: "late-summary", BoundaryID: "late-boundary"}
	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventCompactionEnd, BackgroundJobID: 2, Compaction: payload})
	f.rt.Bus.Publish(RunEnded{SessionID: sctx.SessionID})
	f.rt.Bus.Drain(bgWait)

	if got := sctx.BackgroundCompaction(); got != newer {
		t.Fatalf("state=%+v, want the newer %+v", got, newer)
	}
	if len(changes) != 1 {
		t.Fatalf("%d state events published, want only the newer one", len(changes))
	}
	if !sctx.Compacting() {
		t.Fatal("a background completion cleared the foreground compacting flag")
	}
	for _, e := range sctx.Tree.Entries() {
		if e.Type == session.EntryCompaction {
			t.Fatal("a background completion appended a boundary")
		}
	}
	if q, _ := QueryTyped[GetBackgroundCompaction, core.BackgroundCompactionState](f.rt.Bus, GetBackgroundCompaction{}); q != newer {
		t.Fatalf("query=%+v", q)
	}
}

// A branch requested while an accepted idle cut is being saved must not move
// the tree under that save: the cut settles on both sides, then the branch
// applies to tree and agent together.
func TestBackgroundCompaction_BranchWaitsForAcceptedSave(t *testing.T) {
	f := bgIdleWithHeldSummary(t)
	sctx := f.rt.Context()
	target := sctx.Tree.Entries()[1].ID
	saving, hold := bgHoldSave(t, f, false)
	f.sum.open()
	bgWaitClosed(t, saving, "the idle cut was never saved")

	done := make(chan error, 1)
	go func() { done <- f.rt.Bus.Execute(BranchTo{EntryID: target}) }()
	select {
	case err := <-done:
		t.Fatalf("branch returned (%v) while the accepted save was undecided", err)
	case <-time.After(bgNegative):
	}
	close(hold)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("branch: %v", err)
		}
	case <-time.After(bgWait):
		t.Fatal("branch never returned")
	}
	f.rt.Bus.Drain(bgWait)
	if sctx.unreconciled.Load() {
		t.Fatal("a successful save was recorded as a storage failure")
	}
	cuts := 0
	for _, e := range sctx.Tree.Entries() {
		if e.Type == session.EntryCompaction {
			cuts++
		}
	}
	if cuts != 1 {
		t.Fatalf("tree holds %d boundaries, want the saved one", cuts)
	}
	want, _ := sctx.Tree.BuildContext()
	if got := f.ag.Messages(); !equalMsgs(catomicMsgs(got), catomicMsgs(want)) {
		t.Fatalf("agent and tree diverged:\n agent %v\n tree  %v", catomicMsgs(got), catomicMsgs(want))
	}
	if sctx.Tree.LeafID() != target {
		t.Fatalf("leaf=%s, want the branch target %s", sctx.Tree.LeafID(), target)
	}
}

// Clear is already fenced by the syncer's lock: it waits for the accepted
// save and then clears both sides.
func TestBackgroundCompaction_ClearWaitsForAcceptedSave(t *testing.T) {
	f := bgIdleWithHeldSummary(t)
	sctx := f.rt.Context()
	saving, hold := bgHoldSave(t, f, false)
	f.sum.open()
	bgWaitClosed(t, saving, "the idle cut was never saved")
	done := make(chan error, 1)
	go func() { done <- f.rt.Bus.Execute(ClearSession{}) }()
	select {
	case err := <-done:
		t.Fatalf("clear returned (%v) while the accepted save was undecided", err)
	case <-time.After(bgNegative):
	}
	close(hold)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	f.rt.Bus.Drain(bgWait)
	if sctx.unreconciled.Load() || len(f.ag.Messages()) != 0 || len(sctx.Tree.Entries()) != 0 {
		t.Fatalf("clear vs save: unreconciled=%v agent=%d tree=%d", sctx.unreconciled.Load(), len(f.ag.Messages()), len(sctx.Tree.Entries()))
	}
}

// bgBudgetRun runs B, a run of a live goal started with the remaining budget
// the goal driver gives it, while the summary usage of an earlier run A
// arrives. Returns B's provider calls.
func bgBudgetRun(t *testing.T, replace bool) (calls int, f *bgFix, g *goal.Goal) {
	t.Helper()
	pricing := &core.Pricing{Input: 1000, Output: 1000}
	usd := func(n int) *core.Usage { return &core.Usage{Input: n} } // n/1000 USD
	f = newBGFix(t, bgCfg{window: 1000000, reserve: 1000, keep: 1000, script: func(call int, req core.Request) *core.Message {
		switch call {
		case 1:
			m := bgToolCall("b1", map[string]any{"gate": true}, "one")
			m.Usage = usd(1000)
			return m
		case 2:
			m := bgToolCall("b2", map[string]any{}, "two")
			m.Usage = usd(2500)
			return m
		case 3:
			m := bgToolCall("b3", map[string]any{}, "three")
			m.Usage = usd(3000)
			return m
		}
		return nil
	}})
	if err := f.ag.Reconfigure(nil, core.Model{ID: "bg", MaxInput: 1000000, Pricing: pricing}, "", 0); err != nil {
		t.Fatal(err)
	}
	sctx := f.rt.Context()
	g = goal.New()
	sctx.Goal = g
	if err := g.Enter(goal.Options{Objective: "x", StatePath: t.TempDir() + "/STATE.md", TotalBudget: 10}); err != nil {
		t.Fatal(err)
	}
	act := g.Activation()
	g.AddSpent(4)                                // run A's RunEnded
	if err := f.ag.SetMaxBudget(6); err != nil { // the driver's remaining for B
		t.Fatal(err)
	}
	f.send(t, "B")
	select {
	case <-f.toolStarted:
	case <-time.After(bgWait):
		t.Fatal("B's first tool never started")
	}
	if replace {
		if err := g.Enter(goal.Options{Objective: "y", StatePath: t.TempDir() + "/STATE.md", TotalBudget: 10}); err != nil {
			t.Fatal(err)
		}
	}
	// A's summary usage ($3) arrives while B runs; A has long settled.
	origin := context.WithValue(context.WithValue(context.Background(), runGenKey{}, uint64(999)), goalActivationKey{}, act)
	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventCompactionUsage, BackgroundJobID: 1, Usage: usd(3000), Pricing: pricing, Origin: origin})
	f.rt.Bus.Drain(bgWait)
	f.openTool()
	e := f.waitEnded(t, "B")
	var budget *agent.BudgetExceededError
	if !errors.As(e.Err, &budget) {
		t.Fatalf("B ended with %v, want a budget stop", e.Err)
	}
	f.rt.Bus.Drain(bgWait)
	total, _ := QueryTyped[GetSessionCost, float64](f.rt.Bus, GetSessionCost{})
	if math.Abs(total-(3+e.Cost)) > 1e-9 {
		t.Fatalf("session cost=%v, want A's summary 3 + B's %v once", total, e.Cost)
	}
	return int(f.prov.calls), f, g
}

// B's next request is capped by the goal's live remaining pool once A's late
// summary has been charged to the same activation.
func TestBackgroundCompaction_LateUsageCapsSameGoalRun(t *testing.T) {
	calls, _, g := bgBudgetRun(t, false)
	if calls != 2 {
		t.Fatalf("B made %d requests; with $3.5 spent against a remaining $3 it must stop after 2", calls)
	}
	if math.Abs(g.Spent()-(4+3+3.5)) > 1e-9 {
		t.Fatalf("goal spent=%v, want 10.5 (A, A's summary, B) once each", g.Spent())
	}
}

// A replacement goal neither pays for nor is capped by the old job.
func TestBackgroundCompaction_LateUsageIgnoresReplacementGoal(t *testing.T) {
	calls, _, g := bgBudgetRun(t, true)
	if calls != 3 {
		t.Fatalf("B made %d requests, want 3 (its own $6 cap only)", calls)
	}
	if math.Abs(g.Spent()-6.5) > 1e-9 {
		t.Fatalf("replacement goal spent=%v, want only B's 6.5", g.Spent())
	}
}
