package serve

// Owner reports — semantic turns. These tests reproduce the incident that
// motivated the policy: a child that leaves a background job running for ever
// (a dev server) used to report nothing at all, because the report waited for
// a quiescence that could not arrive and was then discarded at shutdown.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/owner"
)

// shortOwnerQuiescence shortens the wait a completed turn gives the session's
// background work, so a test does not have to spend it.
func shortOwnerQuiescence(t *testing.T, d time.Duration) {
	t.Helper()
	orig := ownerReportQuiescenceTimeout
	ownerReportQuiescenceTimeout = d
	t.Cleanup(func() { ownerReportQuiescenceTimeout = orig })
}

// ownerChild creates a child session of the owner's codebase, which is what
// gets an owner report observer.
func ownerChild(t *testing.T, mgr *Manager, root, title string) *ManagedSession {
	t.Helper()
	child, err := mgr.CreateSession(CreateOpts{CWD: root, Title: title})
	if err != nil {
		t.Fatal(err)
	}
	if child.ownerObserver == nil {
		t.Fatal("the child was built without an owner report observer")
	}
	return child
}

// outbox reads the reports waiting on disk for a codebase. The outbox is the
// boundary that matters: a report in it survives the process, and one that is
// only in memory does not.
func outbox(t *testing.T, mgr *Manager, key string) []owner.Report {
	t.Helper()
	store, err := mgr.ownerStore()
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.LoadReports(key)
	if err != nil {
		t.Fatal(err)
	}
	return pending
}

func waitForOutbox(t *testing.T, mgr *Manager, key string, n int) []owner.Report {
	t.Helper()
	var got []owner.Report
	pollUntil(t, 10*time.Second, "reports reaching the outbox", func() bool {
		got = outbox(t, mgr, key)
		return len(got) >= n
	})
	return got
}

// startRunEvents drives one run of a child through the bus, in publication
// order, exactly as the real run path does.
func publishRunStart(sess *ManagedSession, gen uint64, origin bus.RunOrigin) {
	sess.runtime.Bus.Publish(bus.RunStarted{SessionID: sess.ID, RunGen: gen, Origin: origin})
}

func publishRunEnd(sess *ManagedSession, gen uint64, finalText string) {
	sess.runtime.Bus.Publish(bus.RunEnded{SessionID: sess.ID, RunGen: gen, FinalText: finalText})
}

// A child whose turn finishes while a background job keeps running for ever is
// the incident: the report used to wait for a quiescence that never came. It
// must arrive, say what is still running, and leave the job alone.
func TestTurnWithEndlessBackgroundJobIsStillReported(t *testing.T) {
	shortReportWindow(t, time.Hour) // nothing is delivered; the outbox is the assertion
	shortOwnerQuiescence(t, 200*time.Millisecond)
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, _ := ownerWithSession(t, mgr, root, "Winerim")
	child := ownerChild(t, mgr, root, "the child")

	publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
	child.runtime.Bus.Publish(bus.BashJobStarted{SessionID: child.ID, JobID: "bash-endless", Command: "node catalog-serve.mjs"})
	publishRunEnd(child, 1, "the catalog server is up on :4317")

	got := waitForOutbox(t, mgr, info.CodebaseKey, 1)
	if len(got) != 1 {
		t.Fatalf("expected exactly one report, got %d", len(got))
	}
	if !strings.Contains(got[0].FinalText, "catalog server is up") {
		t.Fatalf("the report lost the turn's conclusion: %+v", got[0])
	}
	if got[0].BackgroundCount != 1 {
		t.Fatalf("background count = %d, want 1", got[0].BackgroundCount)
	}
	// The job is reported, not killed: the session keeps serving.
	if n := child.runtime.BackgroundWork(); n != 1 {
		t.Fatalf("background work = %d, want the job left running", n)
	}
	// And the wait does not produce a second one.
	time.Sleep(3 * ownerReportQuiescenceTimeout)
	if got := outbox(t, mgr, info.CodebaseKey); len(got) != 1 {
		t.Fatalf("the turn was reported %d times, want once", len(got))
	}
}

// When the endless job finally ends it injects its own notification, which is
// a new run. That is the same turn concluding, not a second thing to report.
func TestLateBashCompletionDoesNotReportTheTurnTwice(t *testing.T) {
	shortReportWindow(t, time.Hour)
	shortOwnerQuiescence(t, 150*time.Millisecond)
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, _ := ownerWithSession(t, mgr, root, "Winerim")
	child := ownerChild(t, mgr, root, "the child")

	publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
	child.runtime.Bus.Publish(bus.BashJobStarted{SessionID: child.ID, JobID: "bash-endless"})
	publishRunEnd(child, 1, "server started")
	waitForOutbox(t, mgr, info.CodebaseKey, 1)

	// Hours later: the job ends and reports back through a run of its own.
	publishRunStart(child, 2, bus.RunOrigin{ContinuationOf: []string{"bash-endless"}})
	child.runtime.Bus.Publish(bus.BashJobSettled{SessionID: child.ID, JobID: "bash-endless"})
	publishRunEnd(child, 2, "the server exited with status 0")

	time.Sleep(3 * ownerReportQuiescenceTimeout)
	got := outbox(t, mgr, info.CodebaseKey)
	if len(got) != 1 {
		t.Fatalf("the continuation produced a second report: %d reports", len(got))
	}
	if !strings.Contains(got[0].FinalText, "server started") {
		t.Fatalf("the reported turn changed under the owner: %+v", got[0])
	}
}

