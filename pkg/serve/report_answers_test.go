package serve

// Acceptance criteria (Gherkin) for "a report says when a question was already
// answered":
//
//	Scenario: the owner answers, then the report of the turn before arrives
//	  Given a session with origin owner that asks q1 asynchronously and ends its turn saying it waits
//	  And the owner answers q1 with sessions answer before that turn's "done" report is delivered
//	  When the owner receives the "done" report
//	  Then it says "answered (ask_id q1) by owner:<name>" and "already delivered to the session; do not relay it"
//	  And its "now:" line does not say "it will not continue unless someone sends it a message" while the session works
//
//	Scenario: the needs_input report is delivered after the answer
//	  Given a needs_input report of q1 held because the owner was busy
//	  And the user answers q1
//	  When the batch is delivered
//	  Then it has no "asking (ask_id q1)" and has "answered (ask_id q1) by user at"
//
//	Scenario: the user answers in the session
//	  When the owner receives "the user answered ask_id q1"
//	  Then the entry says "already delivered to the session"
//	  And the footer asks to write down what they decide and send only what the session does not have
//
//	Scenario: the result of sessions answer warns about the earlier report
//	  When the owner answers q1 with sessions answer
//	  Then the result says the session has the answer and not to resend it
//
//	Scenario: the questions cannot be read
//	  Given reading the questions fails
//	  When the batch is painted
//	  Then the report is painted exactly as today

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/owner"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// heldOwnerChildWithQuestion is ownerChildWithQuestion with the owner busy
// from the start, so every report of the child is retained in the batch until
// the test releases the owner (releaseOwner). That is the real way a report
// outlives the answer: the owner was working when it was made.
func heldOwnerChildWithQuestion(t *testing.T) (*f1Env, *ManagedSession, *ManagedSession, string) {
	t.Helper()
	shortReportWindow(t, time.Hour) // only the owner's run ending delivers
	f1Isolate(t)
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{script: f1Script(false)})
	info, err := e.mgr.CreateOwner(CreateOwnerOpts{Root: e.root, Name: "Winerim"})
	if err != nil {
		t.Fatal(err)
	}
	ownerSess, _ := e.mgr.Get(info.SessionID)
	e.send(ownerSess, "F1-HOLD the owner is working")
	pollUntil(t, f1Guard, "owner running", func() bool { return sessState(ownerSess) == StateRunning })
	child, err := e.mgr.CreateSession(CreateOpts{CWD: e.root, Origin: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	e.send(child, "F1-TRIGGER please ask")
	qid := waitPendingAsk(t, child).ID
	f1Settle(t, child)
	return e, ownerSess, child, qid
}

// childHasAnswer waits for the answer to be in the child's conversation and
// for the run it started to end.
func childHasAnswer(t *testing.T, e *f1Env, child *ManagedSession, qid string) {
	t.Helper()
	pollUntil(t, f1Guard, "the answer in the child", func() bool {
		for _, m := range child.History() {
			if m.Custom != nil && m.Custom["source"] == "question_answer" && m.Custom["question_id"] == qid {
				return true
			}
		}
		return false
	})
	pollUntil(t, f1Guard, "the child replying to the answer", func() bool {
		return strings.Contains(f1Blob(child.History()), "F1-GOT-ANSWER")
	})
	f1Settle(t, child)
	waitOutboxed(t, e, child, "F1-GOT-ANSWER")
}

// waitOutboxed waits for the child's report saying marker to be queued for
// the owner, so a release delivers it in the same batch.
func waitOutboxed(t *testing.T, e *f1Env, child *ManagedSession, marker string) {
	t.Helper()
	store, err := e.mgr.ownerStore()
	if err != nil {
		t.Fatal(err)
	}
	own, found, err := store.FindByDir(child.CWD)
	if err != nil || !found {
		t.Fatalf("owner of the child: found=%v err=%v", found, err)
	}
	pollUntil(t, f1Guard, "the child's report queued for the owner", func() bool {
		for _, rep := range outbox(t, e.mgr, own.CodebaseKey) {
			if rep.SessionID == child.ID && strings.Contains(rep.FinalText, marker) {
				return true
			}
		}
		return false
	})
}

// releaseOwner stops the owner's run, which delivers the retained batch, and
// returns its text.
func releaseOwner(t *testing.T, e *f1Env, ownerSess *ManagedSession) string {
	t.Helper()
	if err := e.mgr.Cancel(ownerSess.ID); err != nil {
		t.Fatal(err)
	}
	return waitForOwnerReports(t, ownerSess, 1)[0]
}

// reportEntry is the entry of a batch whose text contains marker.
func reportEntry(t *testing.T, batch, marker string) string {
	t.Helper()
	for _, entry := range strings.Split(batch, "\n- ") {
		if strings.Contains(entry, marker) {
			return entry
		}
	}
	t.Fatalf("no entry with %q in:\n%s", marker, batch)
	return ""
}

func TestStaleTurnReportSaysTheOwnersAnswerWasDelivered(t *testing.T) {
	e, ownerSess, child, qid := heldOwnerChildWithQuestion(t)
	text, isErr := f1OwnerAnswers(t, ownerSess, child.ID, qid)
	if isErr {
		t.Fatal(text)
	}
	childHasAnswer(t, e, child, qid)
	// Working again when the owner reads the report.
	e.send(child, "F1-HOLD more work")
	pollUntil(t, f1Guard, "child running", func() bool { return sessState(child) == StateRunning })

	batch := releaseOwner(t, e, ownerSess)
	stale := reportEntry(t, batch, "said: F1-AFTER-ACK")
	for _, want := range []string{
		"answered (ask_id " + qid + ") by owner:Winerim at ",
		": yes — already delivered to the session; do not relay it",
		"now: working on that answer",
	} {
		if !strings.Contains(stale, want) {
			t.Fatalf("stale report lacks %q:\n%s", want, stale)
		}
	}
	if strings.Contains(stale, "unless someone sends it a message") {
		t.Fatalf("a working session reads as idle:\n%s", stale)
	}
	// The turn that read the answer knew it: nothing to add there.
	if fresh := reportEntry(t, batch, "said: F1-GOT-ANSWER"); strings.Contains(fresh, "answered (ask_id") {
		t.Fatalf("the turn that handled the answer is annotated as if it did not know it:\n%s", fresh)
	}
}

func TestHeldNeedsInputReportSaysTheUserAnswered(t *testing.T) {
	e, ownerSess, child, qid := heldOwnerChildWithQuestion(t)
	if code, body := f1PostAsk(t, e, child.ID, qid, "Sí, despliega"); !f1Is2xx(code) {
		t.Fatalf("answer = %d %s", code, body)
	}
	childHasAnswer(t, e, child, qid)

	batch := releaseOwner(t, e, ownerSess)
	if strings.Contains(batch, "asking (ask_id "+qid+")") {
		t.Fatalf("the batch still says the session is asking:\n%s", batch)
	}
	asked := reportEntry(t, batch, "status: needs_input")
	for _, want := range []string{"answered (ask_id " + qid + ") by user at ", "Sí, despliega — already delivered"} {
		if !strings.Contains(asked, want) {
			t.Fatalf("needs_input entry lacks %q:\n%s", want, asked)
		}
	}
	if strings.Contains(asked, "waiting for the answer above") {
		t.Fatalf("needs_input entry still waits for an answer it has:\n%s", asked)
	}
}

// A linked task of a needs_input report whose question was answered since no
// longer waits for that answer.
func TestLinkedTaskNoticeStopsWaitingOnAnAnsweredQuestion(t *testing.T) {
	rep := owner.Report{SessionID: "s1", Title: "t", Origin: "owner", Status: callbackStatusNeedsInput,
		Pending:     &owner.ReportPending{Kind: pendingKindQuestion, ID: "q_1", Text: "Which?"},
		LinkedTasks: []owner.ReportTask{{ID: 3, Title: "Ship"}}}
	if text := reportsMessage(owner.Owner{}, []owner.Report{rep}); !strings.Contains(text, "It waits for the answer above") {
		t.Fatalf("an open question no longer reads as waited on:\n%s", text)
	}
	rep.Answered = []owner.ReportAnswer{{AskID: "q_1", By: "user", At: "2026-11-22T13:43:59Z", Text: "A", Delivered: true}}
	text := reportsMessage(owner.Owner{}, []owner.Report{rep})
	if strings.Contains(text, "waits for the answer above") {
		t.Fatalf("the linked task still waits for an answer the session has:\n%s", text)
	}
	if !strings.Contains(text, "linked task #3 «Ship» is still open. Before ending your turn") {
		t.Fatalf("the linked task is no longer a decision for the owner:\n%s", text)
	}
}

func TestUserAnswerEntrySaysTheSessionAlreadyHasIt(t *testing.T) {
	e, ownerSess, child, qid := heldOwnerChildWithQuestion(t)
	if code, body := f1PostAsk(t, e, child.ID, qid, "Sí, despliega"); !f1Is2xx(code) {
		t.Fatalf("answer = %d %s", code, body)
	}
	childHasAnswer(t, e, child, qid)

	batch := releaseOwner(t, e, ownerSess)
	said := reportEntry(t, batch, "the user answered ask_id "+qid)
	if !strings.Contains(said, "already delivered to the session at ") || !strings.Contains(said, "do not relay it") {
		t.Fatalf("user-said entry does not say the session has the answer:\n%s", said)
	}
	if !strings.Contains(batch, "that session already has them: write down what they decide, and send it only what it does not have") {
		t.Fatalf("footer still asks to act on the user's words:\n%s", batch)
	}
}

func TestOwnerAnswerResultWarnsAboutTheEarlierReport(t *testing.T) {
	for _, saved := range []bool{false, true} {
		name := map[bool]string{false: "loaded", true: "saved"}[saved]
		t.Run(name, func(t *testing.T) {
			e, _, ownerSess, child, qid := ownerChildWithQuestion(t, false, "owner")
			if saved {
				f1Unload(t, e, child.ID)
			}
			text, isErr := f1OwnerAnswers(t, ownerSess, child.ID, qid)
			if isErr {
				t.Fatal(text)
			}
			for _, want := range []string{"It has the answer now", "A report it wrote before this answer may still arrive: do not resend the answer."} {
				if !strings.Contains(text, want) {
					t.Fatalf("answer result lacks %q: %s", want, text)
				}
			}
		})
	}
}

func TestReportPaintsAsTodayWhenQuestionsCannotBeRead(t *testing.T) {
	e, _, ownerSess, child, qid := ownerChildWithQuestion(t, false, "owner")
	ended := time.Now().UnixMilli()
	if text, isErr := f1OwnerAnswers(t, ownerSess, child.ID, qid); isErr {
		t.Fatal(text)
	}
	pending := []owner.Report{
		{ID: "r1", SessionID: child.ID, Title: "child", Origin: "owner", Status: callbackStatusNeedsInput,
			Pending: &owner.ReportPending{Kind: pendingKindQuestion, ID: qid, Text: f1Question}, EndedAtMs: ended},
		{ID: "r2", SessionID: child.ID, Title: "child", Origin: "owner", Status: callbackStatusDone,
			FinalText: "Waiting on " + qid, EndedAtMs: ended},
	}
	plain := reportsMessage(owner.Owner{}, pending)
	// The store works: the fix applies, so the comparison below is not vacuous.
	if got := reportsMessage(owner.Owner{}, e.mgr.withAnsweredQuestions(pending)); got == plain {
		t.Fatalf("an answered question changed nothing:\n%s", got)
	}

	dbPath := filepath.Join(filepath.Dir(e.mgr.sessionBaseDir), tasks.DatabaseName)
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() //nolint:errcheck
	if _, err := db.Exec(`ALTER TABLE questions RENAME TO questions_unreadable`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`ALTER TABLE questions_unreadable RENAME TO questions`) })

	got := e.mgr.withAnsweredQuestions(pending)
	if !reflect.DeepEqual(got, pending) {
		t.Fatalf("a failed read changed the reports: %+v", got)
	}
	if text := reportsMessage(owner.Owner{}, got); text != plain {
		t.Fatalf("a failed read changed the report:\n%s\nwant:\n%s", text, plain)
	}
}

