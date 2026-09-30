package schedule

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStorePersistsRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedules.json")
	store := NewStore(path)
	due := time.Date(2026, 7, 11, 8, 30, 0, 0, time.FixedZone("CEST", 2*60*60))
	created, err := store.Create(Schedule{
		SessionID: "session-1",
		Text:      "check deployment",
		DueAt:     due,
		TimeZone:  "Europe/Madrid",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.OccurrenceID == "" || created.CreatedAt.IsZero() {
		t.Fatalf("Create did not populate durable fields: %#v", created)
	}
	if !created.DueAt.Equal(due.UTC()) {
		t.Fatalf("DueAt = %s, want %s", created.DueAt, due.UTC())
	}

	loaded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := loaded.Get(created.ID)
	if !ok {
		t.Fatal("persisted schedule was not loaded")
	}
	if got != created {
		t.Fatalf("loaded record = %#v, want %#v", got, created)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatal("schedule file was not written as JSON")
	}
}

func TestCancelIsIdempotentAndPersistent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedules.json")
	store := NewStore(path)
	created, err := store.Create(Schedule{
		SessionID: "session-1",
		Text:      "cancel me",
		DueAt:     time.Now().Add(time.Hour),
		TimeZone:  "Europe/Madrid",
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Cancel(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Cancel(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || second.Status != StatusCanceled {
		t.Fatalf("idempotent cancellation = %#v then %#v", first, second)
	}
	if _, err := store.Cancel("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Cancel missing error = %v, want ErrNotFound", err)
	}
	loaded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := loaded.Get(created.ID)
	if !ok || got.Status != StatusCanceled {
		t.Fatalf("persisted cancellation = %#v, exists %v", got, ok)
	}
}

func TestCancelRejectsDeliveredRecord(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "s.json"))
	rec, _ := store.Create(Schedule{SessionID: "a", Text: "x", DueAt: time.Now(), TimeZone: "UTC"})
	if _, err := store.MarkDelivered(rec.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Cancel(rec.ID); !errors.Is(err, ErrAlreadyDelivered) {
		t.Fatalf("Cancel delivered err = %v", err)
	}
	if got, _ := store.Get(rec.ID); got.Status != StatusDelivered {
		t.Fatalf("status = %q", got.Status)
	}
}

func TestPurgeSettledKeepsPendingAndRecent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	store := NewStore(path)
	now := time.Now()
	mk := func(session string, due time.Time, status string) Schedule {
		r, err := store.Create(Schedule{SessionID: session, Text: "x", DueAt: due, TimeZone: "UTC", Status: status})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	oldDone := mk("a", now.Add(-40*24*time.Hour), StatusDelivered)
	oldCanceled := mk("a", now.Add(-40*24*time.Hour), StatusCanceled)
	oldPending := mk("a", now.Add(-40*24*time.Hour), StatusPending)
	recent := mk("a", now.Add(-time.Hour), StatusDelivered)
	n, err := store.PurgeSettled(now.Add(-RetainSettled))
	if err != nil || n != 2 {
		t.Fatalf("purged %d, %v", n, err)
	}
	reopened, _ := Open(path)
	for id, want := range map[string]bool{oldDone.ID: false, oldCanceled.ID: false, oldPending.ID: true, recent.ID: true} {
		if _, ok := reopened.Get(id); ok != want {
			t.Fatalf("record %s present=%v want %v", id, ok, want)
		}
	}
}
