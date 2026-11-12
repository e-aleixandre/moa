package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/owner"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

func reviewLinkedChild(t *testing.T) (*Manager, owner.Owner, *ManagedSession, tasks.Record) {
	t.Helper()
	shortReportWindow(t, time.Hour)
	shortOwnerQuiescence(t, 100*time.Millisecond)
	m := newOwnerTestManager(t, context.Background())
	root := t.TempDir()
	info, ownSess := ownerWithSession(t, m, root, "Review owner")
	own := owner.Owner{ID: info.ID, SessionID: ownSess.ID, CodebaseKey: info.CodebaseKey}
	child := ownerChild(t, m, root, "review child")
	task := liveOwnerTask(t, m, ownSess, "Live task")
	if err := m.linkExecution(context.Background(), own, task.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	return m, own, child, task
}

func TestReviewRestartedExecutorCannotBeMarkedStoppedByPriorReport(t *testing.T) {
	m, own, child, task := reviewLinkedChild(t)
	publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
	pollUntil(t, 5*time.Second, "first start recorded", func() bool { return liveTask(t, m, task.ID).Execution.StartedAt != 0 })
	// A heartbeat has exclusive admission while it waits for the actor. This
	// holds the previous report between reading the link and admitting it.
	m.reports.admission.Lock()
	locked := true
	defer func() {
		if locked {
			m.reports.admission.Unlock()
		}
	}()
	publishRunEnd(child, 1, "old turn done")
	pollUntil(t, 5*time.Second, "prior report waiting for admission", func() bool {
		if child.ownerDetachMu.TryLock() {
			child.ownerDetachMu.Unlock()
			return false
		}
		return true
	})
	publishRunStart(child, 2, bus.RunOrigin{Explicit: true})
	child.ownerObserver.sync()
	child.runtime.Bus.Drain(time.Second)
	// Join all start writes while the old stop report is blocked.
	m.executions.wg.Wait()
	if !child.ownerObserver.running() {
		t.Fatal("fixture: executor did not restart")
	}
	m.reports.admission.Unlock()
	locked = false
	child.ownerObserver.waitWorkers()
	got := outbox(t, m, own.CodebaseKey)
	ex := liveTask(t, m, task.ID).Execution
	if ex.StoppedAt != 0 || len(got) != 1 || len(got[0].LinkedTasks) != 0 {
		t.Fatalf("restarted executor was warned/marked stopped by the old report: stopped_at=%d running=%v reports=%+v", ex.StoppedAt, child.ownerObserver.running(), got)
	}
}

func TestReviewRelinkBeforeDeliveryRevokesOldReportNotice(t *testing.T) {
	m, own, child, task := reviewLinkedChild(t)
	publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
	child.runtime.Bus.Drain(time.Second)
	m.executions.wg.Wait()
	publishRunEnd(child, 1, "old executor finished")
	child.ownerObserver.sync()
	child.ownerObserver.waitWorkers()
	pending := outbox(t, m, own.CodebaseKey)
	if len(pending) != 1 || len(pending[0].LinkedTasks) != 1 {
		t.Fatalf("fixture: %+v", pending)
	}
	if err := m.linkExecution(context.Background(), own, task.ID, "replacement-worker"); err != nil {
		t.Fatal(err)
	}
	ownSess, ok := m.Get(own.SessionID)
	if !ok {
		t.Fatal("owner missing")
	}
	m.reports.nudge(own.CodebaseKey)
	text := waitForOwnerReports(t, ownSess, 1)[0]
	if strings.Contains(text, "linked task #") {
		t.Fatalf("old executor still mentions task after relink, when rendered for delivery:\n%s", text)
	}
}

func TestReviewStopRemainsUnacknowledgedWhenOutboxCannotPersist(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission failure requires unprivileged process")
	}
	m, own, child, task := reviewLinkedChild(t)
	publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
	pollUntil(t, 5*time.Second, "start recorded", func() bool { return liveTask(t, m, task.ID).Execution.StartedAt != 0 })
	dir := m.reports.store.CodebaseDir(own.CodebaseKey)
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	publishRunEnd(child, 1, "quiet but report cannot persist")
	child.ownerObserver.sync()
	child.ownerObserver.waitWorkers()
	persisted, err := m.reports.store.LoadReportsOutbox(own.CodebaseKey)
	if err != nil {
		t.Fatal(err)
	}
	ex := liveTask(t, m, task.ID).Execution
	candidates, err := m.tasks.InterruptedExecutions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ex.StoppedAt != 0 && len(persisted.Reports) == 0 && len(candidates) == 0 {
		t.Fatalf("stop was durably acknowledged without a durable report: stopped_at=%d persisted_reports=%d boot_candidates=%d; crash now permanently loses the warning", ex.StoppedAt, len(persisted.Reports), len(candidates))
	}
}

