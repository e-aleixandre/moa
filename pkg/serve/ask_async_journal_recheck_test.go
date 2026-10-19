package serve

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/tasks"
)

func TestRecheckDirectorySyncFailureKeepsBarrierAfterUnlink(t *testing.T) {
	s := f1NewJournalScene(t)
	func() {
		s.e.mgr.notices.mu.Lock()
		defer s.e.mgr.notices.mu.Unlock()
		dir := f1JournalDir(s.e.mgr)
		reviewSyncFailure.Store(&reviewSyncFault{path: dir, err: syscall.EIO})
		defer reviewSyncFailure.Store(nil)
		s.lift()
		for i := 0; i < 2; i++ {
			if code, body := f1PostAsk(t, s.e, s.sess.ID, s.q2, "new after Stop"); code != http.StatusServiceUnavailable {
				t.Fatalf("retry %d while directory sync fails=%d %s", i, code, body)
			}
			if f1FileExists(f1JournalPath(s.e.mgr, s.sess.ID)) {
				t.Fatal("unlink did not actually succeed before the injected sync error")
			}
			q, err := s.e.mgr.tasks.GetQuestion(context.Background(), s.q2)
			if err != nil || q.State != tasks.QuestionOpen || q.EventID != "" {
				t.Fatalf("failed sync consumed new answer: %+v %v", q, err)
			}
			if _, ok := s.e.mgr.questions.unfenced.Load(s.sess.ID); !ok {
				t.Fatal("failed sync released RAM barrier")
			}
		}
		reviewSyncFailure.Store(nil)
		if code, body := f1PostAsk(t, s.e, s.sess.ID, s.q2, "new after Stop"); code != 204 {
			t.Fatalf("after sync repaired=%d %s", code, body)
		}
	}()
	f1Settle(t, s.sess)
}

func TestRecheckConcurrentJournalLoadsDoNotRebarANewAnswer(t *testing.T) {
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
		t.Fatal("fixture did not enter global unread state")
	}
	// Both HTTP handlers, not direct map writes, recover the global barrier.
	// Pause only the first loader immediately after its filesystem snapshot.
	e.mgr.notices.mu.Lock()
	defer e.mgr.notices.mu.Unlock()
	if code, body := f1PostAsk(t, e, bid, bq, "B answer"); code != http.StatusServiceUnavailable {
		t.Fatalf("unrelated B answered while the journal was unreadable=%d %s", code, body)
	}
	if q, err := e.mgr.tasks.GetQuestion(context.Background(), bq); err != nil || q.State != tasks.QuestionOpen || q.EventID != "" {
		t.Fatalf("the global 503 consumed B's question: %+v %v", q, err)
	}
	readReached, readRelease := make(chan struct{}), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(readRelease) }) }
	defer release()
	defer reviewLoadAfterRead.Store(nil)
	var loads atomic.Int32
	reviewLoadAfterRead.Store(&reviewLoadCallback{call: func(q *questionService, ids []string) {
		if q != e.mgr.questions {
			return
		}
		if loads.Add(1) == 1 {
			found := false
			for _, id := range ids {
				found = found || id == aid
			}
			if !found {
				t.Errorf("the stale snapshot did not contain A: %v", ids)
			}
			close(readReached)
			<-readRelease
		}
	}})
	s.lift()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	type result struct {
		code int
		body string
	}
	bdone, adone := make(chan result, 1), make(chan result, 1)
	go func() { code, body := f1PostAsk(t, e, bid, bq, "B answer"); bdone <- result{code, body} }()
	select {
	case <-readReached:
	case <-time.After(f1Guard):
		t.Fatal("first journal loader did not reach its snapshot")
	}
	go func() { code, body := f1PostAsk(t, e, aid, s.q2, "A NEW answer"); adone <- result{code, body} }()
	var aresult result
	select {
	case aresult = <-adone:
		q, err := e.mgr.tasks.GetQuestion(context.Background(), s.q2)
		if aresult.code != 204 || err != nil || q.Work != tasks.WorkDue {
			t.Fatalf("A was not accepted before releasing B: HTTP=%+v Q=%+v %v", aresult, q, err)
		}
		if f1FileExists(filepath.Join(dir, aid)) {
			t.Fatal("A's original journal was not retired")
		}
		if _, ok := e.mgr.questions.unfenced.Load(aid); ok {
			t.Fatal("A did not finish retiring its original fence")
		}
		release()
	case <-time.After(time.Second):
		// A correct serialized loader may hold A behind B. Let it finish: the
		// regression test must not require the buggy concurrent interleaving.
		release()
		select {
		case aresult = <-adone:
		case <-time.After(f1Guard):
			t.Fatal("A did not finish after releasing the first loader")
		}
	}
	if aresult.code != 204 {
		t.Fatalf("A acceptance=%+v", aresult)
	}
	select {
	case r := <-bdone:
		if r.code != 204 {
			t.Fatalf("B acceptance=%+v", r)
		}
	case <-time.After(f1Guard):
		t.Fatal("B never finished")
	}
	// An idempotent human retry must not fence the answer it already accepted.
	if code, body := f1PostAsk(t, e, aid, s.q2, "A NEW answer"); code != 204 {
		t.Fatalf("A duplicate=%d %s", code, body)
	}
	q, err := e.mgr.tasks.GetQuestion(context.Background(), s.q2)
	if err != nil {
		t.Fatal(err)
	}
	if q.Work != tasks.WorkDue {
		t.Fatalf("stale journal snapshot from B rebarred A after A retired its journal and accepted a new answer: new_work=%s, want due", q.Work)
	}
}
