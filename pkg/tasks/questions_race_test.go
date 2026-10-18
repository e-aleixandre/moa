package tasks

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// An answer and a close of the same question race. The first transaction to
// hold the question decides: the hook parks it inside its transaction, the
// contender starts meanwhile, and only then is the first released. The
// contender can only commit after it, sees the terminal state, and leaves it
// as it is. No sleeps: the order is the one the test imposes.
func TestAnswerAndCloseRaceInBothOrders(t *testing.T) {
	for _, closeTo := range []QuestionState{QuestionWithdrawn, QuestionDismissed} {
		t.Run("answer first/"+string(closeTo), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), DatabaseName)
			r := openRepo(t, path)
			q, _, _ := r.InsertQuestion(bg, newQuestion("s1", "tc1"))

			reached, release := make(chan struct{}), make(chan struct{})
			r.hookBeforeQuestionEvent = func() error {
				close(reached)
				<-release
				return nil
			}
			answered := make(chan error, 1)
			go func() {
				_, err := r.AnswerQuestion(bg, "s1", q.ID, []string{"yes"}, human, renderTest)
				answered <- err
			}()
			<-reached
			closed := make(chan error, 1)
			go func() {
				_, _, err := r.CloseQuestion(bg, "s1", q.ID, closeTo, "no longer needed")
				closed <- err
			}()
			close(release)
			if err := <-answered; err != nil {
				t.Fatalf("the first transaction lost: %v", err)
			}
			var conflict *ErrQuestionConflict
			if err := <-closed; !errors.As(err, &conflict) || conflict.State != QuestionAnswered {
				t.Fatalf("close after the answer = %v, want a conflict with the answer", err)
			}
			got, _ := r.GetQuestion(bg, q.ID)
			if got.State != QuestionAnswered || got.Reason != "" || got.EventID == "" {
				t.Fatalf("the loser changed the winner: %+v", got)
			}
			if n := countQuestionRows(t, path, "SELECT COUNT(*) FROM task_notifications WHERE kind = 'question_answer'"); n != 1 {
				t.Fatalf("notices = %d, want 1", n)
			}
		})

		t.Run("close first/"+string(closeTo), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), DatabaseName)
			r := openRepo(t, path)
			q, _, _ := r.InsertQuestion(bg, newQuestion("s1", "tc1"))

			reached, release := make(chan struct{}), make(chan struct{})
			r.hookBeforeQuestionClose = func() {
				close(reached)
				<-release
			}
			closed := make(chan error, 1)
			go func() {
				_, _, err := r.CloseQuestion(bg, "s1", q.ID, closeTo, "no longer needed")
				closed <- err
			}()
			<-reached
			answered := make(chan error, 1)
			go func() {
				_, err := r.AnswerQuestion(bg, "s1", q.ID, []string{"yes"}, human, renderTest)
				answered <- err
			}()
			close(release)
			if err := <-closed; err != nil {
				t.Fatalf("the first transaction lost: %v", err)
			}
			var conflict *ErrQuestionConflict
			select {
			case err := <-answered:
				if !errors.As(err, &conflict) || conflict.State != closeTo {
					t.Fatalf("answer after the close = %v, want a conflict with %s", err, closeTo)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the answer never finished")
			}
			got, _ := r.GetQuestion(bg, q.ID)
			if got.State != closeTo || got.EventID != "" || len(got.Answers) != 0 {
				t.Fatalf("the loser changed the winner: %+v", got)
			}
			if n := countQuestionRows(t, path, "SELECT COUNT(*) FROM task_notifications WHERE kind = 'question_answer'"); n != 0 {
				t.Fatalf("notices = %d, want none", n)
			}
		})
	}
}