// The pieces of the painted entry, without a session behind them.
func TestAnsweredReportRendering(t *testing.T) {
	answer := owner.ReportAnswer{AskID: "q_1", By: "owner:Moa", At: "2026-11-22T12:32:06Z", Text: "B", Delivered: true}
	base := owner.Report{SessionID: "s1", Title: "t", Origin: "owner", Status: callbackStatusDone, FinalText: "Waiting on q_1",
		Answered: []owner.ReportAnswer{answer}}

	idle := reportsMessage(owner.Owner{}, []owner.Report{base})
	if !strings.Contains(idle, "said: Waiting on q_1\n  answered (ask_id q_1) by owner:Moa at 2026-11-22T12:32:06Z: B — already delivered to the session; do not relay it\n") {
		t.Fatalf("answered line missing or misplaced:\n%s", idle)
	}
	if !strings.Contains(idle, "now: idle") {
		t.Fatalf("a session that finished with the answer is idle:\n%s", idle)
	}

	working := base
	working.WorkingNow = true
	if got := reportsMessage(owner.Owner{}, []owner.Report{working}); !strings.Contains(got, "now: working on that answer — it will report when it stops") {
		t.Fatalf("working session line missing:\n%s", got)
	}

	permission := working
	permission.Status = callbackStatusNeedsInput
	permission.Pending = &owner.ReportPending{Kind: pendingKindPermission, ID: "p1", Text: "bash rm -rf build"}
	if got := reportsMessage(owner.Owner{}, []owner.Report{permission}); !strings.Contains(got, "now: waiting for the answer above") {
		t.Fatalf("a pending permission lost its now line to an answer:\n%s", got)
	}

	several := working
	several.Answered = []owner.ReportAnswer{answer, {AskID: "q_2", By: "user", At: answer.At, Text: "C", Delivered: true}}
	if got := reportsMessage(owner.Owner{}, []owner.Report{several}); !strings.Contains(got, "now: working on those answers — it will report when it stops") {
		t.Fatalf("several answers read as one:\n%s", got)
	}

	queued := base
	queued.Answered = []owner.ReportAnswer{{AskID: "q_1", By: "user", At: answer.At, Text: "B"}}
	got := reportsMessage(owner.Owner{}, []owner.Report{queued})
	if !strings.Contains(got, "queued for the session, which it wakes; do not relay it") ||
		!strings.Contains(got, "now: that answer is on its way to it — it will continue on its own") {
		t.Fatalf("queued answer not said:\n%s", got)
	}
}

