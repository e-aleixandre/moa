package serve

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/tasks"
)

type legacyRecord struct {
	ID           string    `json:"id"`
	SessionID    string    `json:"session_id"`
	Text         string    `json:"text"`
	DueAt        time.Time `json:"due_at"`
	TimeZone     string    `json:"time_zone"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	DeliveredAt  time.Time `json:"delivered_at,omitempty"`
	OccurrenceID string    `json:"occurrence_id"`
}

// writeLegacy writes schedules.json as the retired /schedule store left it.
func writeLegacy(t *testing.T, path string, recs []legacyRecord) []byte {
	t.Helper()
	b, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return b
}

func legacyFixture(sid string, now time.Time) []legacyRecord {
	return []legacyRecord{
		{ID: "sch_future", SessionID: sid, Text: "Review the report\nand send notes", DueAt: now.Add(2 * time.Hour), TimeZone: "Europe/Madrid", Status: "pending", CreatedAt: now.Add(-time.Hour), OccurrenceID: "o1"},
		{ID: "sch_overdue", SessionID: sid, Text: "rotate keys", DueAt: now.Add(-time.Minute), TimeZone: "Local", Status: "pending", CreatedAt: now.Add(-2 * time.Hour), OccurrenceID: "o2"},
		{ID: "sch_canceled", SessionID: sid, Text: "never", DueAt: now.Add(time.Hour), TimeZone: "UTC", Status: "canceled", CreatedAt: now.Add(-time.Hour), OccurrenceID: "o3"},
		{ID: "sch_delivered", SessionID: sid, Text: "done before", DueAt: now.Add(-time.Hour), TimeZone: "UTC", Status: "delivered", CreatedAt: now.Add(-3 * time.Hour), OccurrenceID: "o4"},
	}
}

func templatesByTitle(t *testing.T, r *tasks.Repo) map[string]tasks.Record {
	t.Helper()
	l, err := r.List(bgc, tasks.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]tasks.Record{}
	for _, rec := range l.Tasks {
		if rec.When != nil {
			out[rec.Title] = rec
		}
	}
	return out
}

// The import is one transaction with its completion flag: a failure commits
// nothing and keeps schedules.json; a retry imports every pending record
// exactly once, with its text and instant, then retires the file.
func TestLegacyScheduleImportAtomic(t *testing.T) {
	h := newSchedHarness(t, newMockProvider(), "2026-09-30T08:00:00Z")
	h.start()
	sid := h.savedSession()
	h.stop()
	now := h.clock.Now()
	src := writeLegacy(t, h.schedulePath(), legacyFixture(sid, now))
	r := h.repo()
	ensureDB(t, r)
	drop := abortTrigger(t, h.dbPath(), "second_template", "BEFORE INSERT ON task_schedules WHEN (SELECT COUNT(*) FROM task_schedules) >= 1")

	h.start()
	if got := templatesByTitle(t, r); len(got) != 0 {
		t.Fatalf("failed import committed %d templates", len(got))
	}
	if done, _ := r.LegacySchedulesImported(bgc); done {
		t.Fatal("failed import set the flag")
	}
	if b, err := os.ReadFile(h.schedulePath()); err != nil || string(b) != string(src) {
		t.Fatalf("source changed after a failed import: %v", err)
	}
	h.stop()
	drop()

	h.start()
	got := templatesByTitle(t, r)
	if len(got) != 2 {
		t.Fatalf("imported %d templates, want the 2 pending: %v", len(got), got)
	}
	future := got["Review the report"]
	if future.Description != "Review the report\nand send notes" || future.TZ != "Europe/Madrid" ||
		future.When.At != now.Add(2*time.Hour).UnixMilli() || future.Target.ID != sid {
		t.Fatalf("future = %+v", future)
	}
	overdue := got["rotate keys"]
	if overdue.TZ != "UTC" {
		t.Fatalf("invalid legacy zone kept: %q", overdue.TZ)
	}
	if done, _ := r.LegacySchedulesImported(bgc); !done {
		t.Fatal("flag not set")
	}
	if _, err := os.Stat(h.schedulePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("schedules.json not retired")
	}
	if b, err := os.ReadFile(h.schedulePath() + ".migrated"); err != nil || string(b) != string(src) {
		t.Fatalf("backup = %v", err)
	}
	h.stop()
	h.start()
	if got := templatesByTitle(t, r); len(got) != 2 {
		t.Fatalf("restart re-imported: %d templates", len(got))
	}
}

// A crash (here: a failed rename) after the import committed but before
// schedules.json was retired never imports twice, and never brings back a
// template the owner deleted.
func TestLegacyScheduleImportCrashBeforeRename(t *testing.T) {
	h := newSchedHarness(t, newMockProvider(), "2026-09-30T08:00:00Z")
	h.start()
	sid := h.savedSession()
	h.stop()
	writeLegacy(t, h.schedulePath(), legacyFixture(sid, h.clock.Now()))
	h.hooks.legacyRename = func(string, string) error { return errors.New("disk full") }
	h.start()
	r := h.repo()
	got := templatesByTitle(t, r)
	if len(got) != 2 {
		t.Fatalf("imported %d", len(got))
	}
	if _, err := os.Stat(h.schedulePath()); err != nil {
		t.Fatal("source should still be there after the failed rename")
	}
	h.stop()
	if h.mgr != nil {
		t.Fatal("unreachable")
	}
	rec := got["Review the report"]
	if err := r.Delete(bgc, rec.ID, rec.Revision, ""); err != nil {
		t.Fatal(err)
	}
	h.hooks.legacyRename = nil
	h.start()
	got = templatesByTitle(t, r)
	if len(got) != 1 {
		t.Fatalf("after restart %d templates, want 1 (no duplicate, no resurrection)", len(got))
	}
	if _, err := os.Stat(h.schedulePath() + ".migrated"); err != nil {
		t.Fatal("source not retired on the next start")
	}
}

// A legacy schedule already due, by however little, is imported as a late
// run: the old scheduler may have been delivering it when it stopped, so it
// runs only after the owner's OK — then exactly once.
func TestLegacyScheduleImportOverdueIsLate(t *testing.T) {
	prov := newMockProvider(simpleResponseHandler("ok"))
	h := newSchedHarness(t, prov, "2026-09-30T08:00:00Z")
	h.start()
	sid := h.savedSession()
	h.stop()
	writeLegacy(t, h.schedulePath(), legacyFixture(sid, h.clock.Now()))
	h.start()
	h.pass()
	r := h.repo()
	overdue := templatesByTitle(t, r)["rotate keys"]
	o := oneRun(t, r, overdue.ID)
	if o.State != tasks.OccLate || o.Trigger != tasks.TriggerLegacy || o.ChildTaskID != 0 {
		t.Fatalf("overdue legacy run = %+v", o)
	}
	h.pass()
	if _, live := h.mgr.Get(sid); live || prov.calls.Load() != 0 {
		t.Fatal("overdue legacy schedule ran without the owner's OK")
	}
	if _, err := r.ConfirmOccurrence(bgc, o.ID, o.Revision, tasks.LateRun); err != nil {
		t.Fatal(err)
	}
	h.pass()
	assignedAndDelivered(t, h, r, o.ID)
}

// /schedule is retired: it points to Tasks and the composer clock and writes
// nothing; a canonical scheduled task is delivered exactly once, by the new
// scheduler only.
func TestScheduleRetiredCommandNoSecondDelivery(t *testing.T) {
	h := newSchedHarness(t, newMockProvider(simpleResponseHandler("ok")), "2026-09-30T08:00:00Z")
	h.start()
	sess := h.session()
	for _, cmd := range []string{"/schedule in 1h -- review report", "/schedule list", "/schedule cancel sch_x", "/schedule"} {
		res, err := h.mgr.ExecCommand(sess.ID, cmd, "")
		if err != nil {
			t.Fatal(err)
		}
		if res.OK || !strings.Contains(res.Message, "Tasks") || strings.Contains(res.Message, "\n") {
			t.Fatalf("%s = %+v", cmd, res)
		}
	}
	if _, err := os.Stat(h.schedulePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("/schedule wrote schedules.json")
	}
	r := h.repo()
	o := fireOnce(t, h, r, "canonical", toSession(sess.ID), tasks.Delivery{})
	assignedAndDelivered(t, h, r, o.ID)
	msgs := 0
	for _, m := range sess.History() {
		if m.Custom["source"] == "schedule" {
			t.Fatalf("legacy scheduler delivered: %+v", m.Custom)
		}
		if isTaskNotice(m.Custom) {
			msgs++
		}
	}
	if msgs != 1 {
		t.Fatalf("%d task notices in the transcript", msgs)
	}
}
