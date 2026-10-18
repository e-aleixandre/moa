package serve

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/tasks"
	_ "modernc.org/sqlite"
)

// An answer is one transaction with its notice. A real storage failure on the
// notice's INSERT (a trigger in the real database standing in for a full disk)
// rolls the answer back with it: the endpoint reports the failure, the
// question is still open and pending, the model was not woken, and the same
// answer, retried once storage works, is accepted.
func TestAskAsyncAnswerFailsAtomicallyWhenItsNoticeCannotBeWritten(t *testing.T) {
	e, sess, qid := f1Open(t, f1KindHuman, false)
	dbPath := filepath.Join(filepath.Dir(e.mgr.sessionBaseDir), tasks.DatabaseName)
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TRIGGER f1_no_notice BEFORE INSERT ON task_notifications
		WHEN NEW.kind = 'question_answer' BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}

	before := e.prov.countSession(t, sess.ID)
	code, body := f1PostAsk(t, e, sess.ID, qid, "yes")
	if f1Is2xx(code) {
		t.Fatalf("answer = %d %s, want a failure: its notice could not be written", code, body)
	}
	ctx := context.Background()
	q, err := e.mgr.tasks.GetQuestion(ctx, qid)
	if err != nil {
		t.Fatal(err)
	}
	if q.State != tasks.QuestionOpen || q.EventID != "" || len(q.Answers) != 0 {
		t.Fatalf("question after the failed answer = %+v, want it untouched", q)
	}
	if p := f1Pending(sess); p == nil || p.ID != qid {
		t.Fatalf("the question is no longer pending after a failed answer: %+v", p)
	}
	if !f1Stable(400*time.Millisecond, func() bool {
		return len(f1Answers(sess.History())) == 0 && e.prov.countSession(t, sess.ID) == before
	}) {
		t.Fatal("a failed answer produced an event or woke the model")
	}

	if _, err := db.Exec(`DROP TRIGGER f1_no_notice`); err != nil {
		t.Fatal(err)
	}
	if code, body := f1PostAsk(t, e, sess.ID, qid, "yes"); !f1Is2xx(code) {
		t.Fatalf("retry once storage works = %d %s", code, body)
	}
	if !f1Eventually(f1Guard, func() bool { return len(f1Answers(sess.History())) == 1 }) {
		t.Fatal("the retried answer never reached the session")
	}
}
