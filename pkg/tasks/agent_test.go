package tasks

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestAgentSeesItsRequestButNotNotesOrOtherRequests(t *testing.T) {
	r := newRepo(t)
	a, b := actor("A", "p"), actor("B", "p")

	reqA, err := r.AgentAsk(bg, a, AgentInput{Title: "A needs a secret"})
	if err != nil {
		t.Fatal(err)
	}
	reqB, _ := r.AgentAsk(bg, b, AgentInput{Title: "B needs a review"})
	private := note(t, r, "private plan: fire the contractor")

	view, err := r.AgentList(bg, a)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Requests) != 1 || view.Requests[0].ID != reqA.ID {
		t.Fatalf("A's requests: %+v", view.Requests)
	}
	all := ids(view.Checklist, view.Requests, view.Backlog)
	if all[private.ID] || all[reqB.ID] {
		t.Fatalf("A sees what it must not: %v", all)
	}

	// Guessing an ID reveals nothing, and the error does not say which it is.
	for _, id := range []int64{private.ID, reqB.ID, 9999} {
		_, err := r.AgentGet(bg, a, id)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("get #%d: %v", id, err)
		}
		if strings.Contains(err.Error(), "fire") || strings.Contains(err.Error(), "review") {
			t.Fatalf("error leaks content: %v", err)
		}
	}
	if _, err := r.AgentUpdate(bg, a, private.ID, AgentPatch{Title: ptr("mine now")}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update of a private note: %v", err)
	}
	if _, err := r.AgentDone(bg, a, private.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("done of a private note: %v", err)
	}
	if got, _ := r.Get(bg, private.ID); got.Title != "private plan: fire the contractor" || got.Status != StatusPending {
		t.Fatalf("private note changed: %+v", got)
	}
	// Nor can a note be built into a dependency edge to probe it.
	if _, err := r.AgentCreate(bg, a, AgentInput{Title: "probe", DependsOn: []int64{private.ID}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("dependency on a note: %v", err)
	}
	if v, _ := r.AgentList(bg, a); len(v.Checklist) != 0 {
		t.Fatalf("a rejected create left a task: %+v", v.Checklist)
	}
}

