package serve

import (
	"context"
	"sync"
	"testing"

	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// Deleting the session a run created, before the run was bound to it,
// settles the run first: a restart cannot create it again. If that SQLite
// write fails the delete is refused and the file stays.
func TestScheduledNewSessionDeleteBeforeBinding(t *testing.T) {
	setup := func(t *testing.T) (*schedHarness, *tasks.Repo, tasks.Occurrence, string, chan struct{}) {
		h := newSchedHarness(t, newMockProvider(simpleResponseHandler("ok")), "2026-09-30T08:00:00Z")
		h.start()
		sid := markedSession(t, h, 1, 1)
		h.stop()
		r := h.repo()
		o := readyNewRun(t, h, r, newTarget(t, h.root))
		release := make(chan struct{})
		var once sync.Once
		h.hooks.beforePass = func(ctx context.Context) {
			once.Do(func() {
				select {
				case <-release:
				case <-ctx.Done():
				}
			})
		}
		h.start()
		return h, r, o, sid, release
	}
	t.Run("settles", func(t *testing.T) {
		h, r, o, sid, release := setup(t)
		if err := h.mgr.Delete(sid); err != nil {
			t.Fatal(err)
		}
		got := occNow(t, r, o.ID)
		if got.State != tasks.OccFailed || got.Reason != tasks.ReasonSessionDeleted {
			t.Fatalf("run after delete = %+v", got)
		}
		close(release)
		h.pass()
		h.stop()
		h.hooks.beforePass = nil
		h.start()
		h.pass()
		if n := countSessions(t, h.base); n != 0 {
			t.Fatalf("%d sessions after restart: the run's session was recreated", n)
		}
		if got := occNow(t, r, o.ID); got.State != tasks.OccFailed {
			t.Fatalf("run = %+v", got)
		}
	})
	t.Run("sql_failure", func(t *testing.T) {
		h, r, o, sid, release := setup(t)
		defer close(release)
		drop := abortTrigger(t, h.dbPath(), "no_settle", "BEFORE UPDATE ON task_occurrences")
		if err := h.mgr.Delete(sid); err == nil {
			t.Fatal("delete succeeded without settling the run")
		}
		if _, _, err := session.FindSessionReadOnly(h.base, sid); err != nil {
			t.Fatalf("session file removed: %v", err)
		}
		if got := occNow(t, r, o.ID); got.State != tasks.OccReady {
			t.Fatalf("run = %+v", got)
		}
		drop()
		if err := h.mgr.Delete(sid); err != nil {
			t.Fatal(err)
		}
		if got := occNow(t, r, o.ID); got.State != tasks.OccFailed {
			t.Fatalf("run after retried delete = %+v", got)
		}
	})
	t.Run("bound", func(t *testing.T) {
		h := newSchedHarness(t, newMockProvider(simpleResponseHandler("ok")), "2026-09-30T08:00:00Z")
		h.start()
		sid := h.savedSession()
		r := h.repo()
		o := fireOnce(t, h, r, "held", toSession(sid), tasks.Delivery{Saved: tasks.DeliverHold})
		waitNoticeByID(t, r, o.NoticeID, tasks.NoticeHeld)
		if err := h.mgr.Delete(sid); err != nil {
			t.Fatal(err)
		}
		got := occNow(t, r, o.ID)
		if got.State != tasks.OccFailed || got.Reason != tasks.ReasonSessionDeleted {
			t.Fatalf("run = %+v", got)
		}
		if n := noticeByID(t, r, o.NoticeID); n.State != tasks.NoticeFailed {
			t.Fatalf("notice = %+v", n)
		}
	})
}
