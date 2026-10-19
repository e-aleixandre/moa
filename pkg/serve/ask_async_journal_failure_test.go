package serve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// This external review test uses the real stores, HTTP answer/Stop routes,
// production startup replay, and abrupt child exit. Only providers are fake.
func TestActivationReviewJournalFailureBoundaries(t *testing.T) {
	if mode := os.Getenv(f1CrashChildEnv); strings.HasPrefix(mode, "activation-review-") {
		reviewJournalCrashChild(t, strings.TrimPrefix(mode, "activation-review-"))
		return
	}
	for _, mode := range []string{"healthy-replay", "healthy-retirement", "unreadable-replay", "failed-retirement"} {
		t.Run(mode, func(t *testing.T) {
			d := f1NewCrashDirs(t)
			t.Setenv("MOA_CONFIG_DIR", d.config)
			manifest := f1RunCrashChild(t, "^TestActivationReviewJournalFailureBoundaries$", "activation-review-"+mode, d)
			dir := filepath.Join(filepath.Dir(manifest.Base), questionFenceDir)
			defer func() { _ = os.Chmod(dir, 0700) }()
			if mode == "unreadable-replay" {
				if _, err := os.ReadDir(dir); !errors.Is(err, os.ErrPermission) {
					t.Fatalf("fixture must fail to read the previously committed journal: %v", err)
				}
			}
			beforeRepo := tasks.New(filepath.Join(filepath.Dir(manifest.Base), "tasks.sqlite"))
			before, err := beforeRepo.GetQuestion(context.Background(), manifest.QuestionID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "failed-retirement" {
				if before.State != tasks.QuestionOpen {
					t.Fatalf("an answer was accepted while the old journal could not be removed: %+v", before)
				}
			} else if before.Work != tasks.WorkDue {
				t.Fatalf("fixture has no owed answer before restart: %+v", before)
			}
			_ = beforeRepo.Close()
			p := &f1Provider{script: f1TwoAskScript()}
			e := f1NewEnv(t, manifest.Base, manifest.Workspace, p)
			key := core.PromptCacheKey(manifest.SessionID)
			calls := func() int {
				return p.countWhere(func(r core.Request) bool {
					return r.Options.PromptCacheKey == key && strings.Contains(f1LatestFixtureInput(r), "<question_answer")
				})
			}
			if mode == "healthy-replay" {
				q, err := e.mgr.tasks.GetQuestion(context.Background(), manifest.QuestionID)
				if err != nil || q.Work != tasks.WorkStopped {
					t.Fatalf("healthy replay did not save the old Stop: %+v %v", q, err)
				}
				if f1FileExists(f1JournalPath(e.mgr, manifest.SessionID)) {
					t.Fatal("healthy replay retained the old journal")
				}
				if !f1Stable(time.Second, func() bool { return calls() == 0 }) {
					t.Fatal("healthy replay resumed the answer stopped before the crash")
				}
				return
			}
			if mode == "unreadable-replay" {
				n, _ := e.mgr.tasks.Notice(context.Background(), before.EventID)
				t.Logf("startup notice=%+v", n)
				_, resumeErr := e.mgr.ResumeSession(manifest.SessionID)
				t.Logf("opening saved session: %v", resumeErr)
				e.mgr.notices.sessionOpened(manifest.SessionID)
				if !f1Stable(5*time.Second, func() bool { return calls() == 0 }) {
					q, _ := e.mgr.tasks.GetQuestion(context.Background(), manifest.QuestionID)
					t.Fatalf("I5: committed Stop journal could not be read, but startup failed open: model_calls=%d work=%s", calls(), q.Work)
				}
				return
			}
			if mode == "failed-retirement" {
				// The journal is still not removable: the session stays barred
				// across the restart. Once storage is fixed, the same answer is
				// accepted and read once.
				if code, body := f1PostAsk(t, e, manifest.SessionID, manifest.QuestionID, "NEW explicit answer after Stop"); code != http.StatusServiceUnavailable {
					t.Fatalf("answer after restart with the journal still stuck=%d %s, want 503", code, body)
				}
				if !f1Stable(time.Second, func() bool { return calls() == 0 }) {
					t.Fatal("a barred session reached the model")
				}
				if err := os.Chmod(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if code, body := f1PostAsk(t, e, manifest.SessionID, manifest.QuestionID, "NEW explicit answer after Stop"); code != http.StatusNoContent {
					t.Fatalf("answer once the journal is removable=%d %s, want 204", code, body)
				}
				if f1FileExists(f1JournalPath(e.mgr, manifest.SessionID)) {
					t.Fatal("the old journal outlived its saved fence")
				}
			}
			q, err := e.mgr.tasks.GetQuestion(context.Background(), manifest.QuestionID)
			if err != nil {
				t.Fatal(err)
			}
			if q.Work == tasks.WorkStopped {
				t.Fatalf("post-Stop answer was accepted with HTTP 204 after SQLite recovered, but the obsolete journal stopped it on restart: before=%s after=%s", before.Work, q.Work)
			}
			pollUntil(t, f1Guard, "new post-Stop answer reaches the model", func() bool { return calls() > 0 })
		})
	}
}

func reviewJournalCrashChild(t *testing.T, mode string) {
	base, root, cfg := os.Getenv(f1CrashBaseEnv), os.Getenv(f1CrashWorkspaceEnv), os.Getenv(f1CrashConfigEnv)
	t.Setenv("MOA_CONFIG_DIR", cfg)
	p := &f1Provider{script: f1TwoAskScript()}
	mgr := f1CrashManager(base, root, cfg, p)
	reached, release := make(chan struct{}, 4), make(chan struct{})
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	mgr.planner.hooks.beforeAdmit = func(string) {
		select {
		case reached <- struct{}{}:
		default:
		}
		<-release
	}
	e := &f1Env{t: t, mgr: mgr, prov: p, root: root, srv: newF1ChildServer(mgr)}
	sess := e.session(f1KindHuman)
	e.send(sess, "F1-TRIGGER please ask")
	q1 := waitPendingAsk(t, sess).ID
	f1Settle(t, sess)
	if code, body := f1PostAsk(t, e, sess.ID, q1, "old answer"); code != 204 {
		t.Fatalf("first answer=%d %s", code, body)
	}
	select {
	case <-reached:
	case <-time.After(f1Guard):
		t.Fatal("old answer never reserved")
	}
	e.send(sess, f1SecondTrigger+" ask again")
	var q2 string
	pollUntil(t, f1Guard, "second question", func() bool {
		for _, q := range sess.runtime.Context().Approvals.AsyncAsks() {
			if q.ID != q1 {
				q2 = q.ID
			}
		}
		return q2 != ""
	})
	f1Settle(t, sess)
	f1ActiveForStop(t, e, sess)
	lift := f1FenceTrigger(t, mgr.tasks.Path())
	resp := apiReq(t, e.srv, "POST", "/api/sessions/"+sess.ID+"/cancel-and-recall", "")
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || body["fence_error"] == nil {
		t.Fatalf("Stop did not exercise the failed fence: %d %+v", resp.StatusCode, body)
	}
	pollUntil(t, f1Guard, "stopped", func() bool { return sessState(sess) == StateIdle && !sess.runtime.Context().Agent.IsRunning() })
	free()
	waitDispatcherIdle(e)
	dir := f1JournalDir(mgr)
	if !f1FileExists(f1JournalPath(mgr, sess.ID)) {
		t.Fatal("Stop journal was not committed")
	}
	// From here SQLite is healthy. The two failing cases concern the journal,
	// not a simultaneous failure of both persistence mechanisms.
	// Freeze the old dispatcher's retries before healing SQLite; the parent
	// verifies the old fence was still due when this process actually exited.
	mgr.notices.mu.Lock()
	lift()
	if mode == "healthy-replay" {
		f1CrashNow(t, sess.ID, q1)
		return
	}
	if mode == "unreadable-replay" {
		if err := os.Chmod(dir, 0000); err != nil {
			t.Fatal(err)
		}
		if _, err := os.ReadDir(dir); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("chmod did not cause EACCES: %v", err)
		}
		f1CrashNow(t, sess.ID, q1)
		return
	}
	// Keep dispatch paused between accepting the NEW answer and process exit,
	// so its durable owed work has not yet been admitted/read.
	if mode == "failed-retirement" {
		if err := os.Chmod(dir, 0500); err != nil {
			t.Fatal(err)
		}
	}
	if mode == "failed-retirement" {
		// Fail closed (changed from the review's reproducer, which required
		// 204 here): while the old journal cannot be removed it still claims a
		// Stop over this session, so a new answer is refused, not accepted
		// into owed work the stale file would stop at the next start.
		if code, body := f1PostAsk(t, e, sess.ID, q2, "NEW explicit answer after Stop"); code != http.StatusServiceUnavailable {
			t.Fatalf("new answer while the old journal cannot be removed=%d %s, want 503", code, body)
		}
		if !f1FileExists(f1JournalPath(mgr, sess.ID)) {
			t.Fatal("fixture did not retain the old journal")
		}
		f1CrashNow(t, sess.ID, q2)
		return
	}
	if code, body := f1PostAsk(t, e, sess.ID, q2, "NEW explicit answer after Stop"); code != 204 {
		t.Fatalf("new answer=%d %s", code, body)
	}
	remains := f1FileExists(f1JournalPath(mgr, sess.ID))
	if mode == "failed-retirement" && !remains {
		t.Fatal("fixture did not retain the old journal")
	}
	if mode == "healthy-retirement" && remains {
		t.Fatal("control did not remove old journal")
	}
	q, err := mgr.tasks.GetQuestion(context.Background(), q2)
	if err != nil || q.Work != tasks.WorkDue {
		t.Fatalf("new answer was not durably accepted as due: %+v %v", q, err)
	}
	f1CrashNow(t, sess.ID, q2)
}
