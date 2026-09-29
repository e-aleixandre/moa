package tasks

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, sc *Scope, params map[string]any) (string, bool) {
	t.Helper()
	res, err := NewTool(sc).Execute(bg, params, nil)
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		sb.WriteString(c.Text)
	}
	return sb.String(), res.IsError
}

func scopeFor(r *Repo, session, project string) *Scope {
	sc := NewScope(r, session, "/work/"+project)
	sc.keyOnce.Do(func() { sc.key = project }) // skip git: the project is given
	return sc
}

func TestToolChecklistFlowKeepsTheFamiliarShape(t *testing.T) {
	r := newRepo(t)
	sc := scopeFor(r, "A", "p")

	out, isErr := run(t, sc, map[string]any{"action": "create", "title": "write tests", "description": "with the real db"})
	if isErr || out != "Created task #1: write tests" {
		t.Fatalf("create: %q err=%v", out, isErr)
	}
	out, _ = run(t, sc, map[string]any{"action": "create", "title": "write tests"})
	if !strings.Contains(out, "Created task #2") || !strings.Contains(out, "open task #1 has the same title") {
		t.Fatalf("duplicate hint: %q", out)
	}
	run(t, sc, map[string]any{"action": "create", "title": "ship", "depends_on": []any{float64(1)}, "subtasks": []any{"a", map[string]any{"title": "b", "done": true}}})
	out, _ = run(t, sc, map[string]any{"action": "list"})
	for _, want := range []string{"Tasks (0/3 done)", "☐ #1: write tests", "with the real db", "waits for: #1", "☑ b"} {
		if !strings.Contains(out, want) {
			t.Fatalf("list lacks %q:\n%s", want, out)
		}
	}
	out, _ = run(t, sc, map[string]any{"action": "update", "id": float64(1), "status": "in_progress", "title": "write more tests"})
	if out != "Updated task #1: write more tests" {
		t.Fatalf("update: %q", out)
	}
	out, _ = run(t, sc, map[string]any{"action": "done", "id": float64(1)})
	if !strings.Contains(out, "Marked task #1 as done (1/3 complete)") {
		t.Fatalf("done: %q", out)
	}
	out, _ = run(t, sc, map[string]any{"action": "get", "id": float64(3)})
	if !strings.Contains(out, "Task #3: ship") || !strings.Contains(out, "Depends on: #1") {
		t.Fatalf("get: %q", out)
	}
	if out, isErr = run(t, sc, map[string]any{"action": "update"}); !isErr || !strings.Contains(out, "id is required") {
		t.Fatalf("missing id: %q", out)
	}
	if _, isErr = run(t, sc, map[string]any{"action": "bogus"}); !isErr {
		t.Fatal("unknown action accepted")
	}
}

func TestToolIdentityIsInjectedNotSuppliedByTheModel(t *testing.T) {
	r := newRepo(t)
	a, b := scopeFor(r, "A", "p"), scopeFor(r, "B", "q")
	run(t, a, map[string]any{"action": "create", "title": "A's task"})

	// Extra arguments naming another session or project change nothing.
	out, _ := run(t, b, map[string]any{"action": "list", "session_id": "A", "project": "p", "project_key": "p"})
	if strings.Contains(out, "A's task") {
		t.Fatalf("B saw A's checklist: %q", out)
	}
	if out, isErr := run(t, b, map[string]any{"action": "get", "id": float64(1), "session_id": "A"}); !isErr || strings.Contains(out, "A's task") {
		t.Fatalf("B read A's task: %q", out)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(NewTool(a).Parameters, &schema); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"session_id", "project", "project_key", "cwd", "place", "assignee"} {
		if _, ok := schema.Properties[forbidden]; ok {
			t.Fatalf("the tool lets the model pass %q", forbidden)
		}
	}
}

