package serve

import (
	"context"
	"fmt"
	"testing"

	"github.com/e-aleixandre/moa/pkg/tasks"
)

// An owner's Tasks list every template scheduled in its project, by any of
// its sessions; another session's list is unchanged. Keep is stored.
func TestOwnerTasksShowItsSessionsSchedules(t *testing.T) {
	f1Isolate(t)
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{})
	info, err := e.mgr.CreateOwner(CreateOwnerOpts{Root: e.root, Name: "Winerim"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := e.mgr.CreateSession(CreateOpts{CWD: e.root})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	rule := tasks.Rule{Freq: "daily", H: 9}
	tmpl, err := e.mgr.tasks.AgentSchedule(ctx, tasks.Actor{SessionID: child.ID, ProjectKey: e.mgr.projectKey(e.root), ProjectCWD: e.root},
		tasks.AgentInput{Title: "count delivery notes"}, tasks.When{Kind: "repeat", Rule: &rule}, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	type view struct {
		Scheduled []tasks.Record `json:"scheduled"`
	}
	ownerView := decode[view](t, mustAPI(t, e.srv, "GET", "/api/sessions/"+info.SessionID+"/tasks", "", 200))
	if len(ownerView.Scheduled) != 1 || ownerView.Scheduled[0].ID != tmpl.ID {
		t.Fatalf("owner's scheduled = %+v", ownerView.Scheduled)
	}
	other, err := e.mgr.CreateSession(CreateOpts{CWD: e.root})
	if err != nil {
		t.Fatal(err)
	}
	if v := decode[view](t, mustAPI(t, e.srv, "GET", "/api/sessions/"+other.ID+"/tasks", "", 200)); len(v.Scheduled) != 0 {
		t.Fatalf("a plain session lists its project's schedules: %+v", v.Scheduled)
	}
	kept := decode[tasks.Record](t, mustAPI(t, e.srv, "POST", fmt.Sprintf("/api/tasks/%d/keep", tmpl.ID), fmt.Sprintf(`{"revision":%d}`, tmpl.Revision), 200))
	if kept.KeptAt == 0 {
		t.Fatalf("keep = %+v", kept)
	}
	mustAPI(t, e.srv, "POST", fmt.Sprintf("/api/tasks/%d/keep", tmpl.ID), fmt.Sprintf(`{"revision":%d}`, tmpl.Revision), 409).Body.Close() //nolint:errcheck
}
