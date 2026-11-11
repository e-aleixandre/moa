package tasks

import (
	"errors"
	"os"
	"testing"
)

func TestReviewCompat7BackupFailurePreservesExistingHandoffsAndThreads(t *testing.T) {
	p := liveTasksCompat6DB(t)
	// Force only the new compat7 backup to fail; the compat6 database is valid.
	if err := os.Mkdir(p+".pre-compat7.bak", 0700); err != nil {
		t.Fatal(err)
	}
	r := openRepo(t, p)
	rec, err := r.Get(bg, 4)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Handoff == nil || rec.CommentCount != 1 {
		t.Fatalf("read-only compat6 projection lost existing data: handoff=%+v comment_count=%d", rec.Handoff, rec.CommentCount)
	}
	a := actor("s1", "p")
	got, err := r.AgentGet(bg, a, 4)
	if err != nil || got.HandoffState != "sent" || len(got.Thread) != 1 {
		t.Fatalf("read-only agent handoff/thread lost: %+v %v", got, err)
	}
	if _, err := r.AgentCreate(bg, a, AgentInput{Title: "must refuse writes"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("write=%v", err)
	}
}

func TestReviewFirstBootWithoutLinksHasNoInterruptedExecutions(t *testing.T) {
	p := liveTasksCompat6DB(t)
	r := openRepo(t, p)
	recs, err := r.InterruptedExecutions(bg)
	if err != nil || len(recs) != 0 {
		t.Fatalf("first boot: %+v %v", recs, err)
	}
}