// answeredWhileHeld answers the child's question while the child is held in a
// model call: the answer is accepted and its event queued, but the session
// has not read it. It returns the question as stored.
func answeredWhileHeld(t *testing.T) (*f1Env, *ManagedSession, tasks.Question) {
	t.Helper()
	e, _, ownerSess, child, qid := ownerChildWithQuestion(t, false, "owner")
	e.send(child, "F1-HOLD keep working")
	pollUntil(t, f1Guard, "child running", func() bool { return sessState(child) == StateRunning })
	if text, isErr := f1OwnerAnswers(t, ownerSess, child.ID, qid); isErr {
		t.Fatal(text)
	}
	q := f1QuestionState(t, e, qid)
	if q.State != tasks.QuestionAnswered || q.Work != tasks.WorkDue || q.EventID == "" {
		t.Fatalf("precondition: an accepted answer still owing its run, got state=%s work=%q event=%q", q.State, q.Work, q.EventID)
	}
	if e.mgr.questions.read(child, q.EventID) {
		t.Fatal("precondition: the held child must not have read the answer yet")
	}
	return e, child, q
}

func needsInputReport(sessionID, qid string) owner.Report {
	return owner.Report{ID: "r1", SessionID: sessionID, Title: "child", Origin: "owner", Status: callbackStatusNeedsInput,
		Pending: &owner.ReportPending{Kind: pendingKindQuestion, ID: qid, Text: f1Question}, EndedAtMs: 1}
}