// The same dedupe for an async subagent, whose completion takes the same rail.
func TestLateSubagentCompletionDoesNotReportTheTurnTwice(t *testing.T) {
	shortReportWindow(t, time.Hour)
	shortOwnerQuiescence(t, 150*time.Millisecond)
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, _ := ownerWithSession(t, mgr, root, "Winerim")
	child := ownerChild(t, mgr, root, "the child")

	publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
	child.runtime.Bus.Publish(bus.SubagentStarted{SessionID: child.ID, JobID: "sub-1", Async: true})
	publishRunEnd(child, 1, "delegated the audit")
	waitForOutbox(t, mgr, info.CodebaseKey, 1)

	publishRunStart(child, 2, bus.RunOrigin{ContinuationOf: []string{"sub-1"}})
	child.runtime.Bus.Publish(bus.SubagentEnded{SessionID: child.ID, JobID: "sub-1", Status: "done"})
	publishRunEnd(child, 2, "the audit came back clean")

	time.Sleep(3 * ownerReportQuiescenceTimeout)
	if got := outbox(t, mgr, info.CodebaseKey); len(got) != 1 {
		t.Fatalf("the subagent continuation produced a second report: %d", len(got))
	}
}

// A job that finishes inside the wait is the ordinary case: one report, and it
// carries what the turn ended up concluding, with nothing left running.
func TestBackgroundWorkFinishingInTimeGivesOneCompleteReport(t *testing.T) {
	shortReportWindow(t, time.Hour)
	shortOwnerQuiescence(t, 10*time.Second) // long: the chain must win the race
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, _ := ownerWithSession(t, mgr, root, "Winerim")
	child := ownerChild(t, mgr, root, "the child")

	publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
	child.runtime.Bus.Publish(bus.BashJobStarted{SessionID: child.ID, JobID: "bash-tests"})
	publishRunEnd(child, 1, "tests are running in the background")

	// The job finishes and delivers its result, which claims the session again
	// before the wait can expire. Publication order mirrors the real path: the
	// continuation run is admitted before the job is marked settled.
	time.Sleep(100 * time.Millisecond)
	publishRunStart(child, 2, bus.RunOrigin{ContinuationOf: []string{"bash-tests"}})
	child.runtime.Bus.Publish(bus.BashJobSettled{SessionID: child.ID, JobID: "bash-tests"})
	publishRunEnd(child, 2, "all tests passed")

	got := waitForOutbox(t, mgr, info.CodebaseKey, 1)
	if len(got) != 1 {
		t.Fatalf("expected one report for one turn, got %d", len(got))
	}
	if !strings.Contains(got[0].FinalText, "all tests passed") {
		t.Fatalf("the report carries the first pass, not the conclusion: %+v", got[0])
	}
	if got[0].BackgroundCount != 0 {
		t.Fatalf("background count = %d, want 0 for a fully settled turn", got[0].BackgroundCount)
	}
}

// A new instruction while the old job is still running is a turn of its own,
// and the old job's eventual completion still adds nothing.
func TestAnExplicitTurnReportsWhileAnOldJobKeepsRunning(t *testing.T) {
	shortReportWindow(t, time.Hour)
	shortOwnerQuiescence(t, 200*time.Millisecond)
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, _ := ownerWithSession(t, mgr, root, "Winerim")
	child := ownerChild(t, mgr, root, "the child")

	publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
	child.runtime.Bus.Publish(bus.BashJobStarted{SessionID: child.ID, JobID: "bash-endless"})
	publishRunEnd(child, 1, "first instruction done")
	waitForOutbox(t, mgr, info.CodebaseKey, 1)

	// The user asks for something else while the server keeps running.
	publishRunStart(child, 2, bus.RunOrigin{Explicit: true})
	publishRunEnd(child, 2, "second instruction done")
	got := waitForOutbox(t, mgr, info.CodebaseKey, 2)
	if len(got) != 2 {
		t.Fatalf("the explicit turn did not report independently: %d reports", len(got))
	}
	if !strings.Contains(got[1].FinalText, "second instruction") {
		t.Fatalf("the second report is not the second turn: %+v", got[1])
	}
	if got[1].BackgroundCount != 1 {
		t.Fatalf("second report background count = %d, want 1", got[1].BackgroundCount)
	}

	// The old job finally reports back: it belongs to the first turn, which the
	// owner already read.
	publishRunStart(child, 3, bus.RunOrigin{ContinuationOf: []string{"bash-endless"}})
	child.runtime.Bus.Publish(bus.BashJobSettled{SessionID: child.ID, JobID: "bash-endless"})
	publishRunEnd(child, 3, "the server exited")
	time.Sleep(3 * ownerReportQuiescenceTimeout)
	if got := outbox(t, mgr, info.CodebaseKey); len(got) != 2 {
		t.Fatalf("the late completion produced a third report: %d", len(got))
	}
}

