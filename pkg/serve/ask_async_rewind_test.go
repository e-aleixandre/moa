package serve

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// Rewinds and question state (I6): the leaf moving never reopens a closed
// question, never re-injects a consumed answer, and a crash between the new
// branch reaching disk and the invalidation reaching SQLite settles itself.

func f1BranchRequest(t *testing.T, e *f1Env, sessionID, entryID string, before bool) int {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"entry_id": entryID, "before": before})
	resp := automationReq(t, e.srv, "/api/sessions/"+sessionID+"/branch", "", string(body), true)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// f1EntryWhere returns the ID of the first entry on the path matching fn.
func f1EntryWhere(t *testing.T, sess *ManagedSession, fn func(core.AgentMessage) bool) string {
	t.Helper()
	for _, en := range sess.runtime.Context().Tree.Path() {
		if en.Type == session.EntryMessage && fn(en.Message) {
			return en.ID
		}
	}
	t.Fatal("no matching entry on the branch")
	return ""
}

func TestAskAsyncRewindAfterOriginDoesNotReinjectAConsumedAnswer(t *testing.T) {
	f1Isolate(t)
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{script: f1Script(false)})
	sess := e.session(f1KindHuman)
	e.send(sess, "F1-TRIGGER please ask")
	qid := waitPendingAsk(t, sess).ID
	f1Settle(t, sess)
	if code, body := f1PostAsk(t, e, sess.ID, qid, "yes"); code != 204 {
		t.Fatalf("answer = %d %s", code, body)
	}
	answered := func(r core.Request) bool { return f1LastUserIsAnswer(r) }
	if !f1Eventually(f1Guard, func() bool { return e.prov.countWhere(answered) == 1 && sessState(sess) == StateIdle }) {
		t.Fatal("the answer was never read")
	}
	f1Settle(t, sess)

	// Back to the reply that followed the acknowledgement: after the origin,
	// before the answer that was consumed on the branch being left.
	target := f1EntryWhere(t, sess, func(m core.AgentMessage) bool { return m.Role == "assistant" && f1MsgText(m) == "F1-AFTER-ACK" })
	if code := f1BranchRequest(t, e, sess.ID, target, false); code != 200 {
		t.Fatalf("rewind = %d", code)
	}
	sess.runtime.Bus.Drain(2 * time.Second)

	before := e.prov.countWhere(answered)
	if n := len(f1Answers(sess.History())); n != 0 {
		t.Fatalf("the rewound branch holds %d answer event(s), want 0", n)
	}
	if q, err := e.mgr.tasks.GetQuestion(context.Background(), qid); err != nil || q.State != tasks.QuestionAnswered {
		t.Fatalf("question after the rewind: %+v %v", q.State, err)
	}
	if !f1Stable(500*time.Millisecond, func() bool { return e.prov.countWhere(answered) == before && !sess.runtime.Context().Agent.IsRunning() }) {
		t.Fatal("the session started working after the rewind")
	}

	// The same answer again is a duplicate; a different one is refused.
	if code, body := f1PostAsk(t, e, sess.ID, qid, "yes"); code != 204 {
		t.Fatalf("repeating the answer = %d %s, want 204", code, body)
	}
	if code, _ := f1PostAsk(t, e, sess.ID, qid, "no"); code != 409 {
		t.Fatalf("a different answer = %d, want 409", code)
	}
	if !f1Stable(400*time.Millisecond, func() bool {
		return e.prov.countWhere(answered) == before && len(f1Answers(sess.History())) == 0
	}) {
		t.Fatal("a repeated answer re-entered the rewound branch")
	}

	// And not after a restart either.
	base := e.mgr.sessionBaseDir
	e.halt()
	prov2 := &f1Provider{script: f1Script(false)}
	e2 := f1NewEnv(t, base, e.root, prov2)
	s2, err := e2.mgr.ResumeSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(f1Answers(s2.History())); n != 0 {
		t.Fatalf("after the restart the branch holds %d answer event(s)", n)
	}
	if !f1Stable(600*time.Millisecond, func() bool { return prov2.count() == 0 }) {
		t.Fatalf("the restart woke the model %d time(s)", prov2.count())
	}
}

// The process dies after the rewound branch is saved and before the question is
// invalidated: the next start settles it from the snapshot, and an answer to
// the stale card is a conflict whether or not anyone reopened the session.
func TestAskAsyncCrashBetweenBranchSnapshotAndInvalidation(t *testing.T) {
	if mode := os.Getenv(f1CrashChildEnv); mode != "" {
		f1RewindCrashChild(t)
		return
	}
	for _, reopen := range []bool{false, true} {
		name := map[bool]string{false: "answer-without-resume", true: "after-resume"}[reopen]
		t.Run(name, func(t *testing.T) {
			d := f1NewCrashDirs(t)
			t.Setenv("MOA_CONFIG_DIR", d.config)
			m := f1RunCrashChild(t, "^TestAskAsyncCrashBetweenBranchSnapshotAndInvalidation$", "rewind", d)
			prov := &f1Provider{script: f1Script(false)}
			e := f1NewEnv(t, m.Base, m.Workspace, prov)
			ctx := context.Background()

			if reopen {
				if _, err := e.mgr.ResumeSession(m.SessionID); err != nil {
					t.Fatal(err)
				}
				q, err := e.mgr.tasks.GetQuestion(ctx, m.QuestionID)
				if err != nil || q.State != tasks.QuestionInvalidated || q.Reason != tasks.ReasonRewound {
					t.Fatalf("question after reloading the rewound session: %s/%s %v", q.State, q.Reason, err)
				}
			}
			if code, body := f1PostAsk(t, e, m.SessionID, m.QuestionID, "yes"); code != 409 {
				t.Fatalf("answering a question whose origin was rewound = %d %s, want 409", code, body)
			}
			q, _ := e.mgr.tasks.GetQuestion(ctx, m.QuestionID)
			if q.State != tasks.QuestionInvalidated || q.EventID != "" {
				t.Fatalf("question after the refused answer: state=%s event=%q", q.State, q.EventID)
			}
			if !f1Stable(400*time.Millisecond, func() bool { return prov.count() == 0 }) {
				t.Fatalf("a refused answer called the model %d time(s)", prov.count())
			}
		})
	}
}

