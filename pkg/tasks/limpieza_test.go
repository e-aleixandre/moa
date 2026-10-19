package tasks

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

const goodNote = "Deployed X at 22:25 UTC.\nChecked /health returns 200 (commit abc123)."

// winerim builds a checklist: open tasks, and done ones completed `ago` ago.
func winerim(t *testing.T, open int, doneAgo []time.Duration) (*Repo, *Scope, func(time.Duration)) {
	t.Helper()
	r := newRepo(t)
	base := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	clock := base
	r.SetClock(func() time.Time { return clock })
	sc := scopeFor(r, "W", "p")
	for i := 0; i < open; i++ {
		run(t, sc, map[string]any{"action": "create", "title": fmt.Sprintf("open %d", i), "description": strings.Repeat("d", 300)})
	}
	for i, ago := range doneAgo {
		clock = base.Add(-ago)
		run(t, sc, map[string]any{"action": "create", "title": fmt.Sprintf("finished %d", i), "description": "long description of a finished task"})
		out, isErr := run(t, sc, map[string]any{"action": "done", "id": float64(open + i + 1), "note": fmt.Sprintf("**Result %d** shipped.\nChecked it.", i)})
		if isErr {
			t.Fatalf("done: %s", out)
		}
	}
	clock = base
	return r, sc, func(d time.Duration) { clock = clock.Add(d) }
}

func TestListDefaultIsCompact(t *testing.T) {
	ago := []time.Duration{}
	for i := 0; i < 4; i++ {
		ago = append(ago, time.Duration(i+1)*time.Hour)
	}
	for i := 0; i < 26; i++ {
		ago = append(ago, time.Duration(30+i*5)*time.Hour)
	}
	_, sc, _ := winerim(t, 17, ago)
	out, isErr := run(t, sc, map[string]any{"action": "list"})
	if isErr {
		t.Fatal(out)
	}
	if strings.Count(out, "☐")+strings.Count(out, "▶") != 17 {
		t.Fatalf("want 17 open:\n%s", out)
	}
	if strings.Count(out, "☑ #") != 4 {
		t.Fatalf("want 4 recent done:\n%s", out)
	}
	if strings.Contains(out, "long description of a finished task") || strings.Contains(out, "Checked it.") {
		t.Fatalf("done tasks must be one line:\n%s", out)
	}
	if !strings.Contains(out, "Result 0 shipped.") || strings.Contains(out, "**") {
		t.Fatalf("first line of the note, without markdown:\n%s", out)
	}
	if strings.Contains(out, strings.Repeat("d", 201)) || !strings.Contains(out, strings.Repeat("d", 200)+"…") {
		t.Fatalf("open descriptions cut at 200:\n%s", out)
	}
	if !strings.Contains(out, "26 older done tasks hidden") || !strings.Contains(out, `status "done"`) {
		t.Fatalf("footer:\n%s", out)
	}
	if len(out) > 8*1024 {
		t.Fatalf("too big: %d", len(out))
	}
}

func TestListStatusDoneNewestFirstAndEmptyParamsChangeNothing(t *testing.T) {
	_, sc, _ := winerim(t, 2, []time.Duration{50 * time.Hour, time.Hour, 100 * time.Hour})
	def, _ := run(t, sc, map[string]any{"action": "list"})
	same, _ := run(t, sc, map[string]any{"action": "list", "search": "", "status": nil})
	if def != same {
		t.Fatalf("empty params changed the output:\n%s\n---\n%s", def, same)
	}
	out, _ := run(t, sc, map[string]any{"action": "list", "status": "done"})
	i1, i0, i2 := strings.Index(out, "finished 1"), strings.Index(out, "finished 0"), strings.Index(out, "finished 2")
	if !(i1 >= 0 && i1 < i0 && i0 < i2) || strings.Contains(out, "open 0") {
		t.Fatalf("done newest first, no open:\n%s", out)
	}
	if out, isErr := run(t, sc, map[string]any{"action": "list", "status": "bogus"}); !isErr || !strings.Contains(out, "open") {
		t.Fatalf("bad status: %q %v", out, isErr)
	}
}

func TestListLimit(t *testing.T) {
	_, sc, _ := winerim(t, 3, []time.Duration{time.Hour, 2 * time.Hour, 3 * time.Hour, 4 * time.Hour, 5 * time.Hour, 6 * time.Hour, 7 * time.Hour, 8 * time.Hour})
	out, _ := run(t, sc, map[string]any{"action": "list", "status": "all", "limit": float64(10)})
	if strings.Count(out, "#") < 10 || !strings.Contains(out, "1 more — narrow with search or status") {
		t.Fatalf("limit:\n%s", out)
	}
	if strings.Count(out, "\n☐ #")+strings.Count(out, "\n☑ #") != 10 {
		t.Fatalf("exactly 10 tasks:\n%s", out)
	}
}