// A run whose provenance names no known job is a new turn. Losing an explicit
// turn is worse than reporting an extra one, so the unknown case errs that way.
func TestUnknownProvenanceStartsItsOwnTurn(t *testing.T) {
	shortReportWindow(t, time.Hour)
	shortOwnerQuiescence(t, 150*time.Millisecond)
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, _ := ownerWithSession(t, mgr, root, "Winerim")
	child := ownerChild(t, mgr, root, "the child")

	publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
	child.runtime.Bus.Publish(bus.BashJobStarted{SessionID: child.ID, JobID: "bash-endless"})
	publishRunEnd(child, 1, "first")
	waitForOutbox(t, mgr, info.CodebaseKey, 1)

	// Zero origin: neither explicit nor tied to a job the observer knows.
	publishRunStart(child, 2, bus.RunOrigin{})
	publishRunEnd(child, 2, "something else happened")
	got := waitForOutbox(t, mgr, info.CodebaseKey, 2)
	if len(got) != 2 {
		t.Fatalf("unknown provenance was folded into the previous turn: %d reports", len(got))
	}
}

// A deleted session has nothing left for the owner to look into, so its
// unreported turn goes with it.
func TestDeletingASessionDropsItsUnreportedTurn(t *testing.T) {
	shortReportWindow(t, time.Hour)
	shortOwnerQuiescence(t, 10*time.Second)
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, _ := ownerWithSession(t, mgr, root, "Winerim")
	child := ownerChild(t, mgr, root, "the child")

	publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
	child.runtime.Bus.Publish(bus.BashJobStarted{SessionID: child.ID, JobID: "bash-endless"})
	publishRunEnd(child, 1, "about to be deleted")
	// The waiter is in flight; the delete must win.
	if err := mgr.Delete(child.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if got := outbox(t, mgr, info.CodebaseKey); len(got) != 0 {
		t.Fatalf("a deleted session reported to its owner: %+v", got)
	}
}

// The incident itself, end to end: a turn finished with a background job that
// will never end, the root context cancelled the way a SIGTERM does, and then
// Shutdown. The report must reach the outbox and a fresh process must find it.
func TestShutdownPersistsTheTurnAnEndlessJobWasHolding(t *testing.T) {
	shortReportWindow(t, time.Hour)
	// Long enough that nothing can self-emit: only the shutdown flush can
	// produce this report.
	shortOwnerQuiescence(t, 5*time.Minute)
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("hello")))
	root := t.TempDir()
	info, _ := ownerWithSession(t, mgr, root, "Winerim")
	child := ownerChild(t, mgr, root, "the child")

	publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
	child.runtime.Bus.Publish(bus.BashJobStarted{SessionID: child.ID, JobID: "bash-endless"})
	publishRunEnd(child, 1, "the design pass is finished")
	// The job's own completion run: the same turn, still unreported.
	publishRunStart(child, 2, bus.RunOrigin{ContinuationOf: []string{"bash-endless"}})
	publishRunEnd(child, 2, "and here is what it changed")
	// Let the observer see all of it before the signal arrives.
	child.runtime.Bus.Drain(2 * time.Second)

	// SIGTERM order: the root context dies first, then Shutdown runs.
	cancel()
	mgr.Shutdown()

	got := outbox(t, mgr, info.CodebaseKey)
	if len(got) != 1 {
		t.Fatalf("shutdown persisted %d reports, want exactly the latest turn: %+v", len(got), got)
	}
	if !strings.Contains(got[0].FinalText, "here is what it changed") {
		t.Fatalf("the persisted report is not the latest outcome: %+v", got[0])
	}
	if got[0].SessionID != child.ID {
		t.Fatalf("report session = %s, want %s", got[0].SessionID, child.ID)
	}

	// A fresh process recovers it and delivers it to the owner: same config
	// directory, same session store, a brand new manager.
	shortReportWindow(t, 50*time.Millisecond)
	nextCfg := core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"}
	next := NewManager(context.Background(), ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) {
			return newMockProvider(simpleResponseHandler("hello")), nil
		},
		DefaultModel:   core.Model{ID: "claude-haiku-4-5-20251001", Provider: "anthropic"},
		WorkspaceRoot:  root,
		MoaCfg:         nextCfg,
		ConfigLoader:   func(string) core.MoaConfig { return nextCfg },
		SessionBaseDir: mgr.sessionBaseDir,
		SchedulePath:   filepath.Join(t.TempDir(), "schedules.json"),
	})
	t.Cleanup(next.Shutdown)
	ownerSess, ok := next.Get(info.SessionID)
	if !ok {
		resumed, err := next.ResumeSession(info.SessionID)
		if errors.Is(err, ErrBusy) {
			pollUntil(t, 5*time.Second, "owner report recovery resuming the owner", func() bool {
				ownerSess, ok = next.Get(info.SessionID)
				return ok
			})
		} else if err != nil {
			t.Fatal(err)
		} else {
			ownerSess = resumed
		}
	}
	delivered := waitForOwnerReports(t, ownerSess, 1)[0]
	if !strings.Contains(delivered, "here is what it changed") {
		t.Fatalf("the recovered report did not reach the owner:\n%s", delivered)
	}
	if !strings.Contains(delivered, "1 background job still running") {
		t.Fatalf("the recovered report does not say what was left running:\n%s", delivered)
	}
}

