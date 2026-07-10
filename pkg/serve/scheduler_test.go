package serve

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/schedule"
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
	created, err := mgr.ExecCommand(sess.ID, "/schedule in 1h -- review report")
	if err != nil || !created.OK {
		t.Fatalf("create = %#v, %v", created, err)
	}
	records := mgr.scheduler.list()
	if len(records) != 1 || records[0].SessionID != sess.ID || records[0].Text != "review report" {
		t.Fatalf("records = %#v", records)
	}
	listed, err := mgr.ExecCommand(sess.ID, "/schedule list")
	if err != nil || !listed.OK || !strings.Contains(listed.Message, records[0].ID) {
		t.Fatalf("list = %#v, %v", listed, err)
	}
	canceled, err := mgr.ExecCommand(sess.ID, "/schedule cancel "+records[0].ID)
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
