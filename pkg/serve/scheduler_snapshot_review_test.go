package serve

import (
	"context"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/schedule"
)

func snapshotReviewBlockedDeliveries() int {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	count := 0
	for _, stack := range strings.Split(string(buf[:n]), "\n\n") {
		if strings.Contains(stack, "(*schedulerService).deliver(") &&
			strings.Contains(stack, "sync.(*RWMutex).RLock") {
			count++
		}
	}
	return count
}

func snapshotReviewWait(t *testing.T, done <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func snapshotReviewFailStoreWrites(t *testing.T, s *schedulerService) func() {
	t.Helper()
	path := s.store.Path()
	backup := path + ".snapshot-review-backup"
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		_ = os.Rename(backup, path)
		t.Fatal(err)
	}
	var once sync.Once
	restore := func() {
		once.Do(func() {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(backup, path); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Cleanup(restore)
	return restore
}

func TestScheduleSnapshotReviewCancelWhileAcceptedAppendPending(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	mgr.scheduler.Close()
	sess, err := mgr.CreateSession(CreateOpts{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	record, err := mgr.scheduler.create(schedule.Schedule{SessionID: sess.ID, Text: "must not report canceled then execute", DueAt: time.Now().Add(-time.Second), TimeZone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}

	// Run admission is synchronous, but the first-run gate precedes the
	// asynchronous history append. Keep that real boundary open through cancel.
	gateEntered := make(chan struct{})
	releaseGate := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseGate) }) }
	defer release()
	sess.runtime.Context().BeforeFirstRun = func(ctx context.Context) error {
		close(gateEntered)
		select {
		case <-releaseGate:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	restore := snapshotReviewFailStoreWrites(t, mgr.scheduler)
	mgr.scheduler.deliverDue(mgr, time.Now())
	snapshotReviewWait(t, gateEntered, "accepted scheduled run at first-run gate")
	if got, ok := mgr.scheduler.store.Get(record.ID); !ok || got.Status != schedule.StatusPending {
		t.Fatalf("status-save failure not reproduced: %#v, exists=%v", got, ok)
	}
	if scheduleOccurrenceExists(sess.History(), record.OccurrenceID) {
		t.Fatal("append is not pending")
	}
	if got := sess.runtime.State.Current(); got != bus.StateRunning {
		t.Fatalf("run not accepted: state=%q", got)
	}
	restore()
	res, err := mgr.ExecCommand(sess.ID, "/schedule cancel "+record.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	release()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !sess.runtime.WaitSettled(ctx) {
		t.Fatal("accepted scheduled run did not settle")
	}
	if !scheduleOccurrenceExists(sess.History(), record.OccurrenceID) {
		t.Fatal("accepted occurrence did not append after gate release")
	}
	if res.OK || !strings.Contains(res.Message, "already delivered") {
		got, _ := mgr.scheduler.store.Get(record.ID)
		t.Fatalf("cancel succeeded before an accepted occurrence appended: result=%#v, status=%q, occurrence now in history", res, got.Status)
	}
}

func TestScheduleSnapshotReviewSettlementWinsBeforeDelivery(t *testing.T) {
	for _, action := range []string{"cancel", "remove-records", "delete-session"} {
		t.Run(action, func(t *testing.T) {
			mgr := newTestManager(t, context.Background(), newMockProvider())
			mgr.scheduler.Close()
			sess, err := mgr.CreateSession(CreateOpts{CWD: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			record, err := mgr.scheduler.create(schedule.Schedule{SessionID: sess.ID, Text: "must not deliver stale snapshot", DueAt: time.Now().Add(-time.Second), TimeZone: "UTC"})
			if err != nil {
				t.Fatal(err)
			}
			started := make(chan bus.RunStarted, 2)
			unsub := sess.runtime.Bus.Subscribe(func(e bus.RunStarted) { started <- e })
			defer unsub()

			sess.lifecycle.Lock()
			locked := true
			defer func() {
				if locked {
					sess.lifecycle.Unlock()
				}
			}()
			deliveryDone := make(chan struct{})
			go func() { defer close(deliveryDone); mgr.scheduler.deliverDue(mgr, time.Now()) }()
			pollUntil(t, 5*time.Second, "delivery past due snapshot and waiting for lifecycle", func() bool {
				return snapshotReviewBlockedDeliveries() == 1
			})

			var deleteDone chan error
			switch action {
			case "cancel":
				if _, err := mgr.scheduler.cancel(sess, record.ID); err != nil {
					t.Fatal(err)
				}
			case "remove-records":
				if err := mgr.scheduler.deleteSession(sess.ID); err != nil {
					t.Fatal(err)
				}
			case "delete-session":
				deleteDone = make(chan error, 1)
				go func() { deleteDone <- mgr.Delete(sess.ID) }()
				pollUntil(t, 5*time.Second, "delete to close admission", sess.closing.Load)
			}
			sess.lifecycle.Unlock()
			locked = false
			snapshotReviewWait(t, deliveryDone, "stale snapshot delivery to return")
			if deleteDone != nil {
				select {
				case err := <-deleteDone:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("session deletion did not complete")
				}
			}
			sess.runtime.Bus.Drain(5 * time.Second)
			select {
			case e := <-started:
				t.Fatalf("stale snapshot started a run after %s: %#v", action, e)
			default:
			}
			if scheduleOccurrenceExists(sess.History(), record.OccurrenceID) {
				t.Fatalf("stale snapshot appended an occurrence after %s", action)
			}
		})
	}
}

func TestScheduleSnapshotReviewConcurrentDueSnapshotsDoNotDuplicate(t *testing.T) {
	for _, failWrites := range []bool{false, true} {
		name := "durable-status"
		if failWrites {
			name = "status-save-fails-append-pending"
		}
		t.Run(name, func(t *testing.T) {
			mgr := newTestManager(t, context.Background(), newMockProvider())
			mgr.scheduler.Close()
			sess, err := mgr.CreateSession(CreateOpts{CWD: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			record, err := mgr.scheduler.create(schedule.Schedule{SessionID: sess.ID, Text: "exactly one", DueAt: time.Now().Add(-time.Second), TimeZone: "UTC"})
			if err != nil {
				t.Fatal(err)
			}
			gateEntered := make(chan struct{})
			releaseGate := make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(releaseGate) }) }
			defer release()
			sess.runtime.Context().BeforeFirstRun = func(ctx context.Context) error {
				close(gateEntered)
				select {
				case <-releaseGate:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if failWrites {
				snapshotReviewFailStoreWrites(t, mgr.scheduler)
			}
			sess.lifecycle.Lock()
			locked := true
			defer func() {
				if locked {
					sess.lifecycle.Unlock()
				}
			}()
			deliveryDone := make(chan struct{}, 2)
			for range 2 {
				go func() { mgr.scheduler.deliverDue(mgr, time.Now()); deliveryDone <- struct{}{} }()
			}
			pollUntil(t, 5*time.Second, "both deliveries past pending snapshots and idle checks", func() bool {
				return snapshotReviewBlockedDeliveries() == 2
			})
			sess.lifecycle.Unlock()
			locked = false
			snapshotReviewWait(t, gateEntered, "scheduled run at append gate")
			for range 2 {
				snapshotReviewWait(t, deliveryDone, "concurrent delivery to return")
			}
			release()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if !sess.runtime.WaitSettled(ctx) {
				t.Fatal("scheduled run did not settle")
			}
			count := 0
			for _, msg := range sess.History() {
				if msg.Custom["source"] == "schedule" && msg.Custom["occurrence_id"] == record.OccurrenceID {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("scheduled occurrence appeared %d times, want one", count)
			}
		})
	}
}

func snapshotReviewDueRecord(t *testing.T, mgr *Manager, text string) (*ManagedSession, schedule.Schedule) {
	t.Helper()
	mgr.scheduler.Close()
	sess, err := mgr.CreateSession(CreateOpts{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	record, err := mgr.scheduler.create(schedule.Schedule{SessionID: sess.ID, Text: text, DueAt: time.Now().Add(-time.Second), TimeZone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	return sess, record
}

func snapshotReviewAccepted(s *schedulerService, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.accepted[id]
	return ok
}

func TestScheduleSnapshotReviewAcceptedMarkerLifecycle(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, record := snapshotReviewDueRecord(t, mgr, "marker lifecycle")
	restore := snapshotReviewFailStoreWrites(t, mgr.scheduler)
	mgr.scheduler.deliverDue(mgr, time.Now())
	if !snapshotReviewAccepted(mgr.scheduler, record.ID) {
		t.Fatal("marker missing after failed durable mark")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !sess.runtime.WaitSettled(ctx) {
		t.Fatal("run did not settle")
	}

	// Cleanup that rolls back keeps the marker.
	if err := mgr.scheduler.deleteSession(sess.ID); err == nil {
		t.Fatal("expected delete failure while store is unwritable")
	}
	if !snapshotReviewAccepted(mgr.scheduler, record.ID) {
		t.Fatal("marker dropped by failed deletion")
	}

	// A durable mark removes it, and a later retry does not duplicate.
	restore()
	mgr.scheduler.deliverDue(mgr, time.Now())
	if snapshotReviewAccepted(mgr.scheduler, record.ID) {
		t.Fatal("marker retained after durable mark")
	}
	if got, _ := mgr.scheduler.store.Get(record.ID); got.Status != schedule.StatusDelivered {
		t.Fatalf("status = %q, want delivered", got.Status)
	}
	count := 0
	for _, msg := range sess.History() {
		if msg.Custom["source"] == "schedule" && msg.Custom["occurrence_id"] == record.OccurrenceID {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("occurrence appeared %d times, want one", count)
	}
}

func TestScheduleSnapshotReviewAcceptedRetryDoesNotDuplicateAfterAppend(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, record := snapshotReviewDueRecord(t, mgr, "retry before append")
	gateEntered := make(chan struct{})
	releaseGate := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseGate) }) }
	defer release()
	sess.runtime.Context().BeforeFirstRun = func(ctx context.Context) error {
		close(gateEntered)
		select {
		case <-releaseGate:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	restore := snapshotReviewFailStoreWrites(t, mgr.scheduler)
	mgr.scheduler.deliverDue(mgr, time.Now())
	snapshotReviewWait(t, gateEntered, "run at first-run gate")
	restore()
	// Protection must survive the admission-to-append window and remain
	// sufficient for a delivery retry after the run settles.
	if !snapshotReviewAccepted(mgr.scheduler, record.ID) {
		t.Fatal("marker missing while append pending")
	}
	release()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !sess.runtime.WaitSettled(ctx) {
		t.Fatal("run did not settle")
	}
	mgr.scheduler.deliverDue(mgr, time.Now())
	count := 0
	for _, msg := range sess.History() {
		if msg.Custom["source"] == "schedule" && msg.Custom["occurrence_id"] == record.OccurrenceID {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("occurrence appeared %d times, want one", count)
	}
	if got, _ := mgr.scheduler.store.Get(record.ID); got.Status != schedule.StatusDelivered {
		t.Fatalf("status = %q, want delivered", got.Status)
	}
}

func TestScheduleSnapshotReviewSessionDeletePrunesAcceptedMarker(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, record := snapshotReviewDueRecord(t, mgr, "prune on delete")
	restore := snapshotReviewFailStoreWrites(t, mgr.scheduler)
	mgr.scheduler.deliverDue(mgr, time.Now())
	restore()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !sess.runtime.WaitSettled(ctx) {
		t.Fatal("run did not settle")
	}
	if !snapshotReviewAccepted(mgr.scheduler, record.ID) {
		t.Fatal("marker missing")
	}
	if err := mgr.scheduler.deleteSession(sess.ID); err != nil {
		t.Fatal(err)
	}
	if snapshotReviewAccepted(mgr.scheduler, record.ID) {
		t.Fatal("marker survived successful deletion")
	}
}
