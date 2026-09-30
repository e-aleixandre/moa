package tasks

import (
	"testing"
	"time"
)

// Lead decision 2: deleting a run's child whose assignment never reached the
// session tells that session nothing; once reserved or admitted it still
// hears about the deletion.
func TestScheduleChildDeleteQuietWhenNeverAdmitted(t *testing.T) {
	for _, state := range []string{NoticePending, NoticeHeld, NoticeSent, NoticeDelivered} {
		t.Run(state, func(t *testing.T) {
			r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
			saved := DeliverWake
			if state == NoticeHeld {
				saved = DeliverHold
			}
			_, o := onceAssigned(t, r, clock, saved)
			if state == NoticeSent || state == NoticeDelivered {
				if ok, err := r.SetNoticeState(bg, o.NoticeID, NoticeChange{From: []string{NoticePending}, State: state}); err != nil || !ok {
					t.Fatalf("set %s: %v %v", state, ok, err)
				}
			}
			child := mustGet(t, r, o.ChildTaskID)
			if err := r.Delete(bg, child.ID, child.Revision, ""); err != nil {
				t.Fatal(err)
			}
			got := countRows(t, r, "task_notifications", "kind = 'agent_deleted' AND task_id = ?", child.ID)
			want := 0
			if state == NoticeSent || state == NoticeDelivered {
				want = 1
			}
			if got != want {
				t.Fatalf("agent_deleted notices = %d, want %d", got, want)
			}
		})
	}
}

// Lead decision 3: a recurring template is failed only while its most recent
// run failed; a later run clears it. A once template stays failed.
func TestScheduleFailedStateFollowsLatestRun(t *testing.T) {
	r, clock := schedRepo(t, "2026-09-28T08:00:00Z")
	rep := mkSchedule(t, r, "daily", dailyDef(9, 0, "UTC", LateRun))
	clock.Set(utc("2026-09-28T09:00:00Z"))
	first := materializeOne(t, r)
	if _, err := r.FailOccurrence(bg, first.ID, "owner_missing", ""); err != nil {
		t.Fatal(err)
	}
	if p := mustGet(t, r, rep.ID); p.ScheduleState != OccFailed || p.Failure == nil {
		t.Fatalf("after failure = %+v", p)
	}
	clock.Set(utc("2026-09-29T09:00:00Z"))
	second := materializeOne(t, r)
	o := mustAssign(t, r, second.ID, "s1")
	child := mustGet(t, r, o.ChildTaskID)
	if _, err := r.Update(bg, child.ID, child.Revision, Patch{Status: ptr(StatusDone)}); err != nil {
		t.Fatal(err)
	}
	if p := mustGet(t, r, rep.ID); p.ScheduleState != "scheduled" || p.Failure != nil {
		t.Fatalf("after a later successful run = state %q failure %+v", p.ScheduleState, p.Failure)
	}

	once := mkSchedule(t, r, "once", onceDef(clock.Now().Add(time.Minute), LateRun))
	clock.Add(time.Minute)
	oo := materializeOne(t, r)
	if _, err := r.FailOccurrence(bg, oo.ID, "owner_missing", ""); err != nil {
		t.Fatal(err)
	}
	clock.Add(48 * time.Hour)
	if p := mustGet(t, r, once.ID); p.ScheduleState != OccFailed || p.Failure == nil {
		t.Fatalf("once after failure = %+v", p)
	}
}

// Lead decision 4: templates are listed in the Scheduled group, not counted
// under You.
func TestScheduleTemplatesNotCountedInYou(t *testing.T) {
	r, _ := schedRepo(t, "2026-09-28T08:00:00Z")
	note(t, r, "mine")
	mkSchedule(t, r, "daily", dailyDef(9, 0, "UTC", ""))
	l, err := r.List(bg, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if l.Counts.You != 1 {
		t.Fatalf("You = %d, want 1 (template counted)", l.Counts.You)
	}
}
