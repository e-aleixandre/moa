package serve

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// A Stop whose fence could not be saved is journaled next to the task
// database, so that neither a restart nor a later answer can resurrect what
// the Stop cut. The journal covers Stop only: a recall keeps its RAM barrier.

const f1SecondTrigger = "F1-TRIGGER2"

// f1TwoAskScript lets the model ask a second question, with its own call ID,
// when the message says so.
func f1TwoAskScript() func(core.Request) <-chan core.AssistantEvent {
	base := f1Script(false)
	return func(req core.Request) <-chan core.AssistantEvent {
		answered := false
		for _, m := range req.Messages {
			answered = answered || (m.Role == "tool_result" && m.ToolCallID == "f1-call-2")
		}
		if strings.HasPrefix(f1LatestFixtureInput(req), f1SecondTrigger) && !answered {
			ch, _ := toolCallHandlerFor("f1-call-2", "ask_user", map[string]any{
				"questions": []any{map[string]any{"question": "Second question?"}},
			})(context.Background(), core.Request{})
			return ch
		}
		return base(req)
	}
}

func f1JournalDir(mgr *Manager) string {
	return filepath.Join(filepath.Dir(mgr.tasks.Path()), "question-fences")
}

func f1JournalPath(mgr *Manager, sessionID string) string {
	return filepath.Join(f1JournalDir(mgr), sessionID)
}

func f1FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// f1FenceTrigger makes the database refuse the update a fence commits (an
// accepted answer's delivery turning into an append). The returned func lifts
// the failure.
func f1FenceTrigger(t *testing.T, dbPath string) (lift func()) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(`CREATE TRIGGER stop_no_fence BEFORE UPDATE OF method ON task_notifications WHEN NEW.kind='question_answer' AND NEW.method='append' BEGIN SELECT RAISE(ABORT,'test fence failure'); END`); err != nil {
		t.Fatal(err)
	}
	return func() {
		if _, err := db.Exec(`DROP TRIGGER IF EXISTS stop_no_fence`); err != nil {
			t.Fatal(err)
		}
	}
}

type f1JournalScene struct {
	e      *f1Env
	base   string
	root   string
	sess   *ManagedSession
	q1, q2 string // q1 is answered before Stop; q2 is asked after it and stays open
	lift   func()
	free   func() // lets the held admission go
}

// f1NewJournalScene leaves one accepted answer held at its admission and a
// second question open, with every fence failing.
func f1NewJournalScene(t *testing.T) *f1JournalScene {
	t.Helper()
	f1Isolate(t)
	s := &f1JournalScene{base: t.TempDir(), root: t.TempDir()}
	reached := make(chan struct{}, 4)
	release := make(chan struct{})
	var once sync.Once
	s.free = func() { once.Do(func() { close(release) }) }
	t.Cleanup(s.free)
	hooks := taskSchedulerHooks{beforeAdmit: func(string) {
		select {
		case reached <- struct{}{}:
		default:
		}
		<-release
	}}
	s.e = f1NewEnv(t, s.base, s.root, &f1Provider{script: f1TwoAskScript()}, hooks)
	s.sess = s.e.session(f1KindHuman)
	s.e.send(s.sess, "F1-TRIGGER please ask")
	s.q1 = waitPendingAsk(t, s.sess).ID
	f1Settle(t, s.sess)
	if code, body := f1PostAsk(t, s.e, s.sess.ID, s.q1, "yes"); code != 204 {
		t.Fatalf("answer = %d %s", code, body)
	}
	select {
	case <-reached:
	case <-time.After(f1Guard):
		t.Fatal("answer not reserved")
	}
	// A session holds one open question at a time: the second one can only be
	// asked once the first has been answered.
	s.e.send(s.sess, f1SecondTrigger+" ask again")
	pollUntil(t, f1Guard, "second question", func() bool {
		for _, ask := range s.sess.runtime.Context().Approvals.AsyncAsks() {
			if ask.ID != s.q1 {
				s.q2 = ask.ID
			}
		}
		return s.q2 != ""
	})
	f1Settle(t, s.sess)
	f1ActiveForStop(t, s.e, s.sess)
	s.lift = f1FenceTrigger(t, s.e.mgr.tasks.Path())
	resp := apiReq(t, s.e.srv, "POST", "/api/sessions/"+s.sess.ID+"/cancel-and-recall", "")
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || body["fence_error"] == nil {
		t.Fatalf("the fixture's Stop must report the unsaved fence: %d %+v", resp.StatusCode, body)
	}
	pollUntil(t, f1Guard, "stopped", func() bool {
		return sessState(s.sess) == StateIdle && !s.sess.runtime.Context().Agent.IsRunning()
	})
	s.free()
	waitDispatcherIdle(s.e)
	return s
}

