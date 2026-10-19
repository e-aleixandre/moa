package serve

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

func TestRecheckRecallCannotEraseAStopPublishedByJournalLoad(t *testing.T) {
	s := f1NewJournalScene(t)
	b := s.e.session(f1KindHuman)
	s.e.send(b, "F1-TRIGGER please ask")
	bq := waitPendingAsk(t, b).ID
	f1Settle(t, b)
	aid, bid := s.sess.ID, b.ID
	s.e.halt()
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
		if _, ok := e.mgr.Get(aid); !ok {
			if _, err := e.mgr.ResumeSession(aid); err != nil {
				t.Fatal(err)
			}
		}
		if q, err := e.mgr.tasks.GetQuestion(context.Background(), s.q1); err != nil || q.Work != tasks.WorkDue {
			t.Fatalf("original answer was not still due: %+v %v", q, err)
		}
		reached, releaseCh := make(chan struct{}), make(chan struct{})
		var once sync.Once
		release := func() { once.Do(func() { close(releaseCh) }) }
		defer release()
		defer reviewLoadAfterRead.Store(nil)
		reviewLoadAfterRead.Store(&reviewLoadCallback{call: func(q *questionService, ids []string) {
			if q != e.mgr.questions {
				return
			}
			found := false
			for _, id := range ids {
				found = found || id == aid
			}
			if !found {
				t.Errorf("loader did not snapshot the old A Stop: %v", ids)
			}
			close(reached)
			<-releaseCh
		}})
		s.lift()
		if err := os.Chmod(dir, 0700); err != nil {
			t.Fatal(err)
		}
		type answerResult struct {
			code int
			body string
		}
		bdone := make(chan answerResult, 1)
		go func() { code, body := f1PostAsk(t, e, bid, bq, "B answer"); bdone <- answerResult{code, body} }()
		select {
		case <-reached:
		case <-time.After(f1Guard):
			t.Fatal("B did not pause with journalMu before publishing A")
		}
		recallDone := make(chan int, 1)
		go func() {
			r := apiReq(t, e.srv, "POST", "/api/sessions/"+aid+"/steers/cancel", "")
			if err := r.Body.Close(); err != nil {
				t.Error(err)
			}
			recallDone <- r.StatusCode
		}()
		// This observation selects the legal schedule in which Recall chose false
		// and saved that weaker fence before waiting for the loader's journalMu.
		// A correct implementation may serialize earlier: release it after the
		// observation window rather than requiring that buggy schedule forever.
		savedRecall := f1Eventually(time.Second, func() bool {
			q, err := e.mgr.tasks.GetQuestion(context.Background(), s.q1)
			return err == nil && q.Work == tasks.WorkStopped
		})
		t.Logf("recall saved its fence before journal publication: %v", savedRecall)
		release()
		select {
		case r := <-bdone:
			if r.code != 204 {
				t.Fatalf("B answer=%d %s", r.code, r.body)
			}
		case <-time.After(f1Guard):
			t.Fatal("B answer blocked")
		}
		select {
		case code := <-recallDone:
			if code != 204 {
				t.Fatalf("recall=%d", code)
			}
		case <-time.After(f1Guard):
			t.Fatal("recall blocked")
		}
		oldFile := f1FileExists(f1JournalPath(e.mgr, aid))
		_, barred := e.mgr.questions.unfenced.Load(aid)
		t.Logf("after recall: old Stop file=%v RAM barrier=%v globalUnread=%v", oldFile, barred, e.mgr.questions.journalUnread.Load())
		if code, body := f1PostAsk(t, e, aid, s.q2, "NEW explicit answer after Stop"); code != 204 {
			t.Fatalf("new A answer=%d %s", code, body)
		}
		if q, err := e.mgr.tasks.GetQuestion(context.Background(), s.q2); err != nil || q.Work != tasks.WorkDue {
			t.Fatalf("new A answer not accepted as due: %+v %v", q, err)
		}
		// As in the CLI's signal path, cancel the root context before releasing
		// the paused dispatcher so shutdown cannot admit either new answer.
		e.cancel()
	}()
	e.halt()
	p := &f1Provider{script: f1TwoAskScript()}
	e2 := f1NewEnv(t, s.base, s.root, p)
	q, err := e2.mgr.tasks.GetQuestion(context.Background(), s.q2)
	if err != nil {
		t.Fatal(err)
	}
	if q.Work == tasks.WorkStopped {
		t.Fatalf("Recall erased the Stop barrier just published by a concurrent journal load: A's NEW HTTP-204 answer became stopped after restart")
	}
	key := core.PromptCacheKey(aid)
	pollUntil(t, f1Guard, "new A answer is read after restart", func() bool {
		return p.countWhere(func(r core.Request) bool {
			return r.Options.PromptCacheKey == key && strings.Contains(f1LatestFixtureInput(r), "<question_answer")
		}) > 0
	})
}