// A shutdown with nothing outstanding must not invent a report, and must not
// spend the quiescence wait either.
func TestShutdownWithoutAnUnreportedTurnIsSilentAndFast(t *testing.T) {
	shortReportWindow(t, time.Hour)
	shortOwnerQuiescence(t, 5*time.Minute)
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("hello")))
	root := t.TempDir()
	info, _ := ownerWithSession(t, mgr, root, "Winerim")
	ownerChild(t, mgr, root, "the child")

	cancel()
	start := time.Now()
	mgr.Shutdown()
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("shutdown waited %v; it must not spend the quiescence budget", elapsed)
	}
	if got := outbox(t, mgr, info.CodebaseKey); len(got) != 0 {
		t.Fatalf("shutdown invented a report: %+v", got)
	}
}

// The outbox is read by the next process, which may be a version that predates
// the field. Old files must load, and a settled turn must not write it.
func TestBackgroundCountIsBackwardCompatibleInTheOutbox(t *testing.T) {
	var old owner.Report
	if err := json.Unmarshal([]byte(`{"id":"rep_1","session_id":"s","origin":"user","status":"done"}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.BackgroundCount != 0 {
		t.Fatalf("an outbox written before the field must read as 0, got %d", old.BackgroundCount)
	}
	data, err := json.Marshal(owner.Report{ID: "rep_1", SessionID: "s", Origin: "user", Status: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "background_count") {
		t.Fatalf("a settled turn should not write the field: %s", data)
	}
	data, err = json.Marshal(owner.Report{ID: "rep_1", SessionID: "s", Origin: "user", Status: "done", BackgroundCount: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"background_count":2`) {
		t.Fatalf("the count is not persisted: %s", data)
	}
}

func TestReportSaysWhatIsStillRunning(t *testing.T) {
	cases := []struct {
		count int
		want  string
	}{
		{0, ""},
		{1, "1 background job still running"},
		{3, "3 background jobs still running"},
	}
	for _, tc := range cases {
		text := reportsMessage(owner.Owner{}, []owner.Report{
			{SessionID: "s", Title: "t", Origin: "user", Status: callbackStatusDone, BackgroundCount: tc.count},
		})
		if tc.want == "" {
			if strings.Contains(text, "background job") {
				t.Fatalf("a settled turn mentions background work:\n%s", text)
			}
			continue
		}
		if !strings.Contains(text, tc.want) {
			t.Fatalf("count %d did not render %q:\n%s", tc.count, tc.want, text)
		}
	}
}

// A positive control for the whole loop, through the real entry point: the
// owner sends to a saved child with its own tool, the child resumes, runs, and
// the outcome comes back as a report the owner reads. Nothing is simulated.
func TestSessionsSendToASavedChildComesBackAsAReport(t *testing.T) {
	shortReportWindow(t, 50*time.Millisecond)
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")

	child, err := mgr.CreateSession(CreateOpts{CWD: root, Origin: "owner", Title: "saved child"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.CloseSession(child.ID); err != nil {
		t.Fatal(err)
	}

	res := runSessionsTool(t, ownerSess, map[string]any{
		"action": "send", "session_id": child.ID, "text": "continue the work",
	})
	if res.IsError {
		t.Fatalf("send failed: %s", toolText(res))
	}

	got := waitForOwnerReports(t, ownerSess, 1)[0]
	if !strings.Contains(got, child.ID) || !strings.Contains(got, "status: done") {
		t.Fatalf("the resumed child's completion did not reach the owner:\n%s", got)
	}
	// Nothing was left running, so the report says nothing about background work.
	if strings.Contains(got, "background job") {
		t.Fatalf("a settled child reported background work:\n%s", got)
	}
}

// Shutdown without a signal (the root context still live) must not sit through
// the quiescence budget of a turn it has already flushed by hand.
func TestShutdownDoesNotWaitOutTheQuiescenceBudget(t *testing.T) {
	shortReportWindow(t, time.Hour)
	shortOwnerQuiescence(t, 5*time.Minute)
	ctx := context.Background() // deliberately NOT cancelled
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, _ := ownerWithSession(t, mgr, root, "Winerim")
	child := ownerChild(t, mgr, root, "the child")

	publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
	child.runtime.Bus.Publish(bus.BashJobStarted{SessionID: child.ID, JobID: "bash-endless"})
	publishRunEnd(child, 1, "finished, server still up")
	child.runtime.Bus.Drain(2 * time.Second)

	start := time.Now()
	mgr.Shutdown()
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("Shutdown took %v: it waited out a budget it did not need", elapsed)
	}
	got := outbox(t, mgr, info.CodebaseKey)
	if len(got) != 1 || !strings.Contains(got[0].FinalText, "server still up") {
		t.Fatalf("the flushed turn is not in the outbox: %+v", got)
	}
	if got[0].BackgroundCount != 1 {
		t.Fatalf("background count = %d, want 1", got[0].BackgroundCount)
	}
}

// The sequence the turn-identity rework exists for. Three explicit turns, the
// first holding a job that only finishes at the end, and a late continuation
// in the middle. Every explicit turn is one thing that happened to the project
// and owes the owner a line; the continuation owes nothing.
func TestThreeExplicitTurnsReportOnceEachDespiteALateContinuation(t *testing.T) {
	shortReportWindow(t, time.Hour)
	shortOwnerQuiescence(t, 200*time.Millisecond)
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, _ := ownerWithSession(t, mgr, root, "Winerim")
	child := ownerChild(t, mgr, root, "the child")

	// T1: explicit, starts a long-lived job.
	publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
	child.runtime.Bus.Publish(bus.BashJobStarted{SessionID: child.ID, JobID: "bash-long"})
	publishRunEnd(child, 1, "turn one done")
	waitForOutbox(t, mgr, info.CodebaseKey, 1)

	// T2: a second instruction while the job runs.
	publishRunStart(child, 2, bus.RunOrigin{Explicit: true})
	publishRunEnd(child, 2, "turn two done")
	waitForOutbox(t, mgr, info.CodebaseKey, 2)

	// The job of T1 finally reports back. Same semantic turn, already read.
	publishRunStart(child, 3, bus.RunOrigin{ContinuationOf: []string{"bash-long"}})
	child.runtime.Bus.Publish(bus.BashJobSettled{SessionID: child.ID, JobID: "bash-long"})
	publishRunEnd(child, 3, "the long job finished")

	// T3: a third instruction afterwards.
	publishRunStart(child, 4, bus.RunOrigin{Explicit: true})
	publishRunEnd(child, 4, "turn three done")
	waitForOutbox(t, mgr, info.CodebaseKey, 3)

	time.Sleep(3 * ownerReportQuiescenceTimeout)
	got := outbox(t, mgr, info.CodebaseKey)
	if len(got) != 3 {
		texts := make([]string, 0, len(got))
		for _, rep := range got {
			texts = append(texts, rep.FinalText)
		}
		t.Fatalf("expected exactly three reports (T1, T2, T3), got %d: %v", len(got), texts)
	}
	for i, want := range []string{"turn one done", "turn two done", "turn three done"} {
		if !strings.Contains(got[i].FinalText, want) {
			t.Fatalf("report %d = %q, want %q", i, got[i].FinalText, want)
		}
	}
}

// A turn's own runs may overlap in publication: the next RunStarted can reach
// subscribers before the previous RunEnded. A terminal event must therefore
// resolve through its OWN generation, not through whatever turn is current.
func TestRunEndedResolvesByItsOwnGenerationNotTheCurrentTurn(t *testing.T) {
	shortReportWindow(t, time.Hour)
	shortOwnerQuiescence(t, 150*time.Millisecond)
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, _ := ownerWithSession(t, mgr, root, "Winerim")
	child := ownerChild(t, mgr, root, "the child")

	// Two explicit turns are admitted before either ends.
	publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
	publishRunStart(child, 2, bus.RunOrigin{Explicit: true})
	// They then end in order; each must land on its own turn.
	publishRunEnd(child, 1, "first turn's conclusion")
	publishRunEnd(child, 2, "second turn's conclusion")

	got := waitForOutbox(t, mgr, info.CodebaseKey, 2)
	if len(got) != 2 {
		t.Fatalf("overlapping generations produced %d reports, want 2", len(got))
	}
	// Both turns settle at the same instant, so which waiter emits first is a
	// genuine race and not something to pin. What must hold is that the two
	// outcomes landed on two DIFFERENT turns and kept their own text: the bug
	// this guards against attributed both to whichever turn was current.
	texts := got[0].FinalText + "|" + got[1].FinalText
	if !strings.Contains(texts, "first turn's conclusion") ||
		!strings.Contains(texts, "second turn's conclusion") {
		t.Fatalf("outcomes landed on the wrong turns: %q", texts)
	}
}

// Machinery that continues the work under way (a goal iteration, auto-verify,
// a compaction, a handoff) is not a new thing that happened to the project.
func TestContinueCurrentDoesNotOpenANewTurn(t *testing.T) {
	shortReportWindow(t, time.Hour)
	shortOwnerQuiescence(t, 150*time.Millisecond)
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, _ := ownerWithSession(t, mgr, root, "Winerim")
	child := ownerChild(t, mgr, root, "the child")

	publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
	publishRunEnd(child, 1, "first pass")
	publishRunStart(child, 2, bus.RunOrigin{ContinueCurrent: true})
	publishRunEnd(child, 2, "the goal's next iteration")

	waitForOutbox(t, mgr, info.CodebaseKey, 1)
	time.Sleep(3 * ownerReportQuiescenceTimeout)
	got := outbox(t, mgr, info.CodebaseKey)
	if len(got) != 1 {
		t.Fatalf("a continuation opened a second turn: %d reports", len(got))
	}
	if !strings.Contains(got[0].FinalText, "next iteration") {
		t.Fatalf("the turn did not carry its latest conclusion: %+v", got[0])
	}
}

// A failure that arrives as a late continuation of a turn the owner already
// read belongs to that turn, so it is not a second entry.
func TestFailedLateContinuationDoesNotDuplicateAReportedTurn(t *testing.T) {
	shortReportWindow(t, time.Hour)
	shortOwnerQuiescence(t, 150*time.Millisecond)
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, _ := ownerWithSession(t, mgr, root, "Winerim")
	child := ownerChild(t, mgr, root, "the child")

	publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
	child.runtime.Bus.Publish(bus.BashJobStarted{SessionID: child.ID, JobID: "bash-long"})
	publishRunEnd(child, 1, "reported already")
	waitForOutbox(t, mgr, info.CodebaseKey, 1)

	publishRunStart(child, 2, bus.RunOrigin{ContinuationOf: []string{"bash-long"}})
	child.runtime.Bus.Publish(bus.RunEnded{
		SessionID: child.ID, RunGen: 2, FinalText: "and then it broke",
		Err: errTestRunFailed,
	})
	time.Sleep(3 * ownerReportQuiescenceTimeout)
	if got := outbox(t, mgr, info.CodebaseKey); len(got) != 1 {
		t.Fatalf("a failed continuation duplicated an already reported turn: %d", len(got))
	}
}

// A failure of a turn in its own right is reported immediately, and says what
// the session left running — an owner acting on a failure needs to know a dev
// server is still up.
func TestFailedTurnReportsBackgroundWork(t *testing.T) {
	shortReportWindow(t, time.Hour)
	shortOwnerQuiescence(t, 5*time.Minute) // nothing may come from the wait
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, _ := ownerWithSession(t, mgr, root, "Winerim")
	child := ownerChild(t, mgr, root, "the child")

	publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
	child.runtime.Bus.Publish(bus.BashJobStarted{SessionID: child.ID, JobID: "bash-long"})
	child.runtime.Bus.Publish(bus.RunEnded{
		SessionID: child.ID, RunGen: 1, FinalText: "could not finish", Err: errTestRunFailed,
	})

	got := waitForOutbox(t, mgr, info.CodebaseKey, 1)
	if got[0].Status != callbackStatusFailed {
		t.Fatalf("status = %q, want failed", got[0].Status)
	}
	if got[0].BackgroundCount != 1 {
		t.Fatalf("a failed turn reported background count %d, want 1", got[0].BackgroundCount)
	}
}

// needs_input is deliberately NOT deduplicated against an already-reported
// turn: a child stuck on a question is the owner's cue to act, and silence
// would strand it. This pins that exception so it cannot be "fixed" by
// accident.
func TestNeedsInputIsReportedEvenForAnAlreadyReportedTurn(t *testing.T) {
	shortReportWindow(t, time.Hour)
	shortOwnerQuiescence(t, 150*time.Millisecond)
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, _ := ownerWithSession(t, mgr, root, "Winerim")
	child := ownerChild(t, mgr, root, "the child")

	publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
	child.runtime.Bus.Publish(bus.BashJobStarted{SessionID: child.ID, JobID: "bash-long"})
	publishRunEnd(child, 1, "done for now")
	waitForOutbox(t, mgr, info.CodebaseKey, 1)

	// The job's continuation asks the user something.
	publishRunStart(child, 2, bus.RunOrigin{ContinuationOf: []string{"bash-long"}})
	child.runtime.Bus.Publish(bus.AskUserRequested{
		SessionID: child.ID, RunGen: 2, ID: "ask-9",
		Questions: []bus.AskQuestion{{Text: "which branch?"}},
	})

	got := waitForOutbox(t, mgr, info.CodebaseKey, 2)
	if len(got) != 2 {
		t.Fatalf("a blocked session was silenced by its turn being reported: %d reports", len(got))
	}
	if got[1].Status != callbackStatusNeedsInput || got[1].Pending == nil || got[1].Pending.ID != "ask-9" {
		t.Fatalf("the question did not reach the owner: %+v", got[1])
	}
}

// Shutdown must flush EVERY completed turn it still holds, not only the last.
func TestShutdownFlushesEveryUnreportedTurnInOrder(t *testing.T) {
	shortReportWindow(t, time.Hour)
	shortOwnerQuiescence(t, 5*time.Minute) // only the flush can emit these
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("hello")))
	root := t.TempDir()
	info, _ := ownerWithSession(t, mgr, root, "Winerim")
	child := ownerChild(t, mgr, root, "the child")

	publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
	child.runtime.Bus.Publish(bus.BashJobStarted{SessionID: child.ID, JobID: "bash-long"})
	publishRunEnd(child, 1, "first turn")
	publishRunStart(child, 2, bus.RunOrigin{Explicit: true})
	publishRunEnd(child, 2, "second turn")
	child.runtime.Bus.Drain(2 * time.Second)

	cancel()
	mgr.Shutdown()

	got := outbox(t, mgr, info.CodebaseKey)
	if len(got) != 2 {
		t.Fatalf("shutdown persisted %d turns, want both: %+v", len(got), got)
	}
	if !strings.Contains(got[0].FinalText, "first turn") || !strings.Contains(got[1].FinalText, "second turn") {
		t.Fatalf("the flushed turns are out of order or wrong: %q / %q", got[0].FinalText, got[1].FinalText)
	}
}

