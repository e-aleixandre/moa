package tasks

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/e-aleixandre/moa/pkg/askuser"
)

func newQuestion(session, call string) Question {
	return Question{
		ID: NewQuestionID(), SessionID: session, OriginMsgID: "m-" + call, OriginToolCallID: call, OriginTool: OriginAskUser,
		Questions: []askuser.Question{{Text: "Deploy?", Options: []string{"yes", "no"}}},
		Ack:       QuestionAck{Text: "open", Custom: map[string]any{"question_state": "open"}},
	}
}

func renderTest(q Question) (string, string) { return "answer " + q.ID, "A: " + q.Answers[0] }

var human = askuser.Responder{Kind: askuser.ResponderUser, Via: "device:d1"}

func countQuestionRows(t *testing.T, path, query string, args ...any) int {
	t.Helper()
	db, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// The questions compat level lands on a database that already holds compat 1
// data: the outbox keeps every row (rowids included) and takes the new kind.
func TestQuestionsCompatMigrationFromCompat1KeepsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg", DatabaseName)
	buildV4(t, path)
	db, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if tooNew, err := migrateWith(db, "", migrations, compatMigrations[:1]); err != nil || tooNew {
		t.Fatalf("compat 1: tooNew=%v err=%v", tooNew, err)
	}
	if _, err := db.Exec(`INSERT INTO task_notifications(id,task_id,kind,recipient_session_id,deliver,method,title,body,state,created_at,updated_at)
		VALUES ('tn_h',4,'handoff','s1','hold','run','t','b','pending',9,9)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if v := compatVersion(t, path); v != 1 {
		t.Fatalf("fixture compat = %d, want 1", v)
	}
	before := snapshot(t, path)

	r := openRepo(t, path)
	if _, err := r.Revision(bg); err != nil {
		t.Fatal(err)
	}
	if v := compatVersion(t, path); v != 2 {
		t.Fatalf("compat = %d, want 2", v)
	}
	if v := pragmaInt(t, path, "PRAGMA user_version"); v != 4 {
		t.Fatalf("user_version = %d, want 4", v)
	}
	if after := snapshot(t, path); after != before {
		t.Fatalf("the questions migration changed existing data:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if _, err := os.Stat(path + ".pre-compat2.bak"); err != nil {
		t.Fatalf("no backup before compat 2: %v", err)
	}
	for _, idx := range []string{"task_notifications_task", "task_notifications_open", "questions_one_open"} {
		if n := pragmaInt(t, path, "SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='"+idx+"'"); n != 1 {
			t.Fatalf("index %s missing", idx)
		}
	}
	q, _, err := r.InsertQuestion(bg, newQuestion("s1", "tc1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.AnswerQuestion(bg, "s1", q.ID, []string{"yes"}, human, renderTest); err != nil {
		t.Fatalf("a question answer does not fit the migrated outbox: %v", err)
	}
}

func TestInsertQuestionIsIdempotentPerInvocationAndBoundedPerSession(t *testing.T) {
	r := openRepo(t, filepath.Join(t.TempDir(), DatabaseName))
	first, existed, err := r.InsertQuestion(bg, newQuestion("s1", "tc1"))
	if err != nil || existed {
		t.Fatalf("first insert: existed=%v err=%v", existed, err)
	}
	retry := newQuestion("s1", "tc1")
	got, existed, err := r.InsertQuestion(bg, retry)
	if err != nil || !existed || got.ID != first.ID || got.Ack.Text != "open" {
		t.Fatalf("retry = %+v existed=%v err=%v, want the first question back", got, existed, err)
	}
	var open *ErrQuestionOpen
	if _, _, err := r.InsertQuestion(bg, newQuestion("s1", "tc2")); !errors.As(err, &open) || open.ID != first.ID {
		t.Fatalf("second open question = %v, want ErrQuestionOpen for %s", err, first.ID)
	}
	// Another session has its own bound.
	if _, _, err := r.InsertQuestion(bg, newQuestion("s2", "tc1")); err != nil {
		t.Fatalf("another session's question: %v", err)
	}
	// Once closed, the session may ask again, and the old call still finds its own.
	if _, _, err := r.CloseQuestion(bg, "s1", first.ID, QuestionWithdrawn, "x"); err != nil {
		t.Fatal(err)
	}
	if _, existed, err := r.InsertQuestion(bg, newQuestion("s1", "tc2")); err != nil || existed {
		t.Fatalf("question after the first closed: existed=%v err=%v", existed, err)
	}
	if got, ok, err := r.QuestionByInvocation(bg, "s1", "m-tc1", "tc1"); err != nil || !ok || got.ID != first.ID || got.State != QuestionWithdrawn {
		t.Fatalf("by invocation = %+v ok=%v err=%v", got, ok, err)
	}
}

func TestAnswerQuestionIsOneTransactionWithItsNotice(t *testing.T) {
	path := filepath.Join(t.TempDir(), DatabaseName)
	r := openRepo(t, path)
	q, _, _ := r.InsertQuestion(bg, newQuestion("s1", "tc1"))

	// A failure after the question was read and before its notice: neither lands.
	r.hookBeforeQuestionEvent = func() error { return errors.New("disk full") }
	if _, err := r.AnswerQuestion(bg, "s1", q.ID, []string{"yes"}, human, renderTest); err == nil {
		t.Fatal("answer succeeded despite the failing transaction")
	}
	r.hookBeforeQuestionEvent = nil
	if got, _ := r.GetQuestion(bg, q.ID); got.State != QuestionOpen || got.EventID != "" {
		t.Fatalf("question after a failed answer = %+v, want it still open", got)
	}
	if n := countQuestionRows(t, path, "SELECT COUNT(*) FROM task_notifications WHERE kind = 'question_answer'"); n != 0 {
		t.Fatalf("%d notice(s) after a failed answer", n)
	}

	res, err := r.AnswerQuestion(bg, "s1", q.ID, []string{"yes"}, human, renderTest)
	if err != nil || res.Duplicate {
		t.Fatalf("answer: %+v %v", res, err)
	}
	got, _ := r.GetQuestion(bg, q.ID)
	if got.State != QuestionAnswered || got.Work != WorkDue || got.Responder != human || got.Answers[0] != "yes" || got.EventID == "" {
		t.Fatalf("answered question = %+v", got)
	}
	n, err := r.Notice(bg, got.EventID)
	if err != nil || n.Kind != NoticeQuestionAnswer || n.TaskID != 0 || n.RecipientSessionID != "s1" ||
		n.State != NoticePending || n.Method != MethodRun || n.Deliver != DeliverWake || n.Text != "A: yes" {
		t.Fatalf("notice = %+v, %v", n, err)
	}

	// The same answer from the same responder is accepted again and writes nothing.
	again, err := r.AnswerQuestion(bg, "s1", q.ID, []string{"yes"}, human, renderTest)
	if err != nil || !again.Duplicate || again.Question.EventID != got.EventID {
		t.Fatalf("retry = %+v, %v", again, err)
	}
	if n := countQuestionRows(t, path, "SELECT COUNT(*) FROM task_notifications WHERE kind = 'question_answer'"); n != 1 {
		t.Fatalf("%d notices after a retry, want 1", n)
	}
	var conflict *ErrQuestionConflict
	if _, err := r.AnswerQuestion(bg, "s1", q.ID, []string{"no"}, human, renderTest); !errors.As(err, &conflict) || conflict.State != QuestionAnswered {
		t.Fatalf("different answer = %v, want a conflict", err)
	}
	other := askuser.Responder{Kind: askuser.ResponderUser, Via: "device:d2"}
	if _, err := r.AnswerQuestion(bg, "s1", q.ID, []string{"yes"}, other, renderTest); !errors.As(err, &conflict) {
		t.Fatalf("another actor repeating the answer = %v, want a conflict", err)
	}
	if _, _, err := r.CloseQuestion(bg, "s1", q.ID, QuestionWithdrawn, ""); !errors.As(err, &conflict) {
		t.Fatalf("withdraw after the answer = %v, want a conflict", err)
	}
	if got, _ := r.GetQuestion(bg, q.ID); got.State != QuestionAnswered {
		t.Fatalf("a losing close erased the answer: %+v", got)
	}
}

func TestAnswerQuestionRefusals(t *testing.T) {
	r := openRepo(t, filepath.Join(t.TempDir(), DatabaseName))
	tap := newQuestion("s1", "tc1")
	tap.RequiresTap = true
	q, _, _ := r.InsertQuestion(bg, tap)

	for _, who := range []askuser.Responder{
		{Kind: askuser.ResponderOwner, ID: "o"}, {Kind: askuser.ResponderAutomation}, {Kind: askuser.ResponderVoice, CallID: "c"},
		{}, {Kind: "User"}, {Kind: "admin"},
	} {
		if _, err := r.AnswerQuestion(bg, "s1", q.ID, []string{"yes"}, who, renderTest); !errors.Is(err, askuser.ErrRequiresTap) {
			t.Errorf("%+v on a tap question = %v, want ErrRequiresTap", who, err)
		}
	}
	if _, err := r.AnswerQuestion(bg, "s1", q.ID, []string{"a", "b"}, human, renderTest); !errors.Is(err, ErrAnswerCount) {
		t.Errorf("two answers for one question = %v", err)
	}
	if _, err := r.AnswerQuestion(bg, "s2", q.ID, []string{"yes"}, human, renderTest); !errors.Is(err, ErrQuestionNotFound) {
		t.Errorf("another session's id = %v, want not found", err)
	}
	if _, err := r.AnswerQuestion(bg, "s1", "q_nope", []string{"yes"}, human, renderTest); !errors.Is(err, ErrQuestionNotFound) {
		t.Errorf("unknown id = %v, want not found", err)
	}
	if got, _ := r.GetQuestion(bg, q.ID); got.State != QuestionOpen {
		t.Fatalf("a refusal consumed the question: %+v", got)
	}
	if _, err := r.AnswerQuestion(bg, "s1", q.ID, []string{"yes"}, human, renderTest); err != nil {
		t.Fatalf("the human could not answer after the refusals: %v", err)
	}
}

func TestCloseQuestionIsIdempotentAndLosesToAnAnswer(t *testing.T) {
	r := openRepo(t, filepath.Join(t.TempDir(), DatabaseName))
	q, _, _ := r.InsertQuestion(bg, newQuestion("s1", "tc1"))
	if _, changed, err := r.CloseQuestion(bg, "s1", q.ID, QuestionDismissed, ""); err != nil || !changed {
		t.Fatalf("dismiss: changed=%v err=%v", changed, err)
	}
	if _, changed, err := r.CloseQuestion(bg, "s1", q.ID, QuestionDismissed, ""); err != nil || changed {
		t.Fatalf("repeated dismiss: changed=%v err=%v", changed, err)
	}
	var conflict *ErrQuestionConflict
	if _, _, err := r.CloseQuestion(bg, "s1", q.ID, QuestionWithdrawn, ""); !errors.As(err, &conflict) || conflict.State != QuestionDismissed {
		t.Fatalf("withdraw after dismiss = %v", err)
	}
	if _, err := r.AnswerQuestion(bg, "s1", q.ID, []string{"yes"}, human, renderTest); !errors.As(err, &conflict) || conflict.State != QuestionDismissed {
		t.Fatalf("answer after dismiss = %v", err)
	}
	if _, _, err := r.CloseQuestion(bg, "s2", q.ID, QuestionDismissed, ""); !errors.Is(err, ErrQuestionNotFound) {
		t.Fatalf("another session dismissing = %v", err)
	}
}

// Many contenders, one winner, one notice.
func TestConcurrentAnswersHaveASingleWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), DatabaseName)
	r := openRepo(t, path)
	q, _, _ := r.InsertQuestion(bg, newQuestion("s1", "tc1"))
	const n = 12
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			who := askuser.Responder{Kind: askuser.ResponderUser, Via: fmt.Sprintf("device:%d", i)}
			_, errs[i] = r.AnswerQuestion(bg, "s1", q.ID, []string{fmt.Sprint("a", i)}, who, renderTest)
		}()
	}
	wg.Wait()
	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("winners = %d, want 1: %v", wins, errs)
	}
	if c := countQuestionRows(t, path, "SELECT COUNT(*) FROM task_notifications WHERE kind = 'question_answer'"); c != 1 {
		t.Fatalf("notices = %d, want 1", c)
	}
}

func TestFenceQuestionAnswers(t *testing.T) {
	r := openRepo(t, filepath.Join(t.TempDir(), DatabaseName))
	q, _, _ := r.InsertQuestion(bg, newQuestion("s1", "tc1"))
	res, err := r.AnswerQuestion(bg, "s1", q.ID, []string{"yes"}, human, renderTest)
	if err != nil {
		t.Fatal(err)
	}
	ev := res.Question.EventID
	if _, err := r.SetNoticeState(bg, ev, NoticeChange{From: []string{NoticePending}, State: NoticeSent, SteerID: "st"}); err != nil {
		t.Fatal(err)
	}
	// A recall fences what was not admitted; the work it owed goes with it.
	if n, err := r.FenceQuestionAnswers(bg, "s1", false); err != nil || n == 0 {
		t.Fatalf("fence: n=%d err=%v", n, err)
	}
	notice, _ := r.Notice(bg, ev)
	got, _ := r.GetQuestion(bg, q.ID)
	if notice.Method != MethodAppend || got.Work != WorkStopped {
		t.Fatalf("fenced: method=%s work=%s, want append/stopped", notice.Method, got.Work)
	}
	// Another session is untouched.
	q2, _, _ := r.InsertQuestion(bg, newQuestion("s2", "tc1"))
	res2, _ := r.AnswerQuestion(bg, "s2", q2.ID, []string{"yes"}, human, renderTest)
	if _, err := r.FenceQuestionAnswers(bg, "s1", true); err != nil {
		t.Fatal(err)
	}
	if n2, _ := r.Notice(bg, res2.Question.EventID); n2.Method != MethodRun {
		t.Fatalf("another session's answer was fenced: %+v", n2)
	}
	// An admitted answer (delivered) keeps its transport but a Stop cancels its run.
	if _, err := r.SetNoticeState(bg, res2.Question.EventID, NoticeChange{From: []string{NoticePending}, State: NoticeDelivered}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.FenceQuestionAnswers(bg, "s2", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.GetQuestion(bg, q2.ID); got.Work != WorkDue {
		t.Fatalf("a recall stopped the work of an admitted answer: %s", got.Work)
	}
	if _, err := r.FenceQuestionAnswers(bg, "s2", true); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.GetQuestion(bg, q2.ID); got.Work != WorkStopped {
		t.Fatalf("Stop left the admitted answer's work %s, want stopped", got.Work)
	}
}

func TestQuestionWorkLifecycle(t *testing.T) {
	r := openRepo(t, filepath.Join(t.TempDir(), DatabaseName))
	q, _, _ := r.InsertQuestion(bg, newQuestion("s1", "tc1"))
	if owes, _ := r.SessionOwesWork(bg, "s1"); !owes {
		t.Fatal("an open question is not owed work")
	}
	res, _ := r.AnswerQuestion(bg, "s1", q.ID, []string{"yes"}, human, renderTest)
	if due, _ := r.DueQuestionWork(bg); len(due) != 1 || due[0].ID != q.ID {
		t.Fatalf("due work = %+v", due)
	}
	if ok, err := r.SetQuestionWork(bg, res.Question.EventID, WorkDue, WorkDone); err != nil || !ok {
		t.Fatalf("done: %v %v", ok, err)
	}
	if ok, _ := r.SetQuestionWork(bg, res.Question.EventID, WorkDue, WorkStopped); ok {
		t.Fatal("work moved twice")
	}
	if owes, _ := r.SessionOwesWork(bg, "s1"); owes {
		t.Fatal("a finished answer still owes work")
	}
	if err := r.InvalidateSessionQuestions(bg, "s2", "session_deleted"); err != nil {
		t.Fatal(err)
	}
}