func f1RewindCrashChild(t *testing.T) {
	base, root, cfgDir := os.Getenv(f1CrashBaseEnv), os.Getenv(f1CrashWorkspaceEnv), os.Getenv(f1CrashConfigEnv)
	t.Setenv("MOA_CONFIG_DIR", cfgDir)
	prov := &f1Provider{script: f1Script(false)}
	mgr := f1CrashManager(base, root, cfgDir, prov)
	e := &f1Env{t: t, mgr: mgr, prov: prov, root: root}
	e.srv = newF1ChildServer(mgr)
	sess := e.session(f1KindHuman)
	sendAndWait(t, mgr, sess, "F1-ANCHOR one")
	sendAndWait(t, mgr, sess, "F1-ANCHOR two")
	anchor := f1EntryWhere(t, sess, func(m core.AgentMessage) bool { return m.Role == "user" && f1MsgText(m) == "F1-ANCHOR two" })
	e.send(sess, "F1-TRIGGER please ask")
	qid := waitPendingAsk(t, sess).ID
	f1Settle(t, sess)
	mgr.questions.hooks.afterBranchSnapshot = func() { f1CrashNow(t, sess.ID, qid) }
	f1BranchRequest(t, e, sess.ID, anchor, true)
	t.Fatal("the rewind finished without reaching the crash point")
}

// A deleted session takes its open question with it, and an answer that never
// got into the conversation fails instead of waiting for a session that is gone.
func TestAskAsyncDeleteSettlesQuestionsAndUnadmittedAnswers(t *testing.T) {
	t.Run("open question", func(t *testing.T) {
		e, sess, qid := f1Open(t, f1KindHuman, false)
		if err := e.mgr.Delete(sess.ID); err != nil {
			t.Fatal(err)
		}
		q, err := e.mgr.tasks.GetQuestion(context.Background(), qid)
		if err != nil || q.State != tasks.QuestionInvalidated || q.Reason != tasks.ReasonSessionDeleted {
			t.Fatalf("open question after the delete: %s/%s %v", q.State, q.Reason, err)
		}
		if code, _ := f1PostAsk(t, e, sess.ID, qid, "yes"); code != 409 {
			t.Fatalf("answering a deleted session's question = %d, want 409", code)
		}
	})
	t.Run("unadmitted answer", func(t *testing.T) {
		old := maxNoticeLoadedSessions
		maxNoticeLoadedSessions = 1
		t.Cleanup(func() { maxNoticeLoadedSessions = old })
		e := f1CapacityEnv(t, nil)
		asker := e.session(f1KindHuman)
		e.send(asker, "F1-TRIGGER please ask")
		qid := waitPendingAsk(t, asker).ID
		f1Settle(t, asker)
		if err := e.mgr.CloseSession(asker.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.mgr.CreateSession(CreateOpts{}); err != nil { // takes the only slot
			t.Fatal(err)
		}
		if code, body := f1PostAsk(t, e, asker.ID, qid, "yes"); code != 204 {
			t.Fatalf("answer = %d %s", code, body)
		}
		ctx := context.Background()
		q, _ := e.mgr.tasks.GetQuestion(ctx, qid)
		if !f1Eventually(f1Guard, func() bool {
			n, err := e.mgr.tasks.Notice(ctx, q.EventID)
			return err == nil && n.State == tasks.NoticePending && n.Reason == tasks.ReasonSessionLimit
		}) {
			t.Fatal("the answer did not wait on the session limit")
		}
		if err := e.mgr.Delete(asker.ID); err != nil {
			t.Fatal(err)
		}
		if n, err := e.mgr.tasks.Notice(ctx, q.EventID); err != nil || n.State != tasks.NoticeFailed || n.Reason != tasks.ReasonSessionDeleted {
			t.Fatalf("answer to a deleted session: %+v %v", n, err)
		}
		if got, _ := e.mgr.tasks.GetQuestion(ctx, qid); got.Work != tasks.WorkStopped {
			t.Fatalf("owed work after the delete = %q, want stopped", got.Work)
		}
	})
}

// Emptying the conversation leaves no origin on the branch: the open question
// closes, and its stale card cannot be answered.
func TestAskAsyncClearInvalidatesOpenQuestion(t *testing.T) {
	e, sess, qid := f1Open(t, f1KindHuman, false)
	if err := sess.runtime.Bus.Execute(bus.ClearSession{SessionID: sess.ID}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if !f1Eventually(f1Guard, func() bool {
		q, err := e.mgr.tasks.GetQuestion(ctx, qid)
		return err == nil && q.State == tasks.QuestionInvalidated
	}) {
		t.Fatal("clearing the conversation left its question open")
	}
	if code, _ := f1PostAsk(t, e, sess.ID, qid, "yes"); code != 409 {
		t.Fatalf("answering a cleared conversation's question = %d, want 409", code)
	}
	if ask := f1Pending(sess); ask != nil && ask.ID == qid {
		t.Fatal("the cleared conversation still shows the question")
	}
}
