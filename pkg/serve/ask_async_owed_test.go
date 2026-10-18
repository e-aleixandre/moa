package serve

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/tasks"
)

// What the goal driver asks the session (SessionContext.AnswerWorkOwed): an
// accepted answer that the model has not read yet owes a run; once the run
// that read it ended it owes nothing, even before its work is settled.
func TestAskAsyncOwedWorkFollowsTheAnswerUntilItIsRead(t *testing.T) {
	f1Isolate(t)
	reached := make(chan struct{}, 4)
	release := make(chan struct{})
	var once sync.Once
	hooks := taskSchedulerHooks{beforeAdmit: func(string) {
		select {
		case reached <- struct{}{}:
		default:
		}
		<-release
	}}
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{script: f1Script(false)}, hooks)
	t.Cleanup(func() { once.Do(func() { close(release) }) })

	sess := e.session(f1KindHuman)
	e.send(sess, "F1-TRIGGER please ask")
	qid := waitPendingAsk(t, sess).ID
	f1Settle(t, sess)

	owed := sess.runtime.Context().AnswerWorkOwed
	if owed == nil {
		t.Fatal("the session does not tell the goal driver about owed answers")
	}
	if owed() {
		t.Fatal("an open question owes no answer work yet")
	}

	if code, body := f1PostAsk(t, e, sess.ID, qid, "yes"); !f1Is2xx(code) {
		t.Fatalf("answer = %d %s, want 2xx", code, body)
	}
	select {
	case <-reached:
	case <-time.After(f1Guard):
		t.Fatal("the accepted answer never reached admission")
	}
	if !owed() {
		t.Fatal("an accepted answer the model has not read must owe a run")
	}

	once.Do(func() { close(release) })
	if !f1Eventually(f1Guard, func() bool { return len(f1Answers(sess.History())) == 1 && sessState(sess) == StateIdle }) {
		t.Fatal("the answer was never read")
	}
	if !f1Eventually(f1Guard, func() bool { return !owed() }) {
		t.Fatal("a read answer still owes a run")
	}
	q, err := e.mgr.tasks.GetQuestion(context.Background(), qid)
	if err != nil {
		t.Fatal(err)
	}
	if !f1Eventually(f1Guard, func() bool {
		q, _ = e.mgr.tasks.GetQuestion(context.Background(), qid)
		return q.Work == tasks.WorkDone
	}) {
		t.Fatalf("work = %q, want done once the run that read it ended", q.Work)
	}
}
