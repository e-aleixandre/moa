package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

func reviewAuthorityTool(t *testing.T, e *f1Env, sessionID string) core.Tool {
	t.Helper()
	sess, ok := e.mgr.Get(sessionID)
	if !ok {
		t.Fatal("session not loaded")
	}
	tool, ok := sess.infra.toolReg.Get("tasks")
	if !ok {
		t.Fatal("bootstrapped tasks tool not registered")
	}
	return tool
}

func reviewAuthorityExecute(t *testing.T, ctx context.Context, tool core.Tool, params map[string]any) core.Result {
	t.Helper()
	res, err := tool.Execute(ctx, params, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestReviewAuthorityRuntimeOwnerAndChildWithdrawal(t *testing.T) {
	f1Isolate(t)
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{})
	ctx := context.Background()
	root, err := e.mgr.CreateOwner(CreateOwnerOpts{Root: e.root, Name: "Project Owner"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := e.mgr.CreateSession(CreateOpts{CWD: e.root, Origin: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := e.mgr.CreateOwner(CreateOwnerOpts{Root: t.TempDir(), Name: "Foreign Owner"})
	if err != nil {
		t.Fatal(err)
	}
	actor := tasks.Actor{SessionID: child.ID, ProjectKey: e.mgr.projectKey(e.root), ProjectCWD: e.root}
	req, err := e.mgr.tasks.AgentAsk(ctx, actor, tasks.AgentInput{Title: "PRIVATE child request"})
	if err != nil {
		t.Fatal(err)
	}
	params := func() map[string]any {
		return map[string]any{"action": "withdraw", "id": float64(req.ID), "note": "already resolved", "owner": true, "session_id": root.SessionID, "project_key": actor.ProjectKey}
	}
	rootTool := reviewAuthorityTool(t, e, root.SessionID)
	childVariant, ok := tasks.ChildVariant(rootTool)
	if !ok {
		t.Fatal("owner tool has no child variant")
	}
	for _, tc := range []struct {
		name string
		ctx  context.Context
		tool core.Tool
	}{
		{"root tool executed by a subagent", core.WithAgentID(ctx, "review-job"), rootTool},
		{"child variant executed by a subagent", core.WithAgentID(ctx, "review-job"), childVariant},
		{"foreign project root owner", ctx, reviewAuthorityTool(t, e, foreign.SessionID)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := e.mgr.tasks.Get(ctx, req.ID)
			if err != nil {
				t.Fatal(err)
			}
			res := reviewAuthorityExecute(t, tc.ctx, tc.tool, params())
			if !res.IsError {
				t.Fatalf("unauthorized withdrawal accepted: %+v", res)
			}
			if strings.Contains(fmt.Sprint(res.Content), "PRIVATE") {
				t.Fatalf("refusal leaked request: %+v", res)
			}
			after, err := e.mgr.tasks.Get(ctx, req.ID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("refused withdrawal changed request: %+v %v", after, err)
			}
			list := reviewAuthorityExecute(t, tc.ctx, tc.tool, map[string]any{"action": "list"})
			if strings.Contains(fmt.Sprint(list.Content), "PRIVATE child request") {
				t.Fatalf("owner authority leaked into list: %+v", list)
			}
		})
	}
	// Rename is cosmetic. The existing runtime must keep its identity and use
	// the current owner name in the durable closing note.
	name := "Renamed Owner"
	if _, err := e.mgr.UpdateOwner(root.ID, UpdateOwnerOpts{Name: &name}); err != nil {
		t.Fatal(err)
	}
	list := reviewAuthorityExecute(t, ctx, rootTool, map[string]any{"action": "list"})
	if list.IsError || !strings.Contains(fmt.Sprint(list.Content), "PRIVATE child request") {
		t.Fatalf("root cannot find project request: %+v", list)
	}
	if res := reviewAuthorityExecute(t, ctx, rootTool, params()); res.IsError {
		t.Fatalf("root owner rejected: %+v", res)
	}
	got, err := e.mgr.tasks.Get(ctx, req.ID)
	if err != nil || got.Status != tasks.StatusDone || got.CompletionNote != "Withdrawn by Renamed Owner: already resolved" {
		t.Fatalf("root withdrawal=%+v %v", got, err)
	}
	// Ordinary sessions retain requester authority, but do not gain project-
	// owner authority over siblings' requests.
	sibling, err := e.mgr.CreateSession(CreateOpts{CWD: e.root, Origin: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := e.mgr.tasks.AgentAsk(ctx, tasks.Actor{SessionID: sibling.ID, ProjectKey: actor.ProjectKey, ProjectCWD: e.root}, tasks.AgentInput{Title: "sibling request"})
	if err != nil {
		t.Fatal(err)
	}
	childTool := reviewAuthorityTool(t, e, child.ID)
	if res := reviewAuthorityExecute(t, ctx, childTool, map[string]any{"action": "withdraw", "id": float64(other.ID), "note": "not needed", "owner": true}); !res.IsError {
		t.Fatalf("ordinary owner-created session inherited authority: %+v", res)
	}
	if res := reviewAuthorityExecute(t, ctx, reviewAuthorityTool(t, e, sibling.ID), map[string]any{"action": "withdraw", "id": float64(other.ID), "note": "requester solved it"}); res.IsError {
		t.Fatalf("requester authority rejected for its own session: %+v", res)
	}
}

func TestReviewAuthorityRuntimeScheduleIdentitySurvivesRenameNotRecreation(t *testing.T) {
	f1Isolate(t)
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{})
	ctx := context.Background()
	sess, err := e.mgr.CreateSession(CreateOpts{CWD: e.root})
	if err != nil {
		t.Fatal(err)
	}
	tool := reviewAuthorityTool(t, e, sess.ID)
	if res := reviewAuthorityExecute(t, ctx, tool, map[string]any{"action": "create", "title": "daily creator job", "when": "every day at 09:00"}); res.IsError {
		t.Fatalf("create=%+v", res)
	}
	list, err := e.mgr.tasks.ScheduledFor(ctx, sess.ID, nil)
	if err != nil || len(list) != 1 {
		t.Fatalf("created schedules=%+v %v", list, err)
	}
	id := list[0].ID
	if _, err := e.mgr.SetTitle(sess.ID, "renamed session"); err != nil {
		t.Fatal(err)
	}
	if res := reviewAuthorityExecute(t, ctx, tool, map[string]any{"action": "pause", "id": float64(id)}); res.IsError {
		t.Fatalf("renamed creator rejected=%+v", res)
	}
	if res := reviewAuthorityExecute(t, ctx, tool, map[string]any{"action": "resume", "id": float64(id)}); res.IsError {
		t.Fatalf("renamed creator resume=%+v", res)
	}
	if err := e.mgr.Delete(sess.ID); err != nil {
		t.Fatal(err)
	}
	replacement, err := e.mgr.CreateSession(CreateOpts{CWD: e.root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.mgr.SetTitle(replacement.ID, "renamed session"); err != nil {
		t.Fatal(err)
	}
	if replacement.ID == sess.ID {
		t.Fatal("session identity reused")
	}
	for _, action := range []string{"pause", "resume", "delete"} {
		res := reviewAuthorityExecute(t, ctx, reviewAuthorityTool(t, e, replacement.ID), map[string]any{"action": action, "id": float64(id), "session_id": sess.ID})
		if !res.IsError {
			t.Fatalf("recreated session inherited %s: %+v", action, res)
		}
	}
	if got, err := e.mgr.tasks.Get(ctx, id); err != nil || got.CreatedBySessionID != sess.ID {
		t.Fatalf("recreation rebound creator: %+v %v", got, err)
	}
}

func TestReviewAuthorityKeepAuthAndCSRF(t *testing.T) {
	a := newSchedAPI(t, "2026-10-20T08:00:00Z")
	tmpl := expect[tasks.Record](a, 201, "POST", "/api/tasks", dailyBody("guarded keep", sessionTarget(a.h.session().ID)))
	handler := NewServer(a.h.mgr, WithAuthToken("review-owner", false), WithAutomationToken("review-automation"))
	path := rev(tmpl.ID) + "/keep"
	body := fmt.Sprintf(`{"revision":%d}`, tmpl.Revision)
	send := func(cookie, authorization, csrf string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Host = "localhost"
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: authCookieName, Value: cookie})
		}
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		if csrf != "" {
			req.Header.Set("X-Moa-Request", csrf)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	for _, tc := range []struct {
		name, cookie, authorization, csrf string
		want                              int
	}{
		{"anonymous", "", "", "", 401},
		{"anonymous with csrf", "", "", "1", 401},
		{"wrong cookie", "wrong", "", "1", 401},
		{"automation bearer", "", "Bearer review-automation", "1", 401},
		{"owner cookie missing csrf", "review-owner", "", "", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := a.rec(tmpl.ID)
			res := send(tc.cookie, tc.authorization, tc.csrf)
			if res.Code != tc.want {
				t.Fatalf("POST /keep = %d %s, want %d", res.Code, res.Body, tc.want)
			}
			if after := a.rec(tmpl.ID); !reflect.DeepEqual(before, after) {
				t.Fatalf("refused request changed task: %+v", after)
			}
			if n := sqlCount(t, a.h.dbPath(), "SELECT COUNT(*) FROM task_schedule_reviews"); n != 0 {
				t.Fatalf("refused request wrote %d reviews", n)
			}
		})
	}
	res := send("review-owner", "", "1")
	if res.Code != 200 {
		t.Fatalf("authenticated + csrf keep=%d %s", res.Code, res.Body)
	}
	var kept tasks.Record
	if err := json.Unmarshal(res.Body.Bytes(), &kept); err != nil {
		t.Fatal(err)
	}
	if kept.KeptAt == 0 || kept.Revision <= tmpl.Revision || sqlCount(t, a.h.dbPath(), "SELECT COUNT(*) FROM task_schedule_reviews WHERE task_id = ?", tmpl.ID) != 1 {
		t.Fatalf("keep did not persist: %+v", kept)
	}
	if res := send("review-owner", "", "1"); res.Code != 409 {
		t.Fatalf("stale keep=%d %s", res.Code, res.Body)
	}
}

func TestReviewAuthorityKeepRejectsNonschedulesAndInvalidBodies(t *testing.T) {
	a := newSchedAPI(t, "2026-10-20T08:00:00Z")
	sid := a.h.session().ID
	repeat := expect[tasks.Record](a, 201, "POST", "/api/tasks", dailyBody("repeat", sessionTarget(sid)))
	once := expect[tasks.Record](a, 201, "POST", "/api/tasks", a.onceBody("once", a.inMs(time.Hour), sessionTarget(sid)))
	plain := expect[tasks.Record](a, 201, "POST", "/api/tasks", map[string]any{"title": "plain", "place": "you"})
	for _, rec := range []tasks.Record{once, plain} {
		if code, raw := a.do("POST", rev(rec.ID)+"/keep", map[string]any{"revision": rec.Revision}); code != 400 {
			t.Fatalf("keep nonschedule=%d %s", code, raw)
		}
	}
	for _, body := range []string{`{}`, `{"revision":0}`, `{"revision":-1}`, `{"revision":1,"owner":true}`, `not-json`} {
		if code, raw := a.do("POST", rev(repeat.ID)+"/keep", body); code != 400 {
			t.Fatalf("invalid body %q=%d %s", body, code, raw)
		}
	}
	if code, raw := a.do("POST", rev(999999)+"/keep", map[string]any{"revision": 1}); code != 404 {
		t.Fatalf("foreign id=%d %s", code, raw)
	}
	if n := sqlCount(t, a.h.dbPath(), "SELECT COUNT(*) FROM task_schedule_reviews"); n != 0 {
		t.Fatalf("invalid keeps wrote %d reviews", n)
	}
}
