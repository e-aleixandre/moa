package tasks

import (
	"database/sql"
	"errors"
	"sync"
	"testing"
)

func TestNoticeInsertFailureRollsBackOwnerGesture(t *testing.T) {
	for _, op := range []string{"create", "update", "delete"} {
		t.Run(op, func(t *testing.T) {
			r := newRepo(t)
			rec := mustCreate(t, r, CreateInput{Title: "original", Place: PlaceAgent, AssigneeSessionID: "a"})
			beforeRev, err := r.Revision(bg)
			if err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", dsn(r.Path(), true))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close() //nolint:errcheck
			if _, err := db.Exec(`CREATE TRIGGER refuse_notice BEFORE INSERT ON task_notifications BEGIN SELECT RAISE(ABORT, 'notice unavailable'); END`); err != nil {
				t.Fatal(err)
			}
			switch op {
			case "create":
				_, err = r.Create(bg, CreateInput{Title: "new", Place: PlaceAgent, AssigneeSessionID: "a"})
			case "update":
				_, err = r.Update(bg, rec.ID, rec.Revision, Patch{Title: ptr("changed"), Notify: true})
			case "delete":
				err = r.Delete(bg, rec.ID, rec.Revision, "")
			}
			if err == nil {
				t.Fatal("gesture succeeded despite failed outbox INSERT")
			}
			got, err := r.Get(bg, rec.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Title != rec.Title || got.Revision != rec.Revision {
				t.Fatalf("failed gesture modified task: %+v", got)
			}
			list, err := r.List(bg, Filter{IncludeAgents: true})
			if err != nil {
				t.Fatal(err)
			}
			if len(list.Tasks) != 1 || list.Revision != beforeRev {
				t.Fatalf("failed gesture modified database: %+v", list)
			}
			wantKinds(t, notices(t, r, rec.ID), NoticeAssigned)
		})
	}
}

func TestConcurrentOwnerCompletionCreatesOneNoticeAcrossConnections(t *testing.T) {
	r := newRepo(t)
	ask, err := r.AgentAsk(bg, actor("a", "p"), AgentInput{Title: "need help"})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := r.Get(bg, ask.ID)
	if err != nil {
		t.Fatal(err)
	}
	other := New(r.Path())
	defer other.Close() //nolint:errcheck
	start := make(chan struct{})
	results := make(chan error, 3)
	var wg sync.WaitGroup
	for _, repo := range []*Repo{r, r, other} {
		wg.Add(1)
		go func(repo *Repo) {
			defer wg.Done()
			<-start
			_, err := repo.Update(bg, rec.ID, rec.Revision, Patch{Status: ptr(StatusDone)})
			results <- err
		}(repo)
	}
	close(start)
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for err := range results {
		var conflict *ConflictError
		switch {
		case err == nil:
			wins++
		case errors.As(err, &conflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 2 {
		t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
	}
	wantKinds(t, notices(t, r, rec.ID), NoticeRequestDone)
}
