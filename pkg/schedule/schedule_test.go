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

func TestParseCreateArgs(t *testing.T) {
	madrid, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	at, err := ParseCreateArgsAt("at 2026-12-01 09:15 America/New_York -- call Alex", now, madrid)
	if err != nil {
		t.Fatal(err)
	}
	wantAt := time.Date(2026, 12, 1, 14, 15, 0, 0, time.UTC)
	if !at.DueAt.Equal(wantAt) || at.TimeZone != "America/New_York" || at.Text != "call Alex" {
		t.Fatalf("at parse = %#v, want due %s", at, wantAt)
	}

	in, err := ParseCreateArgsAt("in 90m -- stretch", now, madrid)
	if err != nil {
		t.Fatal(err)
	}
	if !in.DueAt.Equal(now.Add(90*time.Minute)) || in.TimeZone != "Europe/Madrid" || in.Text != "stretch" {
		t.Fatalf("in parse = %#v", in)
	}

	defaultZone, err := ParseCreateArgsAt("at 2026-07-10 15:30 -- local reminder", now, madrid)
	if err != nil {
		t.Fatal(err)
	}
	if defaultZone.TimeZone != "Europe/Madrid" || !defaultZone.DueAt.Equal(time.Date(2026, 7, 10, 13, 30, 0, 0, time.UTC)) {
		t.Fatalf("default-zone parse = %#v", defaultZone)
	}

	for _, input := range []string{
		"at 2026-12-01 09:15 UTC -- no",
		"at 2026-12-01 09:15 America/New_York -- one -- two",
		"in 1h America/New_York -- no",
		"in -1h -- no",
		"at 2026-12-01 09:15 --",
	} {
		if _, err := ParseCreateArgsAt(input, now, madrid); err == nil {
			t.Errorf("ParseCreateArgsAt(%q) succeeded, want error", input)
		}
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
