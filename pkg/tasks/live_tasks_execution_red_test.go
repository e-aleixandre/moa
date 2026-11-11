package tasks

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"testing"
	"time"
)

// Runtime assertions keep the existing packages compilable during RED, while
// fixing the minimal production API without fake repository implementations.
type liveTasksExecutions interface {
	LinkExecution(context.Context, Actor, int64, string) (Record, error)
	MarkExecutionStarted(context.Context, string) error
	MarkExecutionStopped(context.Context, string) ([]Record, error)
	InterruptedExecutions(context.Context) ([]Record, error)
}

func liveTasksAPI(t *testing.T, r *Repo) liveTasksExecutions {
	t.Helper()
	api, ok := any(r).(liveTasksExecutions)
	if !ok {
		t.Fatal("Repo execution API missing: LinkExecution(ctx, Actor, taskID, sessionID) (Record, error); MarkExecutionStarted(ctx, sessionID) error; MarkExecutionStopped(ctx, sessionID) ([]Record, error); InterruptedExecutions(ctx) ([]Record, error)")
	}
	return api
}

type liveTasksExecution struct {
	SessionID string `json:"session_id"`
	LinkedAt  int64  `json:"linked_at"`
	StartedAt int64  `json:"started_at"`
	StoppedAt int64  `json:"stopped_at"`
}

