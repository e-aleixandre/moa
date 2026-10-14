package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

type noModelProvider struct{ calls atomic.Int32 }

func (p *noModelProvider) Stream(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
	p.calls.Add(1)
	return nil, errors.New("delivery probe forbids model calls")
}

func TestSessionTasksProjectionCarriesPendingHandoff(t *testing.T) {
	prov := &noModelProvider{}
	srv, mgr := newNoticeTestServer(t, prov)
	t.Cleanup(func() {
		if n := prov.calls.Load(); n != 0 {
			t.Errorf("model calls = %d, expected zero", n)
		}
	})
	moa, err := mgr.CreateOwner(CreateOwnerOpts{Root: t.TempDir(), Name: "moa"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = mgr.CreateOwner(CreateOwnerOpts{Root: t.TempDir(), Name: "Infra"})
	if err != nil {
		t.Fatal(err)
	}
	sender, ok := mgr.Get(moa.SessionID)
	if !ok {
		t.Fatal("sender owner is not loaded")
	}
	tl, ok := sender.infra.toolReg.Get("tasks")
	if !ok {
		t.Fatal("registered tasks tool is not reachable")
	}
	res, err := tl.Execute(context.Background(), map[string]any{
		"action": "send", "to": "Infra", "title": "Delivery review: pending handoff",
	}, nil)
	if err != nil || res.IsError {
		t.Fatalf("registered tasks send: result=%+v error=%v", res, err)
	}
	list, err := mgr.tasks.List(context.Background(), tasks.Filter{IncludeAgents: true})
	if err != nil || len(list.Tasks) != 1 {
		t.Fatalf("created tasks = %+v, error=%v", list.Tasks, err)
	}
	rec := list.Tasks[0]
	if rec.Handoff == nil || rec.Handoff.State != tasks.HandoffAwaiting || rec.Place != tasks.PlaceYou {
		t.Fatalf("send without permission did not produce an awaiting handoff: %+v", rec)
	}

	fullPath := fmt.Sprintf("/api/tasks/%d", rec.ID)
	full := ownerAPI(t, srv.URL, "GET", fullPath, "", http.StatusOK)
	var fullRecord map[string]any
	if err := json.Unmarshal(full, &fullRecord); err != nil {
		t.Fatal(err)
	}
	h, ok := fullRecord["handoff"].(map[string]any)
	if !ok || h["state"] != tasks.HandoffAwaiting {
		t.Fatalf("full task endpoint lost handoff: %s", full)
	}

	sessionPath := "/api/sessions/" + moa.SessionID + "/tasks"
	flat := ownerAPI(t, srv.URL, "GET", sessionPath, "", http.StatusOK)
	var sessionTasks struct {
		Requests []map[string]any `json:"requests"`
	}
	if err := json.Unmarshal(flat, &sessionTasks); err != nil {
		t.Fatal(err)
	}
	if len(sessionTasks.Requests) != 1 || sessionTasks.Requests[0]["id"] != float64(rec.ID) {
		t.Fatalf("session endpoint does not expose the actual request: %s", flat)
	}

	patch := fmt.Sprintf(`{"revision":%d,"status":"done","completion_note":"probe"}`, rec.Revision)
	rejection := ownerAPI(t, srv.URL, "PATCH", fullPath, patch, http.StatusBadRequest)
	if !strings.Contains(string(rejection), "waits for your permission") {
		t.Fatalf("Done rejection has an unexpected reason: %s", rejection)
	}
	unchanged, err := mgr.tasks.Get(context.Background(), rec.ID)
	if err != nil || unchanged.Status != tasks.StatusPending || unchanged.Revision != rec.Revision {
		t.Fatalf("rejected Done changed the request: %+v, error=%v", unchanged, err)
	}
	t.Logf("real entry point: tasks send -> GET %s (handoff present) -> GET %s (flat requests); PATCH Done = 400; model calls = %d", fullPath, sessionPath, prov.calls.Load())

	got, ok := sessionTasks.Requests[0]["handoff"].(map[string]any)
	if !ok || got["state"] != tasks.HandoffAwaiting {
		t.Fatalf("GET %s dropped handoff.state=awaiting_permission; pinned Answer is unreachable from this payload: %s", sessionPath, flat)
	}
}
