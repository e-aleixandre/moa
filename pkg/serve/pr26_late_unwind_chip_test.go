package serve

// Review PR #26 — G2's queued steer has a fresh ID while G1's late
// discard removes only its own chip. This contract has no CleanupOnly field.

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

func r26AwaitClosed(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s not reached", label)
	}
}

func TestPR26BLateUnwindDiscardStillRemovesItsOwnChip(t *testing.T) {
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
	)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mgr := newTestManager(t, ctx, prov)
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
	var forwarded []Event
	sess.runtime.Bus.Subscribe(func(e bus.SteersCanceled) {
		if ev, ok := wsEventFromBus(e); ok {
			mu.Lock()
			forwarded = append(forwarded, ev)
			mu.Unlock()
		}
	})

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
	if _, _, _, err := mgr.Send(sess.ID, "queued in G1", nil, "q-g1", ""); err != nil {
		t.Fatal(err)
	}
	close(failG1)
	pollUntil(t, 5*time.Second, "G1 settled", func() bool { return !ag.IsRunning() && sessState(sess) != StateRunning })

	if _, _, _, err := mgr.Send(sess.ID, "next run", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	r26AwaitClosed(t, g2Started, "G2 request")
	if _, _, _, err := mgr.Send(sess.ID, "queued in G2", nil, "q-g2", ""); err != nil {
		t.Fatal(err)
	}
	open()
	ag.Drain(3 * time.Second)
	sess.runtime.Bus.Drain(3 * time.Second)

	mu.Lock()
	defer mu.Unlock()
	ok := false
	for _, ev := range forwarded {
		data, _ := ev.Data.(map[string]any)
		if ids, _ := data["discarded_steer_ids"].([]string); reflect.DeepEqual(ids, []string{"q-g1"}) {
			ok = true
		}
	}
	if !ok {
		t.Errorf("clients were never told G1's q-g1 was discarded (its chip stays queued forever), or were told to clear G2's chips: forwarded=%+v", forwarded)
	}
	if got := ag.PendingSteers(); len(got) != 1 || got[0].ID != "q-g2" {
		t.Errorf("G2 queue = %+v", got)
	}
}