func TestAskAsyncFailedStopFenceIsJournaled(t *testing.T) {
	s := f1NewJournalScene(t)
	path := f1JournalPath(s.e.mgr, s.sess.ID)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("a Stop whose fence failed left no journal: %v", err)
	}
	var entry struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &entry); err != nil || entry.SessionID != s.sess.ID {
		t.Fatalf("journal content = %q (%v)", raw, err)
	}
	if entries, _ := os.ReadDir(f1JournalDir(s.e.mgr)); len(entries) != 1 {
		t.Fatalf("journal directory holds %d entries, want only the session's file", len(entries))
	}
}

func TestAskAsyncRestartAppliesJournaledStopFence(t *testing.T) {
	s := f1NewJournalScene(t)
	sessionID := s.sess.ID
	s.lift()
	s.e.halt()

	prov := &f1Provider{script: f1TwoAskScript()}
	e2 := f1NewEnv(t, s.base, s.root, prov)
	if f1FileExists(f1JournalPath(e2.mgr, sessionID)) {
		t.Fatal("the journal was not consumed by the restart")
	}
	if _, barred := e2.mgr.questions.unfenced.Load(sessionID); barred {
		t.Fatal("a fence that was saved at startup left the session barred")
	}
	f1AssertFenced(t, e2, s.q1)
	e2.mgr.notices.nudge()
	if !f1Stable(300*time.Millisecond, func() bool {
		return prov.countWhere(func(r core.Request) bool { return f1LastUserIsAnswer(r) }) == 0
	}) {
		t.Fatal("the answer cut by Stop reached the provider after the restart")
	}
}

func TestAskAsyncRestartWithBrokenStoreKeepsTheBarrier(t *testing.T) {
	s := f1NewJournalScene(t)
	sessionID := s.sess.ID
	s.e.halt()

	prov := &f1Provider{script: f1TwoAskScript()}
	e2 := f1NewEnv(t, s.base, s.root, prov)
	if !f1FileExists(f1JournalPath(e2.mgr, sessionID)) {
		t.Fatal("the journal was dropped although its fence could not be saved")
	}
	if _, barred := e2.mgr.questions.unfenced.Load(sessionID); !barred {
		t.Fatal("the session is not barred although its Stop fence is still unsaved")
	}
	e2.mgr.notices.nudge()
	waitDispatcherIdle(e2)
	if !f1Stable(300*time.Millisecond, func() bool {
		return prov.countWhere(func(r core.Request) bool { return f1LastUserIsAnswer(r) }) == 0
	}) {
		t.Fatal("an answer was admitted or continued while the Stop fence is unsaved")
	}
}

func TestAskAsyncDeleteRemovesTheJournal(t *testing.T) {
	t.Run("loaded session", func(t *testing.T) {
		s := f1NewJournalScene(t)
		path := f1JournalPath(s.e.mgr, s.sess.ID)
		if !f1FileExists(path) {
			t.Fatal("fixture has no journal")
		}
		if err := s.e.mgr.Delete(s.sess.ID); err != nil {
			t.Fatal(err)
		}
		if f1FileExists(path) {
			t.Fatal("Delete left the session's journal behind")
		}
	})
	t.Run("saved session", func(t *testing.T) {
		s := f1NewJournalScene(t)
		sessionID := s.sess.ID
		s.e.halt()
		e2 := f1NewEnv(t, s.base, s.root, &f1Provider{script: f1TwoAskScript()})
		path := f1JournalPath(e2.mgr, sessionID)
		if !f1FileExists(path) {
			t.Fatal("fixture has no journal")
		}
		// The dispatcher may be loading the session for its held answer; Delete
		// refuses until that is over, whichever way the session ends up.
		var err error
		if !f1Eventually(f1Guard, func() bool {
			err = e2.mgr.Delete(sessionID)
			return !errors.Is(err, ErrBusy)
		}) || err != nil {
			t.Fatalf("Delete = %v", err)
		}
		if f1FileExists(path) {
			t.Fatal("Delete left the saved session's journal behind")
		}
	})
}

func TestAskAsyncStopCreatesNoJournalWhenNothingFailed(t *testing.T) {
	t.Run("pilot never used", func(t *testing.T) {
		pinSyncAskUser(t)
		t.Setenv("MOA_CONFIG_DIR", t.TempDir())
		e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{script: f1Script(false)})
		sess := e.session(f1KindHuman)
		f1ActiveForStop(t, e, sess)
		f1Stop(t, e, sess)
		if f1FileExists(f1JournalDir(e.mgr)) {
			t.Fatal("a Stop with the pilot off created the journal directory")
		}
	})
	t.Run("fence saved", func(t *testing.T) {
		f1Isolate(t)
		e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{script: f1Script(false)})
		sess := e.session(f1KindHuman)
		e.send(sess, "F1-TRIGGER please ask")
		waitPendingAsk(t, sess)
		f1Settle(t, sess)
		f1Stop(t, e, sess)
		if f1FileExists(f1JournalDir(e.mgr)) {
			t.Fatal("a Stop whose fence was saved created the journal directory")
		}
	})
}
