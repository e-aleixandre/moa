package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// cliRepo is the CLI's connection: another repository on serve's file.
func cliRepo(t *testing.T, mgr *Manager) *tasks.Repo {
	t.Helper()
	r := tasks.New(mgr.tasks.Path())
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func decode[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	defer resp.Body.Close() //nolint:errcheck
	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestTasksAPIRequiresOwnerAuthAndCSRF(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	handler := NewServer(mgr, WithAuthToken("owner", false))
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/tasks", ""},
		{"GET", "/api/tasks/1", ""},
		{"POST", "/api/tasks", `{"title":"x","place":"you"}`},
		{"PATCH", "/api/tasks/1", `{"revision":1}`},
		{"DELETE", "/api/tasks/1?revision=1", ""},
		{"GET", "/api/tasks/projects", ""},
		{"GET", "/api/sessions/abc/tasks", ""},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Host = "localhost"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without credentials = %d, want 401", tc.method, tc.path, rec.Code)
		}
	}
	// Authenticated but without the CSRF header, a write is refused and stores nothing.
	req := httptest.NewRequest("POST", "/api/tasks", strings.NewReader(`{"title":"x","place":"you"}`))
	req.Host = "localhost"
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: "owner"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST without X-Moa-Request = %d, want 403", rec.Code)
	}
	if res, _ := mgr.tasks.List(context.Background(), tasks.Filter{IncludeAgents: true}); len(res.Tasks) != 0 {
		t.Fatalf("refused write stored %+v", res.Tasks)
	}
}

func TestTasksAPIOwnerCRUDWithRevisionConflicts(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()

	resp := apiReq(t, srv, "POST", "/api/tasks", `{"title":"write docs","place":"backlog","project_key":"p1","subtasks":[{"title":"outline"}]}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d", resp.StatusCode)
	}
	created := decode[tasks.Record](t, resp)
	if created.ID != 1 || created.Revision != 1 || len(created.Subtasks) != 1 {
		t.Fatalf("created: %+v", created)
	}

	// The CLI edits it; a form loaded before that gets 409 with the current task.
	if _, err := cliRepo(t, mgr).Update(context.Background(), created.ID, created.Revision, tasks.Patch{Title: ptrS("cli edit")}); err != nil {
		t.Fatal(err)
	}
	resp = apiReq(t, srv, "PATCH", "/api/tasks/1", `{"revision":1,"title":"stale form"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale patch = %d, want 409", resp.StatusCode)
	}
	conflict := decode[struct {
		Current tasks.Record `json:"current"`
	}](t, resp)
	if conflict.Current.Title != "cli edit" || conflict.Current.Revision != 2 {
		t.Fatalf("409 body: %+v", conflict.Current)
	}

	resp = apiReq(t, srv, "PATCH", "/api/tasks/1", `{"revision":2,"status":"done","completion_note":"shipped","notify":false}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch = %d", resp.StatusCode)
	}
	if rec := decode[tasks.Record](t, resp); rec.Status != "done" || rec.CompletedAt == 0 || rec.Revision != 3 {
		t.Fatalf("patched: %+v", rec)
	}

	resp = apiReq(t, srv, "GET", "/api/tasks?project=p1", "")
	list := decode[tasks.ListResult](t, resp)
	if len(list.Tasks) != 1 || list.Revision == 0 || list.Counts.Backlog != 0 {
		t.Fatalf("list: %+v", list)
	}
	if resp = apiReq(t, srv, "DELETE", "/api/tasks/1?revision=2", ""); resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale delete = %d", resp.StatusCode)
	}
	if resp = apiReq(t, srv, "DELETE", "/api/tasks/1?revision=3", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
	if resp = apiReq(t, srv, "GET", "/api/tasks/1", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get after delete = %d", resp.StatusCode)
	}
}

func TestTasksAPIValidatesSubtasksCyclesAndPlaces(t *testing.T) {
	srv, _, cancel := newTestServer(t)
	defer cancel()
	post := func(body string) *http.Response { return apiReq(t, srv, "POST", "/api/tasks", body) }

	// A subtask is a title and a done flag: a second level cannot be expressed.
	if r := post(`{"title":"x","place":"you","subtasks":[{"title":"a","subtasks":[{"title":"b"}]}]}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("nested subtask = %d, want 400", r.StatusCode)
	}
	if r := post(`{"title":"x","place":"backlog"}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("backlog without project = %d, want 400", r.StatusCode)
	}
	if r := post(`{"title":"x","place":"agent","assignee_session_id":"nope"}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown assignee = %d, want 400", r.StatusCode)
	}
	a := decode[tasks.Record](t, post(`{"title":"a","place":"you"}`))
	b := decode[tasks.Record](t, post(`{"title":"b","place":"you","waits_for":[1]}`))
	if len(b.WaitsFor) != 1 {
		t.Fatalf("waits: %+v", b)
	}
	r := apiReq(t, srv, "PATCH", "/api/tasks/1", `{"revision":`+itoa(a.Revision)+`,"waits_for":[2]}`)
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("cycle = %d, want 400", r.StatusCode)
	}
	if r := apiReq(t, srv, "GET", "/api/tasks/notanumber", ""); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad id = %d", r.StatusCode)
	}
}

