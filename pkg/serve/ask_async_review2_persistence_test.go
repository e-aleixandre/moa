package serve

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tasks"
	_ "modernc.org/sqlite"
)

// Hold the actual dispatcher after reservation, then Stop followed by a rewind
// that abandons the origin. An accepted but never-admitted answer is invalid
// on the new branch even when Stop already made its work "stopped".
func TestPersistenceReviewStoppedAnswerCannotCrossRewind(t *testing.T) {
	f1Isolate(t)
	reached, release := make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{script: f1Script(false)}, taskSchedulerHooks{
		beforeAdmit: func(string) { reached <- struct{}{}; <-release },
	})
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	sess := e.session(f1KindHuman)
	sendAndWait(t, e.mgr, sess, "F1-ANCHOR before question")
	anchor := f1EntryWhere(t, sess, func(m core.AgentMessage) bool { return m.Role == "user" && f1MsgText(m) == "F1-ANCHOR before question" })
	e.send(sess, "F1-TRIGGER please ask")
	qid := waitPendingAsk(t, sess).ID
	f1Settle(t, sess)
	f1ActiveForStop(t, e, sess)
	if code, body := f1PostAsk(t, e, sess.ID, qid, "yes"); code != 204 {
		t.Fatalf("answer = %d %s", code, body)
	}
	select {
	case <-reached:
	case <-time.After(f1Guard):
		t.Fatal("answer never reserved")
	}
	f1Stop(t, e, sess)
	q, err := e.mgr.tasks.GetQuestion(context.Background(), qid)
	if err != nil || q.Work != tasks.WorkStopped {
		t.Fatalf("precondition: q=%+v err=%v", q, err)
	}
	if code := f1BranchRequest(t, e, sess.ID, anchor, true); code != 200 {
		t.Fatalf("rewind = %d", code)
	}
	if originOnPath(sess.runtime.Context().Tree.Path(), q) {
		t.Fatal("test did not abandon origin")
	}
	once.Do(func() { close(release) })
	waitDispatcherIdle(e)
	sess.runtime.Bus.Drain(2 * time.Second)
	n, err := e.mgr.tasks.Notice(context.Background(), q.EventID)
	t.Logf("after Stop+rewind: notice=%s/%s/%s work=%s answer messages=%d", n.State, n.Reason, n.Method, q.Work, len(f1Answers(sess.History())))
	if err != nil || n.State != tasks.NoticeFailed || n.Reason != tasks.ReasonRewound || len(f1Answers(sess.History())) != 0 {
		t.Fatalf("answer crossed an abandoned branch: notice=%+v err=%v answer messages=%d", n, err, len(f1Answers(sess.History())))
	}
}

// A real SQLite failure of the fence must not leave the old run's answer
// eligible to start a new run after Stop has successfully returned.
func TestPersistenceReviewStopDoesNotSucceedWithoutDurableFence(t *testing.T) {
	f1Isolate(t)
	reached, release := make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{script: f1Script(false)}, taskSchedulerHooks{
		beforeAdmit: func(string) { reached <- struct{}{}; <-release },
	})
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	sess := e.session(f1KindHuman)
	e.send(sess, "F1-TRIGGER please ask")
	qid := waitPendingAsk(t, sess).ID
	f1Settle(t, sess)
	f1ActiveForStop(t, e, sess)
	if code, body := f1PostAsk(t, e, sess.ID, qid, "yes"); code != 204 {
		t.Fatalf("answer = %d %s", code, body)
	}
	select {
	case <-reached:
	case <-time.After(f1Guard):
		t.Fatal("answer never reserved")
	}
	db, err := sql.Open("sqlite", "file:"+e.mgr.tasks.Path()+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err = db.Exec(`CREATE TRIGGER review_no_fence BEFORE UPDATE OF method ON task_notifications WHEN NEW.kind = 'question_answer' AND NEW.method = 'append' BEGIN SELECT RAISE(ABORT, 'review fence write failed'); END`); err != nil {
		t.Fatal(err)
	}
	before := e.prov.countSession(t, sess.ID)
	stopErr := e.mgr.Cancel(sess.ID)
	pollUntil(t, f1Guard, "idle after stop", func() bool { return sessState(sess) == StateIdle && !sess.runtime.Context().Agent.IsRunning() })
	sess.runtime.Bus.Drain(2 * time.Second)
	if _, err = db.Exec(`DROP TRIGGER review_no_fence`); err != nil {
		t.Fatal(err)
	}
	q, _ := e.mgr.tasks.GetQuestion(context.Background(), qid)
	n, _ := e.mgr.tasks.Notice(context.Background(), q.EventID)
	t.Logf("successful Stop? err=%v, durable state before release=%s/%s/%s", stopErr, n.State, n.Method, q.Work)
	once.Do(func() { close(release) })
	waitDispatcherIdle(e)
	woke := f1Eventually(f1Guard, func() bool { return e.prov.countSession(t, sess.ID) > before })
	if stopErr == nil && woke {
		t.Fatalf("Stop returned success but the pre-Stop accepted answer restarted the model; notice=%s/%s work=%s, calls before=%d after=%d", n.State, n.Method, q.Work, before, e.prov.countSession(t, sess.ID))
	}
}

