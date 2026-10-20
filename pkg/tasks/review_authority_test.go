package tasks

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReviewAuthorityScheduleGuardMatrix(t *testing.T) {
	for _, action := range []string{"pause", "resume", "delete"} {
		for _, kind := range []string{"sibling", "cross-project", "user-targets-me", "own-checklist", "own-request", "private-note", "unknown", "empty-session", "owner-not-creator", "recreated-session"} {
			t.Run(action+"/"+kind, func(t *testing.T) {
				r, _ := schedRepo(t, "2026-10-20T08:00:00Z")
				me := actor("creator", "p")
				target := agentRepeat(t, r, me, "PRIVATE schedule title")
				caller := actor("sibling", "p")
				want := ErrNotFound
				switch kind {
				case "cross-project":
					caller = actor("foreign", "q")
				case "user-targets-me":
					def := dailyDef(9, 0, "UTC", "")
					def.Target = sessionTarget(caller.SessionID)
					target = mkSchedule(t, r, "PRIVATE user schedule", def)
				case "own-checklist":
					rec, err := r.AgentCreate(bg, caller, AgentInput{Title: "own checklist"})
					if err != nil {
						t.Fatal(err)
					}
					target = mustGet(t, r, rec.ID)
					want = ErrInvalid
				case "own-request":
					rec, err := r.AgentAsk(bg, caller, AgentInput{Title: "own request"})
					if err != nil {
						t.Fatal(err)
					}
					target = mustGet(t, r, rec.ID)
					want = ErrInvalid
				case "private-note":
					target = note(t, r, "PRIVATE user note")
				case "unknown":
					target.ID = 999999
				case "empty-session":
					caller = Actor{ProjectKey: "p", Owner: true}
				case "owner-not-creator":
					caller.Owner = true
				case "recreated-session":
					caller = actor("new-session-same-project-and-title", "p")
				}
				before, err := r.List(bg, Filter{IncludeAgents: true, IncludeArchived: true})
				if err != nil {
					t.Fatal(err)
				}
				switch action {
				case "pause":
					_, err = r.AgentPause(bg, caller, target.ID)
				case "resume":
					_, err = r.AgentResume(bg, caller, target.ID)
				case "delete":
					err = r.AgentDeleteSchedule(bg, caller, target.ID)
				}
				if !errors.Is(err, want) {
					t.Fatalf("%s #%d as %+v = %v, want %v", action, target.ID, caller, err, want)
				}
				if strings.Contains(err.Error(), "PRIVATE") {
					t.Fatalf("refusal leaked title: %v", err)
				}
				after, err := r.List(bg, Filter{IncludeAgents: true, IncludeArchived: true})
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("refused %s changed persisted records or global revision", action)
				}
			})
		}
	}
}

func TestReviewAuthorityToolIdentityCannotBeForged(t *testing.T) {
	r, _ := schedRepo(t, "2026-10-20T08:00:00Z")
	mine := agentRepeat(t, r, actor("creator", "p"), "PRIVATE scheduled job")
	req, err := r.AgentAsk(bg, actor("creator", "p"), AgentInput{Title: "PRIVATE request"})
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"pause", "resume", "delete", "withdraw"} {
		id := mine.ID
		if action == "withdraw" {
			id = req.ID
		}
		out, isErr := run(t, scopeFor(r, "attacker", "q"), map[string]any{
			"action": action, "id": float64(id), "note": "not needed",
			"session_id": "creator", "project_key": "p", "owner": true,
			"created_by_session_id": "attacker", "requester_session_id": "attacker",
		})
		if !isErr || strings.Contains(out, "PRIVATE") {
			t.Fatalf("forged %s = %q, isErr=%v", action, out, isErr)
		}
	}
	// The identity is the session ID, not the cwd/name used to recreate its scope.
	if out, isErr := run(t, scopeFor(r, "creator", "renamed-project"), map[string]any{"action": "pause", "id": float64(mine.ID)}); isErr {
		t.Fatalf("original creator lost authority after scope rename: %s", out)
	}
	if got := mustGet(t, r, mine.ID); got.ScheduleState != "paused" || got.ProjectKey != "p" {
		t.Fatalf("after creator pause: %+v", got)
	}
}

