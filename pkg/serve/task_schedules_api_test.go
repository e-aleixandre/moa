package serve

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/owner"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// schedAPI is the real HTTP surface over a Manager on a fake clock, so a test
// reads the same JSON the browser does and moves time without sleeping.
type schedAPI struct {
	t   *testing.T
	h   *schedHarness
	srv *httptest.Server
}

func newSchedAPI(t *testing.T, at string) *schedAPI {
	t.Helper()
	h := newSchedHarness(t, newMockProvider(simpleResponseHandler("ok")), at)
	h.start()
	srv := httptest.NewServer(NewServer(h.mgr))
	t.Cleanup(srv.Close)
	return &schedAPI{t: t, h: h, srv: srv}
}

// do sends body (a string or a value marshalled to JSON) and returns the
// status with the raw response.
func (a *schedAPI) do(method, path string, body any) (int, []byte) {
	a.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			a.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, a.srv.URL+path, rd)
	if err != nil {
		a.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Moa-Request", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// expect sends the request and requires the status; it decodes the answer.
func expect[T any](a *schedAPI, status int, method, path string, body any) T {
	a.t.Helper()
	code, raw := a.do(method, path, body)
	if code != status {
		a.t.Fatalf("%s %s = %d, want %d: %s", method, path, code, status, raw)
	}
	var v T
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &v); err != nil {
			a.t.Fatalf("%s %s: %v: %s", method, path, err, raw)
		}
	}
	return v
}

func (a *schedAPI) rec(id int64) tasks.Record {
	a.t.Helper()
	return expect[tasks.Record](a, 200, "GET", fmt.Sprintf("/api/tasks/%d", id), nil)
}

func (a *schedAPI) inMs(d time.Duration) int64 { return a.h.clock.Now().Add(d).UnixMilli() }

func (a *schedAPI) onceBody(title string, at int64, target any) map[string]any {
	return map[string]any{"title": title, "when": map[string]any{"kind": "once", "at": at}, "tz": "Europe/Madrid", "target": target}
}

func dailyBody(title string, target any) map[string]any {
	return map[string]any{"title": title, "tz": "Europe/Madrid", "target": target,
		"when": map[string]any{"kind": "repeat", "rule": map[string]any{"freq": "daily", "h": 9, "mi": 0}}}
}

func ms(s string) int64 { return mustUTC(s).UnixMilli() }

func sessionTarget(id string) map[string]any { return map[string]any{"kind": "session", "id": id} }

type conflictBody struct {
	Error   string          `json:"error"`
	Current json.RawMessage `json:"current"`
}

func (a *schedAPI) makeOwner(name string, sessionID string) owner.Owner {
	a.t.Helper()
	store, err := owner.Default()
	if err != nil {
		a.t.Fatal(err)
	}
	own, err := store.Create(a.t.TempDir(), name, "", "", false, owner.Avatar{})
	if err != nil {
		a.t.Fatal(err)
	}
	own.SessionID = sessionID
	if err := store.Save(own); err != nil {
		a.t.Fatal(err)
	}
	return own
}

func gitProject(t *testing.T) (repo, tree string) {
	t.Helper()
	root := t.TempDir()
	repo, tree = filepath.Join(root, "repo"), filepath.Join(root, "repo-feature")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", repo},
		{"-C", repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
		{"-C", repo, "worktree", "add", "-q", "-b", "feature", tree},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Skipf("git %v: %v %s", args, err, out)
		}
	}
	return repo, tree
}

