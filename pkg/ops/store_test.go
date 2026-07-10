package ops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreRestoresAliasesAndBoundedJournal(t *testing.T) {
	store, state, err := OpenStore(filepath.Join(t.TempDir(), "ops.json"))
	if err != nil {
		t.Fatal(err)
	}
	service := New(Config{MaxMilestones: 2, Persist: store.Save})
	if err := service.Restore(state); err != nil {
		t.Fatal(err)
	}
	addSession(t, service, "session", "/work/project")
	if err := service.SetProjectAliases("/work/project", []string{"Project One"}); err != nil {
		t.Fatal(err)
	}
	if err := service.SetSessionAliases("session", []string{"Main Work"}); err != nil {
		t.Fatal(err)
	}
	// Runtime titles must never enter the durable safe journal.
	if err := service.UpsertSession(SessionInput{ID: "session", Title: "secret transcript-derived title", CanonicalCWD: "/work/project", Presence: PresenceActive}); err != nil {
		t.Fatal(err)
	}
	for i, ref := range []string{"one", "two", "three"} {
		if err := service.RecordMilestone("session", Milestone{Type: MilestoneRunStarted, At: stamp(i + 1), RefID: ref}); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(store.path)
	if err != nil || strings.Contains(string(raw), "secret transcript-derived title") {
		t.Fatalf("unsafe journal content: %q, %v", raw, err)
	}
	_, restored, err := OpenStore(filepath.Join(filepath.Dir(store.path), "ops.json"))
	if err != nil {
		t.Fatal(err)
	}
	restarted := New(Config{MaxMilestones: 2})
	if err := restarted.Restore(restored); err != nil {
		t.Fatal(err)
	}
	s := restarted.Snapshot().Projects[0].Sessions[0]
	if got, want := s.Aliases[0], "Main Work"; got != want {
		t.Fatalf("alias = %q, want %q", got, want)
	}
	if got := s.Milestones; len(got) != 2 || got[0].RefID != "two" || got[1].RefID != "three" {
		t.Fatalf("journal = %#v", got)
	}
	// A replayed bus event after restart must not duplicate its stable ref ID.
	if err := restarted.RecordMilestone("session", Milestone{Type: MilestoneRunStarted, At: stamp(4), RefID: "three"}); err != nil {
		t.Fatal(err)
	}
	if got := restarted.Snapshot().Projects[0].Sessions[0].Milestones; len(got) != 2 {
		t.Fatalf("duplicate journal = %#v", got)
	}
}

func TestAliasesAreNormalizedAndGloballyUnique(t *testing.T) {
	s := New(Config{})
	addSession(t, s, "one", "/one")
	addSession(t, s, "two", "/two")
	if err := s.SetSessionAliases("one", []string{"  Build   Team "}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetProjectAliases("/two", []string{"build team"}); err != ErrAliasCollision {
		t.Fatalf("collision = %v", err)
	}
	if err := s.SetSessionAliases("two", []string{"BUILD TEAM"}); err != ErrAliasCollision {
		t.Fatalf("collision = %v", err)
	}
}

func TestRestoreReconcilesLiveSessionWithoutLosingAliases(t *testing.T) {
	s := New(Config{})
	if err := s.Restore(DurableState{Sessions: []DurableSession{{
		ID: "same", CanonicalCWD: "/old", Aliases: []string{"assigned"},
		Milestones: []Milestone{{Type: MilestoneRunEnded, At: stamp(1), RefID: "run_1"}},
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSession(SessionInput{ID: "same", Title: "not persisted", CanonicalCWD: "/new", Presence: PresenceActive}); err != nil {
		t.Fatal(err)
	}
	project := s.Snapshot().Projects[0]
	if project.CanonicalCWD != "/new" || project.Sessions[0].Aliases[0] != "assigned" || project.Sessions[0].Presence != PresenceActive {
		t.Fatalf("reconciled = %#v", project)
	}
}
