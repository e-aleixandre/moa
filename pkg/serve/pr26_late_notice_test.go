package serve

// Review PR #26 — the interleaving the author left untested: a task notice
// queued as a steer in G1, G1's unwind discards it, and that discard reaches
// the bridge only after G2 started. Its concrete fresh ID must reach
// the real dispatcher; the notice is kept once, without a model turn.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

func TestPR26BLateDiscardKeepsTheRealNoticeByFreshID(t *testing.T) {
	g1First, g1Second, g2Started := make(chan struct{}), make(chan struct{}), make(chan struct{})
	answerG1, failG1 := make(chan struct{}), make(chan struct{})
	prov := newMockProvider(
		func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
			close(g1First)
			<-answerG1
			return simpleResponse("first answer"), nil
		},
		func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
			close(g1Second)
			<-failG1
			return nil, errors.New("provider down")
		},
		func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
			close(g2Started)
			<-ctx.Done()
			return nil, ctx.Err()
		},
		func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
			return simpleResponse("unexpected turn"), nil
		},
	)
	srv, mgr := newNoticeTestServer(t, prov)
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	sctx := sess.runtime.Context()
	ag := sctx.Agent.(*agent.Agent)
	held, release := make(chan struct{}), make(chan struct{})
	var once, openOnce sync.Once
	open := func() { openOnce.Do(func() { close(release) }) }
	t.Cleanup(open)
	baseFilter := sctx.SteerFilter
	sctx.SteerFilter = func(text string) bool {
		if text == "hold the bridge" {
			once.Do(func() { close(held) })
			<-release
		}
		return baseFilter == nil || baseFilter(text)
	}
	var mu sync.Mutex
	var cancellations []bus.SteersCanceled
	sess.runtime.Bus.Subscribe(func(e bus.SteersCanceled) {
		mu.Lock()
		cancellations = append(cancellations, e)
		mu.Unlock()
	})

	rec := askFrom(t, mgr, sess.ID)
	if _, _, _, err := mgr.Send(sess.ID, "start", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	r26AwaitClosed(t, g1First, "G1 first request")
	if _, _, _, err := mgr.Send(sess.ID, "hold the bridge", nil, "hold", ""); err != nil {
		t.Fatal(err)
	}
	close(answerG1)
	r26AwaitClosed(t, held, "bridge held on G1's steer")
	r26AwaitClosed(t, g1Second, "G1 second request")
	completeRequest(t, srv, rec, "", "")
	n := waitNoticeState(t, mgr, rec.ID, tasks.NoticeSent)
	pollUntil(t, 5*time.Second, "notice queued in G1", func() bool { return noticeQueued(sess, n) })
	close(failG1)
	pollUntil(t, 5*time.Second, "G1 settled", func() bool { return !ag.IsRunning() && sessState(sess) != StateRunning })

	if _, _, _, err := mgr.Send(sess.ID, "next run", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	r26AwaitClosed(t, g2Started, "G2 request")
	open()
	ag.Drain(3 * time.Second)
	sess.runtime.Bus.Drain(3 * time.Second)

	mu.Lock()
	gotLate := append([]bus.SteersCanceled(nil), cancellations...)
	mu.Unlock()
	if n.SteerID == "" || len(gotLate) != 1 || len(gotLate[0].SteerIDs) != 1 || gotLate[0].SteerIDs[0] != n.SteerID {
		t.Errorf("late concrete notice discard=%+v, want one fresh ID %q", gotLate, n.SteerID)
	}
	if got, _ := latestNotice(t, mgr, rec.ID); got.State == tasks.NoticeDelivered || got.Method != tasks.MethodAppend {
		t.Errorf("after the late discard, notice = state %s method %s; want kept as a pending append", got.State, got.Method)
	}
	if len(noticeMessages(sess.History(), n.ID)) != 0 {
		t.Error("discarded notice reached G2's history")
	}
	if err := sess.runtime.Bus.Execute(bus.AbortRun{}); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 5*time.Second, "idle after G2", func() bool { return sessState(sess) == StateIdle })
	assertAppendedWithoutTurn(t, mgr, prov, sess, rec.ID, 3)
}