func TestListSearchReachesDoneAndArchivedButKeepsPrivacy(t *testing.T) {
	r, sc, adv := winerim(t, 1, nil)
	run(t, sc, map[string]any{"action": "create", "title": "Albarán cancelado guarda precios"})
	run(t, sc, map[string]any{"action": "done", "id": float64(2), "note": goodNote})
	if _, err := r.Create(bg, CreateInput{Title: "private albarán note", Place: PlaceYou}); err != nil {
		t.Fatal(err)
	}
	other := scopeFor(r, "O", "q")
	run(t, other, map[string]any{"action": "create", "title": "other albarán"})
	adv(8 * 24 * time.Hour) // the done task is now archived

	hid, _ := run(t, sc, map[string]any{"action": "list", "status": "done"})
	if strings.Contains(hid, "Albarán") {
		t.Fatalf("archived must stay hidden by default:\n%s", hid)
	}
	out, _ := run(t, sc, map[string]any{"action": "list", "search": "ALBARAN"})
	if !strings.Contains(out, "Albarán cancelado guarda precios") {
		t.Fatalf("search must find the archived task:\n%s", out)
	}
	if strings.Contains(out, "private") || strings.Contains(out, "other albarán") {
		t.Fatalf("privacy:\n%s", out)
	}
	arch, _ := run(t, sc, map[string]any{"action": "list", "status": "done", "archived": true})
	if !strings.Contains(arch, "Albarán cancelado") {
		t.Fatalf("archived:true:\n%s", arch)
	}
	if got, isErr := run(t, sc, map[string]any{"action": "get", "id": float64(2)}); isErr || !strings.Contains(got, "Deployed X") {
		t.Fatalf("get an archived task of mine: %q", got)
	}
	if _, isErr := run(t, other, map[string]any{"action": "get", "id": float64(2)}); !isErr {
		t.Fatal("another session read an archived task")
	}
}

func TestDoneNeedsAClosingNoteOnOwnTasks(t *testing.T) {
	r := newRepo(t)
	sc := scopeFor(r, "A", "p")
	run(t, sc, map[string]any{"action": "create", "title": "x"})
	out, isErr := run(t, sc, map[string]any{"action": "done", "id": float64(1)})
	if !isErr || !strings.Contains(out, "1–4 lines") || !strings.Contains(out, "what is true now") {
		t.Fatalf("no note: %q", out)
	}
	if out, isErr = run(t, sc, map[string]any{"action": "done", "id": float64(1), "note": "a\nb\nc\nd\ne"}); !isErr || !strings.Contains(out, "1–4 lines") {
		t.Fatalf("five lines: %q", out)
	}
	if out, _ = run(t, sc, map[string]any{"action": "get", "id": float64(1)}); !strings.Contains(out, "Status: pending") {
		t.Fatalf("must stay open: %q", out)
	}
	if out, isErr = run(t, sc, map[string]any{"action": "done", "id": float64(1), "note": "a\n\nb\n"}); isErr {
		t.Fatalf("blank lines do not count: %q", out)
	}
	// Closing again without a note is not an error: nothing changes.
	if out, isErr = run(t, sc, map[string]any{"action": "done", "id": float64(1)}); isErr {
		t.Fatalf("idempotent: %q", out)
	}
}

func TestAgentCommentsItsOwnTaskSilentlyAndTheUserWakesIt(t *testing.T) {
	r := newRepo(t)
	sc := scopeFor(r, "A", "p")
	run(t, sc, map[string]any{"action": "create", "title": "x"})
	if out, isErr := run(t, sc, map[string]any{"action": "comment", "id": float64(1), "body": "deployed at 22:25"}); isErr {
		t.Fatalf("comment: %q", out)
	}
	n := noticeCount(t, r)
	if n != 0 {
		t.Fatalf("own comment notified %d", n)
	}
	if _, err := r.HumanComment(bg, 1, "OK to deploy"); err != nil {
		t.Fatal(err)
	}
	if got := noticeCount(t, r); got != 1 {
		t.Fatalf("user comment must notify the agent once, got %d", got)
	}
	out, _ := run(t, sc, map[string]any{"action": "get", "id": float64(1)})
	if !strings.Contains(out, "you: deployed at 22:25") || !strings.Contains(out, "the user: OK to deploy") {
		t.Fatalf("thread:\n%s", out)
	}
	// Another session cannot comment it; the backlog has no thread.
	if _, isErr := run(t, scopeFor(r, "B", "p"), map[string]any{"action": "comment", "id": float64(1), "body": "hi"}); !isErr {
		t.Fatal("another session commented")
	}
}

func TestListDoneLineCarriesNoteFirstLineAndCommentCount(t *testing.T) {
	_, sc, _ := winerim(t, 0, nil)
	run(t, sc, map[string]any{"action": "create", "title": "Fix Savia"})
	long := strings.Repeat("w", 200)
	run(t, sc, map[string]any{"action": "comment", "id": float64(1), "body": "one"})
	run(t, sc, map[string]any{"action": "comment", "id": float64(1), "body": "two"})
	run(t, sc, map[string]any{"action": "comment", "id": float64(1), "body": "three"})
	run(t, sc, map[string]any{"action": "done", "id": float64(1), "note": long + "\nsecond line"})
	out, _ := run(t, sc, map[string]any{"action": "list", "status": "done"})
	if !strings.Contains(out, "(3 comments)") || strings.Contains(out, "second line") || !strings.Contains(out, strings.Repeat("w", 110)+"…") || strings.Contains(out, strings.Repeat("w", 111)) {
		t.Fatalf("done line:\n%s", out)
	}
	got, _ := run(t, sc, map[string]any{"action": "get", "id": float64(1)})
	if !strings.Contains(got, "second line") || !strings.Contains(got, "three") {
		t.Fatalf("get has the whole note and thread:\n%s", got)
	}
}

func TestChecklistProjectionCarriesTheNote(t *testing.T) {
	r := newRepo(t)
	sc := scopeFor(r, "A", "p")
	run(t, sc, map[string]any{"action": "create", "title": "x"})
	run(t, sc, map[string]any{"action": "done", "id": float64(1), "note": goodNote})
	cl, err := r.Checklist(bg, "A")
	if err != nil || len(cl) != 1 || cl[0].CompletionNote != goodNote {
		t.Fatalf("%+v %v", cl, err)
	}
}

func noticeCount(t *testing.T, r *Repo) int {
	t.Helper()
	var n int
	rd, err := r.reader()
	if err != nil {
		t.Fatal(err)
	}
	if err := rd.QueryRowContext(bg, `SELECT COUNT(*) FROM task_notifications`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