// A programmatic shutdown (root context alive) must still stop deliveries
// before the sessions are torn down: BeginShutdown is acknowledged by the
// actor, so no timer can start a run in an owner that is going away.
func TestShutdownStopsDeliveriesEvenWithALiveRootContext(t *testing.T) {
	// A window short enough that a delivery would fire during the shutdown if
	// anything still allowed one.
	shortReportWindow(t, 10*time.Millisecond)
	ctx := context.Background() // deliberately NOT cancelled
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, ownerSess := ownerWithSession(t, mgr, root, "Winerim")

	mgr.reports.BeginShutdown()
	before := reportDeliveryAttempts.Load()
	mgr.reports.add(info.CodebaseKey, doneReport("rep_after_begin", "sess-x", "queued during shutdown"))
	time.Sleep(200 * time.Millisecond) // many windows

	if got := reportDeliveryAttempts.Load(); got != before {
		t.Fatalf("a delivery was attempted after BeginShutdown: %d → %d", before, got)
	}
	if got := ownerReportText(ownerSess); len(got) != 0 {
		t.Fatalf("a report was delivered into a session being shut down: %v", got)
	}
	// Accepted and persisted all the same: that is the point of accept-only.
	if got := outbox(t, mgr, info.CodebaseKey); len(got) != 1 {
		t.Fatalf("the report was not persisted in accept-only mode: %+v", got)
	}
}