// A schedule is created and read back through the REST API: canonical
// shape, schedule-only defaults, no notice, history newest first, and the
// ordinary task JSON untouched.
func TestScheduleAPICreateAndDetail(t *testing.T) {
	a := newSchedAPI(t, "2026-09-30T14:40:00Z")
	live := a.h.session().ID
	saved := a.h.savedSession()
	repo, tree := gitProject(t)
	for _, dir := range []string{repo, tree} {
		if _, err := a.h.mgr.CreateSession(CreateOpts{CWD: dir}); err != nil {
			t.Fatal(err)
		}
	}
	own := a.makeOwner("Ops", live)

	t.Run("once to a session with the default delivery", func(t *testing.T) {
		at := a.inMs(time.Hour)
		body := a.onceBody("write the report", at, sessionTarget(live))
		body["description"] = "with numbers"
		rec := expect[tasks.Record](a, 201, "POST", "/api/tasks", body)
		if rec.When == nil || rec.When.Kind != "once" || rec.When.At != at || rec.TZ != "Europe/Madrid" ||
			rec.Target == nil || rec.Target.Kind != "session" || rec.Target.ID != live ||
			rec.Delivery == nil || *rec.Delivery != (tasks.Delivery{Busy: "steer", Saved: "wake", Late: "ask"}) ||
			rec.Next != at || rec.ScheduleState != "scheduled" || rec.Place != tasks.PlaceYou || rec.Status != "pending" ||
			rec.Description != "with numbers" || rec.LateCount != 0 {
			t.Fatalf("created = %+v", rec)
		}
		if n := sqlCount(t, a.h.dbPath(), "SELECT COUNT(*) FROM task_notifications"); n != 0 {
			t.Fatalf("a template created %d notices", n)
		}
		detail := expect[map[string]json.RawMessage](a, 200, "GET", fmt.Sprintf("/api/tasks/%d", rec.ID), nil)
		if string(detail["runs"]) != "[]" || string(detail["notices"]) != "[]" {
			t.Fatalf("detail runs=%s notices=%s", detail["runs"], detail["notices"])
		}
	})

	t.Run("repeat to an owner with a partial delivery", func(t *testing.T) {
		body := dailyBody("standup notes", map[string]any{"kind": "owner", "id": own.ID})
		body["delivery"] = map[string]any{"late": "skip"}
		rec := expect[tasks.Record](a, 201, "POST", "/api/tasks", body)
		if rec.When.Kind != "repeat" || rec.When.Rule.Freq != "daily" || rec.Target.Kind != "owner" || rec.Target.ID != own.ID ||
			*rec.Delivery != (tasks.Delivery{Busy: "steer", Saved: "wake", Late: "skip"}) || rec.Next != ms("2026-10-01T07:00:00Z") {
			t.Fatalf("created = %+v", rec)
		}
	})

	t.Run("new session in a chosen directory", func(t *testing.T) {
		body := dailyBody("nightly", map[string]any{"kind": "new", "project": core.CodebaseKey(repo), "project_cwd": tree, "model": "sonnet", "thinking": "low"})
		rec := expect[tasks.Record](a, 201, "POST", "/api/tasks", body)
		full := "anthropic/claude-sonnet-5-5"
		if rec.Target.Kind != "new" || rec.Target.Project != core.CodebaseKey(repo) || rec.Target.CWD != tree || rec.Target.Model != full || rec.Target.Thinking != "low" {
			t.Fatalf("target = %+v", rec.Target)
		}
		if rec.ProjectKey != core.CodebaseKey(repo) {
			t.Fatalf("project = %q", rec.ProjectKey)
		}
	})

	t.Run("saved session with hold", func(t *testing.T) {
		body := a.onceBody("later", a.inMs(2*time.Hour), sessionTarget(saved))
		body["delivery"] = map[string]any{"busy": "wait", "saved": "hold"}
		rec := expect[tasks.Record](a, 201, "POST", "/api/tasks", body)
		if *rec.Delivery != (tasks.Delivery{Busy: "wait", Saved: "hold", Late: "ask"}) {
			t.Fatalf("delivery = %+v", rec.Delivery)
		}
	})

	t.Run("ordinary tasks keep their shape and defaults", func(t *testing.T) {
		note := expect[map[string]json.RawMessage](a, 201, "POST", "/api/tasks", map[string]any{"title": "plain", "place": "you"})
		for _, k := range []string{"when", "tz", "target", "delivery", "next", "schedule_state", "created_by_session_id", "late_count", "failure", "parent_task_id", "occurrence_id", "runs"} {
			if _, has := note[k]; has {
				t.Errorf("ordinary task carries %q: %s", k, note[k])
			}
		}
		assigned := expect[tasks.Record](a, 201, "POST", "/api/tasks", map[string]any{"title": "for later", "place": "agent", "assignee_session_id": saved})
		if got, _ := a.h.mgr.tasks.TaskNotices(bgc, assigned.ID, 5); len(got) != 1 || got[0].Deliver != tasks.DeliverHold {
			t.Fatalf("ordinary assignment to a saved session = %+v, want one hold-mode notice", got)
		}
		detail := expect[map[string]json.RawMessage](a, 200, "GET", fmt.Sprintf("/api/tasks/%d", assigned.ID), nil)
		if _, has := detail["runs"]; has {
			t.Fatalf("ordinary detail has runs: %s", detail["runs"])
		}
	})

	t.Run("history newest first with a cursor", func(t *testing.T) {
		rec := expect[tasks.Record](a, 201, "POST", "/api/tasks", dailyBody("history", sessionTarget(live)))
		for i := 0; i < 25; i++ {
			cur := a.rec(rec.ID)
			if _, err := a.h.mgr.tasks.SkipNext(bgc, cur.ID, cur.Revision, cur.Next); err != nil {
				t.Fatal(err)
			}
		}
		detail := expect[struct {
			tasks.Record
			Runs []tasks.Occurrence `json:"runs"`
		}](a, 200, "GET", fmt.Sprintf("/api/tasks/%d", rec.ID), nil)
		if len(detail.Runs) != 20 {
			t.Fatalf("%d runs, want 20", len(detail.Runs))
		}
		for i := 1; i < len(detail.Runs); i++ {
			if detail.Runs[i].ID >= detail.Runs[i-1].ID || detail.Runs[i].DueAt >= detail.Runs[i-1].DueAt {
				t.Fatalf("runs are not newest first: %+v", detail.Runs)
			}
		}
		if r := detail.Runs[0]; r.State != tasks.OccSkipped || r.Trigger != tasks.TriggerSkipNext || r.Reason != tasks.ReasonOwnerSkip || r.Revision == 0 || r.ObservedAt == 0 {
			t.Fatalf("run = %+v", r)
		}
		older := expect[struct {
			Runs []tasks.Occurrence `json:"runs"`
		}](a, 200, "GET", fmt.Sprintf("/api/tasks/%d?runs_before=%d", rec.ID, detail.Runs[19].ID), nil)
		if len(older.Runs) != 5 || older.Runs[0].ID >= detail.Runs[19].ID {
			t.Fatalf("older runs = %+v", older.Runs)
		}
	})

	t.Run("invalid definitions are refused and store nothing", func(t *testing.T) {
		before := sqlCount(t, a.h.dbPath(), "SELECT COUNT(*) FROM tasks")
		at := a.inMs(time.Hour)
		for name, body := range map[string]string{
			"backlog place":        fmt.Sprintf(`{"title":"x","place":"backlog","project_key":"p","when":{"kind":"once","at":%d},"tz":"UTC","target":{"kind":"session","id":%q}}`, at, live),
			"assignee":             fmt.Sprintf(`{"title":"x","assignee_session_id":%q,"when":{"kind":"once","at":%d},"tz":"UTC","target":{"kind":"session","id":%q}}`, live, at, live),
			"done":                 fmt.Sprintf(`{"title":"x","status":"done","when":{"kind":"once","at":%d},"tz":"UTC","target":{"kind":"session","id":%q}}`, at, live),
			"no target":            fmt.Sprintf(`{"title":"x","when":{"kind":"once","at":%d},"tz":"UTC"}`, at),
			"no tz":                fmt.Sprintf(`{"title":"x","when":{"kind":"once","at":%d},"target":{"kind":"session","id":%q}}`, at, live),
			"bogus tz":             fmt.Sprintf(`{"title":"x","when":{"kind":"once","at":%d},"tz":"Mars/Base","target":{"kind":"session","id":%q}}`, at, live),
			"past":                 fmt.Sprintf(`{"title":"x","when":{"kind":"once","at":%d},"tz":"UTC","target":{"kind":"session","id":%q}}`, a.inMs(-time.Minute), live),
			"unknown when field":   fmt.Sprintf(`{"title":"x","when":{"kind":"once","at":%d,"extra":1},"tz":"UTC","target":{"kind":"session","id":%q}}`, at, live),
			"dom 32":               fmt.Sprintf(`{"title":"x","when":{"kind":"repeat","rule":{"freq":"monthly","dom":32,"h":9,"mi":0}},"tz":"UTC","target":{"kind":"session","id":%q}}`, live),
			"unknown session":      fmt.Sprintf(`{"title":"x","when":{"kind":"once","at":%d},"tz":"UTC","target":{"kind":"session","id":"nope"}}`, at),
			"unknown owner":        fmt.Sprintf(`{"title":"x","when":{"kind":"once","at":%d},"tz":"UTC","target":{"kind":"owner","id":"nope"}}`, at),
			"new without project":  fmt.Sprintf(`{"title":"x","when":{"kind":"once","at":%d},"tz":"UTC","target":{"kind":"new","model":"sonnet"}}`, at),
			"unknown project":      fmt.Sprintf(`{"title":"x","when":{"kind":"once","at":%d},"tz":"UTC","target":{"kind":"new","project":"nope","model":"sonnet"}}`, at),
			"unknown model":        fmt.Sprintf(`{"title":"x","when":{"kind":"once","at":%d},"tz":"UTC","target":{"kind":"new","project":%q,"model":"nope"}}`, at, core.CodebaseKey(repo)),
			"bad thinking":         fmt.Sprintf(`{"title":"x","when":{"kind":"once","at":%d},"tz":"UTC","target":{"kind":"new","project":%q,"model":"sonnet","thinking":"ludicrous"}}`, at, core.CodebaseKey(repo)),
			"bad delivery":         fmt.Sprintf(`{"title":"x","when":{"kind":"once","at":%d},"tz":"UTC","target":{"kind":"session","id":%q},"delivery":{"busy":"maybe"}}`, at, live),
			"delivery unknown key": fmt.Sprintf(`{"title":"x","when":{"kind":"once","at":%d},"tz":"UTC","target":{"kind":"session","id":%q},"delivery":{"soon":true}}`, at, live),
			"when without a task":  `{"when":{"kind":"once","at":1}}`,
			"target without when":  fmt.Sprintf(`{"title":"x","target":{"kind":"session","id":%q}}`, live),
			"tz without when":      `{"title":"x","tz":"UTC"}`,
		} {
			if code, raw := a.do("POST", "/api/tasks", body); code != 400 {
				t.Errorf("%s = %d %s, want 400", name, code, raw)
			}
		}
		if after := sqlCount(t, a.h.dbPath(), "SELECT COUNT(*) FROM tasks"); after != before {
			t.Fatalf("refused creates stored %d tasks", after-before)
		}
	})

	t.Run("edit from the next run on", func(t *testing.T) {
		rec := expect[tasks.Record](a, 201, "POST", "/api/tasks", dailyBody("editable", sessionTarget(live)))
		patched := expect[tasks.Record](a, 200, "PATCH", fmt.Sprintf("/api/tasks/%d", rec.ID), map[string]any{
			"revision": rec.Revision, "title": "edited",
			"when": map[string]any{"kind": "repeat", "rule": map[string]any{"freq": "weekdays", "h": 7, "mi": 30}}, "delivery": map[string]any{"busy": "wait"}})
		if patched.Title != "edited" || patched.When.Rule.Freq != "weekdays" || patched.TZ != "Europe/Madrid" || patched.Target.ID != live ||
			*patched.Delivery != (tasks.Delivery{Busy: "wait", Saved: "wake", Late: "ask"}) || patched.Next != ms("2026-10-01T05:30:00Z") {
			t.Fatalf("patched = %+v", patched)
		}
		tzOnly := expect[tasks.Record](a, 200, "PATCH", fmt.Sprintf("/api/tasks/%d", rec.ID), map[string]any{"revision": patched.Revision, "tz": "Asia/Tokyo"})
		if tzOnly.TZ != "Asia/Tokyo" || tzOnly.When.Rule.H != 7 {
			t.Fatalf("tz edit = %+v", tzOnly)
		}
		for name, body := range map[string]string{
			"null when":       fmt.Sprintf(`{"revision":%d,"when":null}`, tzOnly.Revision),
			"null target":     fmt.Sprintf(`{"revision":%d,"target":null}`, tzOnly.Revision),
			"move":            fmt.Sprintf(`{"revision":%d,"place":"backlog","project_key":"p"}`, tzOnly.Revision),
			"complete":        fmt.Sprintf(`{"revision":%d,"status":"done"}`, tzOnly.Revision),
			"notify":          fmt.Sprintf(`{"revision":%d,"notify":true}`, tzOnly.Revision),
			"assign":          fmt.Sprintf(`{"revision":%d,"assignee_session_id":%q}`, tzOnly.Revision, live),
			"past once":       fmt.Sprintf(`{"revision":%d,"when":{"kind":"once","at":1}}`, tzOnly.Revision),
			"bad target kind": fmt.Sprintf(`{"revision":%d,"target":{"kind":"planet"}}`, tzOnly.Revision),
		} {
			if code, raw := a.do("PATCH", fmt.Sprintf("/api/tasks/%d", rec.ID), body); code != 400 {
				t.Errorf("%s = %d %s, want 400", name, code, raw)
			}
		}
		if code, _ := a.do("PATCH", fmt.Sprintf("/api/tasks/%d", rec.ID), map[string]any{"revision": rec.Revision, "tz": "UTC"}); code != 409 {
			t.Errorf("stale edit = %d, want 409", code)
		}
		plain := expect[tasks.Record](a, 201, "POST", "/api/tasks", map[string]any{"title": "plain2", "place": "you"})
		if code, raw := a.do("PATCH", fmt.Sprintf("/api/tasks/%d", plain.ID), map[string]any{"revision": plain.Revision, "tz": "UTC"}); code != 400 {
			t.Errorf("tz on an ordinary task = %d %s, want 400", code, raw)
		}
		once := expect[tasks.Record](a, 201, "POST", "/api/tasks", a.onceBody("one shot", a.inMs(time.Hour), sessionTarget(live)))
		moved := expect[tasks.Record](a, 200, "PATCH", fmt.Sprintf("/api/tasks/%d", once.ID), map[string]any{"revision": once.Revision, "when": map[string]any{"kind": "once", "at": a.inMs(3 * time.Hour)}})
		if moved.Next != a.inMs(3*time.Hour) {
			t.Fatalf("moved = %+v", moved)
		}
	})
}