func TestOwnerDependencyOnAPrivateNoteIsProjectedAsBlocked(t *testing.T) {
	r := newRepo(t)
	a := actor("A", "p")
	work, _ := r.AgentCreate(bg, a, AgentInput{Title: "deploy"})
	mine, _ := r.AgentCreate(bg, a, AgentInput{Title: "prepare"})
	private := note(t, r, "get the prod password from the vault")

	rec, _ := r.Get(bg, work.ID)
	if _, err := r.Update(bg, rec.ID, rec.Revision, Patch{WaitsFor: &[]int64{private.ID}}); err != nil {
		t.Fatal(err)
	}
	got, err := r.AgentGet(bg, a, work.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PrivateBlockers != 1 || len(got.WaitsFor) != 0 {
		t.Fatalf("projection: %+v", got)
	}

	// Editing its own dependencies must not drop the edge it cannot see.
	if _, err := r.AgentUpdate(bg, a, work.ID, AgentPatch{DependsOn: &[]int64{mine.ID}}); err != nil {
		t.Fatal(err)
	}
	owner, _ := r.Get(bg, work.ID)
	if !contains(owner.WaitsFor, private.ID) || !contains(owner.WaitsFor, mine.ID) {
		t.Fatalf("owner's edge lost or agent's edge missing: %+v", owner.WaitsFor)
	}

	// Once the note is done the block disappears.
	pn, _ := r.Get(bg, private.ID)
	if _, err := r.Update(bg, pn.ID, pn.Revision, Patch{Status: ptr(StatusDone)}); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.AgentGet(bg, a, work.ID); got.PrivateBlockers != 0 {
		t.Fatalf("done note still blocks: %+v", got)
	}
}

func TestSeeingIsNotEditing(t *testing.T) {
	r := newRepo(t)
	a := actor("A", "p")
	bl := backlog(t, r, "shared backlog task", "p")
	req, _ := r.AgentAsk(bg, a, AgentInput{Title: "please decide"})

	// Both are visible to A…
	if _, err := r.AgentGet(bg, a, bl.ID); err != nil {
		t.Fatal(err)
	}
	// …but A can neither finish the backlog task without claiming it, nor its
	// own request to the owner.
	if _, err := r.AgentDone(bg, a, bl.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("done on backlog: %v", err)
	}
	if _, err := r.AgentDone(bg, a, req.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("done on own request: %v", err)
	}
	if _, err := r.AgentUpdate(bg, a, bl.ID, AgentPatch{Title: ptr("hijacked")}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("update on backlog: %v", err)
	}
	if got, _ := r.Get(bg, bl.ID); got.Status != StatusPending || got.Title != "shared backlog task" || got.Place != PlaceBacklog {
		t.Fatalf("backlog changed: %+v", got)
	}
	if got, _ := r.Get(bg, req.ID); got.Status != StatusPending {
		t.Fatalf("request changed: %+v", got)
	}

	// An open request can be corrected; an answered one is closed to the agent,
	// which can still read the owner's note on that request only.
	if _, err := r.AgentUpdate(bg, a, req.ID, AgentPatch{Description: ptr("it's the staging key")}); err != nil {
		t.Fatal(err)
	}
	rec, _ := r.Get(bg, req.ID)
	if _, err := r.Update(bg, rec.ID, rec.Revision, Patch{Status: ptr(StatusDone), CompletionNote: ptr("key is in 1Password")}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AgentUpdate(bg, a, req.ID, AgentPatch{Title: ptr("late edit")}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("update on an answered request: %v", err)
	}
	got, err := r.AgentGet(bg, a, req.ID)
	if err != nil || got.CompletionNote != "key is in 1Password" {
		t.Fatalf("answer not readable: %+v %v", got, err)
	}
}

func TestBacklogOfOtherProjectsIsInvisible(t *testing.T) {
	r := newRepo(t)
	mine := backlog(t, r, "mine", "p1")
	other := backlog(t, r, "other", "p2")
	a := actor("A", "p1")

	view, _ := r.AgentList(bg, a)
	if len(view.Backlog) != 1 || view.Backlog[0].ID != mine.ID {
		t.Fatalf("backlog: %+v", view.Backlog)
	}
	if _, err := r.AgentGet(bg, a, other.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get: %v", err)
	}
	if _, err := r.AgentClaim(bg, a, other.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("claim: %v", err)
	}
	// An agent whose project is unknown reaches no backlog at all.
	nowhere := Actor{SessionID: "X"}
	if v, _ := r.AgentList(bg, nowhere); len(v.Backlog) != 0 {
		t.Fatalf("no project, yet backlog: %+v", v.Backlog)
	}
}

func TestOnlyOneSessionWinsAClaim(t *testing.T) {
	path := newRepo(t).Path()
	repos := []*Repo{openRepo(t, path), openRepo(t, path)}
	bl := backlog(t, repos[0], "contended", "p")
	private := note(t, repos[0], "owner note")

	const sessions = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	var winners, losers []string
	start := make(chan struct{})
	for i := 0; i < sessions; i++ {
		sid := string(rune('A' + i))
		repo := repos[i%2]
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := repo.AgentClaim(bg, actor(sid, "p"), bl.ID)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				winners = append(winners, sid)
			case errors.Is(err, ErrNotFound):
				losers = append(losers, sid)
			default:
				t.Errorf("%s: unexpected error %v", sid, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if len(winners) != 1 || len(losers) != sessions-1 {
		t.Fatalf("winners %v, losers %v", winners, losers)
	}
	got, _ := repos[0].Get(bg, bl.ID)
	if got.Place != PlaceAgent || got.AssigneeSessionID != winners[0] || got.RequesterSessionID != "" {
		t.Fatalf("claimed task: %+v", got)
	}
	// Not duplicated, and gone from everyone else's backlog.
	all, _ := repos[0].List(bg, Filter{IncludeAgents: true})
	n := 0
	for _, tk := range all.Tasks {
		if tk.Title == "contended" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("task duplicated or lost: %d copies", n)
	}
	if v, _ := repos[1].AgentList(bg, actor(losers[0], "p")); len(v.Backlog) != 0 || len(v.Checklist) != 0 {
		t.Fatalf("loser's view: %+v", v)
	}
	if v, _ := repos[1].AgentList(bg, actor(winners[0], "p")); len(v.Checklist) != 1 {
		t.Fatalf("winner's checklist: %+v", v.Checklist)
	}
	// A private note never appears in any backlog and cannot be claimed.
	if _, err := repos[0].AgentClaim(bg, actor("A", "p"), private.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("claim a note: %v", err)
	}
	req, _ := repos[0].AgentAsk(bg, actor("A", "p"), AgentInput{Title: "a request"})
	if _, err := repos[0].AgentClaim(bg, actor("B", "p"), req.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("claim someone's request: %v", err)
	}
}

func TestDoneTasksReachTheAgentUntilArchived(t *testing.T) {
	r := newRepo(t)
	a := actor("A", "p")
	tk, _ := r.AgentCreate(bg, a, AgentInput{Title: "ship"})
	if _, err := r.AgentDone(bg, a, tk.ID); err != nil {
		t.Fatal(err)
	}
	if v, _ := r.AgentList(bg, a); len(v.Checklist) != 1 || v.Checklist[0].Status != StatusDone {
		t.Fatalf("done task should stay listed: %+v", v.Checklist)
	}
	// Completing twice is a no-op, and an agent cannot reach an archived task.
	before, _ := r.Revision(bg)
	if _, err := r.AgentDone(bg, a, tk.ID); err != nil {
		t.Fatal(err)
	}
	if after, _ := r.Revision(bg); after != before {
		t.Fatalf("second done changed the revision")
	}
	r.SetClock(afterArchive(r))
	if v, _ := r.AgentList(bg, a); len(v.Checklist) != 0 {
		t.Fatalf("archived task still listed: %+v", v.Checklist)
	}
	if _, err := r.AgentGet(bg, a, tk.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("archived get: %v", err)
	}
}

func TestSubtasksAndDependenciesFromTheAgentSide(t *testing.T) {
	r := newRepo(t)
	a := actor("A", "p")
	first, _ := r.AgentCreate(bg, a, AgentInput{Title: "first", Subtasks: []SubtaskInput{{Title: "s1"}}})
	second, err := r.AgentCreate(bg, a, AgentInput{Title: "second", DependsOn: []int64{first.ID}})
	if err != nil || len(second.WaitsFor) != 1 {
		t.Fatalf("%+v %v", second, err)
	}
	if _, err := r.AgentUpdate(bg, a, first.ID, AgentPatch{DependsOn: &[]int64{second.ID}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cycle through the agent API: %v", err)
	}
	got, _ := r.AgentGet(bg, a, first.ID)
	if len(got.WaitsFor) != 0 || len(got.Unblocks) != 1 || len(got.Subtasks) != 1 {
		t.Fatalf("first: %+v", got)
	}
	// Another agent's task is not a legal dependency.
	theirs, _ := r.AgentCreate(bg, actor("B", "p"), AgentInput{Title: "theirs"})
	if _, err := r.AgentUpdate(bg, a, first.ID, AgentPatch{DependsOn: &[]int64{theirs.ID}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("dependency on another session's task: %v", err)
	}
}

func TestChecklistIsFlatAndScopedPerSession(t *testing.T) {
	r := newRepo(t)
	a, b := actor("A", "p"), actor("B", "p")
	t1, _ := r.AgentCreate(bg, a, AgentInput{Title: "a1"})
	_, _ = r.AgentCreate(bg, a, AgentInput{Title: "a2", DependsOn: []int64{t1.ID}})
	_, _ = r.AgentCreate(bg, b, AgentInput{Title: "b1"})
	_, _ = r.AgentAsk(bg, a, AgentInput{Title: "request"}) // not part of the checklist
	list, err := r.Checklist(bg, "A")
	if err != nil || len(list) != 2 {
		t.Fatalf("%+v %v", list, err)
	}
	if list[1].DependsOn[0] != int(t1.ID) {
		t.Fatalf("depends_on: %+v", list[1])
	}
	reqs, _ := r.Requests(bg, "A")
	if len(reqs) != 1 || reqs[0].Title != "request" {
		t.Fatalf("requests: %+v", reqs)
	}
	if err := r.CompleteForSession(bg, "A", t1.ID+2); !errors.Is(err, ErrNotFound) { // b1
		t.Fatalf("A completing B's task: %v", err)
	}
	if err := r.ResetChecklist(bg, "A"); err != nil {
		t.Fatal(err)
	}
	if l, _ := r.Checklist(bg, "A"); len(l) != 0 {
		t.Fatalf("reset: %+v", l)
	}
	if l, _ := r.Checklist(bg, "B"); len(l) != 1 {
		t.Fatalf("reset touched another session: %+v", l)
	}
	if l, _ := r.Requests(bg, "A"); len(l) != 1 {
		t.Fatalf("reset touched the requests: %+v", l)
	}
}

func ids(lists ...[]AgentTask) map[int64]bool {
	out := map[int64]bool{}
	for _, l := range lists {
		for _, t := range l {
			out[t.ID] = true
		}
	}
	return out
}

func contains(list []int64, id int64) bool {
	for _, v := range list {
		if v == id {
			return true
		}
	}
	return false
}

func TestClaimDoesNotRevealAnotherSessionsDirectTask(t *testing.T) {
	r := newRepo(t)
	mine, err := r.AgentCreate(bg, actor("A", "p"), AgentInput{Title: "private to A"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.AgentClaim(bg, actor("B", "p"), mine.ID)
	if !errors.Is(err, ErrNotFound) || errors.Is(err, ErrClaimed) {
		t.Fatalf("claiming another session's task = %v, want not available", err)
	}
}
