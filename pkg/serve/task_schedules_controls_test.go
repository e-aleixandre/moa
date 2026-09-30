package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

func (a *schedAPI) runs(parent int64) []tasks.Occurrence { return runsOfT(a.t, a.h.repo(), parent) }

// lateRun makes a template's slot late: it fell due, and the planner first
// saw it ten minutes later.
func (a *schedAPI) lateRun(tmpl tasks.Record) tasks.Occurrence {
	a.t.Helper()
	a.h.clock.Set(time.UnixMilli(tmpl.Next).Add(tasks.LateAfter))
	a.h.pass()
	for _, o := range a.runs(tmpl.ID) {
		if o.State == tasks.OccLate && o.DueAt == tmpl.Next {
			return o
		}
	}
	a.t.Fatalf("template #%d has no late run: %+v", tmpl.ID, a.runs(tmpl.ID))
	return tasks.Occurrence{}
}

func rev(id int64) string { return fmt.Sprintf("/api/tasks/%d", id) }

// Every control is one documented transition guarded by the revision and
// the slot the owner saw: a stale gesture is a 409 with the current state and
// never consumes the following slot.
func TestScheduleAPIControlCAS(t *testing.T) {
	a := newSchedAPI(t, "2026-09-30T14:40:00Z")
	live := a.h.session().ID
	other := a.h.session().ID
	conflict := func(method, path string, body any) json.RawMessage {
		t.Helper()
		c := expect[conflictBody](a, http.StatusConflict, method, path, body)
		if c.Error == "" || len(c.Current) == 0 {
			t.Fatalf("409 without the current representation: %+v", c)
		}
		return c.Current
	}

	t.Run("run now and skip name the slot they saw", func(t *testing.T) {
		tmpl := expect[tasks.Record](a, 201, "POST", "/api/tasks", dailyBody("daily", sessionTarget(live)))
		first := tmpl.Next
		cur := conflict("POST", rev(tmpl.ID)+"/run-now", map[string]any{"revision": tmpl.Revision + 5, "due_at": first})
		var got tasks.Record
		_ = json.Unmarshal(cur, &got)
		if got.ID != tmpl.ID || got.Revision != tmpl.Revision {
			t.Fatalf("409 current = %+v", got)
		}
		conflict("POST", rev(tmpl.ID)+"/run-now", map[string]any{"revision": tmpl.Revision, "due_at": first + 1})
		if n := len(a.runs(tmpl.ID)); n != 0 {
			t.Fatalf("stale gestures consumed %d slots", n)
		}
		for name, body := range map[string]string{"no due": `{"revision":1}`, "unknown field": `{"revision":1,"due_at":1,"now":5}`, "not json": `x`} {
			if code, raw := a.do("POST", rev(tmpl.ID)+"/run-now", body); code != 400 {
				t.Errorf("%s = %d %s, want 400", name, code, raw)
			}
		}
		occ := expect[tasks.Occurrence](a, 200, "POST", rev(tmpl.ID)+"/run-now", map[string]any{"revision": tmpl.Revision, "due_at": first})
		if occ.ScheduleTaskID != tmpl.ID || occ.DueAt != first || occ.Trigger != tasks.TriggerRunNow || occ.Revision == 0 {
			t.Fatalf("run now = %+v", occ)
		}
		if s := occ.State; s != tasks.OccReady && s != tasks.OccAssigned {
			t.Fatalf("run now state %q", s)
		}
		// The planner assigns and delivers the run; wait for it to settle so the
		// revision read next is the final one.
		waitOcc(t, a.h.repo(), occ.ID, "run delivered", func(o tasks.Occurrence) bool { return o.AdmittedAt != 0 })
		after := a.rec(tmpl.ID)
		if after.Next != ms("2026-10-02T07:00:00Z") {
			t.Fatalf("run now did not consume the next slot early: next = %d", after.Next)
		}
		// A retry of the same gesture, and a retry with the fresh revision but
		// the old slot, both conflict: the following slot is never taken.
		conflict("POST", rev(tmpl.ID)+"/run-now", map[string]any{"revision": tmpl.Revision, "due_at": first})
		conflict("POST", rev(tmpl.ID)+"/run-now", map[string]any{"revision": after.Revision, "due_at": first})
		if n := len(a.runs(tmpl.ID)); n != 1 {
			t.Fatalf("%d runs after retries, want 1", n)
		}
		skipped := expect[tasks.Occurrence](a, 200, "POST", rev(tmpl.ID)+"/skip", map[string]any{"revision": after.Revision, "due_at": after.Next})
		if skipped.State != tasks.OccSkipped || skipped.Reason != tasks.ReasonOwnerSkip || skipped.Trigger != tasks.TriggerSkipNext {
			t.Fatalf("skip = %+v", skipped)
		}
		conflict("POST", rev(tmpl.ID)+"/skip", map[string]any{"revision": after.Revision, "due_at": after.Next})
	})

	t.Run("skip is for recurring tasks only", func(t *testing.T) {
		once := expect[tasks.Record](a, 201, "POST", "/api/tasks", a.onceBody("once", a.inMs(time.Hour), sessionTarget(live)))
		if code, raw := a.do("POST", rev(once.ID)+"/skip", map[string]any{"revision": once.Revision, "due_at": once.Next}); code != 400 {
			t.Fatalf("skip a once task = %d %s", code, raw)
		}
		if code, raw := a.do("POST", rev(once.ID)+"/pause", map[string]any{"revision": once.Revision}); code != 400 {
			t.Fatalf("pause a once task = %d %s", code, raw)
		}
		note := expect[tasks.Record](a, 201, "POST", "/api/tasks", map[string]any{"title": "plain", "place": "you"})
		for _, route := range []string{"run-now", "skip", "pause", "resume"} {
			if code, _ := a.do("POST", rev(note.ID)+"/"+route, map[string]any{"revision": note.Revision, "due_at": 1}); code != 400 {
				t.Errorf("%s on an ordinary task = %d, want 400", route, code)
			}
		}
		if code, _ := a.do("POST", rev(999)+"/pause", map[string]any{"revision": 1}); code != 404 {
			t.Errorf("pause of a missing task = %d, want 404", code)
		}
		if code, _ := a.do("POST", "/api/tasks/abc/pause", map[string]any{"revision": 1}); code != 400 {
			t.Errorf("pause of a bad id = %d, want 400", code)
		}
	})

	t.Run("pause and resume", func(t *testing.T) {
		tmpl := expect[tasks.Record](a, 201, "POST", "/api/tasks", dailyBody("pausable", sessionTarget(live)))
		conflict("POST", rev(tmpl.ID)+"/pause", map[string]any{"revision": tmpl.Revision + 1})
		paused := expect[tasks.Record](a, 200, "POST", rev(tmpl.ID)+"/pause", map[string]any{"revision": tmpl.Revision})
		if paused.ScheduleState != "paused" || paused.Revision <= tmpl.Revision {
			t.Fatalf("paused = %+v", paused)
		}
		if code, raw := a.do("POST", rev(tmpl.ID)+"/run-now", map[string]any{"revision": paused.Revision, "due_at": paused.Next}); code != 400 {
			t.Fatalf("run now while paused = %d %s", code, raw)
		}
		conflict("POST", rev(tmpl.ID)+"/resume", map[string]any{"revision": tmpl.Revision})
		a.h.clock.Advance(3 * 24 * time.Hour)
		resumed := expect[tasks.Record](a, 200, "POST", rev(tmpl.ID)+"/resume", map[string]any{"revision": paused.Revision})
		if resumed.ScheduleState != "scheduled" || resumed.Next <= a.h.clock.Now().UnixMilli() {
			t.Fatalf("resumed = %+v", resumed)
		}
		a.h.pass()
		if n := len(a.runs(tmpl.ID)); n != 0 {
			t.Fatalf("resume caught up %d paused dates", n)
		}
	})

	t.Run("confirm answers a late run once", func(t *testing.T) {
		tmpl := expect[tasks.Record](a, 201, "POST", "/api/tasks", a.onceBody("late job", a.inMs(time.Minute), sessionTarget(live)))
		o := a.lateRun(tmpl)
		path := fmt.Sprintf("/api/tasks/occurrences/%d/confirm", o.ID)
		conflict("POST", path, map[string]any{"revision": o.Revision + 1, "action": "run"})
		for name, body := range map[string]string{
			"bad action": fmt.Sprintf(`{"revision":%d,"action":"maybe"}`, o.Revision),
			"no action":  fmt.Sprintf(`{"revision":%d}`, o.Revision),
			"extra":      fmt.Sprintf(`{"revision":%d,"action":"run","due_at":1}`, o.Revision),
		} {
			if code, raw := a.do("POST", path, body); code != 400 {
				t.Errorf("%s = %d %s, want 400", name, code, raw)
			}
		}
		if got := a.runs(tmpl.ID)[0]; got.State != tasks.OccLate {
			t.Fatalf("refused answers changed the run: %+v", got)
		}
		done := expect[tasks.Occurrence](a, 200, "POST", path, map[string]any{"revision": o.Revision, "action": "run"})
		if done.State != tasks.OccReady || done.ConfirmedAt == 0 {
			t.Fatalf("confirm run = %+v", done)
		}
		cur := conflict("POST", path, map[string]any{"revision": o.Revision, "action": "skip"})
		var got tasks.Occurrence
		_ = json.Unmarshal(cur, &got)
		if got.ID != o.ID || got.State == tasks.OccLate {
			t.Fatalf("409 current = %+v", got)
		}
		if code, _ := a.do("POST", "/api/tasks/occurrences/99999/confirm", map[string]any{"revision": 1, "action": "run"}); code != 404 {
			t.Errorf("confirm of a missing run = %d, want 404", code)
		}
		a.h.pass()

		// Skip: a once task ends in Done with the note "Skipped".
		tmpl2 := expect[tasks.Record](a, 201, "POST", "/api/tasks", a.onceBody("skippable", a.inMs(time.Minute), sessionTarget(live)))
		o2 := a.lateRun(tmpl2)
		skipped := expect[tasks.Occurrence](a, 200, "POST", fmt.Sprintf("/api/tasks/occurrences/%d/confirm", o2.ID), map[string]any{"revision": o2.Revision, "action": "skip"})
		if skipped.State != tasks.OccSkipped || skipped.Reason != tasks.ReasonOwnerSkip {
			t.Fatalf("confirm skip = %+v", skipped)
		}
		if rec := a.rec(tmpl2.ID); rec.Status != tasks.StatusDone || rec.CompletionNote != "Skipped" {
			t.Fatalf("skipped once task = %+v", rec)
		}
	})

	t.Run("reroute sends a failed run to an existing session", func(t *testing.T) {
		own := a.makeOwner("Ghost", "")
		tmpl := expect[tasks.Record](a, 201, "POST", "/api/tasks", a.onceBody("needs a home", a.inMs(time.Minute), map[string]any{"kind": "owner", "id": own.ID}))
		a.h.clock.Set(time.UnixMilli(tmpl.Next))
		a.h.pass()
		o := a.runs(tmpl.ID)[0]
		if o.State != tasks.OccFailed || o.Reason == "" {
			t.Fatalf("run = %+v, want failed", o)
		}
		path := fmt.Sprintf("/api/tasks/occurrences/%d/reroute", o.ID)
		conflict("POST", path, map[string]any{"revision": o.Revision + 1, "session_id": other})
		for name, body := range map[string]string{
			"no session":      fmt.Sprintf(`{"revision":%d}`, o.Revision),
			"unknown session": fmt.Sprintf(`{"revision":%d,"session_id":"nope"}`, o.Revision),
			"extra":           fmt.Sprintf(`{"revision":%d,"session_id":%q,"target":"x"}`, o.Revision, other),
		} {
			if code, raw := a.do("POST", path, body); code != 400 {
				t.Errorf("%s = %d %s, want 400", name, code, raw)
			}
		}
		done := expect[tasks.Occurrence](a, 200, "POST", path, map[string]any{"revision": o.Revision, "session_id": other})
		if done.State != tasks.OccAssigned && done.State != tasks.OccReady || done.SessionID != other || done.ChildTaskID == 0 {
			t.Fatalf("reroute = %+v", done)
		}
		conflict("POST", path, map[string]any{"revision": o.Revision, "session_id": other})
		if n := sqlCount(t, a.h.dbPath(), "SELECT COUNT(*) FROM task_occurrences WHERE schedule_task_id = ? AND child_task_id IS NOT NULL", tmpl.ID); n != 1 {
			t.Fatalf("%d children", n)
		}
	})
}

