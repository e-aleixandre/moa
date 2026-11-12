package serve

// Re-review 2's probes, ported from the reviewer's overlays: scheduling seams
// replace the overlay gates; assertions are unchanged.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/owner"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// R2.1. The seam pauses OwnerExecutions between its real task SELECT and the
// SELECTs attaching relations; the real repository's Delete runs there, as a
// browser deleting this task would. A link revoked between the reads is simply
// not linked.
func TestReReview2DeletingTaskDuringNoticeRenderDoesNotPanic(t *testing.T) {
	m, own, child, task := reviewLinkedChild(t)
	m.reports.BeginShutdown()
	removed := false
	hook := func(where string, recs []tasks.Record) {
		if removed || !strings.Contains(where, "id IN (SELECT task_id FROM task_execution_links)") || len(recs) == 0 || recs[0].ID != task.ID {
			return
		}
		if err := m.tasks.Delete(context.Background(), task.ID, task.Revision, ""); err != nil {
			t.Error(err)
			return
		}
		removed = true
	}
	tasks.AfterBareRead.Store(&hook)
	defer tasks.AfterBareRead.Store(nil)
	var got []owner.Report
	var panicked any
	func() {
		defer func() { panicked = recover() }()
		got = m.currentTaskNotices(own, []owner.Report{{SessionID: child.ID, LinkedTasks: []owner.ReportTask{{ID: task.ID, Title: task.Title}}}})
	}()
	if !removed {
		t.Fatal("fixture did not remove task between the actual SQL reads")
	}
	if panicked != nil {
		t.Fatalf("real notice-render path panics when a task is deleted between its selection and execution attachment: %s", fmt.Sprint(panicked))
	}
	if len(got) != 1 || len(got[0].LinkedTasks) != 0 {
		t.Fatalf("deleted task still warned: %+v", got)
	}
}

func rereview2OwnerSettled(t *testing.T, s *ManagedSession) {
	t.Helper()
	pollUntil(t, 5*time.Second, "owner's task-notice run settled", func() bool {
		for _, msg := range s.History() {
			if msg.Role == "assistant" {
				return sessState(s) == StateIdle
			}
		}
		return false
	})
	s.runtime.Bus.Drain(time.Second)
}

// R2.2. Real permission resolution resumes a real foreground tool before the next
// provider request. The durable stopped marker must not survive that work.
func TestReReview2PermissionResumesBeforeNextProvider(t *testing.T) {
	shortReportWindow(t, time.Hour)
	shortOwnerQuiescence(t, 100*time.Millisecond)
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	root := t.TempDir()
	marker, release := filepath.Join(root, "tool-entered"), filepath.Join(root, "tool-release")
	command := fmt.Sprintf("touch %q; while [ ! -e %q ]; do sleep 0.02; done", marker, release)
	provider := reviewProvider(func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		if !requestMentions(req, "do the approved foreground work") {
			return simpleResponse("owner ready"), nil
		}
		for _, msg := range req.Messages {
			if msg.Role == "tool_result" {
				return simpleResponse("finished"), nil
			}
		}
		msg := core.Message{Role: "assistant", Content: []core.Content{core.ToolCallContent("approved-work", "bash", map[string]any{"command": command})}, StopReason: "tool_use", Timestamp: time.Now().Unix()}
		ch := make(chan core.AssistantEvent, 2)
		ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
		ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		close(ch)
		return ch, nil
	})
	m := newTestManagerWithConfig(t, context.Background(), provider, t.TempDir(), core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"})
	info, ownSess := ownerWithSession(t, m, root, "Review2 owner")
	child, err := m.CreateSession(CreateOpts{CWD: root, Title: "permission executor", PermissionMode: "ask"})
	if err != nil {
		t.Fatal(err)
	}
	task := liveOwnerTask(t, m, ownSess, "approved tool")
	rereview2OwnerSettled(t, ownSess)
	t.Cleanup(func() { _ = os.WriteFile(release, []byte("release"), 0600) })
	t.Cleanup(func() { m.reports.BeginShutdown() })
	res := runSessionsTool(t, ownSess, map[string]any{"action": "send", "session_id": child.ID, "text": "do the approved foreground work", "task": float64(task.ID)})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	var perm string
	pollUntil(t, 5*time.Second, "permission pending", func() bool {
		p, err := bus.QueryTyped[bus.GetPendingApproval, bus.PendingApprovalInfo](child.runtime.Bus, bus.GetPendingApproval{})
		if err == nil && p.Permission != nil {
			perm = p.Permission.ID
		}
		return perm != ""
	})
	pending := waitForOutbox(t, m, info.CodebaseKey, 1)
	if len(pending[0].LinkedTasks) != 1 {
		t.Fatalf("fixture: %+v", pending)
	}
	pollUntil(t, 5*time.Second, "permission stop durably marked", func() bool {
		return liveTask(t, m, task.ID).Execution.StoppedAt != 0
	})
	if err := child.runtime.Bus.Execute(bus.ResolvePermission{PermissionID: perm, Approved: true}); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 5*time.Second, "real approved tool executing", func() bool { _, err := os.Stat(marker); return err == nil })
	rec := liveTask(t, m, task.ID)
	candidates, err := m.tasks.InterruptedExecutions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	recovered := false
	for _, r := range candidates {
		recovered = recovered || r.ID == task.ID
	}
	if rec.Execution.StoppedAt != 0 || !recovered {
		t.Fatalf("approved foreground tool is running but its previous stop still suppresses crash recovery: state=%s stopped_at=%d boot_candidate=%v", sessState(child), rec.Execution.StoppedAt, recovered)
	}
}

