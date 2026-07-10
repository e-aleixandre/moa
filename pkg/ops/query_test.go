package ops

import (
	"reflect"
	"testing"
)

func TestSitrepOrdersStatesAndBlockersDeterministically(t *testing.T) {
	service := New(Config{})
	for _, id := range []string{"idle", "run", "verify", "permission", "error"} {
		addSession(t, service, id, "/work/a")
	}
	if err := service.UpdateLifecycle("run", LifecycleUpdate{State: LifecycleRunning, Activity: ActivityRunning, At: stamp(1)}); err != nil {
		t.Fatal(err)
	}
	if err := service.UpdateVerification("verify", Verification{State: VerificationFailed, At: stamp(1)}); err != nil {
		t.Fatal(err)
	}
	if err := service.UpdateLifecycle("permission", LifecycleUpdate{State: LifecycleRunning, Activity: ActivityPermission, At: stamp(1)}); err != nil {
		t.Fatal(err)
	}
	if err := service.UpdateLifecycle("error", LifecycleUpdate{State: LifecycleError, Activity: ActivityError, At: stamp(1)}); err != nil {
		t.Fatal(err)
	}

	briefing := service.Sitrep()
	var ids []string
	for _, status := range briefing.Sessions {
		ids = append(ids, status.ID)
	}
	if want := []string{"error", "permission", "verify", "run", "idle"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("status order = %v, want %v", ids, want)
	}
	var kinds []BlockerKind
	for _, blocker := range briefing.Blockers {
		kinds = append(kinds, blocker.Kind)
	}
	if want := []BlockerKind{BlockerError, BlockerPermission, BlockerVerificationFailed}; !reflect.DeepEqual(kinds, want) {
		t.Fatalf("blockers = %v, want %v", kinds, want)
	}
	if briefing.Spoken != "Ops: 5 sessions; 3 blockers." {
		t.Fatalf("spoken = %q", briefing.Spoken)
	}
}

func TestResolveUsesExactNormalizedExplicitNamesAndReportsAmbiguity(t *testing.T) {
	service := New(Config{})
	for _, input := range []SessionInput{
		{ID: "one", Title: "Deploy API", Aliases: []string{"release"}, ProjectAliases: []string{"backend"}, CanonicalCWD: "/work/api"},
		{ID: "two", Title: "Deploy API", Aliases: []string{"release"}, ProjectAliases: []string{"backend"}, CanonicalCWD: "/work/web"},
	} {
		if err := service.UpsertSession(input); err != nil {
			t.Fatal(err)
		}
	}

	resolution := service.Resolve("  DEPLOY   api ")
	if len(resolution.Candidates) != 2 || resolution.Candidates[0].ID != "one" || resolution.Candidates[1].ID != "two" {
		t.Fatalf("title resolution = %#v", resolution)
	}
	resolution = service.Resolve("backend")
	if len(resolution.Candidates) != 2 || resolution.Candidates[0].Kind != TargetProject || resolution.Candidates[1].Kind != TargetProject {
		t.Fatalf("project resolution = %#v", resolution)
	}
	if got := service.Resolve("depl"); len(got.Candidates) != 0 {
		t.Fatalf("fuzzy match selected candidates: %#v", got)
	}
	if got := service.Resolve("one"); len(got.Candidates) != 1 || got.Candidates[0].Kind != TargetSession {
		t.Fatalf("id resolution = %#v", got)
	}
}

func TestStatusRequiresDisambiguationAndHasStableSafeWording(t *testing.T) {
	service := New(Config{})
	for _, input := range []SessionInput{
		{ID: "a", Title: "same", CanonicalCWD: "/work/a"},
		{ID: "b", Title: "same", CanonicalCWD: "/work/b"},
	} {
		if err := service.UpsertSession(input); err != nil {
			t.Fatal(err)
		}
	}
	if got := service.Status("same"); got.Briefing != nil || len(got.Resolution.Candidates) != 2 {
		t.Fatalf("ambiguous status = %#v", got)
	}
	got := service.Status("a")
	if got.Briefing == nil || len(got.Briefing.Sessions) != 1 || got.Briefing.Sessions[0].ID != "a" || got.Briefing.Spoken != "Status: same is idle." {
		t.Fatalf("status = %#v", got)
	}
	blockers := service.Blockers()
	if blockers.Sessions != nil || blockers.Spoken != "Blockers: 0 blockers." {
		t.Fatalf("blocker briefing = %#v", blockers)
	}
}