func liveTasksExecutionOf(t *testing.T, rec Record) *liveTasksExecution {
	t.Helper()
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Execution *liveTasksExecution `json:"execution"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	return wire.Execution
}

func liveTasksLinkedExecution(t *testing.T, r *Repo, id int64) *liveTasksExecution {
	t.Helper()
	ex := liveTasksExecutionOf(t, mustGet(t, r, id))
	if ex == nil {
		t.Fatalf("task #%d lost its execution projection", id)
	}
	return ex
}

func liveTasksIDs(recs []Record) []int64 {
	ids := make([]int64, 0, len(recs))
	for _, rec := range recs {
		ids = append(ids, rec.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func liveTasksOwner() Actor {
	a := actor("owner", "p")
	a.Owner = true
	return a
}

func TestLiveTasksLinkRequiresOwnersOpenChecklist(t *testing.T) {
	for _, tc := range []struct {
		name     string
		place    Place
		assignee string
		status   string
		owner    bool
		executor string
		allow    bool
	}{
		{"pending", PlaceAgent, "owner", StatusPending, true, "worker", true},
		{"in_progress", PlaceAgent, "owner", StatusInProgress, true, "worker", true},
		{"done", PlaceAgent, "owner", StatusDone, true, "worker", false},
		{"backlog", PlaceBacklog, "", StatusPending, true, "worker", false},
		{"you", PlaceYou, "", StatusPending, true, "worker", false},
		{"others_checklist", PlaceAgent, "another-owner", StatusPending, true, "worker", false},
		{"non_owner", PlaceAgent, "owner", StatusPending, false, "worker", false},
		{"blank_executor", PlaceAgent, "owner", StatusPending, true, "", false},
		{"whitespace_executor", PlaceAgent, "owner", StatusPending, true, "   ", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRepo(t)
			rec := mustCreate(t, r, CreateInput{Title: tc.name, Place: tc.place, ProjectKey: "p", AssigneeSessionID: tc.assignee})
			if tc.status != StatusPending {
				var err error
				rec, err = r.Update(bg, rec.ID, rec.Revision, Patch{Status: &tc.status})
				if err != nil {
					t.Fatal(err)
				}
			}
			a := liveTasksOwner()
			a.Owner = tc.owner
			api := liveTasksAPI(t, r)
			linked, err := api.LinkExecution(bg, a, rec.ID, tc.executor)
			if tc.allow {
				if err != nil {
					t.Fatal(err)
				}
				ex := liveTasksExecutionOf(t, linked)
				if ex == nil || ex.SessionID != tc.executor || ex.LinkedAt == 0 || ex.StartedAt != 0 || ex.StoppedAt != 0 {
					t.Fatalf("new link has incorrect execution state: %+v", ex)
				}
			} else if err == nil {
				t.Fatal("unauthorized/closed/non-checklist link was accepted")
			}
			got := mustGet(t, r, rec.ID)
			if got.Status != rec.Status || got.AssigneeSessionID != rec.AssigneeSessionID {
				t.Fatalf("delegating changed task state/ownership: %+v", got)
			}
			if !tc.allow && liveTasksExecutionOf(t, got) != nil {
				t.Fatal("rejected delegation left a link")
			}
		})
	}
}

func TestLiveTasksExecutionLifecycleIsScopedAndOncePerStop(t *testing.T) {
	r := newRepo(t)
	at := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	r.SetClock(func() time.Time { return at })
	a := liveTasksOwner()
	makeTask := func(title string) Record {
		return mustCreate(t, r, CreateInput{Title: title, Place: PlaceAgent, ProjectKey: "p", AssigneeSessionID: a.SessionID})
	}
	one, done, other := makeTask("first"), makeTask("will complete"), makeTask("other executor")
	api := liveTasksAPI(t, r)
	for _, pair := range []struct {
		id      int64
		session string
	}{{one.ID, "worker"}, {done.ID, "worker"}, {other.ID, "other"}} {
		if _, err := api.LinkExecution(bg, a, pair.id, pair.session); err != nil {
			t.Fatal(err)
		}
		if got := mustGet(t, r, pair.id); got.Status != StatusPending {
			t.Fatal("delegation or queuing is not an admitted execution")
		}
	}
	linkedAt := at.UnixMilli()
	at = at.Add(time.Minute)
	if err := api.MarkExecutionStarted(bg, "worker"); err != nil {
		t.Fatal(err)
	}
	startedAt := at.UnixMilli()
	for _, id := range []int64{one.ID, done.ID} {
		rec := mustGet(t, r, id)
		ex := liveTasksExecutionOf(t, rec)
		if rec.Status != StatusInProgress || ex == nil || ex.LinkedAt != linkedAt || ex.StartedAt != startedAt || ex.StoppedAt != 0 {
			t.Fatalf("admitted run did not mark linked pending task: task=%+v execution=%+v", rec, ex)
		}
	}
	if rec := mustGet(t, r, other.ID); rec.Status != StatusPending || liveTasksLinkedExecution(t, r, other.ID).StartedAt != 0 {
		t.Fatal("starting one session touched the other session's task")
	}
	before := mustGet(t, r, one.ID)
	at = at.Add(time.Minute)
	if err := api.MarkExecutionStarted(bg, "worker"); err != nil {
		t.Fatal(err)
	}
	if after := mustGet(t, r, one.ID); !reflect.DeepEqual(after, before) {
		t.Fatalf("a duplicate start changed the task/link: before=%+v after=%+v", before, after)
	}
	if _, err := agentDone(r, a, done.ID); err != nil {
		t.Fatal(err)
	}
	if ex := liveTasksExecutionOf(t, mustGet(t, r, done.ID)); ex == nil || ex.SessionID != "worker" {
		t.Fatal("completion must retain the execution reference")
	}
	interrupted, err := api.InterruptedExecutions(bg)
	if err != nil || !slices.Equal(liveTasksIDs(interrupted), []int64{one.ID}) {
		t.Fatalf("boot candidates must be open + started + not stopped, not completed or merely delegated: %+v, %v", interrupted, err)
	}
	at = at.Add(time.Minute)
	warn, err := api.MarkExecutionStopped(bg, "worker")
	if err != nil || !slices.Equal(liveTasksIDs(warn), []int64{one.ID}) {
		t.Fatalf("stop warnings=%+v err=%v, want only open task #%d", warn, err, one.ID)
	}
	stoppedAt := at.UnixMilli()
	if rec := mustGet(t, r, one.ID); rec.Status != StatusInProgress || liveTasksLinkedExecution(t, r, one.ID).StoppedAt != stoppedAt {
		t.Fatalf("stopping must mark execution, never complete the task: %+v", rec)
	}
	at = at.Add(10 * 24 * time.Hour)
	if warn, err := api.MarkExecutionStopped(bg, "worker"); err != nil || len(warn) != 0 {
		t.Fatalf("age alone produced repeat stop warnings: %+v, %v", warn, err)
	}
	if ex := liveTasksLinkedExecution(t, r, one.ID); ex.StoppedAt != stoppedAt {
		t.Fatal("repeat stop replaced the original stop timestamp")
	}
	if got, err := api.InterruptedExecutions(bg); err != nil || len(got) != 0 {
		t.Fatalf("gracefully stopped tasks must not reappear at boot: %+v, %v", got, err)
	}
	if err := api.MarkExecutionStarted(bg, "worker"); err != nil {
		t.Fatal(err)
	}
	if ex := liveTasksLinkedExecution(t, r, one.ID); ex.StartedAt != startedAt || ex.StoppedAt != 0 {
		t.Fatalf("resume must preserve first start and clear stop: %+v", ex)
	}
	// Sending a correction to the same executor is not a new attempt.
	if _, err := api.LinkExecution(bg, a, one.ID, "worker"); err != nil {
		t.Fatal(err)
	}
	if ex := liveTasksLinkedExecution(t, r, one.ID); ex.LinkedAt != linkedAt || ex.StartedAt != startedAt {
		t.Fatalf("same-executor delegation reset the current attempt: %+v", ex)
	}
	at = at.Add(time.Minute)
	if warn, err := api.MarkExecutionStopped(bg, "worker"); err != nil || !slices.Equal(liveTasksIDs(warn), []int64{one.ID}) {
		t.Fatalf("resume then stop must warn again once: %+v, %v", warn, err)
	}
	at = at.Add(time.Minute)
	if _, err := api.LinkExecution(bg, a, one.ID, "reviewer"); err != nil {
		t.Fatal(err)
	}
	if ex := liveTasksLinkedExecution(t, r, one.ID); ex.SessionID != "reviewer" || ex.LinkedAt != at.UnixMilli() || ex.StartedAt != 0 || ex.StoppedAt != 0 {
		t.Fatalf("relink must replace the attempt: %+v", ex)
	}
	if warn, err := api.MarkExecutionStopped(bg, "worker"); err != nil || len(warn) != 0 {
		t.Fatalf("old executor warned about a relinked or completed task: %+v, %v", warn, err)
	}
}

func TestLiveTasksInterruptedExecutionsSurviveReopenAndSessionDelete(t *testing.T) {
	r := newRepo(t)
	a := liveTasksOwner()
	one := mustCreate(t, r, CreateInput{Title: "working before crash", Place: PlaceAgent, ProjectKey: "p", AssigneeSessionID: a.SessionID})
	two := mustCreate(t, r, CreateInput{Title: "another worker", Place: PlaceAgent, ProjectKey: "p", AssigneeSessionID: a.SessionID})
	api := liveTasksAPI(t, r)
	for _, pair := range []struct {
		id int64
		s  string
	}{{one.ID, "worker"}, {two.ID, "other"}} {
		if _, err := api.LinkExecution(bg, a, pair.id, pair.s); err != nil {
			t.Fatal(err)
		}
		if err := api.MarkExecutionStarted(bg, pair.s); err != nil {
			t.Fatal(err)
		}
	}
	_ = r.Close()
	r = openRepo(t, r.Path())
	api = liveTasksAPI(t, r)
	if got, err := api.InterruptedExecutions(bg); err != nil || !slices.Equal(liveTasksIDs(got), []int64{one.ID, two.ID}) {
		t.Fatalf("boot lost the persisted working links: %+v, %v", got, err)
	}
	if _, err := r.SettleSessionDeleted(bg, "worker"); err != nil {
		t.Fatal(err)
	}
	if ex := liveTasksExecutionOf(t, mustGet(t, r, one.ID)); ex != nil {
		t.Fatalf("session deletion left a stale execution link: %+v", ex)
	}
	if got, err := api.InterruptedExecutions(bg); err != nil || !slices.Equal(liveTasksIDs(got), []int64{two.ID}) {
		t.Fatalf("session deletion affected another executor: %+v, %v", got, err)
	}
}
