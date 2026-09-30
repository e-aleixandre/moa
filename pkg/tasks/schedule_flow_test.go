package tasks

import (
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"
)

// --- helpers ---------------------------------------------------------------

func runsOf(t *testing.T, r *Repo, parent int64) []Occurrence {
	t.Helper()
	os, err := r.Runs(bg, parent, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for i, j := 0, len(os)-1; i < j; i, j = i+1, j-1 {
		os[i], os[j] = os[j], os[i]
	}
	return os
}

func materialize(t *testing.T, r *Repo) []Occurrence {
	t.Helper()
	os, err := r.MaterializeDue(bg, 100)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	return os
}

func materializeOne(t *testing.T, r *Repo) Occurrence {
	t.Helper()
	os := materialize(t, r)
	if len(os) != 1 {
		t.Fatalf("materialized %d runs, want 1: %+v", len(os), os)
	}
	return os[0]
}

func dest(session string) Destination {
	return Destination{SessionID: session, ProjectKey: "p", ProjectCWD: "/work/p"}
}

func mustAssign(t *testing.T, r *Repo, id int64, session string) Occurrence {
	t.Helper()
	o, err := r.AssignOccurrence(bg, id, dest(session))
	if err != nil {
		t.Fatalf("assign run #%d: %v", id, err)
	}
	if o.State != OccAssigned || o.ChildTaskID == 0 || o.NoticeID == "" {
		t.Fatalf("assign run #%d: %+v", id, o)
	}
	return o
}

func mustOcc(t *testing.T, r *Repo, id int64) Occurrence {
	t.Helper()
	o, err := r.Occurrence(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func mustNotice(t *testing.T, r *Repo, id string) Notice {
	t.Helper()
	n, err := r.Notice(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func mustRev(t *testing.T, r *Repo) int64 {
	t.Helper()
	rev, err := r.Revision(bg)
	if err != nil {
		t.Fatal(err)
	}
	return rev
}

func cursorOf(t *testing.T, r *Repo, id int64) int64 { t.Helper(); return mustGet(t, r, id).Next }

// sqlTrigger installs an aborting trigger through another connection and
// returns a function that removes it.
func sqlTrigger(t *testing.T, r *Repo, name, def string) func() {
	t.Helper()
	db, err := sql.Open("sqlite", dsn(r.Path(), true))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec("CREATE TRIGGER " + name + " " + def + " BEGIN SELECT RAISE(ABORT, 'refused by test'); END"); err != nil {
		t.Fatal(err)
	}
	return func() {
		if _, err := db.Exec("DROP TRIGGER " + name); err != nil {
			t.Fatal(err)
		}
	}
}

func isConflict(err error) bool {
	var c *ConflictError
	var oc *OccurrenceConflictError
	return errors.As(err, &c) || errors.As(err, &oc)
}

// onceAssigned makes a once template due at now+1m, fires it on time and
// assigns its run to s1. deliver is the saved policy of its notice.
func onceAssigned(t *testing.T, r *Repo, c *fakeClock, saved string) (Record, Occurrence) {
	t.Helper()
	def := onceDef(c.Now().Add(time.Minute), "")
	def.Delivery.Saved = saved
	tmpl := mkSchedule(t, r, "once job", def)
	c.Add(time.Minute)
	o := materializeOne(t, r)
	if o.State != OccReady {
		t.Fatalf("on-time run = %+v", o)
	}
	return tmpl, mustAssign(t, r, o.ID, "s1")
}

// --- identity ----------------------------------------------------------------

func TestScheduleTemplateVisibility(t *testing.T) {
	r, c := schedRepo(t, "2026-09-30T12:00:00Z")
	me, other := actor("s1", "p"), actor("s2", "p")
	tmpl, err := r.AgentSchedule(bg, me, AgentInput{Title: "check CI", Description: "look at it"},
		When{Kind: WhenOnce, At: c.Now().Add(time.Hour).UnixMilli()}, "Europe/Madrid")
	if err != nil {
		t.Fatalf("agent schedule: %v", err)
	}
	if tmpl.Place != PlaceYou || tmpl.RequesterSessionID != "" || tmpl.AssigneeSessionID != "" ||
		tmpl.CreatedBySessionID != "s1" || tmpl.Target == nil || tmpl.Target.Kind != TargetSession || tmpl.Target.ID != "s1" ||
		tmpl.ProjectKey != "p" || tmpl.TZ != "Europe/Madrid" || tmpl.ScheduleState != "scheduled" {
		t.Fatalf("template = %+v", tmpl)
	}
	view, err := r.AgentList(bg, me)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Scheduled) != 1 || view.Scheduled[0].ID != tmpl.ID || view.Scheduled[0].Next == 0 ||
		len(view.Checklist) != 0 || len(view.Requests) != 0 {
		t.Fatalf("creator view = %+v", view)
	}
	if _, err := r.AgentGet(bg, me, tmpl.ID); err != nil {
		t.Fatalf("creator cannot read its template: %v", err)
	}
	if _, err := r.AgentUpdate(bg, me, tmpl.ID, AgentPatch{Title: ptr("renamed")}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("agent edited a template: %v", err)
	}
	if _, err := r.AgentDone(bg, me, tmpl.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("agent completed a template: %v", err)
	}
	ov, err := r.AgentList(bg, other)
	if err != nil {
		t.Fatal(err)
	}
	if len(ov.Scheduled)+len(ov.Checklist)+len(ov.Requests)+len(ov.Backlog) != 0 {
		t.Fatalf("stranger sees %+v", ov)
	}
	if _, err := r.AgentGet(bg, other, tmpl.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stranger get = %v", err)
	}
	list, err := r.List(bg, Filter{IncludeAgents: true})
	if err != nil {
		t.Fatal(err)
	}
	if list.Counts.OpenRequests != 0 || list.Counts.Attention != 0 {
		t.Fatalf("template counted as request: %+v", list.Counts)
	}
	if cl, _ := r.Checklist(bg, "s1"); len(cl) != 0 {
		t.Fatalf("template on checklist: %+v", cl)
	}
	if ns := notices(t, r, tmpl.ID); len(ns) != 0 {
		t.Fatalf("template created notices: %+v", ns)
	}
	// Only its run, once assigned, reaches the checklist and the outbox.
	c.Add(time.Hour)
	o := mustAssign(t, r, materializeOne(t, r).ID, "s1")
	if cl, _ := r.Checklist(bg, "s1"); len(cl) != 1 || int64(cl[0].ID) != o.ChildTaskID {
		t.Fatalf("run not on checklist: %+v", cl)
	}
	child := mustGet(t, r, o.ChildTaskID)
	if child.ParentTaskID != tmpl.ID || child.OccurrenceID != o.ID {
		t.Fatalf("child links = %+v", child)
	}
	// Scheduling needs a real session identity and a real zone.
	if _, err := r.AgentSchedule(bg, Actor{}, AgentInput{Title: "x"}, When{Kind: WhenOnce, At: c.Now().Add(time.Hour).UnixMilli()}, "UTC"); err == nil {
		t.Fatal("scheduled without identity")
	}
	if _, err := r.AgentSchedule(bg, me, AgentInput{Title: "x"}, When{Kind: WhenOnce, At: c.Now().Add(time.Hour).UnixMilli()}, "Local"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Local zone accepted: %v", err)
	}
}

// --- T0 ---------------------------------------------------------------------

func TestScheduleFirstObservationLateBoundary(t *testing.T) {
	for _, c := range []struct {
		policy string
		after  time.Duration
		want   string
	}{
		{LateAsk, LateAfter - time.Millisecond, OccReady},
		{LateAsk, LateAfter, OccLate},
		{LateRun, LateAfter - time.Millisecond, OccReady},
		{LateRun, LateAfter, OccReady},
		{LateSkip, LateAfter - time.Millisecond, OccReady},
		{LateSkip, LateAfter, OccSkipped},
	} {
		t.Run(c.policy+"/"+c.after.String(), func(t *testing.T) {
			r, clock := schedRepo(t, "2026-09-30T12:00:00Z")
			due := clock.Now().Add(time.Hour)
			tmpl := mkSchedule(t, r, "job", onceDef(due, c.policy))
			clock.Set(due.Add(c.after))
			o := materializeOne(t, r)
			if o.State != c.want || o.DueAt != due.UnixMilli() || o.ObservedAt != clock.Now().UnixMilli() {
				t.Fatalf("run = %+v, want %s", o, c.want)
			}
			if c.want != OccReady && (o.ChildTaskID != 0 || o.NoticeID != "" ||
				countRows(t, r, "task_notifications", "1=1") != 0 || countRows(t, r, "tasks", "place = 'agent'") != 0) {
				t.Fatalf("%s run created a child or notice", c.want)
			}
			p := mustGet(t, r, tmpl.ID)
			if p.Next != 0 {
				t.Fatalf("once cursor not consumed: %+v", p)
			}
			if c.want == OccSkipped && (p.Status != StatusDone || p.CompletionNote != "Skipped") {
				t.Fatalf("skipped once template = %+v", p)
			}
			if c.want == OccLate && (p.Status != StatusPending || p.ScheduleState != OccLate || p.LateCount != 1) {
				t.Fatalf("late once template = %+v", p)
			}
			if again := materialize(t, r); len(again) != 0 {
				t.Fatalf("second pass consumed again: %+v", again)
			}
		})
	}
}

func TestScheduleMaterializeRollback(t *testing.T) {
	for _, c := range []struct{ name, trigger string }{
		{"occurrence insert", "BEFORE INSERT ON task_occurrences"},
		{"cursor update", "BEFORE UPDATE OF next_due_at ON task_schedules"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
			tmpl := mkSchedule(t, r, "daily", dailyDef(9, 0, "UTC", ""))
			clock.Set(utc("2026-09-30T09:00:00Z"))
			before, rev := mustGet(t, r, tmpl.ID), mustRev(t, r)
			drop := sqlTrigger(t, r, "refuse", c.trigger)
			if _, err := r.MaterializeDue(bg, 10); err == nil {
				t.Fatal("materialize succeeded despite the refused write")
			}
			after := mustGet(t, r, tmpl.ID)
			if after.Next != before.Next || after.Revision != before.Revision || mustRev(t, r) != rev ||
				countRows(t, r, "task_occurrences", "1=1") != 0 {
				t.Fatalf("partial T0 committed: before %+v after %+v", before, after)
			}
			drop()
			o := materializeOne(t, r)
			if o.DueAt != utc("2026-09-30T09:00:00Z").UnixMilli() || cursorOf(t, r, tmpl.ID) != utc("2026-10-01T09:00:00Z").UnixMilli() {
				t.Fatalf("retry: %+v cursor %d", o, cursorOf(t, r, tmpl.ID))
			}
			if countRows(t, r, "task_occurrences", "1=1") != 1 {
				t.Fatal("retry created more than one run")
			}
		})
	}
}

func TestScheduleMaterializeConcurrentConnections(t *testing.T) {
	for round := 0; round < 5; round++ {
		r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
		other := openRepo(t, r.Path())
		other.SetClock(clock.Now)
		tmpl := mkSchedule(t, r, "daily", dailyDef(9, 0, "UTC", ""))
		clock.Set(utc("2026-09-30T09:01:00Z"))
		start := make(chan struct{})
		var wg sync.WaitGroup
		results := make([][]Occurrence, 4)
		errs := make([]error, 4)
		for i, repo := range []*Repo{r, other, r, other} {
			wg.Add(1)
			go func(i int, repo *Repo) {
				defer wg.Done()
				<-start
				results[i], errs[i] = repo.MaterializeDue(bg, 10)
			}(i, repo)
		}
		close(start)
		wg.Wait()
		total := 0
		for i := range results {
			if errs[i] != nil {
				t.Fatalf("writer %d: %v", i, errs[i])
			}
			total += len(results[i])
		}
		if total != 1 || countRows(t, r, "task_occurrences", "1=1") != 1 ||
			cursorOf(t, r, tmpl.ID) != utc("2026-10-01T09:00:00Z").UnixMilli() {
			t.Fatalf("round %d: %d runs reported, %d stored", round, total, countRows(t, r, "task_occurrences", "1=1"))
		}
	}
}

func TestScheduleCatchUpCoalesces(t *testing.T) {
	r, clock := schedRepo(t, "2026-09-26T10:00:00Z")
	tmpl := mkSchedule(t, r, "daily", dailyDef(9, 0, "UTC", ""))
	if cursorOf(t, r, tmpl.ID) != utc("2026-09-27T09:00:00Z").UnixMilli() {
		t.Fatalf("cursor = %d", cursorOf(t, r, tmpl.ID))
	}
	clock.Set(utc("2026-09-30T09:20:00Z"))
	o := materializeOne(t, r)
	if o.DueAt != utc("2026-09-30T09:00:00Z").UnixMilli() || o.State != OccLate || o.MissedCount != 3 {
		t.Fatalf("coalesced run = %+v", o)
	}
	if cursorOf(t, r, tmpl.ID) != utc("2026-10-01T09:00:00Z").UnixMilli() {
		t.Fatalf("next = %s", time.UnixMilli(cursorOf(t, r, tmpl.ID)).UTC())
	}
	// A second pass, and a restarted process, change nothing.
	materialize(t, r)
	fresh := openRepo(t, r.Path())
	fresh.SetClock(clock.Now)
	if got := materialize(t, fresh); len(got) != 0 || len(runsOf(t, fresh, tmpl.ID)) != 1 {
		t.Fatalf("restart consumed again: %+v", got)
	}
}

// --- controls vs T0 ------------------------------------------------------------

func TestScheduleFireVsEdit(t *testing.T) {
	newRule := daily(10, 0)
	edited := func(tmpl Record) *ScheduleDef {
		return &ScheduleDef{When: When{Kind: WhenRepeat, Rule: &newRule}, TZ: "UTC", Target: sessionTarget("s2")}
	}
	t.Run("edit first", func(t *testing.T) {
		r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
		other := openRepo(t, r.Path())
		other.SetClock(clock.Now)
		tmpl := mkSchedule(t, r, "daily", dailyDef(9, 0, "UTC", ""))
		// The timer read the old definition; the owner's edit commits before
		// its write.
		r.hookAfterPlan = func() {
			r.hookAfterPlan = nil
			if _, err := other.Update(bg, tmpl.ID, tmpl.Revision, Patch{Schedule: edited(tmpl), Description: ptr("new text")}); err != nil {
				t.Errorf("edit: %v", err)
			}
		}
		clock.Set(utc("2026-09-30T09:00:00Z"))
		if got := materialize(t, r); len(got) != 0 {
			t.Fatalf("stale T0 fired the old slot: %+v", got)
		}
		if cursorOf(t, r, tmpl.ID) != utc("2026-09-30T10:00:00Z").UnixMilli() {
			t.Fatalf("cursor = %s", time.UnixMilli(cursorOf(t, r, tmpl.ID)).UTC())
		}
		clock.Set(utc("2026-09-30T10:00:00Z"))
		o := materializeOne(t, r)
		if o.Spec.Description != "new text" || o.Spec.Target.ID != "s2" || o.Spec.When.Rule.H != 10 {
			t.Fatalf("new slot uses old definition: %+v", o.Spec)
		}
	})
	t.Run("fire first", func(t *testing.T) {
		r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
		other := openRepo(t, r.Path())
		other.SetClock(clock.Now)
		tmpl := mkSchedule(t, r, "daily", dailyDef(9, 0, "UTC", ""))
		clock.Set(utc("2026-09-30T09:00:00Z"))
		o := materializeOne(t, r)
		_, err := other.Update(bg, tmpl.ID, tmpl.Revision, Patch{Schedule: edited(tmpl), Description: ptr("new text")})
		if !isConflict(err) {
			t.Fatalf("stale edit after fire = %v, want conflict", err)
		}
		cur := mustGet(t, other, tmpl.ID)
		if _, err := other.Update(bg, tmpl.ID, cur.Revision, Patch{Schedule: edited(tmpl), Description: ptr("new text")}); err != nil {
			t.Fatal(err)
		}
		if got := mustOcc(t, r, o.ID); got.Spec.Description != "do daily" || got.Spec.Target.ID != "s1" || got.State != OccReady {
			t.Fatalf("authorized run rewritten: %+v", got)
		}
		if cursorOf(t, r, tmpl.ID) != utc("2026-09-30T10:00:00Z").UnixMilli() {
			t.Fatalf("cursor after edit = %s", time.UnixMilli(cursorOf(t, r, tmpl.ID)).UTC())
		}
	})
}

func TestScheduleFireVsPause(t *testing.T) {
	t.Run("pause first", func(t *testing.T) {
		r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
		other := openRepo(t, r.Path())
		other.SetClock(clock.Now)
		tmpl := mkSchedule(t, r, "daily", dailyDef(9, 0, "UTC", ""))
		r.hookAfterPlan = func() {
			r.hookAfterPlan = nil
			if _, err := other.Pause(bg, tmpl.ID, tmpl.Revision); err != nil {
				t.Errorf("pause: %v", err)
			}
		}
		clock.Set(utc("2026-09-30T09:00:00Z"))
		if got := materialize(t, r); len(got) != 0 {
			t.Fatalf("paused template fired: %+v", got)
		}
		if p := mustGet(t, r, tmpl.ID); p.ScheduleState != "paused" {
			t.Fatalf("state = %q", p.ScheduleState)
		}
		if _, err := r.RunNow(bg, tmpl.ID, mustGet(t, r, tmpl.ID).Revision, utc("2026-09-30T09:00:00Z").UnixMilli()); !errors.Is(err, ErrInvalid) {
			t.Fatalf("run now on paused = %v", err)
		}
	})
	t.Run("fire first", func(t *testing.T) {
		r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
		other := openRepo(t, r.Path())
		other.SetClock(clock.Now)
		tmpl := mkSchedule(t, r, "daily", dailyDef(9, 0, "UTC", ""))
		clock.Set(utc("2026-09-30T09:00:00Z"))
		o := materializeOne(t, r)
		if _, err := other.Pause(bg, tmpl.ID, tmpl.Revision); !isConflict(err) {
			t.Fatalf("stale pause = %v", err)
		}
		if _, err := other.Pause(bg, tmpl.ID, mustGet(t, r, tmpl.ID).Revision); err != nil {
			t.Fatal(err)
		}
		if got := mustAssign(t, r, o.ID, "s1"); got.ID != o.ID {
			t.Fatal("authorized run lost to pause")
		}
		clock.Set(utc("2026-10-01T09:00:00Z"))
		if got := materialize(t, r); len(got) != 0 {
			t.Fatalf("paused template fired: %+v", got)
		}
	})
}

func TestScheduleFireVsDelete(t *testing.T) {
	t.Run("delete first", func(t *testing.T) {
		r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
		other := openRepo(t, r.Path())
		other.SetClock(clock.Now)
		tmpl := mkSchedule(t, r, "daily", dailyDef(9, 0, "UTC", ""))
		r.hookAfterPlan = func() {
			r.hookAfterPlan = nil
			if err := other.Delete(bg, tmpl.ID, tmpl.Revision, ""); err != nil {
				t.Errorf("delete: %v", err)
			}
		}
		clock.Set(utc("2026-09-30T09:00:00Z"))
		if got := materialize(t, r); len(got) != 0 || countRows(t, r, "task_occurrences", "1=1") != 0 {
			t.Fatalf("deleted template fired: %+v", got)
		}
	})
	t.Run("fire first", func(t *testing.T) {
		r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
		tmpl := mkSchedule(t, r, "daily", dailyDef(9, 0, "UTC", ""))
		clock.Set(utc("2026-09-30T09:00:00Z"))
		o := materializeOne(t, r)
		if err := r.Delete(bg, tmpl.ID, tmpl.Revision, ""); !isConflict(err) {
			t.Fatalf("stale delete = %v", err)
		}
		if err := r.Delete(bg, tmpl.ID, mustGet(t, r, tmpl.ID).Revision, ""); err != nil {
			t.Fatal(err)
		}
		got := mustAssign(t, r, o.ID, "s1")
		if child := mustGet(t, r, got.ChildTaskID); child.Title != "daily" || child.ParentTaskID != tmpl.ID {
			t.Fatalf("run after template deletion = %+v", child)
		}
	})
}

func TestScheduleFireVsSkip(t *testing.T) {
	d0, d1 := utc("2026-09-30T09:00:00Z").UnixMilli(), utc("2026-10-01T09:00:00Z").UnixMilli()
	t.Run("skip first", func(t *testing.T) {
		r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
		other := openRepo(t, r.Path())
		other.SetClock(clock.Now)
		tmpl := mkSchedule(t, r, "daily", dailyDef(9, 0, "UTC", ""))
		r.hookAfterPlan = func() {
			r.hookAfterPlan = nil
			if _, err := other.SkipNext(bg, tmpl.ID, tmpl.Revision, d0); err != nil {
				t.Errorf("skip: %v", err)
			}
		}
		clock.Set(utc("2026-09-30T09:00:00Z"))
		if got := materialize(t, r); len(got) != 0 {
			t.Fatalf("skipped slot fired: %+v", got)
		}
		runs := runsOf(t, r, tmpl.ID)
		if len(runs) != 1 || runs[0].DueAt != d0 || runs[0].State != OccSkipped || runs[0].Trigger != TriggerSkipNext ||
			cursorOf(t, r, tmpl.ID) != d1 {
			t.Fatalf("runs = %+v cursor %d", runs, cursorOf(t, r, tmpl.ID))
		}
	})
	t.Run("fire first", func(t *testing.T) {
		r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
		tmpl := mkSchedule(t, r, "daily", dailyDef(9, 0, "UTC", ""))
		clock.Set(utc("2026-09-30T09:00:00Z"))
		materializeOne(t, r)
		if _, err := r.SkipNext(bg, tmpl.ID, tmpl.Revision, d0); !isConflict(err) {
			t.Fatalf("stale skip = %v", err)
		}
		// Even with a fresh revision, the old due time does not reach the
		// following slot.
		if _, err := r.SkipNext(bg, tmpl.ID, mustGet(t, r, tmpl.ID).Revision, d0); !isConflict(err) {
			t.Fatalf("skip of a consumed slot = %v", err)
		}
		runs := runsOf(t, r, tmpl.ID)
		if len(runs) != 1 || runs[0].State != OccReady || cursorOf(t, r, tmpl.ID) != d1 {
			t.Fatalf("runs = %+v", runs)
		}
	})
}

func TestScheduleEditFromNext(t *testing.T) {
	r, clock := schedRepo(t, "2026-09-26T08:00:00Z")
	def := dailyDef(9, 0, "UTC", "")
	def.Delivery.Saved = DeliverHold
	tmpl := mkSchedule(t, r, "daily", def)
	// Sep 26 on time → ready, then assigned with a held notice.
	clock.Set(utc("2026-09-26T09:00:00Z"))
	held := mustAssign(t, r, materializeOne(t, r).ID, "s1")
	// Sep 27 on time → ready (left unassigned).
	clock.Set(utc("2026-09-27T09:00:00Z"))
	ready := materializeOne(t, r)
	// Sep 28 observed late → late.
	clock.Set(utc("2026-09-28T09:30:00Z"))
	late := materializeOne(t, r)
	if held.State != OccAssigned || ready.State != OccReady || late.State != OccLate ||
		mustNotice(t, r, held.NoticeID).Deliver != DeliverHold {
		t.Fatalf("setup: %+v %+v %+v", held, ready, late)
	}
	cur := mustGet(t, r, tmpl.ID)
	rule := daily(18, 30)
	newDef := &ScheduleDef{When: When{Kind: WhenRepeat, Rule: &rule}, TZ: "Europe/Madrid", Target: sessionTarget("s9"),
		Delivery: Delivery{Late: LateRun}}
	if _, err := r.Update(bg, tmpl.ID, cur.Revision, Patch{Description: ptr("edited"), Title: ptr("renamed"), Schedule: newDef}); err != nil {
		t.Fatal(err)
	}
	for _, o := range []Occurrence{held, ready, late} {
		got := mustOcc(t, r, o.ID)
		if got.Spec.Description != "do daily" || got.Spec.Title != "daily" || got.Spec.Target.ID != "s1" ||
			got.Spec.When.Rule.H != 9 || got.Spec.TZ != "UTC" || got.Spec.Delivery.Late != LateAsk || got.State != o.State {
			t.Fatalf("edit reached run #%d: %+v", o.ID, got)
		}
	}
	if child := mustGet(t, r, held.ChildTaskID); child.Description != "do daily" {
		t.Fatalf("edit reached the child: %+v", child)
	}
	// 18:30 Madrid on Sep 28 is 16:30Z, after the edit's now.
	want := utc("2026-09-28T16:30:00Z").UnixMilli()
	if cursorOf(t, r, tmpl.ID) != want {
		t.Fatalf("cursor = %s", time.UnixMilli(cursorOf(t, r, tmpl.ID)).UTC())
	}
	clock.Set(utc("2026-09-28T16:30:00Z"))
	next := materializeOne(t, r)
	if next.Spec.Description != "edited" || next.Spec.Title != "renamed" || next.Spec.Target.ID != "s9" ||
		next.Spec.TZ != "Europe/Madrid" || next.Spec.Delivery.Late != LateRun || next.DueAt != want {
		t.Fatalf("next run = %+v", next.Spec)
	}
	// Recurrence cannot become a once history.
	cur = mustGet(t, r, tmpl.ID)
	onceNow := onceDef(clock.Now().Add(time.Hour), "")
	if _, err := r.Update(bg, tmpl.ID, cur.Revision, Patch{Schedule: onceNow}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("repeat→once after runs = %v", err)
	}
}

func TestScheduleResumeIgnoresPausedDates(t *testing.T) {
	r, clock := schedRepo(t, "2026-09-29T09:30:00Z")
	tmpl := mkSchedule(t, r, "daily", dailyDef(9, 0, "UTC", ""))
	clock.Set(utc("2026-09-30T09:15:00Z"))
	late := materializeOne(t, r)
	if late.State != OccLate {
		t.Fatalf("setup run = %+v", late)
	}
	if _, err := r.Pause(bg, tmpl.ID, mustGet(t, r, tmpl.ID).Revision); err != nil {
		t.Fatal(err)
	}
	clock.Set(utc("2026-10-04T10:00:00Z"))
	fresh := openRepo(t, r.Path())
	fresh.SetClock(clock.Now)
	if got := materialize(t, fresh); len(got) != 0 {
		t.Fatalf("paused template fired: %+v", got)
	}
	p := mustGet(t, fresh, tmpl.ID)
	if p.ScheduleState != "paused" || p.LateCount != 1 {
		t.Fatalf("paused = %+v", p)
	}
	if _, err := fresh.Resume(bg, tmpl.ID, p.Revision); err != nil {
		t.Fatal(err)
	}
	if got := cursorOf(t, fresh, tmpl.ID); got != utc("2026-10-05T09:00:00Z").UnixMilli() {
		t.Fatalf("resume cursor = %s", time.UnixMilli(got).UTC())
	}
	if got := materialize(t, fresh); len(got) != 0 {
		t.Fatalf("resume caught up paused dates: %+v", got)
	}
	if got := mustOcc(t, fresh, late.ID); got.State != OccLate {
		t.Fatalf("late decision lost: %+v", got)
	}
}

func TestScheduleRunNowConsumesNextSlot(t *testing.T) {
	t.Run("repeat", func(t *testing.T) {
		r, clock := schedRepo(t, "2026-09-30T06:00:00Z")
		tmpl := mkSchedule(t, r, "daily", dailyDef(9, 0, "UTC", ""))
		due := utc("2026-09-30T09:00:00Z").UnixMilli()
		o, err := r.RunNow(bg, tmpl.ID, tmpl.Revision, due)
		if err != nil {
			t.Fatal(err)
		}
		if o.DueAt != due || o.State != OccReady || o.Trigger != TriggerRunNow ||
			cursorOf(t, r, tmpl.ID) != utc("2026-10-01T09:00:00Z").UnixMilli() {
			t.Fatalf("run now = %+v cursor %d", o, cursorOf(t, r, tmpl.ID))
		}
		mustAssign(t, r, o.ID, "s1")
		// Retries: with the original revision, or the fresh revision and the
		// original due time, never consume Oct 1.
		if _, err := r.RunNow(bg, tmpl.ID, tmpl.Revision, due); !isConflict(err) {
			t.Fatalf("retry = %v", err)
		}
		if _, err := r.RunNow(bg, tmpl.ID, mustGet(t, r, tmpl.ID).Revision, due); !isConflict(err) {
			t.Fatalf("retry with fresh revision = %v", err)
		}
		clock.Set(utc("2026-09-30T09:30:00Z"))
		if got := materialize(t, r); len(got) != 0 {
			t.Fatalf("slot fired twice: %+v", got)
		}
		if len(runsOf(t, r, tmpl.ID)) != 1 || countRows(t, r, "tasks", "place = 'agent'") != 1 {
			t.Fatal("extra run or child")
		}
	})
	t.Run("once", func(t *testing.T) {
		r, _ := schedRepo(t, "2026-09-30T06:00:00Z")
		due := utc("2026-10-02T09:00:00Z")
		tmpl := mkSchedule(t, r, "once", onceDef(due, ""))
		o, err := r.RunNow(bg, tmpl.ID, tmpl.Revision, due.UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
		if o.DueAt != due.UnixMilli() || cursorOf(t, r, tmpl.ID) != 0 {
			t.Fatalf("once run now = %+v", o)
		}
		if _, err := r.RunNow(bg, tmpl.ID, tmpl.Revision, due.UnixMilli()); !isConflict(err) {
			t.Fatalf("retry = %v", err)
		}
		if len(runsOf(t, r, tmpl.ID)) != 1 {
			t.Fatal("extra run")
		}
	})
}

func TestScheduleConfirmRunVsSkip(t *testing.T) {
	for round := 0; round < 6; round++ {
		r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
		other := openRepo(t, r.Path())
		other.SetClock(clock.Now)
		tmpl := mkSchedule(t, r, "once", onceDef(clock.Now().Add(time.Hour), ""))
		clock.Add(2 * time.Hour)
		late := materializeOne(t, r)
		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := map[string]error{}
		var mu sync.Mutex
		for _, c := range []struct {
			repo   *Repo
			action string
		}{{r, LateRun}, {other, LateSkip}} {
			wg.Add(1)
			go func(repo *Repo, action string) {
				defer wg.Done()
				<-start
				_, err := repo.ConfirmOccurrence(bg, late.ID, late.Revision, action)
				mu.Lock()
				errs[action] = err
				mu.Unlock()
			}(c.repo, c.action)
		}
		close(start)
		wg.Wait()
		runWon := errs[LateRun] == nil
		if runWon == (errs[LateSkip] == nil) || !isConflict(errs[LateRun]) && !isConflict(errs[LateSkip]) {
			t.Fatalf("round %d: run=%v skip=%v", round, errs[LateRun], errs[LateSkip])
		}
		got := mustOcc(t, r, late.ID)
		_, assignErr := r.AssignOccurrence(bg, late.ID, dest("s1"))
		_, _ = r.AssignOccurrence(bg, late.ID, dest("s1"))
		children := countRows(t, r, "tasks", "place = 'agent'")
		noticesN := countRows(t, r, "task_notifications", "1=1")
		if runWon {
			if got.State != OccReady || got.ConfirmedAt == 0 || assignErr != nil || children != 1 || noticesN != 1 {
				t.Fatalf("run won: %+v children %d notices %d", got, children, noticesN)
			}
		} else {
			if got.State != OccSkipped || !isConflict(assignErr) || children != 0 || noticesN != 0 ||
				mustGet(t, r, tmpl.ID).Status != StatusDone {
				t.Fatalf("skip won: %+v children %d", got, children)
			}
		}
		if list, _ := r.List(bg, Filter{}); list.Counts.LateOccurrences != 0 {
			t.Fatalf("decision still counted: %+v", list.Counts)
		}
	}
}

// --- T1 ---------------------------------------------------------------------

func TestScheduleAssignmentOutboxRollback(t *testing.T) {
	r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
	tmpl := mkSchedule(t, r, "once", onceDef(clock.Now().Add(time.Minute), ""))
	clock.Add(time.Minute)
	o := materializeOne(t, r)
	rev, parentRev := mustRev(t, r), mustGet(t, r, tmpl.ID).Revision
	drop := sqlTrigger(t, r, "refuse_notice", "BEFORE INSERT ON task_notifications")
	if _, err := r.AssignOccurrence(bg, o.ID, dest("s1")); err == nil {
		t.Fatal("assignment committed without its notice")
	}
	if got := mustOcc(t, r, o.ID); got.State != OccReady || got.ChildTaskID != 0 || got.NoticeID != "" || got.SessionID != "" ||
		got.Revision != o.Revision {
		t.Fatalf("run after failed T1 = %+v", got)
	}
	if countRows(t, r, "tasks", "place = 'agent'") != 0 || mustRev(t, r) != rev || mustGet(t, r, tmpl.ID).Revision != parentRev {
		t.Fatal("failed T1 left a child or bumped revisions")
	}
	drop()
	a := mustAssign(t, r, o.ID, "s1")
	again, err := r.AssignOccurrence(bg, o.ID, dest("s2"))
	if err != nil || again.ChildTaskID != a.ChildTaskID || again.NoticeID != a.NoticeID || again.SessionID != "s1" {
		t.Fatalf("retry after success = %+v, %v", again, err)
	}
	n := mustNotice(t, r, a.NoticeID)
	child := mustGet(t, r, a.ChildTaskID)
	if n.Kind != NoticeAssigned || n.RecipientSessionID != "s1" || n.TaskID != child.ID || n.Deliver != DeliverWake ||
		child.AssigneeSessionID != "s1" || child.Place != PlaceAgent || len(child.Subtasks) != 1 || child.Subtasks[0].Done {
		t.Fatalf("notice %+v child %+v", n, child)
	}
	if countRows(t, r, "tasks", "place = 'agent'") != 1 || countRows(t, r, "task_notifications", "1=1") != 1 {
		t.Fatal("more than one child or notice")
	}
}

// --- child hooks ----------------------------------------------------------------

// completePaths completes a run's child through each status path.
var completePaths = map[string]func(r *Repo, child Record) error{
	"agent done": func(r *Repo, child Record) error {
		_, err := r.AgentDone(bg, actor(child.AssigneeSessionID, child.ProjectKey), child.ID)
		return err
	},
	"owner update": func(r *Repo, child Record) error {
		_, err := r.Update(bg, child.ID, child.Revision, Patch{Status: ptr(StatusDone), CompletionNote: ptr("all good")})
		return err
	},
	"session /tasks": func(r *Repo, child Record) error {
		return r.CompleteForSession(bg, child.AssigneeSessionID, child.ID)
	},
}

func TestScheduleChildCompletionAtomic(t *testing.T) {
	for name, complete := range completePaths {
		t.Run(name+"/once", func(t *testing.T) {
			r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
			tmpl, o := onceAssigned(t, r, clock, DeliverWake)
			child := mustGet(t, r, o.ChildTaskID)
			rev, parent := mustRev(t, r), mustGet(t, r, tmpl.ID)
			drop := sqlTrigger(t, r, "refuse_run", "BEFORE UPDATE ON task_occurrences")
			if err := complete(r, child); err == nil {
				t.Fatal("completion committed without its run")
			}
			if got := mustGet(t, r, child.ID); got.Status == StatusDone || got.Revision != child.Revision {
				t.Fatalf("child changed: %+v", got)
			}
			if got := mustGet(t, r, tmpl.ID); got.Status != parent.Status || got.Revision != parent.Revision {
				t.Fatalf("parent changed: %+v", got)
			}
			if got := mustOcc(t, r, o.ID); got.State != OccAssigned || mustRev(t, r) != rev {
				t.Fatalf("run changed: %+v", got)
			}
			drop()
			if err := complete(r, child); err != nil {
				t.Fatal(err)
			}
			done := mustGet(t, r, child.ID)
			got := mustOcc(t, r, o.ID)
			p := mustGet(t, r, tmpl.ID)
			if got.State != OccDone || got.CompletedAt != done.CompletedAt || got.Note != done.CompletionNote ||
				p.Status != StatusDone || p.CompletionNote != done.CompletionNote {
				t.Fatalf("after completion: run %+v parent %+v", got, p)
			}
			// Done again is a no-op everywhere.
			rev = mustRev(t, r)
			_ = r.CompleteForSession(bg, "s1", child.ID)
			if _, err := r.AgentDone(bg, actor("s1", "p"), child.ID); err != nil {
				t.Fatal(err)
			}
			if mustRev(t, r) != rev || mustOcc(t, r, o.ID).Revision != got.Revision {
				t.Fatal("repeated done wrote")
			}
		})
		t.Run(name+"/repeat", func(t *testing.T) {
			r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
			tmpl := mkSchedule(t, r, "daily", dailyDef(9, 0, "UTC", ""))
			clock.Set(utc("2026-09-30T09:00:00Z"))
			o := mustAssign(t, r, materializeOne(t, r).ID, "s1")
			cursor := cursorOf(t, r, tmpl.ID)
			if err := complete(r, mustGet(t, r, o.ChildTaskID)); err != nil {
				t.Fatal(err)
			}
			p := mustGet(t, r, tmpl.ID)
			if mustOcc(t, r, o.ID).State != OccDone || p.Status != StatusPending || p.Next != cursor || p.ScheduleState != "scheduled" {
				t.Fatalf("repeat parent after run done = %+v", p)
			}
		})
	}
	// A completion-note-only edit follows to the run.
	r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
	_, o := onceAssigned(t, r, clock, DeliverWake)
	child := mustGet(t, r, o.ChildTaskID)
	child, err := r.Update(bg, child.ID, child.Revision, Patch{Status: ptr(StatusDone)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Update(bg, child.ID, child.Revision, Patch{CompletionNote: ptr("later note")}); err != nil {
		t.Fatal(err)
	}
	if got := mustOcc(t, r, o.ID); got.Note != "later note" || got.State != OccDone {
		t.Fatalf("note not followed: %+v", got)
	}
}

func TestScheduleChildReopenDoesNotFire(t *testing.T) {
	r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
	tmpl, o := onceAssigned(t, r, clock, DeliverWake)
	child := mustGet(t, r, o.ChildTaskID)
	child, err := r.Update(bg, child.ID, child.Revision, Patch{Status: ptr(StatusDone)})
	if err != nil {
		t.Fatal(err)
	}
	notes := countRows(t, r, "task_notifications", "1=1")
	if _, err := r.Update(bg, child.ID, child.Revision, Patch{Status: ptr(StatusPending)}); err != nil {
		t.Fatal(err)
	}
	got := mustOcc(t, r, o.ID)
	p := mustGet(t, r, tmpl.ID)
	if got.State != OccAssigned || got.CompletedAt != 0 || got.NoticeID != o.NoticeID || got.ChildTaskID != o.ChildTaskID ||
		p.Status != StatusPending || p.Next != 0 {
		t.Fatalf("reopen: run %+v parent %+v", got, p)
	}
	clock.Add(48 * time.Hour)
	if runs := materialize(t, r); len(runs) != 0 {
		t.Fatalf("reopen fired a new run: %+v", runs)
	}
	if countRows(t, r, "task_notifications", "1=1") != notes || len(runsOf(t, r, tmpl.ID)) != 1 {
		t.Fatal("reopen created a notice or run")
	}
	// The agent reopening it (in_progress) is the same path.
	c2 := mustGet(t, r, child.ID)
	if _, err := r.Update(bg, c2.ID, c2.Revision, Patch{Status: ptr(StatusDone)}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AgentUpdate(bg, actor("s1", "p"), c2.ID, AgentPatch{Status: ptr(StatusInProgress)}); err != nil {
		t.Fatal(err)
	}
	if got := mustOcc(t, r, o.ID); got.State != OccAssigned {
		t.Fatalf("agent reopen: %+v", got)
	}
}

func TestScheduleRerouteReusesChild(t *testing.T) {
	r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
	_, o := onceAssigned(t, r, clock, DeliverWake)
	// The recipient disappears before delivery: not sent.
	if ok, err := r.SetNoticeState(bg, o.NoticeID, NoticeChange{From: []string{NoticePending}, State: NoticeFailed, Reason: ReasonSessionDeleted}); err != nil || !ok {
		t.Fatalf("fail notice: %v %v", ok, err)
	}
	failed := mustOcc(t, r, o.ID)
	if failed.State != OccFailed || failed.Reason != ReasonSessionDeleted {
		t.Fatalf("run after hard failure = %+v", failed)
	}
	if _, err := r.RerouteOccurrence(bg, o.ID, o.Revision, dest("s2")); !isConflict(err) {
		t.Fatalf("stale reroute = %v", err)
	}
	re, err := r.RerouteOccurrence(bg, o.ID, failed.Revision, Destination{SessionID: "s2", ProjectCWD: "/work/q"})
	if err != nil {
		t.Fatal(err)
	}
	child := mustGet(t, r, re.ChildTaskID)
	if re.State != OccAssigned || re.ChildTaskID != o.ChildTaskID || re.NoticeID == o.NoticeID || re.SessionID != "s2" ||
		child.AssigneeSessionID != "s2" || child.ProjectCWD != "/work/q" || countRows(t, r, "tasks", "place = 'agent'") != 1 {
		t.Fatalf("reroute = %+v child %+v", re, child)
	}
	if n := mustNotice(t, r, re.NoticeID); n.RecipientSessionID != "s2" || n.State != NoticePending || n.TaskID != child.ID {
		t.Fatalf("new notice = %+v", n)
	}
	// The old notice stays failed; late transitions of it do not touch the run.
	if n := mustNotice(t, r, o.NoticeID); n.State != NoticeFailed {
		t.Fatalf("old notice = %+v", n)
	}
	if _, err := r.SetNoticeState(bg, o.NoticeID, NoticeChange{From: []string{NoticeFailed, NoticePending, NoticeSent}, State: NoticeFailed, Reason: "late echo"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SetNoticeState(bg, o.NoticeID, NoticeChange{From: []string{NoticeFailed}, State: NoticeDelivered}); err != nil {
		t.Fatal(err)
	}
	if got := mustOcc(t, r, o.ID); got.State != OccAssigned || got.AdmittedAt != 0 || got.NoticeID != re.NoticeID {
		t.Fatalf("old notice mutated the run: %+v", got)
	}
	// The dispatcher finds a run by its current notice only.
	if cur, ok, err := r.OccurrenceForNotice(bg, re.NoticeID); err != nil || !ok || cur.ID != o.ID {
		t.Fatalf("run for new notice = %+v %v %v", cur, ok, err)
	}
	if _, ok, err := r.OccurrenceForNotice(bg, o.NoticeID); err != nil || ok {
		t.Fatalf("old notice still maps to the run: %v %v", ok, err)
	}
	// Reroute is only for a failed run.
	if _, err := r.RerouteOccurrence(bg, o.ID, mustOcc(t, r, o.ID).Revision, dest("s3")); !isConflict(err) {
		t.Fatalf("reroute of an assigned run = %v", err)
	}
	// Delivered on the new notice records admission.
	if _, err := r.SetNoticeState(bg, re.NoticeID, NoticeChange{From: []string{NoticePending}, State: NoticeSent}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SetNoticeState(bg, re.NoticeID, NoticeChange{From: []string{NoticeSent}, State: NoticeDelivered}); err != nil {
		t.Fatal(err)
	}
	if got := mustOcc(t, r, o.ID); got.AdmittedAt == 0 || got.State != OccAssigned {
		t.Fatalf("admission not recorded: %+v", got)
	}
}

func TestScheduleChildDeleteAndReset(t *testing.T) {
	remove := map[string]func(r *Repo, child Record) error{
		"delete": func(r *Repo, child Record) error { return r.Delete(bg, child.ID, child.Revision, "") },
		"reset":  func(r *Repo, child Record) error { return r.ResetChecklist(bg, child.AssigneeSessionID) },
	}
	for how, rm := range remove {
		for _, state := range []string{NoticePending, NoticeHeld, NoticeSent} {
			t.Run(how+"/"+state, func(t *testing.T) {
				r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
				saved := DeliverWake
				if state == NoticeHeld {
					saved = DeliverHold
				}
				tmpl, o := onceAssigned(t, r, clock, saved)
				if state != NoticePending {
					if ok, err := r.SetNoticeState(bg, o.NoticeID, NoticeChange{From: []string{NoticePending}, State: state}); err != nil || !ok {
						t.Fatalf("set %s: %v %v", state, ok, err)
					}
				}
				if err := rm(r, mustGet(t, r, o.ChildTaskID)); err != nil {
					t.Fatal(err)
				}
				got := mustOcc(t, r, o.ID)
				if got.State != OccSkipped || got.Reason != ReasonChildRemoved || got.ChildTaskID != o.ChildTaskID || got.NoticeID != o.NoticeID {
					t.Fatalf("run after removal = %+v", got)
				}
				wantNote := "Removed before delivery"
				if state == NoticeSent {
					wantNote = "Removed after delivery; outcome unknown"
				}
				if got.Note != wantNote {
					t.Fatalf("note = %q", got.Note)
				}
				if n := mustNotice(t, r, o.NoticeID); n.State != NoticeFailed || n.Reason != ReasonChildRemoved {
					t.Fatalf("assignment still deliverable: %+v", n)
				}
				if ok, _ := r.SetNoticeState(bg, o.NoticeID, NoticeChange{From: []string{NoticePending, NoticeHeld}, State: NoticeSent}); ok {
					t.Fatal("removed assignment reserved")
				}
				if p := mustGet(t, r, tmpl.ID); p.Status != StatusDone || p.CompletionNote != skippedNote {
					t.Fatalf("once parent = %+v", p)
				}
				if len(runsOf(t, r, tmpl.ID)) != 1 {
					t.Fatal("history lost")
				}
			})
		}
		t.Run(how+"/completed", func(t *testing.T) {
			r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
			tmpl, o := onceAssigned(t, r, clock, DeliverWake)
			child := mustGet(t, r, o.ChildTaskID)
			child, err := r.Update(bg, child.ID, child.Revision, Patch{Status: ptr(StatusDone), CompletionNote: ptr("fine")})
			if err != nil {
				t.Fatal(err)
			}
			if err := rm(r, child); err != nil {
				t.Fatal(err)
			}
			if got := mustOcc(t, r, o.ID); got.State != OccDone || got.Note != "fine" {
				t.Fatalf("done history changed: %+v", got)
			}
			if p := mustGet(t, r, tmpl.ID); p.Status != StatusDone || p.CompletionNote != "fine" {
				t.Fatalf("parent = %+v", p)
			}
		})
	}
}

func TestScheduleCompletionSuppressesUnreservedNotice(t *testing.T) {
	for name, complete := range completePaths {
		t.Run(name, func(t *testing.T) {
			r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
			tmpl, o := onceAssigned(t, r, clock, DeliverHold)
			if ok, err := r.SetNoticeState(bg, o.NoticeID, NoticeChange{From: []string{NoticePending}, State: NoticeHeld}); err != nil || !ok {
				t.Fatal(err)
			}
			if err := complete(r, mustGet(t, r, o.ChildTaskID)); err != nil {
				t.Fatal(err)
			}
			if n := mustNotice(t, r, o.NoticeID); n.State != NoticeFailed || n.Reason != ReasonChildDone {
				t.Fatalf("held assignment after completion = %+v", n)
			}
			// The dispatcher's reservation (held/pending → sent) finds nothing.
			if ok, err := r.SetNoticeState(bg, o.NoticeID, NoticeChange{From: []string{NoticePending, NoticeHeld}, State: NoticeSent}); err != nil || ok {
				t.Fatalf("finished run reserved: %v %v", ok, err)
			}
			open, err := r.OpenNotices(bg)
			if err != nil || len(open) != 0 {
				t.Fatalf("open notices after completion: %+v %v", open, err)
			}
			if mustOcc(t, r, o.ID).State != OccDone || mustGet(t, r, tmpl.ID).Status != StatusDone {
				t.Fatal("run or once parent not done")
			}
			// A withdrawn assignment is not a delivery failure in the list.
			list, err := r.List(bg, Filter{IncludeAgents: true})
			if err != nil {
				t.Fatal(err)
			}
			if st := findTask(list.Tasks, o.ChildTaskID).NoticeState; st != "" {
				t.Fatalf("finished run marked %q", st)
			}
		})
	}
	t.Run("reserved first", func(t *testing.T) {
		r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
		tmpl, o := onceAssigned(t, r, clock, DeliverWake)
		if ok, err := r.SetNoticeState(bg, o.NoticeID, NoticeChange{From: []string{NoticePending}, State: NoticeSent}); err != nil || !ok {
			t.Fatal(err)
		}
		child := mustGet(t, r, o.ChildTaskID)
		if _, err := r.Update(bg, child.ID, child.Revision, Patch{Status: ptr(StatusDone)}); err != nil {
			t.Fatal(err)
		}
		if n := mustNotice(t, r, o.NoticeID); n.State != NoticeSent {
			t.Fatalf("reserved notice changed by completion: %+v", n)
		}
		if ok, err := r.SetNoticeState(bg, o.NoticeID, NoticeChange{From: []string{NoticeSent}, State: NoticeDelivered}); err != nil || !ok {
			t.Fatal(err)
		}
		if ok, err := r.SetNoticeState(bg, o.NoticeID, NoticeChange{From: []string{NoticeDelivered}, State: NoticeFailed, Reason: ReasonSessionDeleted}); err != nil || !ok {
			t.Fatal(err)
		}
		if got := mustOcc(t, r, o.ID); got.State != OccDone || mustGet(t, r, tmpl.ID).Status != StatusDone {
			t.Fatalf("completion lost to a later acknowledgment: %+v", got)
		}
		// Owner completion of a reserved run still tells the session, as today.
		kinds := notices(t, r, child.ID)
		if len(kinds) != 2 || kinds[0].Kind != NoticeAgentDone {
			t.Fatalf("notices = %+v", kinds)
		}
	})
	t.Run("owner completion before reservation tells nobody", func(t *testing.T) {
		r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
		_, o := onceAssigned(t, r, clock, DeliverHold)
		child := mustGet(t, r, o.ChildTaskID)
		if _, err := r.Update(bg, child.ID, child.Revision, Patch{Status: ptr(StatusDone), Deliver: DeliverWake}); err != nil {
			t.Fatal(err)
		}
		if ns := notices(t, r, child.ID); len(ns) != 1 || ns[0].ID != o.NoticeID {
			t.Fatalf("finished unsent run notified: %+v", ns)
		}
	})
}

// --- restart re-gating (BRIEF Q1) --------------------------------------------------

func TestScheduleRegateOnRestart(t *testing.T) {
	setup := func(t *testing.T, late string) (*Repo, *fakeClock, Record, Occurrence) {
		r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
		def := onceDef(clock.Now().Add(time.Minute), late)
		def.Delivery.Saved = DeliverHold
		tmpl := mkSchedule(t, r, "once", def)
		clock.Add(time.Minute)
		o := mustAssign(t, r, materializeOne(t, r).ID, "s1")
		if ok, err := r.SetNoticeState(bg, o.NoticeID, NoticeChange{From: []string{NoticePending}, State: NoticeHeld}); err != nil || !ok {
			t.Fatal(err)
		}
		return r, clock, tmpl, o
	}
	restart := func(t *testing.T, r *Repo, clock *fakeClock, after time.Duration) (*Repo, int) {
		clock.Add(after)
		fresh := openRepo(t, r.Path())
		fresh.SetClock(clock.Now)
		n, err := fresh.RegateOnRestart(bg)
		if err != nil {
			t.Fatal(err)
		}
		return fresh, n
	}
	t.Run("ask returns to late", func(t *testing.T) {
		r, clock, tmpl, o := setup(t, LateAsk)
		fresh, n := restart(t, r, clock, 2*time.Hour)
		got := mustOcc(t, fresh, o.ID)
		if n != 1 || got.State != OccLate || got.ChildTaskID != 0 || got.NoticeID != "" || got.SessionID != "" {
			t.Fatalf("re-gated run = %+v (n=%d)", got, n)
		}
		if countRows(t, fresh, "tasks", "id = ?", o.ChildTaskID) != 0 {
			t.Fatal("undelivered child survived")
		}
		if nt := mustNotice(t, fresh, o.NoticeID); nt.State != NoticeFailed || nt.Reason != ReasonRegated {
			t.Fatalf("old notice = %+v", nt)
		}
		if l, _ := fresh.List(bg, Filter{}); l.Counts.LateOccurrences != 1 || mustGet(t, fresh, tmpl.ID).ScheduleState != OccLate {
			t.Fatalf("late not visible: %+v", l.Counts)
		}
		// Run goes through T1 again: a new child and notice.
		ok, err := fresh.ConfirmOccurrence(bg, o.ID, got.Revision, LateRun)
		if err != nil {
			t.Fatal(err)
		}
		re := mustAssign(t, fresh, ok.ID, "s1")
		if re.ChildTaskID == o.ChildTaskID || re.NoticeID == o.NoticeID {
			t.Fatalf("confirm reused withdrawn links: %+v", re)
		}
		// A second restart does not re-gate a confirmed run.
		if _, n := restart(t, fresh, clock, 5*time.Hour); n != 0 {
			t.Fatalf("confirmed run re-gated (%d)", n)
		}
	})
	t.Run("skip settles", func(t *testing.T) {
		r, clock, tmpl, o := setup(t, LateSkip)
		fresh, n := restart(t, r, clock, 2*time.Hour)
		got := mustOcc(t, fresh, o.ID)
		if n != 1 || got.State != OccSkipped || countRows(t, fresh, "tasks", "id = ?", o.ChildTaskID) != 0 {
			t.Fatalf("skip re-gate = %+v", got)
		}
		if p := mustGet(t, fresh, tmpl.ID); p.Status != StatusDone || p.CompletionNote != skippedNote {
			t.Fatalf("once parent = %+v", p)
		}
	})
	t.Run("run continues", func(t *testing.T) {
		r, clock, _, o := setup(t, LateRun)
		fresh, n := restart(t, r, clock, 2*time.Hour)
		if got := mustOcc(t, fresh, o.ID); n != 0 || got.State != OccAssigned || got.ChildTaskID != o.ChildTaskID {
			t.Fatalf("run policy re-gated: %+v", got)
		}
	})
	t.Run("not yet ten minutes", func(t *testing.T) {
		r, clock, _, o := setup(t, LateAsk)
		// due was at +1m, now is due; 9m59.999s later is still on time.
		fresh, n := restart(t, r, clock, LateAfter-time.Millisecond)
		if got := mustOcc(t, fresh, o.ID); n != 0 || got.State != OccAssigned {
			t.Fatalf("re-gated before the threshold: %+v", got)
		}
	})
	t.Run("reserved or admitted is never re-gated", func(t *testing.T) {
		r, clock, _, o := setup(t, LateAsk)
		if ok, err := r.SetNoticeState(bg, o.NoticeID, NoticeChange{From: []string{NoticeHeld}, State: NoticeSent}); err != nil || !ok {
			t.Fatal(err)
		}
		fresh, n := restart(t, r, clock, 2*time.Hour)
		if got := mustOcc(t, fresh, o.ID); n != 0 || got.State != OccAssigned {
			t.Fatalf("reserved run re-gated: %+v", got)
		}
	})
	t.Run("ready and run-now", func(t *testing.T) {
		r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
		a := mkSchedule(t, r, "timer", onceDef(clock.Now().Add(time.Minute), LateAsk))
		b := mkSchedule(t, r, "by hand", onceDef(clock.Now().Add(time.Hour), LateAsk))
		clock.Add(time.Minute)
		ready := materializeOne(t, r)
		byHand, err := r.RunNow(bg, b.ID, b.Revision, clock.Now().Add(59*time.Minute).UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
		fresh, n := restart(t, r, clock, 3*time.Hour)
		if n != 1 || mustOcc(t, fresh, ready.ID).State != OccLate || mustOcc(t, fresh, byHand.ID).State != OccReady {
			t.Fatalf("n=%d timer=%+v byhand=%+v", n, mustOcc(t, fresh, ready.ID), mustOcc(t, fresh, byHand.ID))
		}
		_ = a
	})
}

// --- template deletion, session deletion, counts --------------------------------

func TestScheduleParentDeleteSettlesLate(t *testing.T) {
	r, clock := schedRepo(t, "2026-09-29T08:00:00Z")
	tmpl := mkSchedule(t, r, "daily", dailyDef(9, 0, "UTC", ""))
	clock.Set(utc("2026-09-29T09:00:00Z"))
	ready := materializeOne(t, r)
	clock.Set(utc("2026-09-30T09:30:00Z"))
	late := materializeOne(t, r)
	if err := r.Delete(bg, tmpl.ID, mustGet(t, r, tmpl.ID).Revision, ""); err != nil {
		t.Fatal(err)
	}
	if got := mustOcc(t, r, late.ID); got.State != OccSkipped || got.Reason != ReasonScheduleDeleted {
		t.Fatalf("late after parent delete = %+v", got)
	}
	if _, err := r.ConfirmOccurrence(bg, late.ID, late.Revision, LateRun); !isConflict(err) {
		t.Fatalf("confirm after delete = %v", err)
	}
	if l, _ := r.List(bg, Filter{}); l.Counts.LateOccurrences != 0 {
		t.Fatalf("orphan decision counted: %+v", l.Counts)
	}
	if got := mustAssign(t, r, ready.ID, "s1"); got.State != OccAssigned {
		t.Fatal("authorized run lost")
	}
}

func TestScheduleSessionDeletedSettlement(t *testing.T) {
	r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
	mk := func(title string) Occurrence {
		tmpl := mkSchedule(t, r, title, onceDef(clock.Now().Add(time.Minute), ""))
		_ = tmpl
		clock.Add(time.Minute)
		return materializeOne(t, r)
	}
	pending := mustAssign(t, r, mk("pending").ID, "gone")
	sent := mustAssign(t, r, mk("sent").ID, "gone")
	admitted := mustAssign(t, r, mk("admitted").ID, "gone")
	marker := mk("new session")
	elsewhere := mustAssign(t, r, mk("elsewhere").ID, "kept")
	if _, err := r.SetNoticeState(bg, sent.NoticeID, NoticeChange{From: []string{NoticePending}, State: NoticeSent}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SetNoticeState(bg, admitted.NoticeID, NoticeChange{From: []string{NoticePending}, State: NoticeSent}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SetNoticeState(bg, admitted.NoticeID, NoticeChange{From: []string{NoticeSent}, State: NoticeSent, Admitted: true}); err != nil {
		t.Fatal(err)
	}
	n, err := r.SettleSessionDeleted(bg, "gone", marker.ID)
	if err != nil || n != 4 {
		t.Fatalf("settled %d, %v", n, err)
	}
	for _, c := range []struct {
		o             Occurrence
		state, reason string
	}{
		{pending, OccFailed, ReasonSessionDeleted},
		{sent, OccSkipped, ReasonDeletedDuringDeliver},
		{admitted, OccSkipped, ReasonDeletedAfterDeliver},
		{marker, OccFailed, ReasonSessionDeleted},
		{elsewhere, OccAssigned, ""},
	} {
		got := mustOcc(t, r, c.o.ID)
		if got.State != c.state || got.Reason != c.reason {
			t.Errorf("run #%d = %s/%s, want %s/%s", got.ID, got.State, got.Reason, c.state, c.reason)
		}
		if c.o.NoticeID != "" && c.state != OccAssigned {
			if nt := mustNotice(t, r, c.o.NoticeID); nt.State != NoticeFailed {
				t.Errorf("notice of run #%d still %s", got.ID, nt.State)
			}
		}
	}
	// The dispatcher finding the recipient gone after a reservation is the
	// same uncertain outcome, never "not sent".
	viaDispatcher := mustAssign(t, r, mk("dispatcher").ID, "gone2")
	if _, err := r.SetNoticeState(bg, viaDispatcher.NoticeID, NoticeChange{From: []string{NoticePending}, State: NoticeSent}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SetNoticeState(bg, viaDispatcher.NoticeID, NoticeChange{From: []string{NoticeSent}, State: NoticeFailed, Reason: ReasonSessionDeleted}); err != nil {
		t.Fatal(err)
	}
	if got := mustOcc(t, r, viaDispatcher.ID); got.State != OccSkipped || got.Reason != ReasonDeletedDuringDeliver {
		t.Fatalf("sent then failed = %s/%s", got.State, got.Reason)
	}
	// A settled undelivered run can be sent elsewhere, reusing its child.
	failed := mustOcc(t, r, pending.ID)
	if re, err := r.RerouteOccurrence(bg, failed.ID, failed.Revision, dest("kept")); err != nil || re.ChildTaskID != pending.ChildTaskID {
		t.Fatalf("reroute after session deletion = %+v, %v", re, err)
	}
}

func TestScheduleCountsLateOccurrences(t *testing.T) {
	r, clock := schedRepo(t, "2026-09-27T10:00:00Z")
	for _, s := range []string{"a", "b"} {
		if _, err := r.AgentAsk(bg, actor(s, "p"), AgentInput{Title: "need " + s}); err != nil {
			t.Fatal(err)
		}
	}
	rep := mkSchedule(t, r, "daily", dailyDef(9, 0, "UTC", ""))
	paused := mkSchedule(t, r, "paused daily", dailyDef(9, 0, "UTC", ""))
	failedTmpl := mkSchedule(t, r, "fails", dailyDef(9, 0, "UTC", LateRun))
	mustCreate(t, r, CreateInput{Title: "private note", Place: PlaceYou})
	var lates []Occurrence
	for _, day := range []string{"2026-09-28T09:30:00Z", "2026-09-29T09:30:00Z"} {
		clock.Set(utc(day))
		for _, o := range materialize(t, r) {
			if o.ScheduleTaskID == rep.ID {
				lates = append(lates, o)
			}
			if o.ScheduleTaskID == failedTmpl.ID {
				if _, err := r.FailOccurrence(bg, o.ID, "owner_missing", ""); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	// Leave one late under paused, then pause it.
	var pausedLate int
	for _, o := range runsOf(t, r, paused.ID) {
		if o.State == OccLate {
			pausedLate++
			if pausedLate > 1 {
				if _, err := r.ConfirmOccurrence(bg, o.ID, o.Revision, LateSkip); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if _, err := r.Pause(bg, paused.ID, mustGet(t, r, paused.ID).Revision); err != nil {
		t.Fatal(err)
	}
	counts := func() Counts {
		l, err := r.List(bg, Filter{})
		if err != nil {
			t.Fatal(err)
		}
		return l.Counts
	}
	if c := counts(); c.OpenRequests != 2 || c.LateOccurrences != 3 || c.Attention != 5 {
		t.Fatalf("counts = %+v", c)
	}
	if p := mustGet(t, r, paused.ID); p.ScheduleState != "paused" || p.LateCount != 1 {
		t.Fatalf("paused = %+v", p)
	}
	if f := mustGet(t, r, failedTmpl.ID); f.ScheduleState != OccFailed || f.Failure == nil || f.Failure.Reason != "owner_missing" {
		t.Fatalf("failed = %+v", f)
	}
	if _, err := r.ConfirmOccurrence(bg, lates[0].ID, lates[0].Revision, LateRun); err != nil {
		t.Fatal(err)
	}
	if c := counts(); c.LateOccurrences != 2 || c.Attention != 4 {
		t.Fatalf("after confirm = %+v", c)
	}
	if err := r.Delete(bg, paused.ID, mustGet(t, r, paused.ID).Revision, ""); err != nil {
		t.Fatal(err)
	}
	if c := counts(); c.LateOccurrences != 1 || c.Attention != 3 {
		t.Fatalf("after deleting paused = %+v", c)
	}
}

// --- legacy import -----------------------------------------------------------------

func TestLegacyScheduleImportAtomic(t *testing.T) {
	r, clock := schedRepo(t, "2026-09-30T12:00:00Z")
	items := []LegacySchedule{
		{ID: "sch_future", SessionID: "s1", Text: "Check the deploy\nand report back", DueAt: clock.Now().Add(time.Hour).UnixMilli(), TZ: "Europe/Madrid"},
		{ID: "sch_overdue", SessionID: "s2", Text: "ping", DueAt: clock.Now().Add(-time.Minute).UnixMilli(), TZ: "Local"},
	}
	if _, _, _, err := r.ensure(true); err != nil {
		t.Fatal(err)
	}
	drop := sqlTrigger(t, r, "refuse_second", "BEFORE INSERT ON task_schedules WHEN (SELECT COUNT(*) FROM task_schedules) >= 1")
	if _, err := r.ImportLegacySchedules(bg, items); err == nil {
		t.Fatal("partial import committed")
	}
	if done, _ := r.LegacySchedulesImported(bg); done || countRows(t, r, "tasks", "1=1") != 0 {
		t.Fatal("failed import left rows or the flag")
	}
	drop()
	ok, err := r.ImportLegacySchedules(bg, items)
	if err != nil || !ok {
		t.Fatalf("import = %v %v", ok, err)
	}
	list, _ := r.List(bg, Filter{})
	if len(list.Tasks) != 2 || list.Counts.LateOccurrences != 1 {
		t.Fatalf("imported = %+v", list)
	}
	future, overdue := findByTitle(list.Tasks, "Check the deploy"), findByTitle(list.Tasks, "ping")
	if future.Description != items[0].Text || future.TZ != "Europe/Madrid" || future.Next != items[0].DueAt ||
		future.Target.ID != "s1" || future.ScheduleState != "scheduled" {
		t.Fatalf("future = %+v", future)
	}
	runs := runsOf(t, r, overdue.ID)
	if overdue.TZ != "UTC" || overdue.Next != 0 || len(runs) != 1 || runs[0].State != OccLate || runs[0].Trigger != TriggerLegacy ||
		runs[0].DueAt != items[1].DueAt {
		t.Fatalf("overdue = %+v runs %+v", overdue, runs)
	}
	// Once imported, never again, even after the templates are deleted.
	if err := r.Delete(bg, future.ID, future.Revision, ""); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.ImportLegacySchedules(bg, items); err != nil || ok {
		t.Fatalf("re-import = %v %v", ok, err)
	}
	if countRows(t, r, "task_schedules", "1=1") != 1 {
		t.Fatal("re-import resurrected a deleted template")
	}
}