// The new routes sit on the ordinary owner surface: no credentials is 401, a
// missing CSRF header is 403, and neither the Automation token nor a hook
// secret controls a schedule.
func TestScheduleAPIRoutesAuthorization(t *testing.T) {
	a := newSchedAPI(t, "2026-09-30T14:40:00Z")
	live := a.h.session().ID
	tmpl := expect[tasks.Record](a, 201, "POST", "/api/tasks", dailyBody("guarded", sessionTarget(live)))
	late := expect[tasks.Record](a, 201, "POST", "/api/tasks", a.onceBody("late", a.inMs(time.Minute), sessionTarget(live)))
	o := a.lateRun(late)

	handler := NewServer(a.h.mgr, WithAuthToken("owner", false), WithAutomationToken("auto-secret"))
	routes := []struct{ method, path, body string }{
		{"POST", rev(tmpl.ID) + "/run-now", fmt.Sprintf(`{"revision":%d,"due_at":%d}`, tmpl.Revision, tmpl.Next)},
		{"POST", rev(tmpl.ID) + "/skip", fmt.Sprintf(`{"revision":%d,"due_at":%d}`, tmpl.Revision, tmpl.Next)},
		{"POST", rev(tmpl.ID) + "/pause", fmt.Sprintf(`{"revision":%d}`, tmpl.Revision)},
		{"POST", rev(tmpl.ID) + "/resume", fmt.Sprintf(`{"revision":%d}`, tmpl.Revision)},
		{"POST", fmt.Sprintf("/api/tasks/occurrences/%d/confirm", o.ID), fmt.Sprintf(`{"revision":%d,"action":"run"}`, o.Revision)},
		{"POST", fmt.Sprintf("/api/tasks/occurrences/%d/reroute", o.ID), fmt.Sprintf(`{"revision":%d,"session_id":%q}`, o.Revision, live)},
		{"POST", "/api/tasks/when/parse", `{"text":"tomorrow at 9:00","tz":"UTC"}`},
		{"POST", "/api/tasks", fmt.Sprintf(`{"title":"x","when":{"kind":"once","at":%d},"tz":"UTC","target":{"kind":"session","id":%q}}`, a.inMs(time.Hour), live)},
	}
	send := func(r struct{ method, path, body string }, mutate func(*http.Request)) int {
		req := httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
		req.Host = "localhost"
		mutate(req)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	snapshot := func() string {
		return fmt.Sprintf("%d/%d/%d/%d", a.rec(tmpl.ID).Revision, len(a.runs(tmpl.ID)), len(a.runs(late.ID)), sqlCount(t, a.h.dbPath(), "SELECT COUNT(*) FROM tasks"))
	}
	before := snapshot()
	for _, r := range routes {
		if code := send(r, func(*http.Request) {}); code != http.StatusUnauthorized {
			t.Errorf("%s %s without credentials = %d, want 401", r.method, r.path, code)
		}
		if code := send(r, func(req *http.Request) { req.AddCookie(&http.Cookie{Name: authCookieName, Value: "owner"}) }); code != http.StatusForbidden {
			t.Errorf("%s %s without X-Moa-Request = %d, want 403", r.method, r.path, code)
		}
		if code := send(r, func(req *http.Request) {
			req.Header.Set("Authorization", "Bearer auto-secret")
			req.Header.Set("X-Moa-Request", "1")
		}); code != http.StatusUnauthorized {
			t.Errorf("%s %s with the Automation token = %d, want 401", r.method, r.path, code)
		}
		if code := send(r, func(req *http.Request) {
			req.AddCookie(&http.Cookie{Name: authCookieName, Value: "wrong"})
			req.Header.Set("X-Moa-Request", "1")
		}); code != http.StatusUnauthorized {
			t.Errorf("%s %s with a wrong cookie = %d, want 401", r.method, r.path, code)
		}
	}
	// The hook ingress is a different surface: a source secret in the path
	// reaches the inbox, never a task route.
	req := httptest.NewRequest("POST", "/hooks/tasks/secret/api/tasks/"+fmt.Sprint(tmpl.ID)+"/pause", strings.NewReader(`{}`))
	req.Host = "localhost"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code < 400 {
		t.Errorf("hook path reached a task route: %d", rec.Code)
	}
	if after := snapshot(); after != before {
		t.Fatalf("refused requests changed the database: %s -> %s", before, after)
	}
	// With the owner credentials the same routes work.
	ok := httptest.NewRequest("POST", routes[2].path, strings.NewReader(routes[2].body))
	ok.Host = "localhost"
	ok.AddCookie(&http.Cookie{Name: authCookieName, Value: "owner"})
	ok.Header.Set("X-Moa-Request", "1")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, ok)
	if res.Code != http.StatusOK {
		t.Fatalf("owner pause = %d %s", res.Code, res.Body)
	}
}