// R2.3. A user's Stop of the foreground, still unreported at shutdown, keeps
// its label; the background work it left dies with the process, so its promise
// to report again is concluded like any other.
func TestReReview2UserStoppedForegroundDoesNotLeaveBackgroundPromiseAtShutdown(t *testing.T) {
	shortReportWindow(t, time.Hour)
	shortOwnerQuiescence(t, 5*time.Minute)
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	entered := make(chan struct{})
	provider := reviewProvider(func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		if !requestMentions(req, "foreground waiting for user Stop") {
			return simpleResponse("owner ready"), nil
		}
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	m := newTestManagerWithConfig(t, context.Background(), provider, t.TempDir(), core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"})
	root := t.TempDir()
	info, ownSess := ownerWithSession(t, m, root, "user stop with background")
	child := ownerChild(t, m, root, "foreground user stopped")
	task := liveOwnerTask(t, m, ownSess, "foreground and background work")
	rereview2OwnerSettled(t, ownSess)
	res := runSessionsTool(t, ownSess, map[string]any{"action": "send", "session_id": child.ID, "text": "foreground waiting for user Stop", "task": float64(task.ID)})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	<-entered
	child.runtime.Bus.Publish(bus.BashJobStarted{SessionID: child.ID, JobID: "survives-foreground-stop"})
	child.runtime.Bus.Drain(time.Second)
	ended := make(chan bus.RunEnded, 1)
	unsub := child.runtime.Bus.Subscribe(func(e bus.RunEnded) { ended <- e })
	defer unsub()
	if err := m.Cancel(child.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-ended:
		if !e.Cancelled || e.CancelCause != bus.ErrStopByUser {
			t.Fatalf("fixture cause: %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("user Stop did not settle")
	}
	child.ownerObserver.sync()
	if got := outbox(t, m, info.CodebaseKey); len(got) != 0 {
		t.Fatalf("fixture: user Stop report must still be pending: %+v", got)
	}
	m.Shutdown()
	got := outbox(t, m, info.CodebaseKey)
	var userAt string
	for _, rep := range got {
		if rep.SessionID == child.ID && !rep.InterruptedByRestart {
			userAt = rep.At
		}
	}
	concluded := false
	for _, rep := range got {
		if userAt != "" && rep.InterruptedByRestart && rep.FollowUpOf == userAt {
			concluded = true
		}
	}
	if !concluded || liveTask(t, m, task.ID).Execution.StoppedAt == 0 {
		t.Fatalf("shutdown preserves the foreground's user Stop but leaves its background continuation promise alive: reports=%+v execution=%+v", got, liveTask(t, m, task.ID).Execution)
	}
}

// R2.4. The seams pause a child built before its project had an owner after
// persistence/before publication, and the owner's send right after its early
// loaded snapshot. No condition or write changes.
func TestReReview2ExecutorPublishedBetweenObserverSnapshotAndAdmission(t *testing.T) {
	shortReportWindow(t, time.Hour)
	shortOwnerQuiescence(t, 100*time.Millisecond)
	m := newOwnerTestManager(t, context.Background())
	root := t.TempDir()
	ready, publish := make(chan *ManagedSession, 1), make(chan struct{})
	snapshot, send := make(chan struct{}), make(chan struct{})
	created := make(chan error, 1)
	beforeSessionPublish = func(s *ManagedSession) {
		if s.CWD == root && s.title() == "hidden before owner" {
			ready <- s
			<-publish
		}
	}
	defer func() { beforeSessionPublish = nil; afterSendSnapshot = nil }()
	go func() { _, err := m.CreateSession(CreateOpts{CWD: root, Title: "hidden before owner"}); created <- err }()
	var child *ManagedSession
	select {
	case child = <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("child did not reach publication boundary")
	}
	info, ownSess := ownerWithSession(t, m, root, "created while child builds")
	if child.ownerObserver != nil {
		close(publish)
		<-created
		t.Fatal("fixture: child observed before project had owner")
	}
	task := liveOwnerTask(t, m, ownSess, "real accepted work after publication")
	pollUntil(t, 5*time.Second, "owner notice finished", func() bool {
		for _, msg := range ownSess.History() {
			if msg.Role == "assistant" {
				return sessState(ownSess) == StateIdle
			}
		}
		return false
	})
	ownSess.runtime.Bus.Drain(time.Second)
	roster := runSessionsTool(t, ownSess, map[string]any{"action": "list"})
	if !strings.Contains(toolText(roster), child.ID) {
		close(publish)
		<-created
		t.Fatalf("fixture: saved child is not discoverable to owner %s", info.ID)
	}
	var snapshotted *ManagedSession
	afterSendSnapshot = func(id string, loaded bool) {
		if id == child.ID {
			snapshotted, _ = m.Get(id)
			if !loaded || snapshotted == nil || snapshotted == child {
				t.Error("fixture: owner did not first resume a different runtime")
			} else {
				snapshotted.mu.Lock()
				observed := snapshotted.ownerObserver != nil
				snapshotted.mu.Unlock()
				if !observed {
					t.Error("fixture: resumed runtime lacks its observer")
				}
			}
			close(snapshot)
			<-send
		}
	}
	sent := make(chan bool, 1)
	go func() {
		res := runSessionsTool(t, ownSess, map[string]any{"action": "send", "session_id": child.ID, "text": "start accepted task now", "task": float64(task.ID)})
		if res.IsError {
			t.Errorf("send failed: %s", toolText(res))
		}
		sent <- res.IsError
	}()
	select {
	case <-snapshot:
	case <-time.After(5 * time.Second):
		close(publish)
		<-created
		t.Fatal("send did not reach loaded-snapshot boundary")
	}
	defer func() {
		if snapshotted != nil && snapshotted != child {
			snapshotted.discardOwnerReports()
			snapshotted.infra.sessionCancel()
			snapshotted.runtime.Close()
			snapshotted.waitOwnerReportWorkers()
		}
	}()
	close(publish)
	if err := <-created; err != nil {
		close(send)
		<-sent
		t.Fatal(err)
	}
	close(send)
	if <-sent {
		t.Fatal("owner send refused")
	}
	pollUntil(t, 5*time.Second, "real task instruction completed", func() bool {
		for _, msg := range child.History() {
			if msg.Role == "assistant" && assistantText(msg) != "" {
				return true
			}
		}
		return false
	})
	child.runtime.Bus.Drain(time.Second)
	rec := liveTask(t, m, task.ID)
	child.mu.Lock()
	observed := child.ownerObserver != nil
	child.mu.Unlock()
	if rec.Execution == nil || rec.Execution.StartedAt == 0 || !observed {
		t.Fatalf("real accepted linked instruction has no report observer after publication race: observed=%v execution=%+v", observed, rec.Execution)
	}
}
