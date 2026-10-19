package serve

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/askuser"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

func TestRecheckRuntimeJournalRecoveryDoesNotRemoveAnActiveWrite(t *testing.T) {
	s := f1NewJournalScene(t)
	b := s.e.session(f1KindHuman)
	s.e.send(b, "F1-TRIGGER please ask")
	bq := waitPendingAsk(t, b).ID
	f1Settle(t, b)
	bid := b.ID
	s.e.halt()
	// Prepare the same durable, not-yet-admitted state as a response accepted
	// just before a process dies, without allowing the old dispatcher to read it.
	r := tasks.New(s.e.mgr.tasks.Path())
	if _, err := r.AnswerQuestion(context.Background(), bid, bq, []string{"B original answer"}, askuser.Responder{Kind: askuser.ResponderUser}, renderQuestionAnswer); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	dir := f1JournalDir(s.e.mgr)
	if err := os.Chmod(dir, 0000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0700) }()
	e := f1NewEnv(t, s.base, s.root, &f1Provider{script: f1TwoAskScript()})
	if !e.mgr.questions.journalUnread.Load() {
		t.Fatal("fixture has no unread global barrier")
	}
	func() {
		e.mgr.notices.mu.Lock()
		defer e.mgr.notices.mu.Unlock()
		live, loaded := e.mgr.Get(bid)
		if !loaded {
			var err error
			live, err = e.mgr.ResumeSession(bid)
			if err != nil {
				t.Fatal(err)
			}
		}
		f1ActiveForStop(t, e, live)
		created, release := make(chan string, 1), make(chan struct{})
		var once sync.Once
		free := func() { once.Do(func() { close(release) }) }
		defer free()
		defer reviewWriteAfterCreate.Store(nil)
		reviewWriteAfterCreate.Store(&reviewWriteCallback{call: func(q *questionService, id, path string) {
			if q == e.mgr.questions && id == bid {
				created <- path
				<-release
			}
		}})
		if err := os.Chmod(dir, 0700); err != nil {
			t.Fatal(err)
		}
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			resp := apiReq(t, e.srv, "POST", "/api/sessions/"+bid+"/cancel-and-recall", "")
			defer resp.Body.Close()
			var body map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || resp.StatusCode != 200 || body["fence_error"] == nil {
				t.Errorf("Stop did not report its failed SQLite fence: %d %+v %v", resp.StatusCode, body, err)
			}
		}()
		var tmp string
		select {
		case tmp = <-created:
		case <-time.After(f1Guard):
			t.Fatal("Stop did not open a temporary journal file")
		}
		if _, err := os.Stat(tmp); err != nil {
			t.Fatal(err)
		}
		// A real answer of the other session invokes the new runtime directory
		// recovery while B still owns the open temporary file. SQLite stays broken.
		type answerResult struct {
			code int
			body string
		}
		answerDone := make(chan answerResult, 1)
		go func() {
			code, body := f1PostAsk(t, e, s.sess.ID, s.q2, "A retry")
			answerDone <- answerResult{code, body}
		}()
		var answered answerResult
		select {
		case answered = <-answerDone:
			_, tmpErr := os.Stat(tmp)
			t.Logf("in-flight temporary after recovery, writer still paused: %v", tmpErr)
			free()
		case <-time.After(time.Second):
			// A correct implementation may serialize discovery with the active
			// writer. Do not require the unsafe interleaving to remain possible.
			free()
			select {
			case answered = <-answerDone:
			case <-time.After(f1Guard):
				t.Fatal("A recovery did not finish after releasing B")
			}
		}
		if answered.code != 503 {
			t.Fatalf("A recovery while SQLite remains broken=%d %s", answered.code, answered.body)
		}
		select {
		case <-stopped:
		case <-time.After(f1Guard):
			t.Fatal("Stop did not finish after releasing its write")
		}
		pollUntil(t, f1Guard, "B stopped", func() bool { return sessState(live) == StateIdle && !live.runtime.Context().Agent.IsRunning() })
	}()
	e.halt()
	s.lift()
	p := &f1Provider{script: f1TwoAskScript()}
	e2 := f1NewEnv(t, s.base, s.root, p)
	key := core.PromptCacheKey(bid)
	calls := func() int {
		return p.countWhere(func(req core.Request) bool {
			return req.Options.PromptCacheKey == key && strings.Contains(f1LatestFixtureInput(req), "<question_answer")
		})
	}
	if !f1Stable(time.Second, func() bool { return calls() == 0 }) {
		q, _ := e2.mgr.tasks.GetQuestion(context.Background(), bq)
		t.Fatalf("runtime journal scan removed another Stop's active temp, so that Stop had no durable journal: old B answer resumed after restart, calls=%d work=%s", calls(), q.Work)
	}
}
