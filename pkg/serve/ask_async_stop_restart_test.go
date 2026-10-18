package serve

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestAskAsyncStopWithUnsavedFenceDoesNotResumeAfterRestart(t *testing.T) {
	if os.Getenv(f1CrashChildEnv) == "stop-unfenced" {
		base, root, cfg := os.Getenv(f1CrashBaseEnv), os.Getenv(f1CrashWorkspaceEnv), os.Getenv(f1CrashConfigEnv)
		t.Setenv("MOA_CONFIG_DIR", cfg)
		p := &f1Provider{script: f1Script(false)}
		mgr := f1CrashManager(base, root, cfg, p)
		reached, release := make(chan struct{}, 1), make(chan struct{})
		var once sync.Once
		mgr.planner.hooks.beforeAdmit = func(string) { reached <- struct{}{}; <-release }
		e := &f1Env{t: t, mgr: mgr, prov: p, root: root, srv: newF1ChildServer(mgr)}
		s := e.session(f1KindHuman)
		e.send(s, "F1-TRIGGER please ask")
		qid := waitPendingAsk(t, s).ID
		f1Settle(t, s)
		f1ActiveForStop(t, e, s)
		if code, body := f1PostAsk(t, e, s.ID, qid, "yes"); code != 204 {
			t.Fatalf("answer=%d %s", code, body)
		}
		select {
		case <-reached:
		case <-time.After(f1Guard):
			t.Fatal("answer not reserved")
		}
		db, err := sql.Open("sqlite", "file:"+mgr.tasks.Path()+"?_pragma=busy_timeout(5000)")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`CREATE TRIGGER stop_no_fence BEFORE UPDATE OF method ON task_notifications WHEN NEW.kind='question_answer' AND NEW.method='append' BEGIN SELECT RAISE(ABORT,'test fence failure'); END`); err != nil {
			t.Fatal(err)
		}
		before := p.countSession(t, s.ID)
		resp := apiReq(t, e.srv, "POST", "/api/sessions/"+s.ID+"/cancel-and-recall", "")
		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		t.Logf("actual Stop HTTP=%d fence_error=%v", resp.StatusCode, body["fence_error"])
		if resp.StatusCode != 200 || body["fence_error"] == nil {
			t.Fatalf("invalid Stop response: %d %+v", resp.StatusCode, body)
		}
		pollUntil(t, f1Guard, "stopped", func() bool { return sessState(s) == StateIdle && !s.runtime.Context().Agent.IsRunning() })
		once.Do(func() { close(release) })
		waitDispatcherIdle(e)
		if !f1Stable(300*time.Millisecond, func() bool { return p.countSession(t, s.ID) == before }) {
			t.Fatal("RAM barrier did not stop reactivation in original process")
		}
		if _, blocked := mgr.questions.unfenced.Load(s.ID); !blocked {
			t.Fatal("fixture lost failed fence before crash")
		}
		f1CrashNow(t, s.ID, qid)
		return
	}
	t.Skip("RED: a failed Stop fence is lost on a crash, so the restart replays the answer accepted before Stop (activation condition, see the fence journal proposal)")
	d := f1NewCrashDirs(t)
	t.Setenv("MOA_CONFIG_DIR", d.config)
	manifest := f1RunCrashChild(t, "^TestAskAsyncStopWithUnsavedFenceDoesNotResumeAfterRestart$", "stop-unfenced", d)
	p := &f1Provider{script: f1Script(false)}
	e := f1NewEnv(t, manifest.Base, manifest.Workspace, p)
	key := core.PromptCacheKey(manifest.SessionID)
	calls := func() int {
		return p.countWhere(func(r core.Request) bool { return r.Options.PromptCacheKey == key && f1LastUserIsAnswer(r) })
	}
	// Stop was confirmed to the user before the fence was saved: the answer
	// accepted before it must not reach the provider again after a restart.
	if f1Stable(f1Guard, func() bool { return calls() == 0 }) {
		return
	}
	q, err := e.mgr.tasks.GetQuestion(context.Background(), manifest.QuestionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("answer accepted before Stop reached the provider again after restart: calls=%d work=%s", calls(), q.Work)
}
