package ops

import (
	"reflect"
	"testing"
	"time"
)

func stamp(n int) time.Time { return time.Date(2026, 7, 10, 12, 0, n, 0, time.UTC) }

func addSession(t *testing.T, service *Service, id, cwd string) {
	t.Helper()
	if err := service.UpsertSession(SessionInput{ID: id, CanonicalCWD: cwd, Presence: PresenceActive}); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotGroupsCanonicalCWDAndSortsDeterministically(t *testing.T) {
	service := New(Config{})
	if err := service.UpsertSession(SessionInput{ID: "z", CanonicalCWD: "/work/b", ProjectAliases: []string{"beta", "b"}, Aliases: []string{"zed"}, Presence: PresenceSaved}); err != nil {
		t.Fatal(err)
	}
	if err := service.UpsertSession(SessionInput{ID: "b", CanonicalCWD: "/work/a", ProjectAliases: []string{"alpha"}, Presence: PresenceActive}); err != nil {
		t.Fatal(err)
	}
	if err := service.UpsertSession(SessionInput{ID: "a", CanonicalCWD: "/work/a", ProjectAliases: []string{"alpha", "a"}, Presence: PresenceActive}); err != nil {
		t.Fatal(err)
	}

	snapshot := service.Snapshot()
	if len(snapshot.Projects) != 2 || snapshot.Projects[0].CanonicalCWD != "/work/a" || snapshot.Projects[1].CanonicalCWD != "/work/b" {
		t.Fatalf("unexpected projects: %#v", snapshot.Projects)
	}
	project := snapshot.Projects[0]
	if got, want := []string{project.Sessions[0].ID, project.Sessions[1].ID}, []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("session order = %v, want %v", got, want)
	}
	if got, want := project.Aliases, []string{"a", "alpha"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("project aliases = %v, want %v", got, want)
	}
}

func TestStateUpdatesAreProjected(t *testing.T) {
	service := New(Config{})
	addSession(t, service, "s", "/work/a")
	if err := service.UpdateLifecycle("s", LifecycleUpdate{State: LifecycleRunning, Activity: ActivityRunning, At: stamp(1)}); err != nil {
		t.Fatal(err)
	}
	if err := service.UpdateJobs("s", JobCounts{Subagents: 2, Bash: 3}); err != nil {
		t.Fatal(err)
	}
	if err := service.UpdateVerification("s", Verification{State: VerificationPassed, At: stamp(2)}); err != nil {
		t.Fatal(err)
	}
	session := service.Snapshot().Projects[0].Sessions[0]
	if session.Lifecycle != LifecycleRunning || session.Activity != ActivityRunning || !session.LastTransitionAt.Equal(stamp(1)) || session.Jobs != (JobCounts{Subagents: 2, Bash: 3}) || session.Verification.State != VerificationPassed {
		t.Fatalf("unexpected state: %#v", session)
	}
}

func TestMilestonesAreChronologicalAndBounded(t *testing.T) {
	service := New(Config{MaxMilestones: 2})
	addSession(t, service, "s", "/work/a")
	for _, milestone := range []Milestone{{Type: MilestoneRunEnded, At: stamp(3), RefID: "end"}, {Type: MilestoneRunStarted, At: stamp(1), RefID: "start"}, {Type: MilestoneVerification, At: stamp(2), RefID: "verify"}} {
		if err := service.RecordMilestone("s", milestone); err != nil {
			t.Fatal(err)
		}
	}
	got := service.Snapshot().Projects[0].Sessions[0].Milestones
	if len(got) != 2 || got[0].RefID != "verify" || got[1].RefID != "end" {
		t.Fatalf("bounded journal = %#v", got)
	}
}

func TestRemoveSessionRemovesItsProject(t *testing.T) {
	service := New(Config{})
	addSession(t, service, "s", "/work/a")
	if !service.RemoveSession("s") || service.RemoveSession("s") || len(service.Snapshot().Projects) != 0 {
		t.Fatal("session was not removed")
	}
}

func TestSnapshotIsIsolated(t *testing.T) {
	service := New(Config{})
	if err := service.UpsertSession(SessionInput{ID: "s", CanonicalCWD: "/work/a", ProjectAliases: []string{"a"}, Aliases: []string{"one"}}); err != nil {
		t.Fatal(err)
	}
	if err := service.RecordMilestone("s", Milestone{Type: MilestoneRunStarted, At: stamp(1), RefID: "run"}); err != nil {
		t.Fatal(err)
	}
	snapshot := service.Snapshot()
	snapshot.Projects[0].Aliases[0] = "changed"
	snapshot.Projects[0].Sessions[0].Aliases[0] = "changed"
	snapshot.Projects[0].Sessions[0].Milestones[0].RefID = "changed"
	fresh := service.Snapshot()
	if fresh.Projects[0].Aliases[0] != "a" || fresh.Projects[0].Sessions[0].Aliases[0] != "one" || fresh.Projects[0].Sessions[0].Milestones[0].RefID != "run" {
		t.Fatalf("snapshot mutation leaked: %#v", fresh)
	}
}