// An accepted answer the session has not read is not "delivered": it is
// queued, and the session will be woken by it.
func TestAnswerNotYetReadIsQueuedNotDelivered(t *testing.T) {
	e, child, q := answeredWhileHeld(t)
	text := reportsMessage(owner.Owner{}, e.mgr.withAnsweredQuestions([]owner.Report{needsInputReport(child.ID, q.ID)}))
	if strings.Contains(text, "already delivered") {
		t.Fatalf("an answer the session has not read is reported as delivered:\n%s", text)
	}
	if !strings.Contains(text, "answered (ask_id "+q.ID+")") || !strings.Contains(text, "queued for the session, which it wakes") {
		t.Fatalf("the queued answer is not said:\n%s", text)
	}
}

// A rewind abandons an answer the session never read (AbandonQuestionAnswer:
// the event fails, the work is stopped). It will never arrive, so the report
// must not say it is delivered or on its way: it paints as it was written.
func TestAbandonedAnswerIsNotClaimed(t *testing.T) {
	e, child, q := answeredWhileHeld(t)
	pending := []owner.Report{needsInputReport(child.ID, q.ID)}
	plain := reportsMessage(owner.Owner{}, pending)
	if abandoned, err := e.mgr.tasks.AbandonQuestionAnswer(context.Background(), q.EventID, "rewound"); err != nil || !abandoned {
		t.Fatalf("abandon = %v, %v", abandoned, err)
	}
	if got := f1QuestionState(t, e, q.ID); got.Work != tasks.WorkStopped {
		t.Fatalf("precondition: work = %q, want stopped", got.Work)
	}
	if text := reportsMessage(owner.Owner{}, e.mgr.withAnsweredQuestions(pending)); text != plain {
		t.Fatalf("an abandoned answer changed the report:\n%s\nwant:\n%s", text, plain)
	}
}

