package tasks

import (
	"path/filepath"
	"testing"
)

// A deleted session takes its open questions, its unadmitted answers and the
// work they owed with it; another session's are untouched.
func TestSettleSessionDeletedClosesQuestionsAndFailsUnadmittedAnswers(t *testing.T) {
	r := openRepo(t, filepath.Join(t.TempDir(), DatabaseName))
	open, _, _ := r.InsertQuestion(bg, newQuestion("gone", "tc-open"))
	other, _, _ := r.InsertQuestion(bg, newQuestion("kept", "tc-kept"))
	// A session holds one open question: answer the first, then ask another.
	res, err := r.AnswerQuestion(bg, "gone", open.ID, []string{"yes"}, human, renderTest)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := r.InsertQuestion(bg, newQuestion("gone", "tc-second"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.SettleSessionDeleted(bg, "gone"); err != nil {
		t.Fatal(err)
	}
	if q, _ := r.GetQuestion(bg, second.ID); q.State != QuestionInvalidated || q.Reason != ReasonSessionDeleted {
		t.Fatalf("open question after delete: %s/%s", q.State, q.Reason)
	}
	n, err := r.Notice(bg, res.Question.EventID)
	if err != nil || n.State != NoticeFailed || n.Reason != ReasonSessionDeleted {
		t.Fatalf("unadmitted answer after delete: %+v %v", n, err)
	}
	if q, _ := r.GetQuestion(bg, open.ID); q.Work != WorkStopped {
		t.Fatalf("owed work after delete = %q, want stopped", q.Work)
	}
	if q, _ := r.GetQuestion(bg, other.ID); q.State != QuestionOpen {
		t.Fatalf("another session's question was touched: %s", q.State)
	}
}

// AbandonQuestionAnswer fails a not-yet-admitted answer and stops its work in
// one step, and leaves an admitted one (delivered) alone.
func TestAbandonQuestionAnswer(t *testing.T) {
	r := openRepo(t, filepath.Join(t.TempDir(), DatabaseName))
	q, _, _ := r.InsertQuestion(bg, newQuestion("s1", "tc1"))
	res, _ := r.AnswerQuestion(bg, "s1", q.ID, []string{"yes"}, human, renderTest)
	ok, err := r.AbandonQuestionAnswer(bg, res.Question.EventID, ReasonRewound)
	if err != nil || !ok {
		t.Fatalf("abandon = %v %v", ok, err)
	}
	if n, _ := r.Notice(bg, res.Question.EventID); n.State != NoticeFailed || n.Reason != ReasonRewound {
		t.Fatalf("notice = %+v", n)
	}
	if got, _ := r.GetQuestion(bg, q.ID); got.Work != WorkStopped || got.State != QuestionAnswered {
		t.Fatalf("question after abandon: %s/%s", got.State, got.Work)
	}

	q2, _, _ := r.InsertQuestion(bg, newQuestion("s1", "tc2"))
	res2, _ := r.AnswerQuestion(bg, "s1", q2.ID, []string{"no"}, human, renderTest)
	if _, err := r.SetNoticeState(bg, res2.Question.EventID, NoticeChange{From: []string{NoticePending}, State: NoticeDelivered}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.AbandonQuestionAnswer(bg, res2.Question.EventID, ReasonRewound); ok {
		t.Fatal("an admitted answer was abandoned")
	}
}
