package tasks

import (
	"testing"
	"time"
)

// Q1 in the repo: only a reserved, never acknowledged assignment becomes an
// uncertain decision; an acknowledged one or one never reserved is left
// alone, and one whose template is gone is skipped.
func TestRound3MarkDeliveryUncertain(t *testing.T) {
	for _, tc := range []string{"reserved", "pending", "acknowledged", "template_deleted"} {
		t.Run(tc, func(t *testing.T) {
			r, clock := schedRepo(t, "2026-09-28T08:00:00Z")
			tmpl := mkSchedule(t, r, "uncertain", onceDef(clock.Now().Add(time.Minute), LateRun))
			clock.Set(clock.Now().Add(time.Minute))
			o := mustAssign(t, r, materializeOne(t, r).ID, "s1")
			if tc != "pending" {
				if ok, err := r.SetNoticeState(bg, o.NoticeID, NoticeChange{From: []string{NoticePending}, State: NoticeSent, Admitted: tc == "acknowledged"}); err != nil || !ok {
					t.Fatalf("reserve: %v %v", ok, err)
				}
			}
			if tc == "template_deleted" {
				cur, err := r.Get(bg, tmpl.ID)
				if err != nil {
					t.Fatal(err)
				}
				if err := r.Delete(bg, tmpl.ID, cur.Revision, ""); err != nil {
					t.Fatal(err)
				}
			}
			marked, err := r.MarkDeliveryUncertain(bg, o.NoticeID)
			if err != nil {
				t.Fatal(err)
			}
			got, n := mustOcc(t, r, o.ID), mustNotice(t, r, o.NoticeID)
			switch tc {
			case "pending", "acknowledged":
				if marked || got.State != OccAssigned || got.NoticeID != o.NoticeID {
					t.Fatalf("%s changed: marked=%t run=%+v", tc, marked, got)
				}
				return
			case "template_deleted":
				if !marked || got.State != OccSkipped || got.Reason != ReasonUncertain {
					t.Fatalf("orphan uncertain run = %s/%s", got.State, got.Reason)
				}
				return
			}
			if !marked || got.State != OccLate || got.Reason != ReasonUncertain || got.ChildTaskID != 0 || got.NoticeID != "" ||
				n.State != NoticeFailed || n.Reason != ReasonUncertain {
				t.Fatalf("uncertain run = %+v notice %s/%s", got, n.State, n.Reason)
			}
			if _, err := r.Get(bg, o.ChildTaskID); err == nil {
				t.Fatal("uncertain run kept its child")
			}
			rec, err := r.Get(bg, tmpl.ID)
			if err != nil || rec.LateCount != 1 || rec.ScheduleState != OccLate {
				t.Fatalf("uncertain decision not counted: %+v %v", rec, err)
			}
			if again, _ := r.MarkDeliveryUncertain(bg, o.NoticeID); again {
				t.Fatal("marked twice")
			}
		})
	}
}