// A question asked after a report's text was final is not news to that
// report, even when it is answered after it.
func TestQuestionAskedAfterTheReportIsNotAnnotated(t *testing.T) {
	e, _, ownerSess, child, qid := ownerChildWithQuestion(t, false, "owner")
	if text, isErr := f1OwnerAnswers(t, ownerSess, child.ID, qid); isErr {
		t.Fatal(text)
	}
	q := f1QuestionState(t, e, qid)
	report := func(endedAt int64) []owner.Report {
		return []owner.Report{{ID: "r1", SessionID: child.ID, Title: "child", Origin: "owner",
			Status: callbackStatusDone, FinalText: "earlier work", EndedAtMs: endedAt}}
	}
	if got := e.mgr.withAnsweredQuestions(report(q.AskedAt)); len(got[0].Answered) != 1 {
		t.Fatalf("a question open when the text was final is not annotated: %+v", got[0].Answered)
	}
	if got := e.mgr.withAnsweredQuestions(report(q.AskedAt - 1)); len(got[0].Answered) != 0 {
		t.Fatalf("a question asked after the report is annotated on it: %+v", got[0].Answered)
	}
}

// A Stop fences an answer the session has not admitted (FenceQuestionAnswers
// with stopRun): it may still join the transcript as an append, but it starts
// no run. Unread, it is neither delivered nor on its way: the report paints as
// it was written.
func TestStoppedUnreadAnswerIsNotClaimed(t *testing.T) {
	e, child, q := answeredWhileHeld(t)
	pending := []owner.Report{needsInputReport(child.ID, q.ID)}
	plain := reportsMessage(owner.Owner{}, pending)
	if n, err := e.mgr.tasks.FenceQuestionAnswers(context.Background(), child.ID, true, nil); err != nil || n == 0 {
		t.Fatalf("fence = %d, %v", n, err)
	}
	if got := f1QuestionState(t, e, q.ID); got.Work != tasks.WorkStopped {
		t.Fatalf("precondition: work = %q, want stopped", got.Work)
	}
	if e.mgr.questions.read(child, q.EventID) {
		t.Fatal("precondition: the held child must not have read the fenced answer")
	}
	if text := reportsMessage(owner.Owner{}, e.mgr.withAnsweredQuestions(pending)); text != plain {
		t.Fatalf("a stopped, unread answer changed the report:\n%s\nwant:\n%s", text, plain)
	}
}
