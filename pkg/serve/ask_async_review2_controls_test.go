package serve

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/askuser"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

func TestAuthorityReviewHTTPCallbackOriginalRunCannotFinishUnreadAnswer(t *testing.T) {
	f1Isolate(t)
	rc := newCallbackReceiver(t)
	modelReached := make(chan struct{}, 1)
	endOriginal := make(chan struct{})
	admitReached := make(chan struct{}, 4)
	admitRelease := make(chan struct{})
	var modelOnce, admitOnce sync.Once
	p := &f1Provider{script: func(req core.Request) <-chan core.AssistantEvent {
		if f1HasResult(req) && !strings.Contains(f1LastUser(req), "<question_answer") {
			select {
			case modelReached <- struct{}{}:
			default:
			}
			<-endOriginal
			return f1Text("Original run finished without reading the answer")
		}
		return f1Script(false)(req)
	}}
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), p, taskSchedulerHooks{beforeAdmit: func(string) { admitReached <- struct{}{}; <-admitRelease }})
	t.Cleanup(func() { modelOnce.Do(func() { close(endOriginal) }); admitOnce.Do(func() { close(admitRelease) }) })
	id, _, err := e.mgr.CreateAutomationRun(AutomationRunRequest{Prompt: "F1-TRIGGER please ask", CallbackURL: rc.srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := e.mgr.Get(id)
	qid := waitPendingAsk(t, sess).ID
	select {
	case <-modelReached:
	case <-time.After(f1Guard):
		t.Fatal("model did not continue beyond acknowledgment")
	}
	rc.waitForCallbacks(t, 1)
	response := automationReq(t, e.srv, "/api/automation/sessions/"+id+"/ask-response", testAutomationToken, `{"id":"`+qid+`","answers":["main"]}`, false)
	response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatalf("answer=%d", response.StatusCode)
	}
	select {
	case <-admitReached:
	case <-time.After(f1Guard):
		t.Fatal("dispatcher not reached")
	}
	modelOnce.Do(func() { close(endOriginal) })
	f1Settle(t, sess)
	sess.runtime.WaitQuiescent(context.Background())
	f1Eventually(time.Second, func() bool { return rc.count() > 1 })
	t.Logf("original-run HTTP payloads while owed=%v admitted=%d: %+v", sess.runtime.Context().AnswerWorkOwed(), len(f1Answers(sess.History())), rc.all())
	for _, payload := range rc.all() {
		if payload.Status == callbackStatusDone {
			t.Errorf("original run emitted HTTP done before its answer was read: %+v", payload)
		}
	}
	admitOnce.Do(func() { close(admitRelease) })
}

func TestAuthorityReviewHTTPCallbackDoesNotFinishAnUnreadAnswer(t *testing.T) {
	f1Isolate(t)
	rc := newCallbackReceiver(t)
	reached := make(chan struct{}, 4)
	release := make(chan struct{})
	var once sync.Once
	hooks := taskSchedulerHooks{beforeAdmit: func(string) { reached <- struct{}{}; <-release }}
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{script: f1Script(false)}, hooks)
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	id, _, err := e.mgr.CreateAutomationRun(AutomationRunRequest{Prompt: "F1-TRIGGER please ask", CallbackURL: rc.srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := e.mgr.Get(id)
	qid := waitPendingAsk(t, sess).ID
	f1Settle(t, sess)
	rc.waitForCallbacks(t, 1)
	sess.runtime.WaitQuiescent(context.Background())
	body := `{"id":"` + qid + `","answers":["main"]}`
	response := automationReq(t, e.srv, "/api/automation/sessions/"+id+"/ask-response", testAutomationToken, body, false)
	response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatalf("answer = %d", response.StatusCode)
	}
	select {
	case <-reached:
	case <-time.After(f1Guard):
		t.Fatal("dispatcher not reached")
	}
	e.send(sess, "F1-NEXT independent work; answer is not admitted")
	f1Settle(t, sess)
	sess.runtime.WaitQuiescent(context.Background())
	f1Eventually(time.Second, func() bool { return rc.count() > 1 })
	t.Logf("HTTP payloads while owed=%v and admitted=%d: %+v", sess.runtime.Context().AnswerWorkOwed(), len(f1Answers(sess.History())), rc.all())
	for _, payload := range rc.all() {
		if payload.Status == callbackStatusDone {
			t.Errorf("HTTP done arrived with answer unadmitted and work owed: %+v", payload)
		}
	}
	once.Do(func() { close(release) })
}

