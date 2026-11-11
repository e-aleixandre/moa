package serve

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/owner"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// liveOwnerTask puts an open task on the owner conversation's checklist.
func liveOwnerTask(t *testing.T, mgr *Manager, ownerSess *ManagedSession, title string) tasks.Record {
	t.Helper()
	rec, err := mgr.tasks.Create(context.Background(), tasks.CreateInput{Title: title, Place: tasks.PlaceAgent,
		ProjectKey: core.CodebaseKey(ownerSess.CWD), AssigneeSessionID: ownerSess.ID})
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func liveTask(t *testing.T, mgr *Manager, id int64) tasks.Record {
	t.Helper()
	rec, err := mgr.tasks.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func liveSessionTitled(t *testing.T, mgr *Manager, title string) *ManagedSession {
	t.Helper()
	for _, info := range mgr.List() {
		if info.Title == title {
			if sess, ok := mgr.Get(info.ID); ok {
				return sess
			}
		}
	}
	return nil
}

// new is where delegation happens, so it is where the task is asked for: an
// id of the owner's open checklist, or an explicit "none".
func TestSessionsNewRequiresATaskAndLinksIt(t *testing.T) {
	shortReportWindow(t, time.Hour)
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")
	task := liveOwnerTask(t, mgr, ownerSess, "Export invoices to CSV")
	idea, err := mgr.tasks.Create(ctx, tasks.CreateInput{Title: "Maybe a dark mode", Place: tasks.PlaceBacklog, ProjectKey: core.CodebaseKey(root)})
	if err != nil {
		t.Fatal(err)
	}

	before := len(mgr.List())
	res := runSessionsTool(t, ownerSess, map[string]any{"action": "new", "cwd": root, "text": "do it", "title": "untracked"})
	if !res.IsError || !strings.Contains(toolText(res), `"none"`) || !strings.Contains(toolText(res), "tasks create") {
		t.Fatalf("new without task: %s", toolText(res))
	}
	res = runSessionsTool(t, ownerSess, map[string]any{"action": "new", "cwd": root, "text": "do it", "title": "idea", "task": float64(idea.ID)})
	if !res.IsError || !strings.Contains(toolText(res), "claim") {
		t.Fatalf("new with an idea of the backlog must say how to promote it: %s", toolText(res))
	}
	res = runSessionsTool(t, ownerSess, map[string]any{"action": "new", "cwd": root, "text": "do it", "title": "nowhere", "task": float64(99999)})
	if !res.IsError || !strings.Contains(toolText(res), "not on your checklist") {
		t.Fatalf("new with an unknown task: %s", toolText(res))
	}
	if got := len(mgr.List()); got != before {
		t.Fatalf("a refused new created a session: %d -> %d", before, got)
	}

	res = runSessionsTool(t, ownerSess, map[string]any{"action": "new", "cwd": root, "text": "export the invoices", "title": "csv export", "task": float64(task.ID)})
	if res.IsError || !strings.Contains(toolText(res), "task #") {
		t.Fatalf("new with a task: %s", toolText(res))
	}
	created := liveSessionTitled(t, mgr, "csv export")
	if created == nil {
		t.Fatal("new did not create the session")
	}
	// The prompt's run is the admitted start: the task is in progress.
	pollUntil(t, 5*time.Second, "the linked task marked started", func() bool {
		rec := liveTask(t, mgr, task.ID)
		return rec.Status == tasks.StatusInProgress && rec.Execution != nil && rec.Execution.StartedAt != 0
	})
	if ex := liveTask(t, mgr, task.ID).Execution; ex.SessionID != created.ID {
		t.Fatalf("task linked to %q, want %q", ex.SessionID, created.ID)
	}
	if out := toolText(runSessionsTool(t, ownerSess, map[string]any{"action": "list"})); !strings.Contains(out, "task: #") || !strings.Contains(out, "Export invoices to CSV") {
		t.Fatalf("list does not show the linked task:\n%s", out)
	}

	// A subagent of the owner does not speak for it.
	tool, _ := ownerSess.infra.toolReg.Get(SessionsToolName)
	res, err = tool.Execute(core.WithAgentID(ctx, "sub-1"), map[string]any{"action": "new", "cwd": root, "text": "x", "title": "by a subagent", "task": float64(task.ID)}, nil)
	if err != nil || !res.IsError || !strings.Contains(toolText(res), "subagent") {
		t.Fatalf("a subagent linked a task: %s %v", toolText(res), err)
	}
}

// send with task moves the task to the session it sends to: a new attempt.
func TestSessionsSendWithTaskReplacesTheExecutor(t *testing.T) {
	shortReportWindow(t, time.Hour)
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, ownerSess := ownerWithSession(t, mgr, root, "Winerim")
	task := liveOwnerTask(t, mgr, ownerSess, "Review the export")
	first := ownerChild(t, mgr, root, "first")
	second := ownerChild(t, mgr, root, "second")
	own := owner.Owner{ID: info.ID, SessionID: ownerSess.ID, CodebaseKey: info.CodebaseKey}
	if err := mgr.linkExecution(ctx, own, task.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	res := runSessionsTool(t, ownerSess, map[string]any{"action": "send", "session_id": second.ID, "text": "review it", "task": float64(task.ID)})
	if res.IsError || !strings.Contains(toolText(res), "task #") {
		t.Fatalf("send with task: %s", toolText(res))
	}
	if ex := liveTask(t, mgr, task.ID).Execution; ex == nil || ex.SessionID != second.ID {
		t.Fatalf("executor not replaced: %+v", ex)
	}
}

// Any run start of an executor marks its linked tasks, and only its own.
func TestRunStartedMarksOnlyTheExecutorsTasks(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, ownerSess := ownerWithSession(t, mgr, root, "Winerim")
	own := owner.Owner{ID: info.ID, SessionID: ownerSess.ID, CodebaseKey: info.CodebaseKey}
	mine, other := liveOwnerTask(t, mgr, ownerSess, "mine"), liveOwnerTask(t, mgr, ownerSess, "other")
	worker, idle := ownerChild(t, mgr, root, "worker"), ownerChild(t, mgr, root, "idle")
	if err := mgr.linkExecution(ctx, own, mine.ID, worker.ID); err != nil {
		t.Fatal(err)
	}
	if err := mgr.linkExecution(ctx, own, other.ID, idle.ID); err != nil {
		t.Fatal(err)
	}
	// A continuation, not an explicit message: any origin is a start.
	publishRunStart(worker, 1, bus.RunOrigin{ContinueCurrent: true})
	pollUntil(t, 5*time.Second, "the executor's task marked started", func() bool {
		return liveTask(t, mgr, mine.ID).Status == tasks.StatusInProgress
	})
	if rec := liveTask(t, mgr, other.ID); rec.Status != tasks.StatusPending || rec.Execution.StartedAt != 0 {
		t.Fatalf("another session's run touched this task: %+v", rec)
	}
}

// A quiet executor's report names its open linked task once per stop, and the
// owner hears it again only after the executor runs and stops once more.
func TestQuietExecutorReportNamesItsTaskOncePerStop(t *testing.T) {
	shortReportWindow(t, time.Hour)
	shortOwnerQuiescence(t, 100*time.Millisecond)
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, ownerSess := ownerWithSession(t, mgr, root, "Winerim")
	task := liveOwnerTask(t, mgr, ownerSess, "Export invoices to CSV")
	child := ownerChild(t, mgr, root, "csv")

	res := runSessionsTool(t, ownerSess, map[string]any{"action": "send", "session_id": child.ID, "text": "export them", "task": float64(task.ID)})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	got := waitForOutbox(t, mgr, info.CodebaseKey, 1)
	if len(got[0].LinkedTasks) != 1 || got[0].LinkedTasks[0].ID != task.ID {
		t.Fatalf("the quiet executor's report does not name its task: %+v", got[0])
	}
	text := reportsMessage(owner.Owner{}, got[:1])
	want := "linked task #" + itoa(task.ID) + " «Export invoices to CSV» is still open. Before ending your turn: close it with evidence, " +
		"send the next step (to this session or a new one with task=" + itoa(task.ID) + "), or comment what it waits for and who unblocks it."
	if !strings.Contains(text, want) || !strings.Contains(text, liveTasksIdle) {
		t.Fatalf("report text:\n%s", text)
	}
	pollUntil(t, 5*time.Second, "the stop recorded", func() bool {
		ex := liveTask(t, mgr, task.ID).Execution
		return ex != nil && ex.StoppedAt != 0
	})
	if rec := liveTask(t, mgr, task.ID); rec.Status != tasks.StatusInProgress {
		t.Fatalf("a stop changed the task: %+v", rec)
	}

	// Another report of the same stop (no run started in between) says nothing.
	child.runtime.Bus.Publish(bus.RunEnded{SessionID: child.ID, RunGen: 77, FinalText: "still here"})
	got = waitForOutbox(t, mgr, info.CodebaseKey, 2)
	if len(got[1].LinkedTasks) != 0 {
		t.Fatalf("the same stop was said twice: %+v", got[1])
	}

	// Run again, stop again: said again.
	res = runSessionsTool(t, ownerSess, map[string]any{"action": "send", "session_id": child.ID, "text": "continue"})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	got = waitForOutbox(t, mgr, info.CodebaseKey, 3)
	if len(got[2].LinkedTasks) != 1 {
		t.Fatalf("a new stop after a run was not said: %+v", got[2])
	}
}

// A process that died mid-turn leaves started links nobody saw stop. The next
// start tells each owner once, through the outbox, and does not repeat it.
func TestBootReportsInterruptedExecutionsOnce(t *testing.T) {
	shortReportWindow(t, time.Hour)
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	ctx := context.Background()
	root, base := t.TempDir(), t.TempDir()
	repo := tasks.New(filepath.Join(t.TempDir(), tasks.DatabaseName))
	t.Cleanup(func() { _ = repo.Close() })
	cfg := core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"}
	start := func() *Manager {
		return NewManager(ctx, ManagerConfig{
			ProviderFactory: func(core.Model) (core.Provider, error) {
				return newMockProvider(simpleResponseHandler("hello")), nil
			},
			DefaultModel: core.Model{ID: "test", Provider: "mock"}, WorkspaceRoot: root,
			MoaCfg: cfg, ConfigLoader: isolatedTestConfigLoader(t, cfg), Tasks: repo,
			SessionBaseDir: base, SchedulePath: filepath.Join(t.TempDir(), "schedules.json"),
		})
	}
	mgr := start()
	info, ownerSess := ownerWithSession(t, mgr, root, "Winerim")
	own := owner.Owner{ID: info.ID, SessionID: ownerSess.ID, CodebaseKey: info.CodebaseKey}
	working := liveOwnerTask(t, mgr, ownerSess, "Migrate the invoices")
	delegated := liveOwnerTask(t, mgr, ownerSess, "Not started yet")
	// The executor is gone with the process: no session, no orderly stop.
	if err := mgr.linkExecution(ctx, own, working.ID, "lost-worker"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.linkExecution(ctx, own, delegated.ID, "never-ran"); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkExecutionStarted(ctx, "lost-worker"); err != nil {
		t.Fatal(err)
	}
	mgr.Shutdown()

	mgr = start()
	got := outbox(t, mgr, info.CodebaseKey)
	if len(got) != 1 || len(got[0].RestartTasks) != 1 || got[0].RestartTasks[0].ID != working.ID {
		t.Fatalf("restart entry: %+v", got)
	}
	text := reportsMessage(owner.Owner{}, got)
	if !strings.Contains(text, "After the restart, these linked tasks have a session that was working and is not anymore:") ||
		!strings.Contains(text, "#"+itoa(working.ID)+" «Migrate the invoices» — session lost-worker (stopped mid-turn)") {
		t.Fatalf("restart entry text:\n%s", text)
	}
	if ex := liveTask(t, mgr, working.ID).Execution; ex == nil || ex.StoppedAt == 0 {
		t.Fatalf("the interrupted link was not marked after the entry: %+v", ex)
	}
	mgr.Shutdown()

	mgr = start()
	defer mgr.Shutdown()
	restarts := 0
	for _, rep := range outbox(t, mgr, info.CodebaseKey) {
		if len(rep.RestartTasks) > 0 {
			restarts++
		}
	}
	if restarts != 1 {
		t.Fatalf("a second start repeated the restart entry: %d entries", restarts)
	}
}

func TestReportsSayTheNextScheduledRun(t *testing.T) {
	rep := owner.Report{Status: callbackStatusDone, NextRun: &owner.ReportNextRun{TaskID: 12, At: "2026-11-12T09:00:00Z"}}
	text := reportsMessage(owner.Owner{}, []owner.Report{rep})
	if !strings.Contains(text, "  "+liveTasksIdle+"; next scheduled run 2026-11-12T09:00:00Z (#12)\n") {
		t.Fatalf("now line without the next run:\n%s", text)
	}
	waiting := owner.Report{Status: callbackStatusNeedsInput, LinkedTasks: []owner.ReportTask{{ID: 3, Title: "Ship"}}}
	if text := reportsMessage(owner.Owner{}, []owner.Report{waiting}); !strings.Contains(text,
		"linked task #3 «Ship» is still open. It waits for the answer above; comment it on the task if it is a real blocker.") {
		t.Fatalf("waiting notice:\n%s", text)
	}
}
