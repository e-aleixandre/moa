package serve

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/tasks"
)

func TestAuthorityReviewSendDoesNotAnswerAnAsyncQuestion(t *testing.T) {
	for _, saved := range []bool{false, true} {
		name := "loaded"
		if saved {
			name = "saved"
		}
		t.Run(name, func(t *testing.T) {
			e, info, ownerSess, child, qid := ownerChildWithQuestion(t, false, "owner")
			store, err := e.mgr.ownerStore()
			if err != nil {
				t.Fatal(err)
			}
			info.Owner.AnswerAsks = false
			if err := store.Save(info.Owner); err != nil {
				t.Fatal(err)
			}
			if saved {
				f1Unload(t, e, child.ID)
			}
			result := runSessionsTool(t, ownerSess, map[string]any{"action": "send", "session_id": child.ID, "text": "F1-NEXT independent status request"})
			q := f1QuestionState(t, e, qid)
			t.Logf("sessions send: error=%v text=%q; durable state=%s responder=%+v answers=%q", result.IsError, toolText(result), q.State, q.Responder, q.Answers)
			if q.State != tasks.QuestionOpen {
				t.Errorf("sessions send consumed the ordinary async question with AnswerAsks=false: state=%s responder=%+v answers=%q", q.State, q.Responder, q.Answers)
			}
			if result.IsError {
				t.Errorf("independent send was refused: %s", toolText(result))
			}
		})
	}
}

func TestAuthorityReviewSendToHumanAsyncSessionIsNotBlockedByOpenQuestion(t *testing.T) {
	e, _, ownerSess, child, qid := ownerChildWithQuestion(t, false, "user")
	result := runSessionsTool(t, ownerSess, map[string]any{"action": "send", "session_id": child.ID, "text": "F1-NEXT independent project information"})
	t.Logf("sessions send: error=%v text=%q", result.IsError, toolText(result))
	if result.IsError {
		t.Errorf("an open async question does not block reading a message, but sessions send was refused: %s", toolText(result))
	}
	if q := f1QuestionState(t, e, qid); q.State != tasks.QuestionOpen {
		t.Errorf("send consumed question: %+v", q)
	}
}


func TestAuthorityReviewCallbackDoneWaitsForTheAcceptedAnswer(t *testing.T) {
	f1Isolate(t)
	reached := make(chan struct{}, 4)
	release := make(chan struct{})
	var once sync.Once
	hooks := taskSchedulerHooks{beforeAdmit: func(string) { reached <- struct{}{}; <-release }}
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{script: f1Script(false)}, hooks)
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	sess := e.session(f1KindHuman)
	out := &f1Outcomes{}
	subscribeRunOutcomes(sess, out.add)
	e.send(sess, "F1-TRIGGER please ask")
	qid := waitPendingAsk(t, sess).ID
	f1Settle(t, sess)
	if !f1Eventually(f1Guard, func() bool { return len(out.statuses()) == 1 }) {
		t.Fatalf("missing needs_input: %v", out.statuses())
	}
	sess.runtime.WaitQuiescent(context.Background())
	if code, body := f1PostAsk(t, e, sess.ID, qid, "yes"); code != http.StatusNoContent {
		t.Fatalf("answer=%d %s", code, body)
	}
	select {
	case <-reached:
	case <-time.After(f1Guard):
		t.Fatal("dispatcher not reached")
	}
	if !sess.runtime.Context().AnswerWorkOwed() {
		t.Fatal("answer no longer owed before admission")
	}
	e.send(sess, "F1-NEXT independent work, not the answer")
	f1Settle(t, sess)
	sess.runtime.WaitQuiescent(context.Background())
	f1Eventually(time.Second, func() bool { return len(out.statuses()) > 1 })
	t.Logf("outcomes=%v owed=%v admitted_answer_events=%d", out.statuses(), sess.runtime.Context().AnswerWorkOwed(), len(f1Answers(sess.History())))
	for _, status := range out.statuses() {
		if status == callbackStatusDone {
			t.Errorf("done emitted before the answer's continuation while AnswerWorkOwed=true")
		}
	}
	once.Do(func() { close(release) })
}

