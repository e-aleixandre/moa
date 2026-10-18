package serve

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/askuser"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// The consumers that read a session from the outside (automation callbacks,
// the owner's reports and its sessions tool) see an open question as what it
// is: something to answer, not a session that is stuck.

type f1Outcomes struct {
	mu  sync.Mutex
	got []runOutcome
}

func (o *f1Outcomes) add(out runOutcome) {
	o.mu.Lock()
	o.got = append(o.got, out)
	o.mu.Unlock()
}

func (o *f1Outcomes) statuses() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	var s []string
	for _, out := range o.got {
		s = append(s, out.Status)
	}
	return s
}

// A callback caller hears needs_input once per question, never "done" while
// the question is open (also after a second run that leaves it open), and
// "done" once the answer's continuation has ended.
func TestAskAsyncCallbackOutcomesFollowTheQuestion(t *testing.T) {
	f1Isolate(t)
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{script: f1Script(false)})
	sess := e.session(f1KindHuman)
	out := &f1Outcomes{}
	subscribeRunOutcomes(sess, out.add)

	e.send(sess, "F1-TRIGGER please ask")
	qid := waitPendingAsk(t, sess).ID
	f1Settle(t, sess)
	if !f1Eventually(f1Guard, func() bool { return slices.Equal(out.statuses(), []string{callbackStatusNeedsInput}) }) {
		t.Fatalf("outcomes after the run that asked = %v, want one needs_input", out.statuses())
	}
	if p := out.got[0].Pending; p == nil || p.ID != qid {
		t.Fatalf("needs_input pending = %+v, want question %s", p, qid)
	}
	// The run is over and the session quiet: a done would be due by now.
	sess.runtime.WaitQuiescent(context.Background())
	if !f1Stable(400*time.Millisecond, func() bool { return len(out.statuses()) == 1 }) {
		t.Fatalf("a run ended with the question open reported %v", out.statuses())
	}

	// A second run with the question still open: no done, no second needs_input.
	before := e.prov.countSession(t, sess.ID)
	e.send(sess, "F1-NEXT anything new?")
	if !f1Eventually(f1Guard, func() bool { return e.prov.countSession(t, sess.ID) > before && sessState(sess) == StateIdle }) {
		t.Fatal("the second run never ran")
	}
	sess.runtime.WaitQuiescent(context.Background())
	if !f1Stable(400*time.Millisecond, func() bool { return len(out.statuses()) == 1 }) {
		t.Fatalf("a second run with the question open reported %v", out.statuses())
	}

	if code, body := f1PostAsk(t, e, sess.ID, qid, "yes"); !f1Is2xx(code) {
		t.Fatalf("answer = %d %s", code, body)
	}
	if !f1Eventually(f1Guard, func() bool {
		return slices.Equal(out.statuses(), []string{callbackStatusNeedsInput, callbackStatusDone})
	}) {
		t.Fatalf("outcomes after the answer's continuation = %v, want needs_input then done", out.statuses())
	}
	if !f1Stable(400*time.Millisecond, func() bool { return len(out.statuses()) == 2 }) {
		t.Fatalf("the answer's continuation reported more than once: %v", out.statuses())
	}
}

// An asynchronous question neither counts as a blocking prompt nor is hidden
// by one: a permission asked while it is open is still the session going from
// working to blocked, and the question itself is told once when it is posted.
func TestAskAsyncOwnerObserverTellsQuestionAndPermissionApart(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	ownerWithSession(t, mgr, root, "Winerim")
	child := ownerChild(t, mgr, root, "the child")
	var mu sync.Mutex
	var told []string
	child.ownerObserver.emit = func(out runOutcome) {
		if out.Status == callbackStatusNeedsInput && out.Pending != nil {
			mu.Lock()
			told = append(told, out.Pending.ID)
			mu.Unlock()
		}
	}
	wait := func(n int) []string {
		t.Helper()
		child.ownerObserver.sync()
		var got []string
		pollUntil(t, 5*time.Second, "needs_input reports", func() bool {
			mu.Lock()
			defer mu.Unlock()
			got = slices.Clone(told)
			return len(got) >= n
		})
		slices.Sort(got)
		return got
	}

	publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
	child.runtime.Bus.Publish(bus.AskUserRequested{SessionID: child.ID, RunGen: 1, ID: "q_1", Async: true,
		Questions: []bus.AskQuestion{{Text: "which branch?"}}})
	wait(1)
	// Same run, still working, then blocked on a permission.
	child.runtime.Bus.Publish(bus.PermissionRequested{SessionID: child.ID, RunGen: 1, ID: "perm_1", ToolName: "bash"})
	if got := wait(2); !slices.Equal(got, []string{"perm_1", "q_1"}) {
		t.Fatalf("needs_input reports = %v, want the question and the permission", got)
	}
}

// ownerChildWithQuestion posts a question from a session of the owner's
// project, left idle and loaded.
func ownerChildWithQuestion(t *testing.T, tap bool, origin string) (*f1Env, OwnerInfo, *ManagedSession, *ManagedSession, string) {
	t.Helper()
	f1Isolate(t)
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{script: f1Script(tap)})
	info, err := e.mgr.CreateOwner(CreateOwnerOpts{Root: e.root, Name: "Winerim"})
	if err != nil {
		t.Fatal(err)
	}
	ownerSess, _ := e.mgr.Get(info.SessionID)
	child, err := e.mgr.CreateSession(CreateOpts{CWD: e.root, Origin: origin})
	if err != nil {
		t.Fatal(err)
	}
	e.send(child, "F1-TRIGGER please ask")
	qid := waitPendingAsk(t, child).ID
	f1Settle(t, child)
	return e, info, ownerSess, child, qid
}

