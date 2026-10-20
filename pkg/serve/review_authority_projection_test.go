package serve

import (
	"context"
	"fmt"
	"testing"

	"github.com/e-aleixandre/moa/pkg/tasks"
)

func TestReviewAuthorityExtendedOwnerScheduledForCrossProject(t *testing.T) {
	f1Isolate(t)
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{})
	ctx := context.Background()
	own, err := e.mgr.CreateOwner(CreateOwnerOpts{Root: e.root, Name: "Project P"})
	if err != nil {
		t.Fatal(err)
	}
	foreignRoot := t.TempDir()
	foreignKey := e.mgr.projectKey(foreignRoot)
	if own.CodebaseKey == "" || foreignKey == "" || own.CodebaseKey == foreignKey {
		t.Fatal("fixture needs two different nonempty project keys")
	}
	creator, err := e.mgr.CreateSession(CreateOpts{CWD: e.root})
	if err != nil {
		t.Fatal(err)
	}
	rule := tasks.Rule{Freq: "daily", H: 9}
	projectSchedule, err := e.mgr.tasks.AgentSchedule(ctx, tasks.Actor{SessionID: creator.ID, ProjectKey: own.CodebaseKey, ProjectCWD: e.root}, tasks.AgentInput{Title: "P session schedule"}, tasks.When{Kind: tasks.WhenRepeat, Rule: &rule}, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	makeForeign := func(title string, target tasks.Target) tasks.Record {
		t.Helper()
		rec, err := e.mgr.tasks.Create(ctx, tasks.CreateInput{Title: title, ProjectKey: foreignKey, ProjectCWD: foreignRoot, Schedule: &tasks.ScheduleDef{When: tasks.When{Kind: tasks.WhenRepeat, Rule: &rule}, TZ: "UTC", Target: target}})
		if err != nil {
			t.Fatal(err)
		}
		if rec.ProjectKey != foreignKey {
			t.Fatalf("foreign schedule was rebound: %+v", rec)
		}
		return rec
	}
	ownerTarget := makeForeign("Q targeting P owner", tasks.Target{Kind: tasks.TargetOwner, ID: own.ID})
	sessionTarget := makeForeign("Q targeting P owner session", tasks.Target{Kind: tasks.TargetSession, ID: own.SessionID})
	unrelated := makeForeign("Q unrelated schedule", tasks.Target{Kind: tasks.TargetSession, ID: "unrelated-q-session"})
	type projection struct {
		Scheduled []tasks.Record `json:"scheduled"`
	}
	got := decode[projection](t, mustAPI(t, e.srv, "GET", "/api/sessions/"+own.SessionID+"/tasks", "", 200))
	ids := map[int64]bool{}
	for _, rec := range got.Scheduled {
		if ids[rec.ID] {
			t.Fatalf("duplicate merged schedule #%d", rec.ID)
		}
		ids[rec.ID] = true
		if (rec.ID == ownerTarget.ID || rec.ID == sessionTarget.ID) && rec.ProjectKey != foreignKey {
			t.Fatalf("project aggregation rebound targeted schedule: %+v", rec)
		}
	}
	if len(ids) != 3 || !ids[projectSchedule.ID] || !ids[ownerTarget.ID] || !ids[sessionTarget.ID] || ids[unrelated.ID] {
		t.Fatalf("extended owner ScheduledFor=%v", ids)
	}
	// Human projections must not grant the owner's agent creator authority.
	tool := reviewAuthorityTool(t, e, own.SessionID)
	for _, rec := range []tasks.Record{projectSchedule, ownerTarget, sessionTarget} {
		for _, action := range []string{"pause", "resume", "delete"} {
			res := reviewAuthorityExecute(t, ctx, tool, map[string]any{"action": action, "id": float64(rec.ID)})
			if !res.IsError {
				t.Fatalf("human projection granted root-owner %s over schedule #%d: %+v", action, rec.ID, res)
			}
		}
	}
	other, err := e.mgr.CreateSession(CreateOpts{CWD: e.root})
	if err != nil {
		t.Fatal(err)
	}
	plain := decode[projection](t, mustAPI(t, e.srv, "GET", fmt.Sprintf("/api/sessions/%s/tasks", other.ID), "", 200))
	if len(plain.Scheduled) != 0 {
		t.Fatalf("project aggregation leaked to ordinary session: %+v", plain.Scheduled)
	}
}