func TestReviewAuthorityOnceCanOnlyBeDeleted(t *testing.T) {
	r, c := schedRepo(t, "2026-10-20T08:00:00Z")
	a := actor("creator", "p")
	rec, err := r.AgentSchedule(bg, a, AgentInput{Title: "once"}, When{Kind: WhenOnce, At: c.Now().Add(time.Minute).UnixMilli()}, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	for _, control := range []func() (Record, error){func() (Record, error) { return r.AgentPause(bg, a, rec.ID) }, func() (Record, error) { return r.AgentResume(bg, a, rec.ID) }} {
		if _, err := control(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("once pause/resume = %v", err)
		}
	}
	c.Add(time.Minute)
	o := materializeOne(t, r)
	if _, err := r.FailOccurrence(bg, o.ID, "resume_failed", "Destination could not resume"); err != nil {
		t.Fatal(err)
	}
	c.Add(8 * 24 * time.Hour)
	if got := mustGet(t, r, rec.ID); got.Status == StatusDone || got.ArchivedAt != 0 {
		t.Fatalf("expired once disappeared into Done: %+v", got)
	}
	if err := r.AgentDeleteSchedule(bg, a, rec.ID); err != nil {
		t.Fatal(err)
	}
	if got := mustOcc(t, r, o.ID); got.State != OccFailed {
		t.Fatalf("delete rewrote failed run history: %+v", got)
	}
}

func TestReviewAuthorityPauseDeletePreserveAuthorizedRuns(t *testing.T) {
	r, c := schedRepo(t, "2026-10-20T08:59:00Z")
	a := actor("creator", "p")
	tmpl := agentRepeat(t, r, a, "daily")
	c.Add(time.Minute)
	active := mustAssign(t, r, materializeOne(t, r).ID, a.SessionID)
	if matched, err := r.SetNoticeState(bg, active.NoticeID, NoticeChange{From: []string{NoticePending}, State: NoticeDelivered, Admitted: true}); err != nil || !matched {
		t.Fatalf("admit active run: matched=%v err=%v", matched, err)
	}
	active = mustOcc(t, r, active.ID)
	beforeChild := mustGet(t, r, active.ChildTaskID)
	beforeNotice := mustNotice(t, r, active.NoticeID)
	if _, err := r.AgentPause(bg, a, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	c.Add(24 * time.Hour)
	if got := materialize(t, r); len(got) != 0 {
		t.Fatalf("paused schedule materialized: %+v", got)
	}
	if _, err := r.AgentResume(bg, a, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	c.Add(24*time.Hour + 11*time.Minute)
	late := materializeOne(t, r)
	if late.State != OccLate {
		t.Fatalf("late run = %+v", late)
	}
	if err := r.AgentDeleteSchedule(bg, a, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	if got := mustOcc(t, r, late.ID); got.State != OccSkipped || got.Reason != ReasonScheduleDeleted {
		t.Fatalf("unanswered run survived delete: %+v", got)
	}
	if got := mustOcc(t, r, active.ID); !reflect.DeepEqual(got, active) {
		t.Fatalf("authorized occurrence changed: before=%+v after=%+v", active, got)
	}
	if got := mustGet(t, r, active.ChildTaskID); !reflect.DeepEqual(got, beforeChild) {
		t.Fatalf("authorized child changed: %+v", got)
	}
	if got := mustNotice(t, r, active.NoticeID); !reflect.DeepEqual(got, beforeNotice) {
		t.Fatalf("authorized notice changed: %+v", got)
	}
	if _, err := r.AgentClose(bg, a, active.ChildTaskID, "", "Finished after deleting schedule."); err != nil {
		t.Fatal(err)
	}
	if got := mustOcc(t, r, active.ID); got.State != OccDone {
		t.Fatalf("orphaned run could not complete: %+v", got)
	}
	c.Add(24 * time.Hour)
	if got := materialize(t, r); len(got) != 0 {
		t.Fatalf("deleted schedule fired: %+v", got)
	}
}

func TestReviewAuthorityWithdrawEmptyProjectAndKinds(t *testing.T) {
	r := newRepo(t)
	asker := actor("asker", "")
	req, err := r.AgentAsk(bg, asker, AgentInput{Title: "PRIVATE unknown-project request"})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []Actor{{SessionID: "owner", Owner: true}, {SessionID: "owner", ProjectKey: "p", Owner: true}, {ProjectKey: "", Owner: true}} {
		if _, err := r.AgentWithdraw(bg, a, "owner", req.ID, "not needed"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("empty-project request withdrawn by %+v: %v", a, err)
		}
		view, err := r.AgentList(bg, a)
		if err != nil || len(view.ProjectRequests) != 0 {
			t.Fatalf("empty-project listing = %+v, %v", view.ProjectRequests, err)
		}
	}
	if _, err := r.AgentWithdraw(bg, asker, "", req.ID, "solved"); err != nil {
		t.Fatalf("empty-project requester rejected: %v", err)
	}

	me := actor("me", "p")
	plain, err := r.AgentCreate(bg, me, AgentInput{Title: "checklist"})
	if err != nil {
		t.Fatal(err)
	}
	moa, infra := testOwners()
	handoff := mustSend(t, r, moa, infra, false)
	scheduled := agentRepeat(t, r, me, "scheduled")
	private := note(t, r, "PRIVATE note")
	for _, tc := range []struct {
		id   int64
		a    Actor
		want error
	}{
		{plain.ID, me, ErrInvalid}, {scheduled.ID, me, ErrInvalid}, {handoff.ID, Actor{SessionID: moa.SessionID, ProjectKey: moa.ProjectKey, Owner: true}, ErrInvalid}, {private.ID, me, ErrNotFound}, {999999, me, ErrNotFound},
	} {
		before := mustRev(t, r)
		if _, err := r.AgentWithdraw(bg, tc.a, "Moa", tc.id, "not needed"); !errors.Is(err, tc.want) {
			t.Fatalf("withdraw #%d = %v, want %v", tc.id, err, tc.want)
		}
		if mustRev(t, r) != before {
			t.Fatal("refused withdraw advanced revision")
		}
	}
}

func TestReviewAuthorityWithdrawalKeepsDependencyAndOccurrenceHistory(t *testing.T) {
	r, c := schedRepo(t, "2026-10-20T08:59:00Z")
	asker, worker := actor("asker", "p"), actor("worker", "p")
	owner := actor("root-owner", "p")
	owner.Owner = true
	req, err := r.AgentAsk(bg, asker, AgentInput{Title: "upload reference"})
	if err != nil {
		t.Fatal(err)
	}
	tmpl := agentRepeat(t, r, worker, "dependent schedule")
	if _, err := r.Update(bg, tmpl.ID, tmpl.Revision, Patch{WaitsFor: &[]int64{req.ID}}); err != nil {
		t.Fatal(err)
	}
	c.Add(time.Minute)
	active := mustAssign(t, r, materializeOne(t, r).ID, worker.SessionID)
	child, err := r.AgentGet(bg, worker, active.ChildTaskID)
	if err != nil || child.PrivateBlockers != 1 {
		t.Fatalf("before withdrawal child=%+v err=%v", child, err)
	}
	revision := mustRev(t, r)
	if _, err := r.AgentWithdraw(bg, owner, "Project Owner", req.ID, "found the reference elsewhere"); err != nil {
		t.Fatal(err)
	}
	if mustRev(t, r) != revision+1 {
		t.Fatal("withdrawal was not one committed change")
	}
	got := mustGet(t, r, req.ID)
	if got.Status != StatusDone || got.CompletedAt == 0 || got.ArchivedAt != 0 || got.RequesterSessionID != asker.SessionID || got.CompletionNote != "Withdrawn by Project Owner: found the reference elsewhere" {
		t.Fatalf("withdrawn record=%+v", got)
	}
	if len(got.Unblocks) != 2 || !contains(got.Unblocks, tmpl.ID) || !contains(got.Unblocks, active.ChildTaskID) {
		t.Fatalf("dependency history lost: %+v", got.Unblocks)
	}
	child, err = r.AgentGet(bg, worker, active.ChildTaskID)
	if err != nil || child.PrivateBlockers != 0 || len(child.WaitsFor) != 0 {
		t.Fatalf("completed private dependency still blocks/leaks: %+v %v", child, err)
	}
	if got := mustOcc(t, r, active.ID); !reflect.DeepEqual(got, active) {
		t.Fatalf("withdrawal rewrote occurrence: %+v", got)
	}
	if got := mustGet(t, r, tmpl.ID); got.ScheduleState != "scheduled" || !reflect.DeepEqual(got.WaitsFor, []int64{req.ID}) {
		t.Fatalf("withdrawal cancelled schedule or removed edge: %+v", got)
	}
	c.Add(24 * time.Hour)
	next := mustAssign(t, r, materializeOne(t, r).ID, worker.SessionID)
	if got := mustGet(t, r, next.ChildTaskID); !reflect.DeepEqual(got.WaitsFor, []int64{req.ID}) {
		t.Fatalf("future run dependency lost: %+v", got)
	}
	again := openRepo(t, r.Path())
	if got := mustGet(t, again, req.ID); got.CompletionNote != "Withdrawn by Project Owner: found the reference elsewhere" {
		t.Fatalf("withdrawal lost after reopen: %+v", got)
	}
	c.Add(8 * 24 * time.Hour)
	if got := mustGet(t, r, req.ID); got.ArchivedAt == 0 {
		t.Fatal("withdrawn request did not archive normally")
	}
}

func TestReviewAuthorityExecutionNoticeMatchesCreatorNotDestination(t *testing.T) {
	for _, destination := range []string{"creator", "different-session"} {
		t.Run(destination, func(t *testing.T) {
			r, c := schedRepo(t, "2026-10-20T08:59:00Z")
			tmpl := agentRepeat(t, r, actor("creator", "p"), "scheduled job")
			c.Add(time.Minute)
			o := mustAssign(t, r, materializeOne(t, r).ID, destination)
			text := mustNotice(t, r, o.NoticeID).Text
			if !strings.Contains(text, fmt.Sprintf(`action "done", id %d`, o.ChildTaskID)) || !strings.Contains(text, fmt.Sprintf("Task #%d is the schedule, not this run", tmpl.ID)) {
				t.Fatalf("ambiguous run/template notice: %s", text)
			}
			own := destination == "creator"
			if own != strings.Contains(text, fmt.Sprintf(`action "pause" or "delete", id %d`, tmpl.ID)) || own == strings.Contains(text, "Only the user can pause or delete") {
				t.Fatalf("wrong authority notice: %s", text)
			}
			if _, err := r.AgentPause(bg, actor(destination, "p"), tmpl.ID); own && err != nil || !own && !errors.Is(err, ErrNotFound) {
				t.Fatalf("notice and actual control disagree: %v", err)
			}
		})
	}
}

func TestReviewAuthorityScheduledForCrossProjectIsTargetBound(t *testing.T) {
	r, _ := schedRepo(t, "2026-10-20T08:00:00Z")
	mine := agentRepeat(t, r, actor("creator", "p"), "p job")
	foreign := agentRepeat(t, r, actor("foreign", "q"), "q job")
	def := dailyDef(9, 0, "UTC", "")
	def.Target = sessionTarget("creator")
	crossTarget := mustCreate(t, r, CreateInput{Title: "q targeting p session", ProjectKey: "q", ProjectCWD: "/work/q", Schedule: def})
	def = dailyDef(9, 0, "UTC", "")
	def.Target = Target{Kind: TargetOwner, ID: "owner-p"}
	ownerTarget := mustCreate(t, r, CreateInput{Title: "q targeting p owner", ProjectKey: "q", ProjectCWD: "/work/q", Schedule: def})
	list, err := r.ScheduledFor(bg, "creator", []string{"owner-p"})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[int64]bool{}
	for _, rec := range list {
		ids[rec.ID] = true
	}
	if len(ids) != 3 || !ids[mine.ID] || !ids[crossTarget.ID] || !ids[ownerTarget.ID] || ids[foreign.ID] {
		t.Fatalf("target-bound projection=%v", ids)
	}
	for _, rec := range []Record{crossTarget, ownerTarget} {
		if _, err := r.AgentPause(bg, actor("creator", "p"), rec.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("visibility granted control #%d: %v", rec.ID, err)
		}
	}
	if list, err := r.ScheduledFor(bg, "", []string{"owner-p"}); err != nil || len(list) != 0 {
		t.Fatalf("empty-session projection=%+v %v", list, err)
	}
	if list, err := r.ScheduledInProject(bg, ""); err != nil || len(list) != 0 {
		t.Fatalf("empty-project projection=%+v %v", list, err)
	}
}