// The admission gate, exercised directly: senders racing Close must either be
// fully accepted (and persisted by the final drain) or cleanly refused. None
// may be silently dropped, and none may panic on a closed actor.
func TestCoordinatorPostRacesCloseWithoutLosingReports(t *testing.T) {
	shortReportWindow(t, time.Hour)
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, _ := ownerWithSession(t, mgr, root, "Winerim")
	coord := mgr.reports

	const senders = 32
	var accepted atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < senders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			rep := doneReport(fmt.Sprintf("rep_%02d", i), "sess-race", "racing close")
			ack := make(chan struct{})
			if coord.post(reportCommand{key: info.CodebaseKey, report: &rep, ack: ack}) {
				<-ack
				accepted.Add(1)
			}
		}(i)
	}
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		<-start
		time.Sleep(time.Millisecond)
		coord.Close()
	}()
	close(start)
	wg.Wait()
	<-closed

	// Every report the coordinator said yes to is on disk. A refused one is a
	// clean no, not a loss of something already promised.
	pending := outbox(t, mgr, info.CodebaseKey)
	if int64(len(pending)) != accepted.Load() {
		t.Fatalf("accepted %d reports but the outbox holds %d", accepted.Load(), len(pending))
	}
	// And after Close nothing can be enqueued at all.
	if coord.post(reportCommand{key: info.CodebaseKey}) {
		t.Fatal("a command was admitted after Close returned")
	}
}

