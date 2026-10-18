package serve

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/tasks"
)

// The fence a Stop commits between an answer's reservation and its admission
// holds against every later attempt of the dispatcher: another trigger in this
// process, and a new process. Same scene as
// TestF1BisREDStopFencesAcceptedAnswerBeforeAdmission, which checks the held
// attempt itself.
func TestAskAsyncStopFenceHoldsAgainstLaterDispatcherAttempts(t *testing.T) {
	f1Isolate(t)
	base, root := t.TempDir(), t.TempDir()
	reached := make(chan string, 16)
	release := make(chan struct{})
	var once sync.Once
	hooks := taskSchedulerHooks{beforeAdmit: func(noticeID string) {
		select {
		case reached <- noticeID:
		default:
		}
		<-release
	}}
	e := f1NewEnv(t, base, root, &f1Provider{script: f1Script(false)}, hooks)
	t.Cleanup(func() { once.Do(func() { close(release) }) })

	sess := e.session(f1KindHuman)
	e.send(sess, "F1-TRIGGER please ask")
	qid := waitPendingAsk(t, sess).ID
	f1Settle(t, sess)
	f1ActiveForStop(t, e, sess)
	if code, body := f1PostAsk(t, e, sess.ID, qid, "yes"); !f1Is2xx(code) {
		t.Fatalf("answer = %d %s", code, body)
	}
	select {
	case <-reached:
	case <-time.After(f1Guard):
		t.Fatal("the answer never reached the reservation")
	}
	before := e.prov.countSession(t, sess.ID)
	f1Stop(t, e, sess)
	once.Do(func() { close(release) })
	waitDispatcherIdle(e)
	sess.runtime.Bus.Drain(2 * time.Second)

	f1AssertFenced(t, e, qid)
	q, _ := e.mgr.tasks.GetQuestion(context.Background(), qid)
	eventID := q.EventID

	// Every trigger the dispatcher has, one after another, each followed by a
	// full pass so the retry has certainly run.
	pass := func() {
		e.mgr.notices.nudge()
		waitDispatcherIdle(e)
	}
	for name, fire := range map[string]func(){
		"session opened": func() { e.mgr.notices.sessionOpened(sess.ID) },
		"session idle":   func() { e.mgr.notices.sessionIdle(sess.ID) },
		"slot freed":     func() { e.mgr.notices.slotFreed() },
		"retry all":      func() { e.mgr.notices.trigger(func() { e.mgr.notices.retryAll = true }) },
	} {
		fire()
		pass()
		if !f1Stable(300*time.Millisecond, func() bool { return e.prov.countSession(t, sess.ID) == before }) {
			t.Fatalf("%s: the fenced answer woke the model", name)
		}
		f1AssertFenced(t, e, qid)
		if n, err := e.mgr.tasks.Notice(context.Background(), eventID); err != nil || n.State == tasks.NoticeDelivered && len(f1Answers(sess.History())) == 0 {
			t.Fatalf("%s: notice = %+v %v", name, n, err)
		}
	}

	// A new process finds the fence in the store, not in memory.
	e.halt()
	prov2 := &f1Provider{script: f1Script(false)}
	e2 := f1NewEnv(t, base, root, prov2)
	e2.mgr.notices.trigger(func() { e2.mgr.notices.retryAll = true })
	if !f1Stable(800*time.Millisecond, func() bool { return prov2.count() == 0 }) {
		t.Fatalf("after a restart the fenced answer woke the model %d time(s)", prov2.count())
	}
	f1AssertFenced(t, e2, qid)
}

// waitDispatcherIdle returns once no attempt holds the dispatcher's lock: an
// attempt in flight when it was called has finished.
func waitDispatcherIdle(e *f1Env) {
	done := make(chan struct{})
	go func() {
		e.mgr.notices.mu.Lock()
		defer e.mgr.notices.mu.Unlock()
		close(done)
	}()
	<-done
}