func TestAuthorityReviewOffLegacyAuthorityIsDeliberatelyHardened(t *testing.T) {
	pinSyncAskUser(t)
	_, child, qid := f1bisOwnerTapChild(t)
	if ask := f1Pending(child); ask == nil || ask.Async {
		t.Fatal("fixture must use the flag-off legacy path")
	}
	for _, who := range []askuser.Responder{{}, {Kind: "alien"}, {Kind: askuser.ResponderOwner}, {Kind: askuser.ResponderAutomation}, {Kind: askuser.ResponderVoice}} {
		err := child.runtime.Bus.Execute(bus.ResolveAskUser{AskID: qid, Answers: []string{"yes"}, Responder: who})
		if !errors.Is(err, askuser.ErrRequiresTap) {
			t.Errorf("off resolver accepts %+v: %v", who, err)
		}
		if ask := f1Pending(child); ask == nil || ask.ID != qid {
			t.Fatal("rejection consumed the legacy ask")
		}
	}
	if qid == "ask_1" {
		t.Errorf("legacy ID was not randomized: %s", qid)
	}
	t.Logf("flag-off legacy ID=%s; nonhuman responders refused before consumption", qid)
	f1bisAssertHumanThenResolves(t, child, qid)
}

func TestAuthorityReviewAsyncQuestionsStayProjectScopedAndDetached(t *testing.T) {
	for _, saved := range []bool{false, true} {
		name := "loaded"
		if saved {
			name = "saved"
		}
		t.Run(name, func(t *testing.T) {
			e, _, ownerSess, child, qid := ownerChildWithQuestion(t, false, "owner")
			if saved {
				f1Unload(t, e, child.ID)
			}
			foreignInfo, err := e.mgr.CreateOwner(CreateOwnerOpts{Root: t.TempDir(), Name: "Other project"})
			if err != nil {
				t.Fatal(err)
			}
			foreignOwner, _ := e.mgr.Get(foreignInfo.SessionID)
			for _, viewer := range []*ManagedSession{foreignOwner, ownerSess} {
				if viewer == ownerSess {
					if _, err := e.mgr.SetOwnerDetached(child.ID, true); err != nil {
						t.Fatal(err)
					}
				}
				listed := runSessionsTool(t, viewer, map[string]any{"action": "list"})
				if strings.Contains(toolText(listed), qid) || strings.Contains(toolText(listed), f1Question) {
					t.Errorf("private Q leaked into roster: %s", toolText(listed))
				}
				for _, action := range []string{"read", "answer"} {
					result := runSessionsTool(t, viewer, map[string]any{"action": action, "session_id": child.ID, "ask_id": qid, "answers": []any{"yes"}})
					if !result.IsError {
						t.Errorf("%s allowed outside project/detached: %s", action, toolText(result))
					}
				}
				if q := f1QuestionState(t, e, qid); q.State != tasks.QuestionOpen {
					t.Errorf("unauthorized action mutated Q: %+v", q)
				}
			}
		})
	}
}

func TestAuthorityReviewManagedGoalActuallyYieldsOnAsyncQuestion(t *testing.T) {
	f1Isolate(t)
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{script: f1Script(false)})
	sess := e.session(f1KindHuman)
	err := sess.runtime.Bus.Execute(bus.EnterGoal{Objective: "F1-TRIGGER get confirmation before claiming success", WorkDir: e.root, StatePath: filepath.Join(t.TempDir(), "STATE.md"), MaxIterations: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sess.runtime.Context().Goal.Active() {
			_ = sess.runtime.Bus.Execute(bus.ExitGoal{})
		}
	})
	ask := waitPendingAsk(t, sess)
	if !ask.Async {
		t.Fatal("goal did not use async question")
	}
	f1Settle(t, sess)
	calls := e.prov.countSession(t, sess.ID)
	if !f1Stable(500*time.Millisecond, func() bool {
		return e.prov.countSession(t, sess.ID) == calls && sess.runtime.Context().Goal.Info().Iteration == 0 && sess.runtime.Context().Goal.Active()
	}) {
		t.Fatalf("goal did not yield: calls=%d info=%+v", e.prov.countSession(t, sess.ID), sess.runtime.Context().Goal.Info())
	}
	if sessState(sess) != StateIdle {
		t.Fatalf("open async Q parked session: %s", sessState(sess))
	}
	t.Logf("real EnterGoal yielded: calls=%d iteration=0 active=true state=idle Q=%s", calls, ask.ID)
}