// errTestRunFailed is the error a test run ends with.
var errTestRunFailed = errors.New("the run failed")

// A goal the user starts is a new thing that happened to the project, so it
// opens its own turn even though every iteration after it continues that one.
// The bug this guards against: the first kick carried the same provenance as
// the loop's relaunches, so starting a goal after a reported turn attached the
// whole goal to work the owner had already read, and the goal's outcome was
// silently folded into it.
func TestGoalStartOpensItsOwnTurnAfterAReportedOne(t *testing.T) {
	shortReportWindow(t, time.Hour)
	shortOwnerQuiescence(t, 150*time.Millisecond)
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, _ := ownerWithSession(t, mgr, root, "Winerim")
	child := ownerChild(t, mgr, root, "the child")

	// An ordinary instruction, reported.
	publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
	publishRunEnd(child, 1, "the first instruction")
	waitForOutbox(t, mgr, info.CodebaseKey, 1)

	// The user now starts a goal: its own turn. (That "goal_start" maps to
	// Explicit and "goal" to ContinueCurrent is pinned by TestOriginFromCustom
	// in pkg/bus; this is what those origins then mean to the owner.)
	publishRunStart(child, 2, bus.RunOrigin{Explicit: true})
	publishRunEnd(child, 2, "goal iteration one")
	// Which the loop then continues; still that turn.
	publishRunStart(child, 3, bus.RunOrigin{ContinueCurrent: true})
	publishRunEnd(child, 3, "goal objective reached")

	waitForOutbox(t, mgr, info.CodebaseKey, 2)
	time.Sleep(3 * ownerReportQuiescenceTimeout)
	got := outbox(t, mgr, info.CodebaseKey)
	if len(got) != 2 {
		texts := make([]string, 0, len(got))
		for _, r := range got {
			texts = append(texts, r.FinalText)
		}
		t.Fatalf("want the instruction and the goal as two turns, got %d: %v", len(got), texts)
	}
	if !strings.Contains(got[1].FinalText, "objective reached") {
		t.Fatalf("the goal's turn did not carry its final iteration: %q", got[1].FinalText)
	}
}

// The observer's barrier is a proof about one subscriber, not a timeout: after
// sync returns, everything published before it has been handled by the
// observer's own callback.
func TestObserverSyncProvesItsSubscriberCaughtUp(t *testing.T) {
	shortReportWindow(t, time.Hour)
	shortOwnerQuiescence(t, 5*time.Minute) // only a flush may emit
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, _ := ownerWithSession(t, mgr, root, "Winerim")
	child := ownerChild(t, mgr, root, "the child")

	observer := child.ownerObserver
	if observer == nil {
		t.Fatal("the child has no owner observer")
	}

	// Publish a whole turn and do NOT drain: sync alone must make the observer
	// know about it.
	child.runtime.Bus.Publish(bus.RunStarted{
		SessionID: child.ID, RunGen: 1, Origin: bus.RunOrigin{Explicit: true},
	})
	child.runtime.Bus.Publish(bus.RunEnded{
		SessionID: child.ID, RunGen: 1, FinalText: "published but not drained",
	})

	observer.sync()
	// The observer now knows the turn is complete, so a flush must produce it.
	observer.flush()
	got := waitForOutbox(t, mgr, info.CodebaseKey, 1)
	if !strings.Contains(got[0].FinalText, "not drained") {
		t.Fatalf("the barrier did not prove the turn was consumed: %+v", got)
	}
}

