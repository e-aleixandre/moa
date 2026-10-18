package serve

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// An answer and a withdrawal or a dismissal of the same question, through the
// real entry points and in both orders: the first one in the durable order
// wins, the other is refused and leaves the winner (its event, its state, its
// projection) as it was. The forced interleaving inside one transaction is in
// tasks.TestAnswerAndCloseRaceInBothOrders.

func f1Dismiss(t *testing.T, e *f1Env, sessionID, qid string) int {
	t.Helper()
	resp := automationReq(t, e.srv, "/api/sessions/"+sessionID+"/questions/"+qid+"/dismiss", "", `{}`, true)
	_ = resp.Body.Close()
	return resp.StatusCode
}

func f1Withdraw(t *testing.T, sess *ManagedSession, qid string) core.Result {
	t.Helper()
	res, err := f1AskUserTool(t, sess).Execute(context.Background(),
		map[string]any{"action": "withdraw", "question_id": qid, "reason": "no longer needed"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestAskAsyncAnswerVersusCloseInBothOrders(t *testing.T) {
	closers := map[string]func(t *testing.T, e *f1Env, sess *ManagedSession, qid string) (refused bool){
		"withdraw": func(t *testing.T, e *f1Env, sess *ManagedSession, qid string) bool {
			res := f1Withdraw(t, sess, qid)
			// Losing to an answer is reported as text, not as a failure of the
			// tool; winning says it was withdrawn.
			return !strings.HasSuffix(strings.TrimSpace(toolText(res)), "authorizes nothing.")
		},
		"dismiss": func(t *testing.T, e *f1Env, sess *ManagedSession, qid string) bool {
			switch code := f1Dismiss(t, e, sess.ID, qid); code {
			case 204:
				return false
			case 409:
				return true
			default:
				t.Fatalf("dismiss = %d, want 204 or 409", code)
				return false
			}
		},
	}
	for name, closer := range closers {
		t.Run(name+" after the answer", func(t *testing.T) {
			e, sess, qid := f1Open(t, f1KindHuman, false)
			if code, body := f1PostAsk(t, e, sess.ID, qid, "yes"); !f1Is2xx(code) {
				t.Fatalf("answer = %d %s", code, body)
			}
			if !f1Eventually(f1Guard, func() bool { return len(f1Answers(sess.History())) == 1 && sessState(sess) == StateIdle }) {
				t.Fatal("the answer never reached the model")
			}
			if !closer(t, e, sess, qid) {
				t.Fatalf("%s after an answer was accepted", name)
			}
			q := f1QuestionState(t, e, qid)
			if q.State != tasks.QuestionAnswered || q.Answers[0] != "yes" {
				t.Fatalf("the loser changed the answer: %+v", q)
			}
			if !f1Stable(300*time.Millisecond, func() bool { return len(f1Answers(sess.History())) == 1 }) {
				t.Fatal("the answer's event was duplicated or removed")
			}
		})
		t.Run(name+" before the answer", func(t *testing.T) {
			e, sess, qid := f1Open(t, f1KindHuman, false)
			if closer(t, e, sess, qid) {
				t.Fatalf("%s of an open question was refused", name)
			}
			before := e.prov.countSession(t, sess.ID)
			if code, _ := f1PostAsk(t, e, sess.ID, qid, "yes"); code != 409 {
				t.Fatalf("answer after %s = %d, want 409", name, code)
			}
			q := f1QuestionState(t, e, qid)
			if q.State == tasks.QuestionAnswered || q.EventID != "" {
				t.Fatalf("the loser reached the store: %+v", q)
			}
			if f1Pending(sess) != nil {
				t.Fatalf("a closed question is still pending")
			}
			if !f1Stable(400*time.Millisecond, func() bool {
				return len(f1Answers(sess.History())) == 0 && e.prov.countSession(t, sess.ID) == before
			}) {
				t.Fatal("a refused answer woke the model or produced an event")
			}
		})
	}
}