func TestToolAskDoesNotBlockAndClaimTakesBacklog(t *testing.T) {
	r := newRepo(t)
	a := scopeFor(r, "A", "p")
	bl := backlog(t, r, "shared chore", "p")
	private := note(t, r, "PRIVATE-NOTE-TITLE")
	other := backlog(t, r, "elsewhere", "q")

	out, isErr := run(t, a, map[string]any{"action": "ask", "title": "put the secret in GitHub"})
	if isErr || !strings.Contains(out, "request #") || !strings.Contains(out, "Keep working") {
		t.Fatalf("ask: %q", out)
	}
	out, _ = run(t, a, map[string]any{"action": "list"})
	for _, want := range []string{"Your requests to the owner", "put the secret in GitHub", "Backlog you can claim", "shared chore"} {
		if !strings.Contains(out, want) {
			t.Fatalf("list lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "PRIVATE-NOTE-TITLE") || strings.Contains(out, "elsewhere") {
		t.Fatalf("list leaks:\n%s", out)
	}
	out, isErr = run(t, a, map[string]any{"action": "claim", "id": float64(bl.ID)})
	if isErr || !strings.Contains(out, "Claimed task") {
		t.Fatalf("claim: %q", out)
	}
	if out, isErr = run(t, a, map[string]any{"action": "claim", "id": float64(private.ID)}); !isErr || strings.Contains(out, "PRIVATE") {
		t.Fatalf("claiming a note: %q", out)
	}
	if _, isErr = run(t, a, map[string]any{"action": "claim", "id": float64(other.ID)}); !isErr {
		t.Fatal("claimed another project's backlog")
	}
	if out, isErr = run(t, a, map[string]any{"action": "done", "id": float64(4)}); !isErr || !strings.Contains(out, "not yours") {
		t.Fatalf("done on own request: %q", out)
	}
	// The tool cannot publish to the backlog or the owner's notes.
	run(t, a, map[string]any{"action": "create", "title": "attempt", "place": "backlog", "project_key": "p"})
	if res, _ := r.List(bg, Filter{}); countPlace(res.Tasks, PlaceBacklog) != 1 || findByTitle(res.Tasks, "attempt").Place == PlaceBacklog {
		t.Fatalf("an agent published to the backlog: %+v", res.Tasks)
	}
}

func TestToolShowsAPrivateBlockerWithoutRevealingIt(t *testing.T) {
	r := newRepo(t)
	a := scopeFor(r, "A", "p")
	run(t, a, map[string]any{"action": "create", "title": "deploy"})
	private := note(t, r, "SECRET-TITLE")
	rec, _ := r.Get(bg, 1)
	if _, err := r.Update(bg, rec.ID, rec.Revision, Patch{WaitsFor: &[]int64{private.ID}}); err != nil {
		t.Fatal(err)
	}
	for _, params := range []map[string]any{{"action": "list"}, {"action": "get", "id": float64(1)}} {
		out, _ := run(t, a, params)
		if !strings.Contains(out, "Blocked by a private task") {
			t.Fatalf("%v: no blocker shown:\n%s", params, out)
		}
		if strings.Contains(out, "SECRET-TITLE") || strings.Contains(out, "#2") {
			t.Fatalf("%v: blocker revealed:\n%s", params, out)
		}
	}
}

func TestToolRefusesSubtasksOfSubtasks(t *testing.T) {
	r := newRepo(t)
	a := scopeFor(r, "A", "p")
	out, isErr := run(t, a, map[string]any{"action": "create", "title": "x",
		"subtasks": []any{map[string]any{"title": "child", "subtasks": []any{"grandchild"}}}})
	if !isErr || !strings.Contains(out, "subtasks cannot have subtasks") {
		t.Fatalf("nested subtasks accepted: %q", out)
	}
	if res, _ := r.List(bg, Filter{IncludeAgents: true}); len(res.Tasks) != 0 {
		t.Fatalf("a refused create stored a task: %+v", res.Tasks)
	}
}

func TestScopeProjectionKeepsTheOldJSONFields(t *testing.T) {
	r := newRepo(t)
	sc := scopeFor(r, "A", "p")
	run(t, sc, map[string]any{"action": "create", "title": "one", "description": "d"})
	run(t, sc, map[string]any{"action": "create", "title": "two", "depends_on": []any{float64(1)}})
	run(t, sc, map[string]any{"action": "done", "id": float64(1)})

	b, _ := json.Marshal(sc.Checklist())
	var raw []map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"id": true, "title": true, "description": true, "status": true, "depends_on": true, "created_at": true, "completed_at": true}
	for _, item := range raw {
		for k := range item {
			if !allowed[k] {
				t.Fatalf("new field %q in the checklist projection Pulse parses: %s", k, b)
			}
		}
	}
	if raw[0]["status"] != "done" || raw[0]["completed_at"] == nil || raw[1]["depends_on"].([]any)[0] != float64(1) {
		t.Fatalf("projection: %s", b)
	}

	// ChangedChecklist reports each real change once.
	if _, changed := sc.ChangedChecklist(); changed {
		t.Fatal("no change since Checklist(), yet reported")
	}
	run(t, sc, map[string]any{"action": "done", "id": float64(2)})
	if _, changed := sc.ChangedChecklist(); !changed {
		t.Fatal("change not reported")
	}
	if _, changed := sc.ChangedChecklist(); changed {
		t.Fatal("same change reported twice")
	}
}

func TestScopeCommandsStayInsideTheSession(t *testing.T) {
	r := newRepo(t)
	a, b := scopeFor(r, "A", "p"), scopeFor(r, "B", "p")
	run(t, a, map[string]any{"action": "create", "title": "a1"})
	run(t, a, map[string]any{"action": "ask", "title": "ask A"})
	run(t, b, map[string]any{"action": "create", "title": "b1"})
	bl := backlog(t, r, "backlog", "p")
	n := note(t, r, "note")

	if err := a.MarkDone(3); err == nil { // b1
		t.Fatal("A completed B's task")
	}
	if err := a.MarkDone(int(bl.ID)); err == nil {
		t.Fatal("/tasks done reached the backlog")
	}
	if err := a.MarkDone(int(n.ID)); err == nil {
		t.Fatal("/tasks done reached a private note")
	}
	if err := a.MarkDone(2); err != nil { // its own request, completed by the owner
		t.Fatal(err)
	}
	if err := a.ResetChecklist(); err != nil {
		t.Fatal(err)
	}
	res, _ := r.List(bg, Filter{IncludeAgents: true})
	titles := []string{}
	for _, tk := range res.Tasks {
		titles = append(titles, tk.Title)
	}
	if strings.Join(titles, ",") != "ask A,b1,backlog,note" {
		t.Fatalf("after reset: %v", titles)
	}
}

func TestSharedRepoIsOnePerConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOA_CONFIG_DIR", dir)
	a, b := Shared(), Shared()
	if a != b || a.Path() != filepath.Join(dir, DatabaseName) {
		t.Fatalf("shared: %p %p %q", a, b, a.Path())
	}
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	if Shared() == a {
		t.Fatal("a different config dir reused the repository")
	}
	_ = a.Close()
}

func countPlace(list []Record, p Place) int {
	n := 0
	for _, r := range list {
		if r.Place == p {
			n++
		}
	}
	return n
}

func findByTitle(list []Record, title string) Record {
	for _, r := range list {
		if r.Title == title {
			return r
		}
	}
	return Record{}
}
