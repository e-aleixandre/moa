package tasks

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func agentRepeat(t *testing.T, r *Repo, a Actor, title string) Record {
	t.Helper()
	rule := daily(9, 0)
	rec, err := r.AgentSchedule(bg, a, AgentInput{Title: title}, When{Kind: WhenRepeat, Rule: &rule}, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestSessionStopsOnlyWhatItScheduled(t *testing.T) {
	r, _ := schedRepo(t, "2026-10-20T08:00:00Z")
	me, other := actor("s1", "p"), actor("s2", "p")
	mine := agentRepeat(t, r, me, "count delivery notes")
	theirs := agentRepeat(t, r, other, "other job")
	users := mkSchedule(t, r, "user job", dailyDef(9, 0, "UTC", ""))
	plain, err := r.AgentCreate(bg, me, AgentInput{Title: "ordinary"})
	if err != nil {
		t.Fatal(err)
	}

	if rec, err := r.AgentPause(bg, me, mine.ID); err != nil || rec.ScheduleState != "paused" {
		t.Fatalf("pause own = %+v, %v", rec, err)
	}
	if rec, err := r.AgentResume(bg, me, mine.ID); err != nil || rec.ScheduleState != "scheduled" || rec.Next == 0 {
		t.Fatalf("resume own = %+v, %v", rec, err)
	}
	for _, id := range []int64{theirs.ID, users.ID} {
		if _, err := r.AgentPause(bg, me, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("paused #%d that is not its own: %v", id, err)
		}
		if err := r.AgentDeleteSchedule(bg, me, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("deleted #%d that is not its own: %v", id, err)
		}
	}
	if _, err := r.AgentPause(bg, me, plain.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("paused an ordinary task: %v", err)
	}
	if err := r.AgentDeleteSchedule(bg, me, plain.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("deleted an ordinary task: %v", err)
	}
	if err := r.AgentDeleteSchedule(bg, me, mine.ID); err != nil {
		t.Fatalf("delete own: %v", err)
	}
	if _, err := r.Get(bg, mine.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("own schedule still there: %v", err)
	}
	if mustGet(t, r, theirs.ID).ScheduleState != "scheduled" || mustGet(t, r, users.ID).ScheduleState != "scheduled" {
		t.Fatal("someone else's schedule changed")
	}
}

func TestScheduleToolPauseResumeDelete(t *testing.T) {
	r := newRepo(t)
	sc, other := scopeFor(r, "s1", "p"), scopeFor(r, "s2", "p")
	out, isErr := run(t, sc, map[string]any{"action": "create", "title": "daily check", "when": "every day at 09:00"})
	if isErr {
		t.Fatal(out)
	}
	id := templates(t, r)[0].ID
	if out, isErr := run(t, other, map[string]any{"action": "pause", "id": float64(id)}); !isErr {
		t.Fatalf("another session paused it: %s", out)
	}
	if out, isErr := run(t, sc, map[string]any{"action": "pause", "id": float64(id)}); isErr || !strings.Contains(out, "Paused") {
		t.Fatalf("pause: %s", out)
	}
	if out, isErr := run(t, sc, map[string]any{"action": "resume", "id": float64(id)}); isErr || !strings.Contains(out, "Resumed") {
		t.Fatalf("resume: %s", out)
	}
	if out, isErr := run(t, sc, map[string]any{"action": "delete", "id": float64(id)}); isErr || !strings.Contains(out, "Deleted") {
		t.Fatalf("delete: %s", out)
	}
	if len(templates(t, r)) != 0 {
		t.Fatal("template still listed")
	}
}

func TestRunNoticeSaysWhatTheSessionCanDoWithTheSchedule(t *testing.T) {
	r, c := schedRepo(t, "2026-10-20T08:59:00Z")
	agentRepeat(t, r, actor("s1", "p"), "own job")
	users := mkSchedule(t, r, "user job", dailyDef(9, 0, "UTC", ""))
	c.Add(2 * time.Minute)
	for _, o := range materialize(t, r) {
		o = mustAssign(t, r, o.ID, "s1")
		text := mustNotice(t, r, o.NoticeID).Text
		if !strings.Contains(text, "is the schedule, not this run") {
			t.Fatalf("notice does not tell the run from the schedule:\n%s", text)
		}
		own := o.ScheduleTaskID != users.ID
		if own != strings.Contains(text, `action "pause" or "delete"`) || own == strings.Contains(text, "Only the user can pause") {
			t.Fatalf("own=%v notice:\n%s", own, text)
		}
	}
}

func TestKeepIsStoredAndSurvivesReopen(t *testing.T) {
	r, _ := schedRepo(t, "2026-10-20T08:00:00Z")
	rec := agentRepeat(t, r, actor("s1", "p"), "daily")
	once := mkSchedule(t, r, "once", onceDef(time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC), ""))
	if _, err := r.Keep(bg, once.ID, once.Revision); !errors.Is(err, ErrInvalid) {
		t.Fatalf("kept a once task: %v", err)
	}
	kept, err := r.Keep(bg, rec.ID, rec.Revision)
	if err != nil || kept.KeptAt == 0 || kept.Revision == rec.Revision {
		t.Fatalf("keep = %+v, %v", kept, err)
	}
	if _, err := r.Keep(bg, rec.ID, rec.Revision); !isConflict(err) {
		t.Fatalf("stale keep: %v", err)
	}
	again := openRepo(t, r.Path())
	list, err := again.List(bg, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if findByTitle(list.Tasks, "daily").KeptAt != kept.KeptAt {
		t.Fatalf("keep lost on reopen: %+v", findByTitle(list.Tasks, "daily"))
	}
}

func TestRunStatsCountRunsAndNeverInventACost(t *testing.T) {
	r, c := schedRepo(t, "2026-10-20T08:59:00Z")
	rec := agentRepeat(t, r, actor("s1", "p"), "daily")
	if got := mustGet(t, r, rec.ID).RunStats; got != nil {
		t.Fatalf("stats before any run = %+v", got)
	}
	for day := 0; day < 3; day++ {
		c.Add(2 * time.Minute)
		o := mustAssign(t, r, materializeOne(t, r).ID, "s1")
		if _, err := r.AgentClose(bg, actor("s1", "p"), o.ChildTaskID, "", "Counted 3 new notes; none failed."); err != nil {
			t.Fatal(err)
		}
		c.Add(24*time.Hour - 2*time.Minute)
	}
	st := mustGet(t, r, rec.ID).RunStats
	if st == nil || st.Ran != 3 || st.FirstAt == 0 || st.Last == nil || st.Last.State != OccDone || st.CostUSD != nil {
		t.Fatalf("stats = %+v", st)
	}
}

func TestOwnerSeesItsProjectsScheduledTasks(t *testing.T) {
	r, _ := schedRepo(t, "2026-10-20T08:00:00Z")
	agentRepeat(t, r, actor("s1", "p"), "in p")
	agentRepeat(t, r, actor("s9", "q"), "in q")
	got, err := r.ScheduledInProject(bg, "p")
	if err != nil || len(got) != 1 || got[0].Title != "in p" {
		t.Fatalf("project p = %+v, %v", got, err)
	}
}

func TestWithdrawRequestAuthority(t *testing.T) {
	r := newRepo(t)
	asker := actor("s1", "p")
	owner := actor("own-p", "p")
	owner.Owner = true
	otherOwner := actor("own-q", "q")
	otherOwner.Owner = true
	sibling := actor("s2", "p")
	ask := func(title string) AgentTask {
		t.Helper()
		req, err := r.AgentAsk(bg, asker, AgentInput{Title: title})
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	a, b := ask("upload the PDFs"), ask("accept the agreement")

	if _, err := r.AgentWithdraw(bg, asker, "", a.ID, " "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("withdraw without a note: %v", err)
	}
	for _, who := range []Actor{otherOwner, sibling} {
		if _, err := r.AgentWithdraw(bg, who, "Other", a.ID, "not needed"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s withdrew it: %v", who.SessionID, err)
		}
	}
	got, err := r.AgentWithdraw(bg, asker, "", a.ID, "found them myself")
	if err != nil || got.Status != StatusDone {
		t.Fatalf("asker withdraw = %+v, %v", got, err)
	}
	view, err := r.AgentList(bg, owner)
	if err != nil || len(view.ProjectRequests) != 2 {
		t.Fatalf("owner list = %+v, %v", view.ProjectRequests, err)
	}
	if v, _ := r.AgentList(bg, otherOwner); len(v.ProjectRequests) != 0 {
		t.Fatalf("another project's owner lists them: %+v", v.ProjectRequests)
	}
	if _, err := r.AgentWithdraw(bg, owner, "Winerim", b.ID, "already accepted"); err != nil {
		t.Fatalf("owner withdraw: %v", err)
	}
	for id, want := range map[int64]string{a.ID: "Withdrawn by the agent that asked: found them myself", b.ID: "Withdrawn by Winerim: already accepted"} {
		rec := mustGet(t, r, id)
		if rec.Status != StatusDone || rec.CompletionNote != want || rec.ArchivedAt != 0 {
			t.Fatalf("#%d = %q %q", id, rec.Status, rec.CompletionNote)
		}
	}
	if _, err := r.AgentWithdraw(bg, owner, "Winerim", b.ID, "again"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("withdrew a done request: %v", err)
	}
	mine, err := r.AgentCreate(bg, owner, AgentInput{Title: "own checklist"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.AgentWithdraw(bg, owner, "Winerim", mine.ID, "x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("withdrew a checklist task: %v", err)
	}
}
