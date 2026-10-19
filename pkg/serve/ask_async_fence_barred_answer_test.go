package serve

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/askuser"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// While a Stop's fence is not saved, the session accepts no answer.

func f1PostAskStatus(t *testing.T, e *f1Env, sessionID, qid string) (int, string) {
	t.Helper()
	return f1PostAsk(t, e, sessionID, qid, "ok")
}

func TestAskAsyncFailedStopFenceBarsAnswersUntilSaved(t *testing.T) {
	s := f1NewJournalScene(t)
	path := f1JournalPath(s.e.mgr, s.sess.ID)
	code, body := f1PostAskStatus(t, s.e, s.sess.ID, s.q2)
	if code != http.StatusServiceUnavailable || !strings.Contains(strings.ToLower(body), "retry") {
		t.Fatalf("answer while the Stop is unsaved = %d %q, want 503 telling to retry", code, body)
	}
	if open, _ := s.e.mgr.tasks.GetQuestion(context.Background(), s.q2); open.State != tasks.QuestionOpen {
		t.Fatalf("the rejected answer changed the question: %+v", open)
	}

	s.lift()
	if code, body := f1PostAskStatus(t, s.e, s.sess.ID, s.q2); code != 204 {
		t.Fatalf("answer once the store works = %d %s, want 204", code, body)
	}
	if f1FileExists(path) {
		t.Fatal("the journal survived the fence being saved")
	}
	f1AssertFenced(t, s.e, s.q1)
}

func TestAskAsyncRestartWithBrokenStoreRejectsAnswersUntilSaved(t *testing.T) {
	s := f1NewJournalScene(t)
	sessionID := s.sess.ID
	s.e.halt()
	e2 := f1NewEnv(t, s.base, s.root, &f1Provider{script: f1TwoAskScript()})
	if code, body := f1PostAskStatus(t, e2, sessionID, s.q2); code != http.StatusServiceUnavailable {
		t.Fatalf("answer while barred after a restart = %d %s, want 503", code, body)
	}

	lift := func() {
		db, err := sql.Open("sqlite", "file:"+e2.mgr.tasks.Path()+"?_pragma=busy_timeout(5000)")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close() //nolint:errcheck
		if _, err := db.Exec(`DROP TRIGGER IF EXISTS stop_no_fence`); err != nil {
			t.Fatal(err)
		}
	}
	lift()
	if code, body := f1PostAskStatus(t, e2, sessionID, s.q2); code != 204 {
		t.Fatalf("answer once the store works = %d %s, want 204", code, body)
	}
	if f1FileExists(f1JournalPath(e2.mgr, sessionID)) {
		t.Fatal("the journal survived the fence being saved")
	}
}

// An answer that arrives through the bus (voice, owners, automation) is refused
// with the same typed error, and the question stays open.
func TestAskAsyncBarredSessionRefusesBusAnswers(t *testing.T) {
	s := f1NewJournalScene(t)
	err := s.sess.runtime.Bus.Execute(bus.ResolveAskUser{AskID: s.q2, Answers: []string{"ok"}, Responder: askuser.Responder{Kind: askuser.ResponderVoice, CallID: "c1"}})
	if !errors.Is(err, errFenceBarred) {
		t.Fatalf("bus answer while barred = %v, want errFenceBarred", err)
	}
	if q, _ := s.e.mgr.tasks.GetQuestion(context.Background(), s.q2); q.State != tasks.QuestionOpen {
		t.Fatalf("a refused answer changed the question: %+v", q)
	}
}
