package serve

import (
	"os"
	"testing"
	"time"
)

// The receipt comes before the inference: an accepted answer whose admission
// cannot be saved never reaches the model. The store failure is real (the
// session's directory becomes a file), not a mock.
func TestAskAsyncAnswerIsNotReadWhenItsReceiptCannotBeSaved(t *testing.T) {
	e, sess, qid := f1Open(t, f1KindHuman, false)
	before := e.prov.countSession(t, sess.ID)

	dir := sess.persister.store.Dir()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("blocked real store"), 0o600); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(dir); _ = os.MkdirAll(dir, 0o700) }()

	if code, body := f1PostAsk(t, e, sess.ID, qid, "yes"); code != 204 {
		t.Fatalf("answer = %d %s, want 204 (accepting is durable in the task store, not the transcript)", code, body)
	}
	if !f1Eventually(f1Guard, func() bool { return len(f1Answers(sess.History())) == 1 }) {
		t.Fatal("the answer was never admitted to the session")
	}
	pollUntil(t, f1Guard, "the run ended on the failed save", func() bool {
		return sessState(sess) == StateError && !sess.runtime.Context().Agent.IsRunning()
	})
	if !f1Stable(300*time.Millisecond, func() bool { return e.prov.countSession(t, sess.ID) == before }) {
		t.Fatalf("the model was called %d time(s) after an answer whose receipt could not be saved", e.prov.countSession(t, sess.ID)-before)
	}
}