func TestTasksAPIAssignsToASessionAndDerivesTheProjectFromIt(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	resp := apiReq(t, srv, "POST", "/api/tasks", `{"title":"review the diff","place":"agent","assignee_session_id":"`+sess.ID+`","project_key":"ignored"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d", resp.StatusCode)
	}
	rec := decode[tasks.Record](t, resp)
	if rec.ProjectKey != core.CodebaseKey(sess.CWD) || rec.ProjectCWD != sess.CWD || rec.AssigneeSessionID != sess.ID {
		t.Fatalf("project not derived from the session's directory: %+v", rec)
	}

	// The same task through the session route, live or saved.
	resp = apiReq(t, srv, "GET", "/api/sessions/"+sess.ID+"/tasks", "")
	got := decode[struct {
		Checklist []tasks.Task `json:"checklist"`
		Requests  []tasks.Task `json:"requests"`
	}](t, resp)
	if len(got.Checklist) != 1 || got.Checklist[0].Title != "review the diff" || len(got.Requests) != 0 {
		t.Fatalf("session tasks: %+v", got)
	}
	if r := apiReq(t, srv, "GET", "/api/sessions/unknown/tasks", ""); r.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown session = %d", r.StatusCode)
	}
}

func TestTasksWebSocketInvalidatesWhenTheCLIWrites(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	ctx, wsCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer wsCancel()
	// The database must exist for a CLI write to be a *change* to notice.
	if _, err := mgr.tasks.Create(ctx, tasks.CreateInput{Title: "seed", Place: tasks.PlaceYou}); err != nil {
		t.Fatal(err)
	}
	conn, _, err := websocket.Dial(ctx, srv.URL+"/api/tasks/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck

	readChange := func() int64 {
		t.Helper()
		var msg struct {
			Type     string `json:"type"`
			Revision int64  `json:"revision"`
		}
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Type != "tasks_changed" {
			t.Fatalf("message type %q", msg.Type)
		}
		return msg.Revision
	}
	first := readChange() // the hello carries the current revision
	if _, err := cliRepo(t, mgr).AgentAsk(ctx, tasks.Actor{SessionID: "cli"}, tasks.AgentInput{Title: "from the CLI"}); err != nil {
		t.Fatal(err)
	}
	// Coalescing may deliver one message for several commits, but never zero.
	for got := first; got <= first; got = readChange() {
	}
}

// Pulse and the web client read tasks_update and InitData.tasks with the
// fields they always had. Changes made by the CLI reach a connected session,
// and the payload gains no field.
func TestSessionWebSocketKeepsTheTasksContractAdditive(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	cli := cliRepo(t, mgr)
	actor := tasks.Actor{SessionID: sess.ID, ProjectKey: core.CodebaseKey(sess.CWD)}
	ctx, wsCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer wsCancel()
	first, err := cli.AgentCreate(ctx, actor, tasks.AgentInput{Title: "before connect", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	// Owner-only material next to it must not appear in the session's payloads.
	if _, err := cli.Create(ctx, tasks.CreateInput{Title: "PRIVATE-NOTE", Place: tasks.PlaceYou}); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.AgentAsk(ctx, tasks.Actor{SessionID: "other"}, tasks.AgentInput{Title: "OTHER-REQUEST"}); err != nil {
		t.Fatal(err)
	}

	conn, _, err := websocket.Dial(ctx, srv.URL+"/api/sessions/"+sess.ID+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck

	allowed := map[string]bool{"id": true, "title": true, "description": true, "status": true, "depends_on": true, "created_at": true, "completed_at": true}
	check := func(what string, raw json.RawMessage, wantTitles ...string) {
		t.Helper()
		if strings.Contains(string(raw), "PRIVATE-NOTE") || strings.Contains(string(raw), "OTHER-REQUEST") {
			t.Fatalf("%s leaks owner-only tasks: %s", what, raw)
		}
		var items []map[string]any
		if err := json.Unmarshal(raw, &items); err != nil {
			t.Fatalf("%s: %v (%s)", what, err, raw)
		}
		if len(items) != len(wantTitles) {
			t.Fatalf("%s: %d tasks, want %d: %s", what, len(items), len(wantTitles), raw)
		}
		for i, item := range items {
			for k := range item {
				if !allowed[k] {
					t.Fatalf("%s: new field %q would reach Pulse: %s", what, k, raw)
				}
			}
			if item["title"] != wantTitles[i] {
				t.Fatalf("%s: task %d is %v, want %q", what, i, item["title"], wantTitles[i])
			}
		}
	}

	var init struct {
		Type string `json:"type"`
		Data struct {
			Tasks json.RawMessage `json:"tasks"`
		} `json:"data"`
	}
	if err := wsjson.Read(ctx, conn, &init); err != nil || init.Type != "init" {
		t.Fatalf("init: %v %q", err, init.Type)
	}
	check("init.tasks", init.Data.Tasks, "before connect")

	// A change by the CLI reaches the open connection as tasks_update.
	if _, err := cli.AgentDone(ctx, actor, first.ID); err != nil {
		t.Fatal(err)
	}
	for {
		var evt struct {
			Type string `json:"type"`
			Data struct {
				Tasks json.RawMessage `json:"tasks"`
			} `json:"data"`
		}
		if err := wsjson.Read(ctx, conn, &evt); err != nil {
			t.Fatalf("waiting for tasks_update: %v", err)
		}
		if evt.Type != "tasks_update" {
			continue
		}
		check("tasks_update", evt.Data.Tasks, "before connect")
		if !strings.Contains(string(evt.Data.Tasks), `"done"`) {
			t.Fatalf("tasks_update is stale: %s", evt.Data.Tasks)
		}
		break
	}
}

// The checklist an older version left in a session JSON is inert: resuming
// does not restore it, and saving does not delete it.
func TestOldChecklistInSessionMetadataStaysInertButIsKept(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root, sessionBase := t.TempDir(), t.TempDir()
	store, err := session.NewFileStore(sessionBase, root)
	if err != nil {
		t.Fatal(err)
	}
	saved := store.Create()
	legacy := map[string]any{"tasks": []any{map[string]any{"id": float64(1), "title": "old checklist item", "status": "pending"}}, "next_task_id": float64(1)}
	saved.Metadata = map[string]any{"model": "gpt-5.3-codex", "cwd": root, "tasks": legacy}
	saved.Messages = []core.AgentMessage{core.WrapMessage(core.NewUserMessage("hello"))}
	if err := store.Save(saved); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return newMockProvider(), nil },
		DefaultModel:    core.Model{ID: "test-model", Provider: "mock"},
		WorkspaceRoot:   root,
		MoaCfg:          core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"},
		ConfigLoader:    isolatedTestConfigLoader(t, core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"}),
		SessionBaseDir:  sessionBase,
	})
	t.Cleanup(mgr.Shutdown)

	sess, err := mgr.ResumeSession(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := bus.QueryTyped[bus.GetTasks, []tasks.Task](sess.runtime.Bus, bus.GetTasks{})
	if len(got) != 0 {
		t.Fatalf("an old checklist was restored: %+v", got)
	}
	if err := sess.runtime.Flush(); err != nil {
		t.Fatal(err)
	}
	after, err := store.Load(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	blob, _ := json.Marshal(after.Metadata["tasks"])
	want, _ := json.Marshal(legacy)
	if string(blob) != string(want) {
		t.Fatalf("saving changed the inert checklist: %s", blob)
	}
}

func ptrS(s string) *string { return &s }

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestTasksAPIAssignmentProjectAlwaysComesFromTheTargetSession(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	dirA, dirB := t.TempDir(), t.TempDir()
	a, err := mgr.CreateSession(CreateOpts{CWD: dirA})
	if err != nil {
		t.Fatal(err)
	}
	b, err := mgr.CreateSession(CreateOpts{CWD: dirB})
	if err != nil {
		t.Fatal(err)
	}
	if core.CodebaseKey(a.CWD) == core.CodebaseKey(b.CWD) {
		t.Fatal("sessions must have distinct projects")
	}
	wantB := func(rec tasks.Record, what string) {
		t.Helper()
		if rec.ProjectKey != core.CodebaseKey(b.CWD) || rec.ProjectCWD != b.CWD || rec.AssigneeSessionID != b.ID {
			t.Fatalf("%s: project not from target session: %+v", what, rec)
		}
	}

	// POST with a conflicting key and cwd.
	resp := apiReq(t, srv, "POST", "/api/tasks", `{"title":"t","place":"agent","assignee_session_id":"`+b.ID+`","project_key":"evil","project_cwd":"/evil"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d", resp.StatusCode)
	}
	wantB(decode[tasks.Record](t, resp), "post")

	// Reassignment without place.
	rec := decode[tasks.Record](t, apiReq(t, srv, "POST", "/api/tasks", `{"title":"t2","place":"agent","assignee_session_id":"`+a.ID+`"}`))
	resp = apiReq(t, srv, "PATCH", "/api/tasks/"+itoa(rec.ID), `{"revision":`+itoa(rec.Revision)+`,"assignee_session_id":"`+b.ID+`"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reassign = %d", resp.StatusCode)
	}
	rec = decode[tasks.Record](t, resp)
	wantB(rec, "reassign without place")

	// Reassignment back and forth with conflicting project fields.
	resp = apiReq(t, srv, "PATCH", "/api/tasks/"+itoa(rec.ID), `{"revision":`+itoa(rec.Revision)+`,"assignee_session_id":"`+a.ID+`"}`)
	rec = decode[tasks.Record](t, resp)
	resp = apiReq(t, srv, "PATCH", "/api/tasks/"+itoa(rec.ID), `{"revision":`+itoa(rec.Revision)+`,"assignee_session_id":"`+b.ID+`","project_key":"evil","project_cwd":"/evil"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reassign with project = %d", resp.StatusCode)
	}
	rec = decode[tasks.Record](t, resp)
	wantB(rec, "reassign with conflicting project")

	// Move from You with a place and conflicting fields.
	you := decode[tasks.Record](t, apiReq(t, srv, "POST", "/api/tasks", `{"title":"t3","place":"you"}`))
	resp = apiReq(t, srv, "PATCH", "/api/tasks/"+itoa(you.ID), `{"revision":`+itoa(you.Revision)+`,"place":"agent","assignee_session_id":"`+b.ID+`","project_key":"evil"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("move = %d", resp.StatusCode)
	}
	wantB(decode[tasks.Record](t, resp), "move with conflicting key")

	// An owner cannot silently misfile an assigned task by editing only its project.
	resp = apiReq(t, srv, "PATCH", "/api/tasks/"+itoa(rec.ID), `{"revision":`+itoa(rec.Revision)+`,"project_key":"evil","project_cwd":"/evil"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("project-only patch = %d", resp.StatusCode)
	}
	wantB(decode[tasks.Record](t, resp), "project-only patch")
}