// Delete abandons reports rather than flushing them. A failed-report worker can
// be waiting for the bus to drain when delete starts, so it must recheck
// discard before crossing the outbox boundary, and Delete must wait for it.
func TestDeleteDiscardsDelayedFailedReportWorker(t *testing.T) {
	shortReportWindow(t, time.Hour)
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, _ := ownerWithSession(t, mgr, root, "Winerim")
	child := ownerChild(t, mgr, root, "the child")

	blocked := make(chan struct{})
	release := make(chan struct{})
	var blockedOnce, releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	child.runtime.Bus.SubscribeAll(func(event any) {
		if _, ok := event.(bus.RunEnded); !ok {
			return
		}
		blockedOnce.Do(func() { close(blocked) })
		<-release
	})

	publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
	child.runtime.Bus.Publish(bus.RunEnded{
		SessionID: child.ID,
		RunGen:    1,
		FinalText: "the failed turn",
		Err:       errors.New("provider failed"),
	})
	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("failed report worker never reached its drain")
	}

	deleted := make(chan error, 1)
	go func() { deleted <- mgr.Delete(child.ID) }()
	pollUntil(t, 5*time.Second, "delete discarding owner reports", func() bool {
		child.ownerObserver.mu.Lock()
		defer child.ownerObserver.mu.Unlock()
		return child.ownerObserver.discard
	})
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-deleted:
		if err != nil {
			t.Fatalf("Delete: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Delete did not wait for the delayed report worker")
	}

	if got := outbox(t, mgr, info.CodebaseKey); len(got) != 0 {
		t.Fatalf("Delete let a discarded report reach the outbox: %+v", got)
	}
}

// The generic outcome-worker gate, exercised directly: admission and the
// WaitGroup Add are one critical section, so a Wait that follows a closed
// admission cannot race an Add. Under -race this is what catches the bare
// WaitGroup the gate replaced.
func TestOutcomeWorkerGateDoesNotRaceItsWait(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	ownerWithSession(t, mgr, root, "Winerim")
	sess := ownerChild(t, mgr, root, "the child")

	var admitted, denied atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if sess.admitOutcomeWorker() {
				admitted.Add(1)
				go func() {
					defer sess.outcomeWorkerDone()
					time.Sleep(time.Millisecond)
				}()
				return
			}
			denied.Add(1)
		}()
	}
	closer := make(chan struct{})
	go func() {
		defer close(closer)
		<-start
		sess.closeOutcomeWorkerAdmission()
		sess.outcomeWorkers.Wait()
	}()
	close(start)
	wg.Wait()
	<-closer

	if admitted.Load()+denied.Load() != 32 {
		t.Fatalf("workers lost: %d admitted + %d denied", admitted.Load(), denied.Load())
	}
	// After close, admission is a definite no.
	if sess.admitOutcomeWorker() {
		t.Fatal("a worker was admitted after admission closed")
	}
}

// Closing a child must not lose the turn it has not reported yet.
//
// The terminal gap itself — state idle, RunEnded not yet published — is
// refused at the bus level, where the admission decision lives and can be
// synthesised deterministically (TestDoIfQuiescent_RefusesInsideTheTerminalGap
// in pkg/bus). What this pins is the other half: a close that IS admitted
// hands the completed turn to the owner, from a bus that is still live and a
// subscriber that is still attached.
func TestCloseSessionKeepsAnUnreportedTurn(t *testing.T) {
	shortReportWindow(t, time.Hour)
	shortOwnerQuiescence(t, 5*time.Minute) // only the close's flush may emit
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, _ := ownerWithSession(t, mgr, root, "Winerim")
	child := ownerChild(t, mgr, root, "the child")

	publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
	publishRunEnd(child, 1, "finished just before the close")

	if err := mgr.CloseSession(child.ID); err != nil {
		t.Fatalf("CloseSession refused a settled session: %v", err)
	}
	got := waitForOutbox(t, mgr, info.CodebaseKey, 1)
	if !strings.Contains(got[0].FinalText, "just before the close") {
		t.Fatalf("the turn was lost by the close: %+v", got)
	}
}

// A close is refused while the child still has background work, so the turn
// stays with the live session rather than being cut short by a close.
func TestCloseSessionRefusedWhileBackgroundWorkRuns(t *testing.T) {
	shortReportWindow(t, time.Hour)
	shortOwnerQuiescence(t, 5*time.Minute)
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, _ := ownerWithSession(t, mgr, root, "Winerim")
	child := ownerChild(t, mgr, root, "the child")

	publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
	child.runtime.Bus.Publish(bus.BashJobStarted{SessionID: child.ID, JobID: "bash-long"})
	publishRunEnd(child, 1, "done, but the server is still up")
	child.runtime.Bus.Drain(2 * time.Second)

	if err := mgr.CloseSession(child.ID); !errors.Is(err, ErrBusy) {
		t.Fatalf("CloseSession = %v while a bash job was running, want ErrBusy", err)
	}
	// And nothing was reported behind the refusal: the turn is still the live
	// session's to report when its 15s are up.
	if got := outbox(t, mgr, info.CodebaseKey); len(got) != 0 {
		t.Fatalf("a refused close still emitted: %+v", got)
	}
}
