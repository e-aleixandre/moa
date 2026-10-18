package serve

import (
	"context"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/tasks"
)

// A recall commits what becomes of an accepted answer before the queue is
// emptied: the answer stays in the outbox as an append, durably, and starts no
// run, in this process or after a restart.

type f1RecallScene struct {
	e       *f1Env
	sess    *ManagedSession
	qid     string
	eventID string
}

// f1RecallPrepare leaves an accepted answer queued behind a running turn and
// recalls the queue.
func f1RecallPrepare(t *testing.T) f1RecallScene {
	t.Helper()
	f1Isolate(t)
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{script: f1Script(false)})
	sess := e.session(f1KindHuman)
	e.send(sess, "F1-TRIGGER please ask")
	qid := waitPendingAsk(t, sess).ID
	f1Settle(t, sess)
	f1ActiveForStop(t, e, sess)

	if code, body := f1PostAsk(t, e, sess.ID, qid, "yes"); !f1Is2xx(code) {
		t.Fatalf("answer = %d %s, want 2xx", code, body)
	}
	ctx := context.Background()
	q, err := e.mgr.tasks.GetQuestion(ctx, qid)
	if err != nil {
		t.Fatal(err)
	}
	if !f1Eventually(f1Guard, func() bool {
		n, err := e.mgr.tasks.Notice(ctx, q.EventID)
		return err == nil && n.State == tasks.NoticeSent && noticeQueued(sess, n)
	}) {
		t.Fatal("the accepted answer never queued behind the running turn")
	}
	resp := apiReq(t, e.srv, "POST", "/api/sessions/"+sess.ID+"/steers/cancel", "")
	_ = resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("recall = %d, want 204", resp.StatusCode)
	}
	return f1RecallScene{e: e, sess: sess, qid: qid, eventID: q.EventID}
}

func f1AssertFenced(t *testing.T, e *f1Env, qid string) {
	t.Helper()
	q, err := e.mgr.tasks.GetQuestion(context.Background(), qid)
	if err != nil {
		t.Fatal(err)
	}
	n, err := e.mgr.tasks.Notice(context.Background(), q.EventID)
	if err != nil {
		t.Fatal(err)
	}
	if n.Method != tasks.MethodAppend || q.Work != tasks.WorkStopped {
		t.Fatalf("after the recall: method=%s work=%s, want append/stopped", n.Method, q.Work)
	}
}

func TestAskAsyncRecallKeepsAcceptedAnswerAsAppend(t *testing.T) {
	s := f1RecallPrepare(t)
	f1AssertFenced(t, s.e, s.qid)

	before := s.e.prov.countSession(t, s.sess.ID)
	f1Stop(t, s.e, s.sess)
	if !f1Eventually(f1Guard, func() bool { return len(f1Answers(s.sess.History())) == 1 }) {
		t.Fatal("the recalled answer never reached the transcript")
	}
	if !f1Stable(500*time.Millisecond, func() bool {
		return s.e.prov.countSession(t, s.sess.ID) == before && !s.sess.runtime.Context().Agent.IsRunning()
	}) {
		t.Fatalf("the recalled answer started a run (+%d call(s))", s.e.prov.countSession(t, s.sess.ID)-before)
	}
	f1AssertFenced(t, s.e, s.qid)
}

func TestAskAsyncRecallFenceSurvivesRestart(t *testing.T) {
	s := f1RecallPrepare(t)
	base, root := s.e.mgr.sessionBaseDir, s.e.root
	sessionID := s.sess.ID
	s.e.halt()

	prov := &f1Provider{script: f1Script(false)}
	e2 := f1NewEnv(t, base, root, prov)
	f1AssertFenced(t, e2, s.qid)
	sess, err := e2.mgr.ResumeSession(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !f1Eventually(f1Guard, func() bool { return len(f1Answers(sess.History())) == 1 }) {
		t.Fatal("the recalled answer never reached the transcript after the restart")
	}
	if !f1Stable(600*time.Millisecond, func() bool { return prov.count() == 0 && !sess.runtime.Context().Agent.IsRunning() }) {
		t.Fatalf("the recalled answer started a run after the restart (%d call(s))", prov.count())
	}
}