// Delete settles the database before draining in-flight lifecycle readers.
// Keep such a reader alive, allow a real agent post to finish after settlement,
// then verify it did not leave an actionable question for the deleted session.
func TestPersistenceReviewDeleteSerializesWithPublication(t *testing.T) {
	f1Isolate(t)
	checkpoint, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{script: f1Script(false)})
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	if _, err := e.mgr.tasks.Create(context.Background(), tasks.CreateInput{Title: "initialize database", Place: tasks.PlaceYou}); err != nil {
		t.Fatal(err)
	}
	sess := e.session(f1KindHuman)
	posted := make(chan string, 1)
	sess.runtime.Bus.Subscribe(func(ev bus.AskUserRequested) { posted <- ev.ID })
	e.mgr.questions.hooks.afterCheckpoint = func() { close(checkpoint); <-release }
	e.send(sess, "F1-TRIGGER please ask")
	select {
	case <-checkpoint:
	case <-time.After(f1Guard):
		t.Fatal("post never checkpointed")
	}
	sess.lifecycle.RLock()
	var readerOnce sync.Once
	unlockReader := func() { readerOnce.Do(sess.lifecycle.RUnlock) }
	defer unlockReader()
	deleted := make(chan error, 1)
	go func() { deleted <- e.mgr.Delete(sess.ID) }()
	pollUntil(t, f1Guard, "delete past settlement and waiting for in-flight reader", func() bool { return sess.closing.Load() })
	if _, live := e.mgr.Get(sess.ID); live {
		t.Fatal("delete has not removed loaded session")
	}
	once.Do(func() { close(release) })
	var qid string
	select {
	case qid = <-posted:
	case <-time.After(f1Guard):
		t.Fatal("post never completed after checkpoint")
	}
	f1Settle(t, sess)
	unlockReader()
	select {
	case err := <-deleted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(f1Guard):
		t.Fatal("delete never returned")
	}
	q, err := e.mgr.tasks.GetQuestion(context.Background(), qid)
	if err != nil {
		t.Fatal(err)
	}
	code, body := f1PostAsk(t, e, sess.ID, qid, "yes")
	t.Logf("question after completed delete = %s/%s, answer HTTP=%d %s", q.State, q.Reason, code, body)
	if q.State == tasks.QuestionOpen || code == 204 {
		t.Fatalf("deleted session left an actionable question: state=%s/%s; answering removed session=%d", q.State, q.Reason, code)
	}
}

// Recall only removes queued steers, not the run already reading a durably
// admitted answer. A delayed outbox ack does not turn that receipt back into
// an unadmitted answer.
func TestPersistenceReviewRecallDoesNotStopAlreadyAdmittedWork(t *testing.T) {
	f1Isolate(t)
	attempted, release, reading := make(chan struct{}, 1), make(chan struct{}), make(chan struct{}, 1)
	var once sync.Once
	script := f1Script(false)
	p := &f1Provider{script: func(req core.Request) <-chan core.AssistantEvent {
		if f1LastUserIsAnswer(req) {
			reading <- struct{}{}
			return make(chan core.AssistantEvent)
		}
		return script(req)
	}}
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), p, taskSchedulerHooks{attempted: func(string) { attempted <- struct{}{}; <-release }})
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	sess := e.session(f1KindHuman)
	e.send(sess, "F1-TRIGGER please ask")
	qid := waitPendingAsk(t, sess).ID
	f1Settle(t, sess)
	if code, body := f1PostAsk(t, e, sess.ID, qid, "yes"); code != 204 {
		t.Fatalf("answer=%d %s", code, body)
	}
	select {
	case <-attempted:
	case <-time.After(f1Guard):
		t.Fatal("no attempt")
	}
	select {
	case <-reading:
	case <-time.After(f1Guard):
		t.Fatal("provider did not read answer")
	}
	q, err := e.mgr.tasks.GetQuestion(context.Background(), qid)
	if err != nil {
		t.Fatal(err)
	}
	n, err := e.mgr.tasks.Notice(context.Background(), q.EventID)
	if err != nil || n.State != tasks.NoticeSent || q.Work != tasks.WorkDue {
		t.Fatalf("precondition n=%+v q=%+v err=%v", n, q, err)
	}
	if !sess.persister.has(func(s *session.Session) bool { return transcriptHasNotice(s, q.EventID) }) {
		t.Fatal("precondition: receipt not durable")
	}
	if err := sess.runtime.Bus.Execute(bus.SteerAgent{ID: "review-unrelated", Text: "F1-UNRELATED queued message"}); err != nil {
		t.Fatal(err)
	}
	resp := apiReq(t, e.srv, "POST", "/api/sessions/"+sess.ID+"/steers/cancel", "")
	_ = resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("recall=%d", resp.StatusCode)
	}
	after, _ := e.mgr.tasks.GetQuestion(context.Background(), qid)
	n, _ = e.mgr.tasks.Notice(context.Background(), q.EventID)
	running := sess.runtime.Context().Agent.IsRunning()
	once.Do(func() { close(release) })
	waitDispatcherIdle(e)
	// Stop only for cleanup, after capturing the durable state at recall.
	_ = e.mgr.Cancel(sess.ID)
	t.Logf("recall of unrelated steer: work=%s method=%s, original run still active=%v", after.Work, n.Method, running)
	if after.Work != tasks.WorkDue {
		t.Fatalf("recall canceled recovery of an already-admitted active answer: work=%s method=%s", after.Work, n.Method)
	}
}
