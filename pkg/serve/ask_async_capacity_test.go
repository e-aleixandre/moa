package serve

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// An accepted answer for a saved session that cannot be loaded yet (the house
// is full) waits and enters by itself once there is room: no human reopens the
// session. Tasks' own notices keep waiting for the owner
// (TestNoticeSessionLimitWaitsForTheOwner).

func f1CapacityEnv(t *testing.T, clock taskClock) *f1Env {
	t.Helper()
	f1Isolate(t)
	prov := &f1Provider{script: f1Script(false)}
	cfg := core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"}
	ctx, cancel := context.WithCancel(context.Background())
	root := t.TempDir()
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(core.Model) (core.Provider, error) { return prov, nil },
		DefaultModel:    core.Model{ID: "claude-haiku-4-5-20251001", Provider: "anthropic", MaxInput: 200_000},
		WorkspaceRoot:   root,
		MoaCfg:          cfg,
		ConfigLoader:    isolatedTestConfigLoader(t, cfg),
		SessionBaseDir:  t.TempDir(),
		SchedulePath:    filepath.Join(t.TempDir(), "schedules.json"),
		clock:           clock,
	})
	e := &f1Env{t: t, mgr: mgr, prov: prov, root: root, cancel: cancel}
	e.srv = newF1ChildServer(mgr)
	t.Cleanup(func() { e.srv.Close(); e.halt() })
	return e
}

func TestAskAsyncAnswerWaitsForCapacityWithoutResume(t *testing.T) {
	for _, via := range []string{"slot freed", "tick"} {
		t.Run(via, func(t *testing.T) {
			old := maxNoticeLoadedSessions
			maxNoticeLoadedSessions = 1
			t.Cleanup(func() { maxNoticeLoadedSessions = old })
			var clock *fakeTaskClock
			var tc taskClock
			if via == "tick" {
				clock = newFakeTaskClock(mustUTC("2026-10-18T08:00:00Z"))
				tc = clock
			}
			e := f1CapacityEnv(t, tc)

			asker := e.session(f1KindHuman)
			e.send(asker, "F1-TRIGGER please ask")
			qid := waitPendingAsk(t, asker).ID
			f1Settle(t, asker)
			if err := e.mgr.CloseSession(asker.ID); err != nil {
				t.Fatal(err)
			}
			other, err := e.mgr.CreateSession(CreateOpts{})
			if err != nil {
				t.Fatal(err)
			}

			if code, body := f1PostAsk(t, e, asker.ID, qid, "yes"); code != 204 {
				t.Fatalf("answer to a saved session = %d %s, want 204", code, body)
			}
			ctx := context.Background()
			q, err := e.mgr.tasks.GetQuestion(ctx, qid)
			if err != nil {
				t.Fatal(err)
			}
			if !f1Eventually(f1Guard, func() bool {
				n, err := e.mgr.tasks.Notice(ctx, q.EventID)
				return err == nil && n.State == tasks.NoticePending && n.Reason == tasks.ReasonSessionLimit
			}) {
				n, _ := e.mgr.tasks.Notice(ctx, q.EventID)
				t.Fatalf("the answer did not wait on the session limit: %+v", n)
			}
			if _, live := e.mgr.Get(asker.ID); live {
				t.Fatal("the session was loaded over the limit")
			}

			if via == "tick" {
				// Room appears without any event reaching the dispatcher.
				maxNoticeLoadedSessions = 2
				clock.Advance(noticeReconcileInterval)
			} else if err := e.mgr.CloseSession(other.ID); err != nil {
				t.Fatal(err)
			}

			answered := func(r core.Request) bool { return f1LastUserIsAnswer(r) }
			if !f1Eventually(f1Guard*2, func() bool { return e.prov.countWhere(answered) == 1 }) {
				n, _ := e.mgr.tasks.Notice(ctx, q.EventID)
				t.Fatalf("the answer never reached the model once there was room (%+v)", n)
			}
			s2, ok := e.mgr.Get(asker.ID)
			if !ok || len(f1Answers(s2.History())) != 1 {
				t.Fatal("the answer is not in the loaded session exactly once")
			}
			if !f1Stable(400*time.Millisecond, func() bool { return e.prov.countWhere(answered) == 1 }) {
				t.Fatal("the answer woke the model more than once")
			}
		})
	}
}
