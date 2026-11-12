package tasks

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// The owner's list says who does each open task of its checklist, from the
// link's facts; a child's list has no such line.
func TestOwnerListShowsEachOpenTasksExecution(t *testing.T) {
	r := newRepo(t)
	at := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	r.SetClock(func() time.Time { return at })
	sc := scopeFor(r, "owner", "p").WithOwners(nil, true).WithSessionTitles(func(id string) string {
		return map[string]string{"worker": "Export CSV", "idle": "Audit"}[id]
	})
	a := liveTasksOwner()
	mk := func(title string) Record {
		return mustCreate(t, r, CreateInput{Title: title, Place: PlaceAgent, ProjectKey: "p", AssigneeSessionID: "owner"})
	}
	working, stopped, delegated, unlinked, closed := mk("working task"), mk("stopped task"), mk("delegated task"), mk("unlinked task"), mk("closed task")
	for _, l := range []struct {
		id int64
		s  string
	}{{working.ID, "worker"}, {stopped.ID, "idle"}, {delegated.ID, "waiting-start"}, {closed.ID, "worker"}} {
		if _, err := r.LinkExecution(bg, a, l.id, l.s); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []string{"worker", "idle"} {
		if err := r.MarkExecutionStarted(bg, s); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.MarkExecutionStopped(bg, "idle"); err != nil {
		t.Fatal(err)
	}
	if _, err := agentDone(r, a, closed.ID); err != nil {
		t.Fatal(err)
	}
	at = at.Add(3 * 24 * time.Hour)

	out, isErr := run(t, sc, map[string]any{"action": "list"})
	if isErr {
		t.Fatal(out)
	}
	for _, want := range []string{
		"#" + itoa(working.ID) + ": working task\n    → worker «Export CSV» · working",
		"#" + itoa(stopped.ID) + ": stopped task\n    → idle «Audit» · stopped 3d",
		"#" + itoa(delegated.ID) + ": delegated task\n    → waiting-start · delegated, not started",
		"#" + itoa(unlinked.ID) + ": unlinked task\n    → no executor",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("owner list lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "→") != 4 {
		t.Errorf("a done task must not show an executor line:\n%s", out)
	}

	child := scopeFor(r, "worker", "p")
	run(t, child, map[string]any{"action": "create", "title": "child step"})
	if out, _ := run(t, child, map[string]any{"action": "list"}); strings.Contains(out, "→") {
		t.Errorf("a child's list shows execution lines:\n%s", out)
	}
}

func TestOwnerBacklogListingAndCreateErrors(t *testing.T) {
	r := newRepo(t)
	sc := scopeFor(r, "owner", "p").WithOwners(nil, true)
	if out, _ := run(t, sc, map[string]any{"action": "list", "place": "backlog"}); out != "No ideas in the backlog." {
		t.Fatalf("empty backlog: %q", out)
	}
	out, isErr := run(t, sc, map[string]any{"action": "create", "title": "One idea", "place": "backlog"})
	if isErr || !strings.Contains(out, "Wrote idea #1 to the backlog") {
		t.Fatalf("create idea: %q", out)
	}
	if out, _ := run(t, sc, map[string]any{"action": "list"}); !strings.Contains(out, "Backlog: 1 idea (list place=backlog)") {
		t.Fatalf("singular summary: %q", out)
	}
	if out, isErr := run(t, sc, map[string]any{"action": "create", "title": "x", "place": "backlog", "when": "in 20m"}); !isErr {
		t.Fatalf("an idea was scheduled: %q", out)
	}
	if out, isErr := run(t, sc, map[string]any{"action": "create", "title": "x", "place": "elsewhere"}); !isErr {
		t.Fatalf("unknown place accepted: %q", out)
	}
	// A backlog idea cannot be linked until it is promoted, and the error
	// tells the owner how.
	if _, err := r.LinkExecution(bg, liveTasksOwner(), 1, "worker"); err == nil || !strings.Contains(err.Error(), "claim") {
		t.Fatalf("linking an idea: %v", err)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// A task's row shows what its thread last said: the blocker the owner wrote.
func TestRecordCarriesItsLatestComment(t *testing.T) {
	r := newRepo(t)
	rec := mustCreate(t, r, CreateInput{Title: "blocked", Place: PlaceAgent, ProjectKey: "p", AssigneeSessionID: "owner"})
	if got, err := r.Get(bg, rec.ID); err != nil || got.LastComment != "" {
		t.Fatalf("no thread yet: %q %v", got.LastComment, err)
	}
	for _, body := range []string{"waits for the API key", "waits for Ana's review"} {
		if _, err := r.HumanComment(bg, rec.ID, body); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.Get(bg, rec.ID)
	if err != nil || got.LastComment != "waits for Ana's review" || got.CommentCount != 2 {
		t.Fatalf("latest comment = %q (count %d) %v", got.LastComment, got.CommentCount, err)
	}
}
