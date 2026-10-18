package serve

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

// Failure boundaries of a post (spec §4): the origin is saved, the question
// commits, then it is announced and acknowledged. Real Manager, FileStore and
// SQLite; the seams are questionHooks (nil in production) and, for the
// storage failure, closing the real database between the two steps.

func f1AskUserTool(t *testing.T, sess *ManagedSession) core.Tool {
	t.Helper()
	tool, ok := sess.infra.toolReg.Get("ask_user")
	if !ok {
		t.Fatal("session has no ask_user")
	}
	return tool
}

// A storage failure after the origin is saved and before the question
// commits: nothing is published, nothing is announced, the call gets its one
// error result, and a restart finds no question.
func TestAskAsyncPostFailureAfterCheckpointPublishesNothing(t *testing.T) {
	f1Isolate(t)
	base, root := t.TempDir(), t.TempDir()
	e := f1NewEnv(t, base, root, &f1Provider{script: f1Script(false)})
	sess := e.session(f1KindHuman)
	var announced atomic.Int32
	sess.runtime.Bus.Subscribe(func(bus.AskUserRequested) { announced.Add(1) })
	e.mgr.questions.hooks.afterCheckpoint = func() { _ = e.mgr.tasks.Close() }

	e.send(sess, "F1-TRIGGER please ask")
	if !f1Eventually(f1Guard, func() bool { return len(f1Results(sess.History())) == 1 && sessState(sess) == StateIdle }) {
		t.Fatalf("the failed post never produced its result (state=%s)", sessState(sess))
	}
	sess.runtime.Bus.Drain(2 * time.Second)

	res := f1Results(sess.History())
	if !res[0].IsError || !strings.Contains(f1MsgText(res[0]), "was not posted") {
		t.Fatalf("result = error:%v %q, want the failure of the post", res[0].IsError, f1MsgText(res[0]))
	}
	if _, ok := res[0].Custom["question_id"]; ok {
		t.Fatalf("a failed post carries a question id: %v", res[0].Custom)
	}
	if ok, why := f1PairsValid(sess.History()); !ok {
		t.Fatalf("pairs: %s", why)
	}
	if announced.Load() != 0 || f1Pending(sess) != nil {
		t.Fatalf("a post that failed was announced (events=%d, pending=%v)", announced.Load(), f1Pending(sess))
	}

	sessionID := sess.ID
	e.halt()
	e2 := f1NewEnv(t, base, root, &f1Provider{script: f1Script(false)})
	open, err := e2.mgr.tasks.OpenQuestions(context.Background(), sessionID)
	if err != nil || len(open) != 0 {
		t.Fatalf("questions after the restart = %v, %v, want none", open, err)
	}
	resumed, err := e2.mgr.ResumeSession(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if f1Pending(resumed) != nil {
		t.Fatal("the restart projected a question that was never posted")
	}
	if got := f1Results(resumed.History()); len(got) != 1 || !got[0].IsError {
		t.Fatalf("results after the restart = %+v, want the one error", got)
	}
}

// A process death at each side of the commit. Before it the call is closed as
// an unpublished one and no question exists; after it the next load closes the
// call with the very acknowledgement the row stored, once, also after another
// restart. In neither case does the model run.
func TestAskAsyncCrashAroundThePostCommit(t *testing.T) {
	if mode := os.Getenv(f1CrashChildEnv); mode != "" {
		f1PostBoundaryChild(t, mode)
		return
	}
	for _, mode := range []string{"after-checkpoint", "after-commit"} {
		t.Run(mode, func(t *testing.T) {
			d := f1NewCrashDirs(t)
			t.Setenv("MOA_CONFIG_DIR", d.config)
			m := f1RunCrashChild(t, "^TestAskAsyncCrashAroundThePostCommit$", mode, d)

			prov := &f1Provider{script: f1Script(false)}
			e := f1NewEnv(t, m.Base, m.Workspace, prov)
			open, err := e.mgr.tasks.OpenQuestions(context.Background(), m.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if (mode == "after-commit") != (len(open) == 1) {
				t.Fatalf("%s: open questions = %d", mode, len(open))
			}

			for restart := 0; restart < 2; restart++ {
				sess, err := e.mgr.ResumeSession(m.SessionID)
				if err != nil {
					t.Fatal(err)
				}
				results := f1Results(sess.History())
				if len(results) != 1 {
					t.Fatalf("%s: results for the call = %d, want exactly one", mode, len(results))
				}
				if ok, why := f1PairsValid(sess.History()); !ok {
					t.Fatalf("pairs: %s", why)
				}
				switch mode {
				case "after-checkpoint":
					if !results[0].IsError {
						t.Fatalf("an unpublished call closed with %q, want an error", f1MsgText(results[0]))
					}
					if _, ok := results[0].Custom["question_id"]; ok || f1Pending(sess) != nil {
						t.Fatalf("a question appeared that was never committed: %v", results[0].Custom)
					}
				case "after-commit":
					f1AssertAck(t, results[0], m.QuestionID)
					if p := f1Pending(sess); p == nil || p.ID != m.QuestionID {
						t.Fatalf("the committed question is not open after the restart: %+v", p)
					}
					want := open[0].Ack
					if f1MsgText(results[0]) != want.Text {
						t.Fatalf("recovered acknowledgement %q differs from the stored one %q", f1MsgText(results[0]), want.Text)
					}
				}
				if restart == 0 {
					// Save the repaired transcript and restart again: still one result.
					if err := e.mgr.CloseSession(m.SessionID); err != nil {
						t.Fatal(err)
					}
				}
			}
			if !f1Stable(400*time.Millisecond, func() bool { return prov.count() == 0 }) {
				t.Fatalf("%s: recovering a post called the model %d time(s)", mode, prov.count())
			}
		})
	}
}

func f1PostBoundaryChild(t *testing.T, mode string) {
	base, root, cfgDir := os.Getenv(f1CrashBaseEnv), os.Getenv(f1CrashWorkspaceEnv), os.Getenv(f1CrashConfigEnv)
	t.Setenv("MOA_CONFIG_DIR", cfgDir)
	prov := &f1Provider{script: f1Script(false)}
	mgr := f1CrashManager(base, root, cfgDir, prov)
	e := &f1Env{t: t, mgr: mgr, prov: prov, root: root}
	sess := e.session(f1KindHuman)
	crash := func() {
		qid := "none"
		if open, err := mgr.tasks.OpenQuestions(context.Background(), sess.ID); err == nil && len(open) == 1 {
			qid = open[0].ID
		}
		f1CrashNow(t, sess.ID, qid)
	}
	switch mode {
	case "after-checkpoint":
		mgr.questions.hooks.afterCheckpoint = crash
	case "after-commit":
		mgr.questions.hooks.afterCommit = crash
	}
	e.send(sess, "F1-TRIGGER please ask")
	<-time.After(30 * time.Second)
	t.Fatal("the post never reached the crash point")
}

// A retry of the same invocation gets the same acknowledgement and no second
// question or announcement; a different invocation while one is open is
// refused with the open one's id, and neither announces anything.
func TestAskAsyncPostRetryAndBound(t *testing.T) {
	f1Isolate(t)
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{script: f1Script(false)})
	sess := e.session(f1KindHuman)
	announced := make(chan string, 8)
	sess.runtime.Bus.Subscribe(func(ev bus.AskUserRequested) { announced <- ev.ID })
	tool := f1AskUserTool(t, sess)
	params := func() map[string]any {
		return map[string]any{"questions": []any{map[string]any{"question": "Which branch?"}}}
	}
	call := func(callID string) core.Result {
		ctx := core.WithToolCallMessageID(core.WithToolCallID(context.Background(), callID), "msg-assistant-1")
		res, err := tool.Execute(ctx, params(), nil)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	first := call("tc-a")
	if first.IsError {
		t.Fatalf("post failed: %s", toolText(first))
	}
	qid, _ := first.Custom["question_id"].(string)
	retry := call("tc-a")
	if retry.IsError || retry.Custom["question_id"] != qid || toolText(retry) != toolText(first) {
		t.Fatalf("retry = error:%v %v %q, want the same acknowledgement as %q", retry.IsError, retry.Custom, toolText(retry), toolText(first))
	}
	if open, _ := e.mgr.tasks.OpenQuestions(context.Background(), sess.ID); len(open) != 1 {
		t.Fatalf("open questions after a retry = %d, want 1", len(open))
	}

	second := call("tc-b")
	if !second.IsError || !strings.Contains(toolText(second), qid) {
		t.Fatalf("second question with one open = error:%v %q, want an error naming %s", second.IsError, toolText(second), qid)
	}
	if _, ok := second.Custom["question_id"]; ok {
		t.Fatalf("the refused post carries a question id: %v", second.Custom)
	}
	sess.runtime.Bus.Drain(2 * time.Second)
	if n := len(announced); n != 1 {
		t.Fatalf("announcements = %d, want exactly the first question", n)
	}
	if got := <-announced; got != qid {
		t.Fatalf("announced %s, want %s", got, qid)
	}
	if open, _ := e.mgr.tasks.OpenQuestions(context.Background(), sess.ID); len(open) != 1 || open[0].ID != qid {
		t.Fatalf("open questions after the refused post = %+v", open)
	}
}