func f1QuestionState(t *testing.T, e *f1Env, qid string) tasks.Question {
	t.Helper()
	q, err := e.mgr.tasks.GetQuestion(context.Background(), qid)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func f1OwnerAnswers(t *testing.T, ownerSess *ManagedSession, sessionID, qid string) (string, bool) {
	t.Helper()
	res := runSessionsTool(t, ownerSess, map[string]any{
		"action": "answer", "session_id": sessionID, "ask_id": qid, "answers": []any{"yes"},
	})
	return toolText(res), res.IsError
}

func f1Unload(t *testing.T, e *f1Env, id string) {
	t.Helper()
	if err := e.mgr.CloseSession(id); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.mgr.Get(id); ok {
		t.Fatal("the session is still loaded")
	}
}

// An idle child with an open question reads as having a question open, not as
// stuck; once saved it still does, and the owner answers it from the store.
// The answer wakes that child, and only it. The gates the owner always had
// (answer_asks, detach) hold on that path too.
func TestAskAsyncOwnerAnswersASavedChildThroughTheStore(t *testing.T) {
	e, info, ownerSess, child, qid := ownerChildWithQuestion(t, false, "owner")

	live := e.mgr.ownerPendingLine(child.ID)
	if !strings.Contains(live, "open question") || !strings.Contains(live, qid) || strings.Contains(live, "waiting for the user to approve") {
		t.Fatalf("idle child with an open question reads %q", live)
	}

	f1Unload(t, e, child.ID)
	listed := toolText(runSessionsTool(t, ownerSess, map[string]any{"action": "list"}))
	if !strings.Contains(listed, qid) || !strings.Contains(listed, f1Question) {
		t.Fatalf("the saved child's question is not in the roster:\n%s", listed)
	}

	open := func(why string) {
		t.Helper()
		if q := f1QuestionState(t, e, qid); q.State != tasks.QuestionOpen {
			t.Fatalf("%s consumed the question: %+v", why, q)
		}
	}
	store, err := e.mgr.ownerStore()
	if err != nil {
		t.Fatal(err)
	}
	off := info.Owner
	off.AnswerAsks = false
	if err := store.Save(off); err != nil {
		t.Fatal(err)
	}
	if text, isErr := f1OwnerAnswers(t, ownerSess, child.ID, qid); !isErr || !strings.Contains(text, "not allowed") {
		t.Fatalf("answer with answer_asks off = %q (error=%v)", text, isErr)
	}
	open("an owner without answer_asks")
	if err := store.Save(info.Owner); err != nil {
		t.Fatal(err)
	}

	if _, err := e.mgr.SetOwnerDetached(child.ID, true); err != nil {
		t.Fatal(err)
	}
	if text, isErr := f1OwnerAnswers(t, ownerSess, child.ID, qid); !isErr || !strings.Contains(text, "detached") {
		t.Fatalf("answer on a detached child = %q (error=%v)", text, isErr)
	}
	open("an answer on a detached child")
	if _, err := e.mgr.SetOwnerDetached(child.ID, false); err != nil {
		t.Fatal(err)
	}

	if text, isErr := f1OwnerAnswers(t, ownerSess, child.ID, qid); isErr {
		t.Fatalf("answer on a saved owner child: %s", text)
	}
	q := f1QuestionState(t, e, qid)
	if q.State != tasks.QuestionAnswered || q.Responder.Kind != askuser.ResponderOwner || q.Responder.Name != "Winerim" {
		t.Fatalf("question = %+v, want answered by the owner", q)
	}
	if !f1Eventually(f1Guard, func() bool {
		woken, ok := e.mgr.Get(child.ID)
		return ok && len(f1Answers(woken.History())) == 1
	}) {
		t.Fatal("the child was not woken with the owner's answer")
	}
}

// A question of the user's own session is not the owner's to answer, loaded or
// saved: the store is not a way around the origin gate.
func TestAskAsyncOwnerCannotAnswerAUsersSavedSession(t *testing.T) {
	e, _, ownerSess, human, qid := ownerChildWithQuestion(t, false, "user")
	f1Unload(t, e, human.ID)
	text, isErr := f1OwnerAnswers(t, ownerSess, human.ID, qid)
	if !isErr || !strings.Contains(text, "the user's") {
		t.Fatalf("owner answer on the user's session = %q (error=%v)", text, isErr)
	}
	if q := f1QuestionState(t, e, qid); q.State != tasks.QuestionOpen {
		t.Fatalf("the user's question was consumed: %+v", q)
	}
}

// A confirmation that needs the human's tap is refused to the owner on the
// saved path as well, and stays open for the human.
func TestAskAsyncOwnerCannotAnswerATapQuestionOfASavedChild(t *testing.T) {
	e, _, ownerSess, child, qid := ownerChildWithQuestion(t, true, "owner")
	f1Unload(t, e, child.ID)
	if text, isErr := f1OwnerAnswers(t, ownerSess, child.ID, qid); !isErr {
		t.Fatalf("owner answered a requires_tap question: %s", text)
	}
	if q := f1QuestionState(t, e, qid); q.State != tasks.QuestionOpen {
		t.Fatalf("the tap question was consumed: %+v", q)
	}
	if code, body := f1PostAsk(t, e, child.ID, qid, "Yes, by tap"); !f1Is2xx(code) {
		t.Fatalf("the human's tap = %d %s", code, body)
	}
	if q := f1QuestionState(t, e, qid); q.State != tasks.QuestionAnswered || q.Responder.Kind != askuser.ResponderUser {
		t.Fatalf("question = %+v, want answered by the user", q)
	}
}
