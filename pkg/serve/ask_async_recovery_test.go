package serve

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// Recovery of the run an admitted answer owes. The child admits and saves the
// answer, then the model "reads" it (its provider blocks on the request that
// carries it) and the process dies before the run ends. Real Manager,
// FileStore and SQLite throughout; same subprocess pattern as the crash
// contract tests.

type f1ReadingProvider struct {
	f1Provider
	reading chan struct{}
}

func (p *f1ReadingProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	if !isTestAutoTitleRequest(req) && f1LastUserIsAnswer(req) {
		select {
		case p.reading <- struct{}{}:
		default:
		}
		ch := make(chan core.AssistantEvent)
		go func() { <-ctx.Done(); close(ch) }()
		return ch, nil
	}
	return p.f1Provider.Stream(ctx, req)
}

func TestAskAsyncCrashWhileReadingAnswerOwesOneContinuation(t *testing.T) {
	if mode := os.Getenv(f1CrashChildEnv); mode != "" {
		f1RecoveryChild(t, mode)
		return
	}
	for _, tc := range []struct {
		mode string
		work string
	}{{"continue", tasks.WorkDone}, {"stop", tasks.WorkStopped}} {
		t.Run(tc.mode, func(t *testing.T) {
			d := f1NewCrashDirs(t)
			t.Setenv("MOA_CONFIG_DIR", d.config)
			m := f1RunCrashChild(t, "^TestAskAsyncCrashWhileReadingAnswerOwesOneContinuation$", tc.mode, d)

			// The fixture is only valid if the answer was in the saved transcript.
			saved, _, err := session.FindSessionReadOnly(m.Base, m.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if n := len(f1Answers(treeMessages(saved))); n != 1 {
				t.Fatalf("saved transcript holds %d question_answer events before the restart, want 1", n)
			}

			prov := &f1Provider{script: f1Script(false)}
			e := f1NewEnv(t, m.Base, m.Workspace, prov)
			answered := func(r core.Request) bool { return f1LastUserIsAnswer(r) }

			if tc.mode == "stop" {
				if f1Eventually(f1Guard, func() bool { return prov.countWhere(answered) > 0 }) {
					t.Fatal("a session whose run Stop had cancelled was continued after the restart")
				}
			} else {
				if !f1Eventually(f1Guard, func() bool { return prov.countWhere(answered) == 1 }) {
					t.Fatalf("the interrupted run was not continued (requests carrying the answer = %d)", prov.countWhere(answered))
				}
			}
			if !f1Stable(500*time.Millisecond, func() bool { return prov.countWhere(answered) == map[string]int{"continue": 1, "stop": 0}[tc.mode] }) {
				t.Fatalf("requests carrying the answer = %d after settling", prov.countWhere(answered))
			}
			if !f1Eventually(f1Guard, func() bool {
				q, err := e.mgr.tasks.GetQuestion(context.Background(), m.QuestionID)
				return err == nil && q.Work == tc.work
			}) {
				q, _ := e.mgr.tasks.GetQuestion(context.Background(), m.QuestionID)
				t.Fatalf("work = %q, want %q", q.Work, tc.work)
			}

			// Whichever way it went, no second event exists and a further
			// restart does nothing.
			if sess, ok := e.mgr.Get(m.SessionID); ok {
				if n := len(f1Answers(sess.History())); n != 1 {
					t.Fatalf("question_answer events in the session = %d, want 1", n)
				}
			}
			e.halt()
			prov2 := &f1Provider{script: f1Script(false)}
			e2 := f1NewEnv(t, m.Base, m.Workspace, prov2)
			if !f1Stable(600*time.Millisecond, func() bool { return prov2.count() == 0 }) {
				t.Fatalf("a second restart called the model %d time(s)", prov2.count())
			}
			if sess, err := e2.mgr.ResumeSession(m.SessionID); err != nil {
				t.Fatal(err)
			} else if n := len(f1Answers(sess.History())); n != 1 {
				t.Fatalf("question_answer events after the second restart = %d, want 1", n)
			}
			if !f1Stable(400*time.Millisecond, func() bool { return prov2.count() == 0 }) {
				t.Fatalf("resuming the session called the model %d time(s)", prov2.count())
			}
		})
	}
}

// treeMessages is the messages of a saved session's active branch.
func treeMessages(s *session.Session) []core.AgentMessage {
	if len(s.Entries) == 0 {
		return s.Messages
	}
	var out []core.AgentMessage
	for _, e := range s.Entries {
		if e.Type == session.EntryMessage {
			out = append(out, e.Message)
		}
	}
	return out
}

func f1RecoveryChild(t *testing.T, mode string) {
	base, root, cfgDir := os.Getenv(f1CrashBaseEnv), os.Getenv(f1CrashWorkspaceEnv), os.Getenv(f1CrashConfigEnv)
	t.Setenv("MOA_CONFIG_DIR", cfgDir)
	prov := &f1ReadingProvider{f1Provider: f1Provider{script: f1Script(false)}, reading: make(chan struct{}, 1)}
	mgr := f1CrashManager(base, root, cfgDir, prov)
	e := &f1Env{t: t, mgr: mgr, prov: &prov.f1Provider, root: root}
	e.srv = newF1ChildServer(mgr)
	sess := e.session(f1KindHuman)

	e.send(sess, "F1-TRIGGER please ask")
	qid := waitPendingAsk(t, sess).ID
	f1Settle(t, sess)
	if code, body := f1PostAsk(t, e, sess.ID, qid, "yes"); code != 204 {
		t.Fatalf("answer = %d %s, want 204", code, body)
	}
	select {
	case <-prov.reading:
	case <-time.After(30 * time.Second):
		t.Fatal("the model never received the answer")
	}
	if mode == "stop" {
		if err := mgr.Cancel(sess.ID); err != nil {
			t.Fatalf("stop: %v", err)
		}
		pollUntil(t, f1Guard, "idle after stop", func() bool {
			return sessState(sess) == StateIdle && !sess.runtime.Context().Agent.IsRunning()
		})
		sess.runtime.Bus.Drain(2 * time.Second)
	}
	if !strings.HasPrefix(qid, "q_") {
		t.Fatalf("unexpected question id %q", qid)
	}
	f1CrashNow(t, sess.ID, qid)
}
