package serve

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/attachment"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

// lateUnwindFixture ends run G1 with a provider error while a steer carrying
// an attachment is queued, so G1's own unwind discards it. The bridge is held
// on an earlier G1 event (through the session's real steer filter) until G2
// has started and queued a steer that reuses the discarded steer's ID. Then
// the held events are released.
type lateUnwindFixture struct {
	sess     *ManagedSession
	store    *attachment.Store
	attID    string
	canceled []bus.SteersCanceled
}

func runLateUnwind(t *testing.T) *lateUnwindFixture {
	t.Helper()
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
	store, err := attachment.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mgr.attachStore = store
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
	f := &lateUnwindFixture{sess: sess, store: store}
	var mu sync.Mutex
	sess.runtime.Bus.Subscribe(func(e bus.SteersCanceled) {
		mu.Lock()
		f.canceled = append(f.canceled, e)
		mu.Unlock()
	})

	// G1: the first answer ends the turn; the queued steer is delivered, and
	// its announcement holds the bridge while the run goes on.
	if _, _, _, err := mgr.Send(sess.ID, "start", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	awaitClosed(t, g1First, "G1 first request")
	if _, _, _, err := mgr.Send(sess.ID, "hold the bridge", nil, "hold", ""); err != nil {
		t.Fatal(err)
	}
	close(answerG1)
	awaitClosed(t, held, "bridge held on G1's steer")
	awaitClosed(t, g1Second, "G1 second request")
	_, _, descs, err := mgr.Send(sess.ID, "with image", []Attachment{{Name: "image.png", Mime: "image/png", Data: b64(pngBytes(64))}}, "q-reused", "")
	if err != nil || len(descs) != 1 {
		t.Fatalf("queue attachment steer: %v %v", descs, err)
	}
	f.attID = descs[0].ID
	if _, ok := store.Lookup(sess.ID, f.attID); !ok {
		t.Fatal("fixture attachment has no reference")
	}
	close(failG1)
	pollUntil(t, 5*time.Second, "G1 settled", func() bool { return !ag.IsRunning() && sessState(sess) != StateRunning })

	// G2 starts and queues a steer that reuses the discarded ID.
	if _, _, _, err := mgr.Send(sess.ID, "next run", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	awaitClosed(t, g2Started, "G2 request")
	if _, id, _, err := mgr.Send(sess.ID, "G2 queued", nil, "q-reused", ""); err != nil || id != "q-reused" {
		t.Fatalf("G2 steer = %q, %v", id, err)
	}

	open()
	ag.Drain(3 * time.Second)
	sess.runtime.Bus.Drain(3 * time.Second)
	if got := steerIDs(ag.PendingSteers()); len(got) != 1 || got[0] != "q-reused" {
		t.Errorf("G2 queue = %v, want its own q-reused steer kept", got)
	}
	mu.Lock()
	defer mu.Unlock()
	f.canceled = append([]bus.SteersCanceled(nil), f.canceled...)
	return f
}

func awaitClosed(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s not reached", label)
	}
}

func steerIDs(items []core.SteerItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

// G1's unwind discard reaches the bridge only after G2 started. Its
// attachment reference still has to be released.
func TestLateUnwindDiscardReleasesItsAttachment(t *testing.T) {
	f := runLateUnwind(t)
	pollUntil(t, 3*time.Second, "G1 attachment released", func() bool {
		_, ok := f.store.Lookup(f.sess.ID, f.attID)
		return !ok
	})
}

// The same late discard still reaches clients, but with concrete IDs only: it
// must not clear G2's chips, including the one that reuses the discarded ID.
func TestLateUnwindDiscardDoesNotClearNextRunChips(t *testing.T) {
	f := runLateUnwind(t)
	for _, e := range f.canceled {
		ev, ok := wsEventFromBus(e)
		if !ok {
			t.Errorf("late G1 discard hidden from clients: %+v", e)
			continue
		}
		data, _ := ev.Data.(map[string]any)
		if ids, ok := data["discarded_steer_ids"].([]string); !ok || ids == nil || len(ids) != 0 {
			t.Errorf("late G1 discard names live chips or is not an explicit empty list: %+v", data)
		}
	}
}
