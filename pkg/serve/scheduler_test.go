package serve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/schedule"
)

func TestSchedulerDeliversIdleScheduleWithCustomMetadata(t *testing.T) {
	// The normal test manager uses a temporary session base directory, so its
	// sibling schedules file is isolated too.
	mgr := newTestManager(t, context.Background(), newMockProvider())
	if mgr.scheduler == nil {
		t.Fatal("scheduler is unavailable")
	}
	sess, err := mgr.CreateSession(CreateOpts{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	record, err := mgr.scheduler.create(schedule.Schedule{
		SessionID: sess.ID,
		Text:      "scheduled check",
		DueAt:     time.Now().Add(-time.Second),
		TimeZone:  time.Local.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	mgr.scheduler.deliverDue(mgr, time.Now())
	pollUntil(t, time.Second, "scheduled run to finish", func() bool {
		return sess.runtime.State.Current() == "idle"
	})
	got, ok := mgr.scheduler.store.Get(record.ID)
	if !ok || got.Status != schedule.StatusDelivered || got.DeliveredAt.IsZero() {
		t.Fatalf("schedule after delivery = %#v, exists %v", got, ok)
	}
	messages := sess.History()
	if len(messages) == 0 || messages[0].Custom["source"] != "schedule" ||
		messages[0].Custom["schedule_id"] != record.ID || messages[0].Custom["occurrence_id"] != record.OccurrenceID {
		t.Fatalf("scheduled message custom = %#v", messages)
	}
}

func TestScheduleCommandCreateListAndCancel(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	created, err := mgr.ExecCommand(sess.ID, "/schedule in 1h -- review report", "")
	if err != nil || !created.OK {
		t.Fatalf("create = %#v, %v", created, err)
	}
	records := mgr.scheduler.list()
	if len(records) != 1 || records[0].SessionID != sess.ID || records[0].Text != "review report" {
		t.Fatalf("records = %#v", records)
	}
	listed, err := mgr.ExecCommand(sess.ID, "/schedule list", "")
	if err != nil || !listed.OK || !strings.Contains(listed.Message, records[0].ID) {
		t.Fatalf("list = %#v, %v", listed, err)
	}
	canceled, err := mgr.ExecCommand(sess.ID, "/schedule cancel "+records[0].ID, "")
	if err != nil || !canceled.OK {
		t.Fatalf("cancel = %#v, %v", canceled, err)
	}
	if got, _ := mgr.scheduler.store.Get(records[0].ID); got.Status != schedule.StatusCanceled {
		t.Fatalf("status = %q", got.Status)
	}
}

func TestSchedulerLeavesSchedulePendingForUnloadedSession(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	record, err := mgr.scheduler.create(schedule.Schedule{
		SessionID: "saved-session-not-loaded",
		Text:      "do not resume",
		DueAt:     time.Now().Add(-time.Second),
		TimeZone:  time.Local.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	mgr.scheduler.deliverDue(mgr, time.Now())
	got, _ := mgr.scheduler.store.Get(record.ID)
	if got.Status != schedule.StatusPending {
		t.Fatalf("unloaded session schedule status = %q, want pending", got.Status)
	}
}

func TestSchedulerAnnouncesDeliveredPromptLiveWithTimes(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	appended := make(chan bus.UserMessageAppended, 4)
	unsub := sess.runtime.Bus.Subscribe(func(e bus.UserMessageAppended) { appended <- e })
	defer unsub()
	due := time.Now().Add(-time.Minute)
	if _, err := mgr.scheduler.create(schedule.Schedule{SessionID: sess.ID, Text: "live check", DueAt: due, TimeZone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	mgr.scheduler.deliverDue(mgr, time.Now())
	select {
	case e := <-appended:
		if e.Custom["source"] != "schedule" || e.Custom["scheduled_for"] != due.UTC().Format(time.RFC3339) || e.Custom["delivered_at"] == "" {
			t.Fatalf("announced custom = %#v", e.Custom)
		}
		ev, _ := wsEventFromBus(e)
		projected := ev.Data.(UserMessageData).Custom
		if projected["scheduled_for"] == nil || projected["delivered_at"] == nil {
			t.Fatalf("projection dropped schedule times: %#v", projected)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("scheduled prompt was not announced live")
	}
}

func TestScheduleCommandRejectsCancelOfDeliveredAndPastAt(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	record, err := mgr.scheduler.create(schedule.Schedule{SessionID: sess.ID, Text: "done", DueAt: time.Now(), TimeZone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.scheduler.store.MarkDelivered(record.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	res, err := mgr.ExecCommand(sess.ID, "/schedule cancel "+record.ID, "")
	if err != nil || res.OK || !strings.Contains(res.Message, "already delivered") {
		t.Fatalf("cancel delivered = %#v, %v", res, err)
	}
	if got, _ := mgr.scheduler.store.Get(record.ID); got.Status != schedule.StatusDelivered {
		t.Fatalf("status = %q, want delivered", got.Status)
	}
	res, err = mgr.ExecCommand(sess.ID, "/schedule at 2020-01-01 03:00 Europe/Madrid -- old", "")
	if err != nil || res.OK || !strings.Contains(res.Message, "past") {
		t.Fatalf("past at = %#v, %v", res, err)
	}
}

func TestScheduleCommandConfirmationShowsIDTimeAndRemaining(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	res, err := mgr.ExecCommand(sess.ID, "/schedule in 2h -- later", "")
	if err != nil || !res.OK {
		t.Fatalf("create = %#v, %v", res, err)
	}
	id := mgr.scheduler.list()[0].ID
	if !strings.Contains(res.Message, id) || !strings.Contains(res.Message, "in 2h") || !strings.Contains(res.Message, "for ") {
		t.Fatalf("message = %q", res.Message)
	}
}

func TestDeletingSessionRemovesItsSchedules(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	gone, err := mgr.CreateSession(CreateOpts{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	kept, err := mgr.CreateSession(CreateOpts{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{gone.ID, kept.ID} {
		if _, err := mgr.scheduler.create(schedule.Schedule{SessionID: id, Text: "x", DueAt: time.Now().Add(time.Hour), TimeZone: "UTC"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := mgr.Delete(gone.ID); err != nil {
		t.Fatal(err)
	}
	reopened, err := schedule.Open(mgr.scheduler.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	records := reopened.List()
	if len(records) != 1 || records[0].SessionID != kept.ID {
		t.Fatalf("persisted records after delete = %#v", records)
	}
}

// A lifecycle writer (session delete) waiting behind a command that holds the
// read side must not wedge the scheduler: the command waits for the scheduler,
// and the delivery loop used to hold the scheduler while waiting for the read
// side, which the pending writer blocks.
func TestSchedulerDoesNotDeadlockWithPendingLifecycleWriter(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.scheduler.create(schedule.Schedule{SessionID: sess.ID, Text: "x", DueAt: time.Now().Add(-time.Second), TimeZone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	sess.lifecycle.RLock() // the /schedule command, inside its read section
	writer := make(chan struct{})
	go func() { sess.lifecycle.Lock(); sess.lifecycle.Unlock(); close(writer) }() //nolint:staticcheck // barrier
	time.Sleep(50 * time.Millisecond)
	delivery := make(chan struct{})
	go func() { mgr.scheduler.deliverDue(mgr, time.Now()); close(delivery) }()
	time.Sleep(100 * time.Millisecond)

	listed := make(chan struct{})
	go func() { mgr.scheduler.list(); close(listed) }()
	select {
	case <-listed:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler is wedged: list() waits behind a delivery blocked on the lifecycle lock")
	}
	sess.lifecycle.RUnlock()
	for name, ch := range map[string]chan struct{}{"writer": writer, "delivery": delivery} {
		select {
		case <-ch:
		case <-time.After(3 * time.Second):
			t.Fatalf("%s never finished", name)
		}
	}
}

func TestCancelRefusesOccurrenceAlreadyInHistoryEvenIfDeliveredWasNotPersisted(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("directory permissions do not bind root")
	}
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	record, err := mgr.scheduler.create(schedule.Schedule{SessionID: sess.ID, Text: "once", DueAt: time.Now().Add(-time.Second), TimeZone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(mgr.scheduler.store.Path())
	if err := os.Chmod(dir, 0o500); err != nil { // persisting `delivered` will fail
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()
	mgr.scheduler.deliverDue(mgr, time.Now())
	pollUntil(t, 2*time.Second, "prompt in history", func() bool {
		return scheduleOccurrenceExists(sess.History(), record.OccurrenceID)
	})
	pollUntil(t, 2*time.Second, "run to finish", func() bool { return sess.runtime.State.Current() == "idle" })
	if got, _ := mgr.scheduler.store.Get(record.ID); got.Status != schedule.StatusPending {
		t.Fatalf("precondition: status = %q, want pending (persist failed)", got.Status)
	}
	res, err := mgr.ExecCommand(sess.ID, "/schedule cancel "+record.ID, "")
	if err != nil || res.OK || !strings.Contains(res.Message, "already delivered") {
		t.Fatalf("cancel = %#v, %v", res, err)
	}
	if got, _ := mgr.scheduler.store.Get(record.ID); got.Status == schedule.StatusCanceled {
		t.Fatal("delivered occurrence was marked canceled")
	}
}

func TestScheduleListIsBoundedAndSaysHowManyAreHidden(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 22; i++ {
		status := schedule.StatusDelivered
		if i%11 == 0 {
			status = schedule.StatusPending
		}
		if _, err := mgr.scheduler.create(schedule.Schedule{SessionID: sess.ID, Text: "t", DueAt: time.Now().Add(time.Duration(i-30) * time.Hour), TimeZone: "UTC", Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	res, err := mgr.ExecCommand(sess.ID, "/schedule list", "")
	if err != nil || !res.OK {
		t.Fatalf("list = %#v, %v", res, err)
	}
	lines := strings.Split(res.Message, "\n")
	if len(lines) != scheduleListMax+1 || lines[len(lines)-1] != "+14 more" {
		t.Fatalf("list = %d lines, last %q", len(lines), lines[len(lines)-1])
	}
	if strings.Count(res.Message, " pending ") != 2 || !strings.Contains(lines[0], "pending") || !strings.Contains(lines[1], "pending") {
		t.Fatalf("pending records must come first:\n%s", res.Message)
	}
}