func TestReviewShutdownConcludesAlreadyReportedBackgroundTurn(t *testing.T) {
	m, own, child, task := reviewLinkedChild(t)
	publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
	pollUntil(t, 5*time.Second, "start recorded", func() bool { return liveTask(t, m, task.ID).Execution.StartedAt != 0 })
	child.runtime.Bus.Publish(bus.BashJobStarted{SessionID: child.ID, JobID: "long-job"})
	publishRunEnd(child, 1, "job still working")
	pending := waitForOutbox(t, m, own.CodebaseKey, 1)
	if pending[0].BackgroundCount != 1 {
		t.Fatalf("fixture: %+v", pending)
	}
	child.ownerObserver.waitWorkers()
	m.Shutdown()
	pending = outbox(t, m, own.CodebaseKey)
	restarted := false
	for _, rep := range pending {
		restarted = restarted || rep.InterruptedByRestart
	}
	ex := liveTask(t, m, task.ID).Execution
	if !restarted || ex.StoppedAt == 0 {
		t.Fatalf("orderly shutdown left the promised continuation silent: restart_report=%v stopped_at=%d reports=%+v", restarted, ex.StoppedAt, pending)
	}
}

func TestReviewUserStopNotRelabeledWhenObserverLagsShutdown(t *testing.T) {
	shortReportWindow(t, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	provider := newMockProvider(toolCallHandlerFor("list-first", "tasks", map[string]any{"action": "list"}), func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	root := t.TempDir()
	cfg := core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"}
	m := NewManager(ctx, ManagerConfig{ProviderFactory: func(core.Model) (core.Provider, error) { return provider, nil }, DefaultModel: core.Model{ID: "test", Provider: "mock"}, WorkspaceRoot: root, MoaCfg: cfg, ConfigLoader: isolatedTestConfigLoader(t, cfg), SessionBaseDir: t.TempDir()})
	defer m.Shutdown()
	info, _ := ownerWithSession(t, m, root, "Review owner")
	child := ownerChild(t, m, root, "stopped by user")
	terminal := make(chan bus.RunEnded, 1)
	unsub := child.runtime.Bus.Subscribe(func(e bus.RunEnded) { terminal <- e })
	defer unsub()
	if _, _, _, err := m.Send(child.ID, "work", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider not entered")
	}
	child.ownerObserver.sync()
	child.ownerObserver.mu.Lock()
	if err := m.Cancel(child.ID); err != nil {
		child.ownerObserver.mu.Unlock()
		t.Fatal(err)
	}
	select {
	case e := <-terminal:
		if !e.Cancelled {
			child.ownerObserver.mu.Unlock()
			t.Fatal("fixture: not cancelled")
		}
	case <-time.After(5 * time.Second):
		child.ownerObserver.mu.Unlock()
		t.Fatal("Stop did not end run")
	}
	// Stop has already ended the run. Only delivery of its owner event lags.
	cancel()
	child.ownerObserver.mu.Unlock()
	child.ownerObserver.sync()
	child.ownerObserver.waitWorkers()
	got := outbox(t, m, info.CodebaseKey)
	if len(got) != 1 || got[0].InterruptedByRestart {
		t.Fatalf("completed user Stop relabeled as restart because subscriber lagged: %+v", got)
	}
}

func TestReviewIncludeOwnersRequiresBrowserOwnerAuth(t *testing.T) {
	m, _, _, _ := reviewLinkedChild(t)
	handler := NewServer(m, WithAuthToken("review-only-owner", false))
	for _, credential := range []string{"", "wrong"} {
		req := httptest.NewRequest("GET", "http://localhost/api/tasks?include_owners=1", nil)
		if credential != "" {
			req.AddCookie(&http.Cookie{Name: authCookieName, Value: credential})
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("non-owner credential %q got status=%d body=%s", credential, rec.Code, rec.Body.String())
		}
	}
}

func TestReviewQueuedLinkedSteerStartsWhenConsumed(t *testing.T) {
	shortReportWindow(t, time.Hour)
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	var consumed atomic.Bool
	provider := reviewProvider(func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		childRequest := false
		for _, msg := range req.Messages {
			for _, c := range msg.Content {
				childRequest = childRequest || strings.Contains(c.Text, "review unrelated work")
				if strings.Contains(c.Text, "review linked instruction") {
					consumed.Store(true)
				}
			}
		}
		if childRequest && calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return simpleResponse("completed requested step"), nil
	})
	m := newTestManagerWithConfig(t, context.Background(), provider, t.TempDir(), core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"})
	root := t.TempDir()
	info, ownSess := ownerWithSession(t, m, root, "Review owner")
	own := owner.Owner{ID: info.ID, SessionID: ownSess.ID, CodebaseKey: info.CodebaseKey}
	child := ownerChild(t, m, root, "busy executor")
	if _, _, _, err := m.Send(child.ID, "review unrelated work", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider not started")
	}
	child.runtime.Bus.Drain(time.Second)
	m.executions.wg.Wait()
	task := liveOwnerTask(t, m, ownSess, "Next step handed to busy executor")
	res := m.ownerSendWithTask(context.Background(), own, map[string]any{"session_id": child.ID, "text": "review linked instruction", "task": float64(task.ID)})
	if res.IsError {
		close(release)
		t.Fatal(toolText(res))
	}
	before := liveTask(t, m, task.ID)
	if before.Status != tasks.StatusPending || before.Execution.StartedAt != 0 {
		close(release)
		t.Fatalf("queued-only message marked started: %+v", before)
	}
	close(release)
	pollUntil(t, 5*time.Second, "linked instruction consumed and run idle", func() bool { return consumed.Load() && sessState(child) == StateIdle })
	child.runtime.Bus.Drain(time.Second)
	m.executions.wg.Wait()
	after := liveTask(t, m, task.ID)
	if after.Status != tasks.StatusInProgress || after.Execution.StartedAt == 0 {
		t.Fatalf("provider consumed and completed linked steer, but task never started: status=%s execution=%+v", after.Status, after.Execution)
	}
}

func TestReviewRestartLineIncludesNextScheduledRun(t *testing.T) {
	text := nowLine(owner.Report{InterruptedByRestart: true, NextRun: &owner.ReportNextRun{TaskID: 42, At: "2026-11-12T09:00:00Z"}})
	if !strings.Contains(text, "next scheduled run 2026-11-12T09:00:00Z (#42)") {
		t.Fatalf("restart line hid a known future run: %s", text)
	}
}

type reviewProvider func(context.Context, core.Request) (<-chan core.AssistantEvent, error)

func (p reviewProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	return p(ctx, req)
}

func TestReviewSavedExecutorRunStillMarksItsLinkedTaskStarted(t *testing.T) {
	m, own, child, task := reviewLinkedChild(t)
	closeSession(t, m, child.ID)
	res := m.ownerSendToSession(own, child.ID, "execute the linked task after resume")
	if res.IsError {
		t.Fatal(toolText(res))
	}
	pollUntil(t, 5*time.Second, "saved executor's start recorded", func() bool {
		rec := liveTask(t, m, task.ID)
		return rec.Status == tasks.StatusInProgress && rec.Execution != nil && rec.Execution.StartedAt != 0
	})
}