// A new-session target names a project by the key /api/tasks/projects lists
// and a directory that belongs to it: the chosen worktree is what is stored.
func TestScheduleAPIProjectDirectorySelection(t *testing.T) {
	a := newSchedAPI(t, "2026-09-30T14:40:00Z")
	repo, tree := gitProject(t)
	for _, dir := range []string{repo, tree} {
		if _, err := a.h.mgr.CreateSession(CreateOpts{CWD: dir}); err != nil {
			t.Fatal(err)
		}
	}
	elsewhere := t.TempDir()
	if _, err := a.h.mgr.CreateSession(CreateOpts{CWD: elsewhere}); err != nil {
		t.Fatal(err)
	}
	key := core.CodebaseKey(repo)
	projects := expect[struct {
		Projects []taskProject `json:"projects"`
	}](a, 200, "GET", "/api/tasks/projects", nil)
	var listed *taskProject
	for i, p := range projects.Projects {
		if p.Key == key {
			listed = &projects.Projects[i]
		}
	}
	if listed == nil || len(listed.CWDs) != 2 {
		t.Fatalf("projects = %+v", projects.Projects)
	}
	newTarget := func(extra map[string]any) map[string]any {
		target := map[string]any{"kind": "new", "project": key, "model": "opus", "thinking": "high"}
		for k, v := range extra {
			target[k] = v
		}
		return dailyBody("worktree job", target)
	}
	rec := expect[tasks.Record](a, 201, "POST", "/api/tasks", newTarget(map[string]any{"project_cwd": tree}))
	if rec.Target.CWD != tree || rec.Target.Project != key || rec.Target.Model != "anthropic/claude-opus-5-5" || rec.Target.Thinking != "high" {
		t.Fatalf("target = %+v", rec.Target)
	}
	if got := a.rec(rec.ID).Target; got == nil || got.CWD != tree {
		t.Fatalf("stored target = %+v", got)
	}
	// The stored name of the directory is accepted too: a client can send back
	// what it read.
	if r := expect[tasks.Record](a, 201, "POST", "/api/tasks", newTarget(map[string]any{"cwd": repo})); r.Target.CWD != repo {
		t.Fatalf("cwd alias = %+v", r.Target)
	}
	if code, raw := a.do("POST", "/api/tasks", newTarget(nil)); code != 400 {
		t.Errorf("two directories and no choice = %d %s, want 400", code, raw)
	}
	if code, raw := a.do("POST", "/api/tasks", newTarget(map[string]any{"project_cwd": elsewhere})); code != 400 {
		t.Errorf("directory of another project = %d %s, want 400", code, raw)
	}
	if code, raw := a.do("POST", "/api/tasks", newTarget(map[string]any{"project_cwd": tree + "/../repo"})); code != 400 {
		t.Errorf("non-canonical directory = %d %s, want 400", code, raw)
	}
	if code, raw := a.do("POST", "/api/tasks", newTarget(map[string]any{"project_cwd": tree, "cwd": repo})); code != 400 {
		t.Errorf("two different directories = %d %s, want 400", code, raw)
	}
	// A project with a single directory needs no choice.
	one := expect[tasks.Record](a, 201, "POST", "/api/tasks", dailyBody("solo", map[string]any{"kind": "new", "project": core.CodebaseKey(elsewhere), "model": "anthropic/claude-haiku-4-5-20251001"}))
	if one.Target.CWD != elsewhere {
		t.Fatalf("solo target = %+v", one.Target)
	}
	// The existing shape of /api/tasks/projects is retained.
	raw := expect[map[string][]map[string]json.RawMessage](a, 200, "GET", "/api/tasks/projects", nil)
	for _, p := range raw["projects"] {
		if _, ok := p["key"]; !ok {
			t.Fatalf("project without key: %v", p)
		}
	}
	// Editing the target keeps the same rules.
	patched := expect[tasks.Record](a, 200, "PATCH", rev(rec.ID), map[string]any{"revision": rec.Revision, "target": map[string]any{"kind": "new", "project": key, "project_cwd": repo, "model": "sonnet"}})
	if patched.Target.CWD != repo || patched.Target.Thinking != "" {
		t.Fatalf("patched target = %+v", patched.Target)
	}
	if code, _ := a.do("PATCH", rev(rec.ID), map[string]any{"revision": patched.Revision, "target": map[string]any{"kind": "new", "project": key, "project_cwd": elsewhere, "model": "sonnet"}}); code != 400 {
		t.Errorf("patch to a foreign directory = %d, want 400", code)
	}
}

