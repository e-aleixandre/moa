package tasks

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// A backup that cannot be made leaves the legacy data readable and refuses
// writes with an explanation.
func TestCompatBackupFailureBlocksWriteNotRead(t *testing.T) {
	// The database name is valid, but its backup suffix exceeds NAME_MAX.
	p := v4DB(t, strings.Repeat("d", 247))
	r := New(p)
	defer func() { _ = r.Close() }()
	before := snapshot(t, p)
	_, err := r.Create(bg, CreateInput{Title: "must not write", Place: PlaceYou})
	if err == nil || !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "read-only") ||
		!strings.Contains(err.Error(), "backup before") {
		t.Fatalf("write error = %v", err)
	}
	if rec, err := r.Get(bg, 4); err != nil || rec.Title != "checklist" || len(rec.Subtasks) != 2 || rec.Handoff != nil {
		t.Fatalf("legacy read: %+v, %v", rec, err)
	}
	view, err := r.AgentList(bg, Actor{SessionID: "s1", ProjectKey: "p"})
	if err != nil || len(view.Checklist) == 0 {
		t.Fatalf("agent read: %+v, %v", view, err)
	}
	if snapshot(t, p) != before {
		t.Fatal("failed backup changed legacy rows")
	}
}

// The same degradation when the backup name is taken by something that is not
// a valid backup, on a regular tasks.sqlite: nothing is deleted or accepted.
func TestCompatBackupBlockedByDirectoryKeepsReads(t *testing.T) {
	p := v4DB(t, DatabaseName)
	if err := os.Mkdir(p+".pre-compat1.bak", 0o700); err != nil {
		t.Fatal(err)
	}
	r := New(p)
	defer func() { _ = r.Close() }()
	if _, err := r.Create(bg, CreateInput{Title: "x", Place: PlaceYou}); err == nil || !strings.Contains(err.Error(), "backup before") {
		t.Fatalf("write error = %v", err)
	}
	if rec, err := r.Get(bg, 4); err != nil || rec.Title != "checklist" || len(rec.Subtasks) != 2 || rec.Handoff != nil {
		t.Fatalf("legacy read: %+v, %v", rec, err)
	}
}