// The session's tasks endpoint keeps every field installed clients read and
// adds the schedules that concern the session in a separate array.
func TestScheduleInstalledClientProjection(t *testing.T) {
	a := newSchedAPI(t, "2026-09-30T14:40:00Z")
	sid := a.h.session().ID
	stranger := a.h.session().ID
	own := a.makeOwner("Ops", sid)
	strangerOwn := a.makeOwner("Elsewhere", stranger)

	direct := expect[tasks.Record](a, 201, "POST", "/api/tasks", dailyBody("direct", sessionTarget(sid)))
	viaOwner := expect[tasks.Record](a, 201, "POST", "/api/tasks", a.onceBody("via owner", a.inMs(time.Hour), map[string]any{"kind": "owner", "id": own.ID}))
	expect[tasks.Record](a, 201, "POST", "/api/tasks", dailyBody("someone else", sessionTarget(stranger)))
	expect[tasks.Record](a, 201, "POST", "/api/tasks", dailyBody("their owner", map[string]any{"kind": "owner", "id": strangerOwn.ID}))
	mine, err := a.h.mgr.tasks.AgentSchedule(bgc, tasks.Actor{SessionID: sid, ProjectKey: "p", ProjectCWD: "/w"}, tasks.AgentInput{Title: "self made"},
		tasks.When{Kind: tasks.WhenOnce, At: a.inMs(2 * time.Hour)}, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	// An ordinary checklist task and a request sit where they always did.
	expect[tasks.Record](a, 201, "POST", "/api/tasks", map[string]any{"title": "checklist item", "place": "agent", "assignee_session_id": sid})
	if _, err := a.h.mgr.tasks.AgentAsk(bgc, tasks.Actor{SessionID: sid}, tasks.AgentInput{Title: "a question"}); err != nil {
		t.Fatal(err)
	}

	raw := expect[map[string]json.RawMessage](a, 200, "GET", "/api/sessions/"+sid+"/tasks", nil)
	for _, k := range []string{"checklist", "requests", "scheduled"} {
		if _, ok := raw[k]; !ok {
			t.Fatalf("missing %q in %v", k, keysOf(raw))
		}
	}
	if len(raw) != 3 {
		t.Fatalf("unexpected keys %v", keysOf(raw))
	}
	var flat struct {
		Checklist []map[string]json.RawMessage `json:"checklist"`
		Requests  []map[string]json.RawMessage `json:"requests"`
		Scheduled []tasks.Record               `json:"scheduled"`
	}
	if err := json.Unmarshal(mustJSON(t, raw), &flat); err != nil {
		t.Fatal(err)
	}
	if len(flat.Checklist) != 1 || len(flat.Requests) != 1 {
		t.Fatalf("checklist %d requests %d, want the ordinary ones only", len(flat.Checklist), len(flat.Requests))
	}
	allowed := map[string]bool{"id": true, "title": true, "description": true, "status": true, "depends_on": true, "created_at": true, "completed_at": true}
	for k := range flat.Checklist[0] {
		if !allowed[k] {
			t.Errorf("checklist task carries new field %q", k)
		}
	}
	if string(flat.Checklist[0]["title"]) != `"checklist item"` {
		t.Fatalf("checklist = %v", flat.Checklist)
	}
	got := map[int64]bool{}
	for _, r := range flat.Scheduled {
		got[r.ID] = true
		if r.When == nil || r.Target == nil || r.ScheduleState == "" {
			t.Errorf("scheduled entry without its definition: %+v", r)
		}
	}
	if len(flat.Scheduled) != 3 || !got[direct.ID] || !got[viaOwner.ID] || !got[mine.ID] {
		t.Fatalf("scheduled = %+v, want the direct, owner and self-made templates", flat.Scheduled)
	}
	// A session with nothing scheduled still answers the array, empty.
	none := a.h.session().ID
	empty := expect[map[string]json.RawMessage](a, 200, "GET", "/api/sessions/"+none+"/tasks", nil)
	if string(empty["scheduled"]) != "[]" || string(empty["checklist"]) != "[]" {
		t.Fatalf("empty session = %v", empty)
	}
	// A run's child links to its template and occurrence; its flat checklist
	// entry stays as it was.
	a.h.clock.Set(time.UnixMilli(viaOwner.Next))
	a.h.pass()
	o := a.runs(viaOwner.ID)[0]
	waitOcc(t, a.h.repo(), o.ID, "assigned", func(o tasks.Occurrence) bool { return o.ChildTaskID != 0 })
	o = occNow(t, a.h.repo(), o.ID)
	child := a.rec(o.ChildTaskID)
	if child.ParentTaskID != viaOwner.ID || child.OccurrenceID != o.ID || child.Place != tasks.PlaceAgent || child.AssigneeSessionID != sid || child.When != nil {
		t.Fatalf("child = %+v", child)
	}
	after := expect[struct {
		Checklist []map[string]json.RawMessage `json:"checklist"`
	}](a, 200, "GET", "/api/sessions/"+sid+"/tasks", nil)
	if len(after.Checklist) != 2 {
		t.Fatalf("checklist after the run = %v", after.Checklist)
	}
	for _, item := range after.Checklist {
		for k := range item {
			if !allowed[k] {
				t.Errorf("run child in the flat checklist carries %q", k)
			}
		}
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type countsResp struct {
	Counts tasks.Counts `json:"counts"`
}

// The footer counts occurrences waiting for the owner, wherever their parent
// is, next to the open requests, and ignores every filter.
func TestScheduleFooterCountsOccurrences(t *testing.T) {
	a := newSchedAPI(t, "2026-09-30T14:40:00Z")
	sid := a.h.session().ID
	saved := a.h.savedSession()
	repo := a.h.repo()
	for _, title := range []string{"first question", "second question"} {
		if _, err := repo.AgentAsk(bgc, tasks.Actor{SessionID: sid}, tasks.AgentInput{Title: title}); err != nil {
			t.Fatal(err)
		}
	}
	// Noise that must not count: a private note, a held assignment, a failed run.
	expect[tasks.Record](a, 201, "POST", "/api/tasks", map[string]any{"title": "private", "place": "you"})
	expect[tasks.Record](a, 201, "POST", "/api/tasks", map[string]any{"title": "held", "place": "agent", "assignee_session_id": saved})
	ghost := a.makeOwner("Ghost", "")
	failing := expect[tasks.Record](a, 201, "POST", "/api/tasks", a.onceBody("fails", a.inMs(time.Minute), map[string]any{"kind": "owner", "id": ghost.ID}))
	a.h.clock.Advance(time.Minute)
	a.h.pass()
	if r := a.runs(failing.ID); len(r) != 1 || r[0].State != tasks.OccFailed {
		t.Fatalf("failing run = %+v", r)
	}
	// Two recurring tasks with a late run each, and one late run under a
	// parent that is then paused. (A later slot of the same task would
	// coalesce an unanswered late run into itself.)
	repeat := expect[tasks.Record](a, 201, "POST", "/api/tasks", dailyBody("repeats", sessionTarget(sid)))
	expect[tasks.Record](a, 201, "POST", "/api/tasks", dailyBody("repeats too", sessionTarget(sid)))
	paused := expect[tasks.Record](a, 201, "POST", "/api/tasks", dailyBody("will pause", sessionTarget(sid)))
	first := a.lateRun(repeat)
	if r := a.runs(paused.ID); len(r) != 1 || r[0].State != tasks.OccLate {
		t.Fatalf("paused template runs = %+v", r)
	}
	if _, err := a.h.mgr.tasks.Pause(bgc, paused.ID, a.rec(paused.ID).Revision); err != nil {
		t.Fatal(err)
	}

	counts := func(query string) tasks.Counts {
		return expect[countsResp](a, 200, "GET", "/api/tasks"+query, nil).Counts
	}
	if c := counts(""); c.OpenRequests != 2 || c.LateOccurrences != 3 || c.Attention != 5 {
		t.Fatalf("counts = %+v, want 2 requests, 3 late, attention 5", c)
	}
	// Filters and the agents toggle change the list, never the footer.
	for _, q := range []string{"?include_agents=1", "?project=none", "?project=none&include_agents=true&include_archived=1"} {
		if c := counts(q); c.Attention != 5 || c.LateOccurrences != 3 || c.OpenRequests != 2 {
			t.Errorf("counts %s = %+v", q, c)
		}
	}
	expect[tasks.Occurrence](a, 200, "POST", fmt.Sprintf("/api/tasks/occurrences/%d/confirm", first.ID), map[string]any{"revision": first.Revision, "action": "skip"})
	if c := counts(""); c.Attention != 4 || c.LateOccurrences != 2 || c.OpenRequests != 2 {
		t.Fatalf("after confirming one = %+v", c)
	}
	pr := a.rec(paused.ID)
	if pr.ScheduleState != "paused" || pr.LateCount != 1 {
		t.Fatalf("paused parent = %+v", pr)
	}
	if code, raw := a.do("DELETE", fmt.Sprintf("/api/tasks/%d?revision=%d", paused.ID, pr.Revision), nil); code != 204 {
		t.Fatalf("delete = %d %s", code, raw)
	}
	if c := counts(""); c.Attention != 3 || c.LateOccurrences != 1 || c.OpenRequests != 2 {
		t.Fatalf("after deleting the paused parent = %+v", c)
	}
	// The counts of an old client's decoder keep working.
	raw := expect[struct {
		Counts map[string]json.RawMessage `json:"counts"`
	}](a, 200, "GET", "/api/tasks", nil)
	for _, k := range []string{"open_requests", "late_occurrences", "attention", "you", "backlog", "agents"} {
		if _, ok := raw.Counts[k]; !ok {
			t.Errorf("counts lacks %q", k)
		}
	}
}

// A confirm, a pause or an edit written by another process invalidates the
// Tasks socket with a new revision; the socket's shape does not change and a
// fresh read carries the committed state and counts.
func TestScheduleAPIWatchOnExternalOccurrenceChange(t *testing.T) {
	a := newSchedAPI(t, "2026-09-30T14:40:00Z")
	sid := a.h.session().ID
	tmpl := expect[tasks.Record](a, 201, "POST", "/api/tasks", a.onceBody("watched", a.inMs(time.Minute), sessionTarget(sid)))
	repeat := expect[tasks.Record](a, 201, "POST", "/api/tasks", dailyBody("watched daily", sessionTarget(sid)))
	o := a.lateRun(tmpl)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, a.srv.URL+"/api/tasks/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck
	read := func() (int64, map[string]json.RawMessage) {
		t.Helper()
		var msg map[string]json.RawMessage
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			t.Fatal(err)
		}
		var r int64
		_ = json.Unmarshal(msg["revision"], &r)
		return r, msg
	}
	last, msg := read()
	if len(msg) != 2 || string(msg["type"]) != `"tasks_changed"` {
		t.Fatalf("hello = %v", msg)
	}
	external := tasks.New(a.h.dbPath())
	external.SetClock(a.h.clock.Now)
	t.Cleanup(func() { _ = external.Close() })
	change := func(what string, do func()) {
		t.Helper()
		do()
		rev, msg := read()
		for rev <= last {
			rev, msg = read()
		}
		if len(msg) != 2 || string(msg["type"]) != `"tasks_changed"` {
			t.Fatalf("%s: shape changed: %v", what, msg)
		}
		last = rev
	}
	if c := expect[countsResp](a, 200, "GET", "/api/tasks", nil).Counts; c.LateOccurrences != 1 || c.Attention != 1 {
		t.Fatalf("counts before = %+v", c)
	}
	change("confirm", func() {
		if _, err := external.ConfirmOccurrence(bgc, o.ID, o.Revision, tasks.LateSkip); err != nil {
			t.Fatal(err)
		}
	})
	if c := expect[countsResp](a, 200, "GET", "/api/tasks", nil).Counts; c.LateOccurrences != 0 || c.Attention != 0 {
		t.Fatalf("counts after the external confirm = %+v", c)
	}
	if got := a.runs(tmpl.ID)[0]; got.State != tasks.OccSkipped {
		t.Fatalf("run = %+v", got)
	}
	change("pause", func() {
		if _, err := external.Pause(bgc, repeat.ID, repeat.Revision); err != nil {
			t.Fatal(err)
		}
	})
	if got := a.rec(repeat.ID); got.ScheduleState != "paused" {
		t.Fatalf("after the external pause: %+v", got)
	}
	change("edit", func() {
		cur := a.rec(repeat.ID)
		title := "renamed elsewhere"
		if _, err := external.Update(bgc, repeat.ID, cur.Revision, tasks.Patch{Title: &title}); err != nil {
			t.Fatal(err)
		}
	})
	if got := a.rec(repeat.ID); got.Title != "renamed elsewhere" {
		t.Fatalf("after the external edit: %+v", got)
	}
}
