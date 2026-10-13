package bus_test

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

// Only the scheduling of typed terminal reactors is controlled. Publish,
// tree sync, persistence, agent execution and teardown are production code.
type delayedCostBus struct {
	*bus.LocalBus
	gate      <-chan struct{}
	entered   chan struct{}
	enterOnce sync.Once
}

func (b *delayedCostBus) Subscribe(handler any) func() {
	if h, ok := handler.(func(bus.RunEnded)); ok {
		return b.LocalBus.Subscribe(func(e bus.RunEnded) {
			b.enterOnce.Do(func() { close(b.entered) })
			<-b.gate
			h(e)
		})
	}
	return b.LocalBus.Subscribe(handler)
}

// The adapter writes a real FileStore, using the session runtime's snapshot.
type diskPersister struct {
	mu        sync.Mutex
	store     *session.FileStore
	saved     *session.Session
	firstSave chan struct{}
	once      sync.Once
}

func (p *diskPersister) Snapshot(messages []core.AgentMessage, epoch int, meta map[string]any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.saved.Messages, p.saved.CompactionEpoch, p.saved.Metadata = messages, epoch, meta
	err := p.store.Save(p.saved)
	p.once.Do(func() { close(p.firstSave) })
	return err
}

func (p *diskPersister) SnapshotTree(entries []session.Entry, leaf string, meta map[string]any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.saved.Entries, p.saved.LeafID, p.saved.Metadata = entries, leaf, meta
	err := p.store.Save(p.saved)
	p.once.Do(func() { close(p.firstSave) })
	return err
}

// WaitSettled guarantees publication, not that the typed cost reactor ran.
// Flush-before-Close is the shutdown sequence used by the manager.
func TestSessionCostQueuedAtShutdownIsPersisted(t *testing.T) {
	gate := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate) }) }
	b := &delayedCostBus{LocalBus: bus.NewLocalBus(), gate: gate, entered: make(chan struct{})}
	store, err := session.NewFileStore(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	saved := store.Create()
	p := &diskPersister{store: store, saved: saved, firstSave: make(chan struct{})}
	provider := &poolBudgetProvider{steps: []func(context.Context) core.Message{
		func(context.Context) core.Message { return poolBudgetText("paid answer", 50) },
	}}
	parent, err := agent.New(agent.AgentConfig{Provider: provider, Model: core.Model{ID: "pool", Provider: "mock", Pricing: &core.Pricing{Input: 10_000}}})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := bus.NewSessionRuntime(bus.RuntimeConfig{SessionID: saved.ID, Bus: b, Agent: parent, Persister: p})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	t.Cleanup(release)
	if err := b.Execute(bus.SendPrompt{Text: "work"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if !rt.WaitSettled(ctx) {
		t.Fatal("run did not settle")
	}
	select {
	case <-b.entered:
	case <-ctx.Done():
		t.Fatal("terminal reactor not reached")
	}
	select {
	case <-p.firstSave:
	case <-ctx.Done():
		t.Fatal("tree save did not complete")
	}
	if math.Abs(parent.RunCost()-.50) > 1e-9 {
		t.Fatalf("real spend=%v, want .50", parent.RunCost())
	}
	if err := rt.Flush(); err != nil {
		t.Fatal(err)
	}

	closed := make(chan struct{})
	go func() { rt.Close(); close(closed) }()
	// Release the delayed consumer only once shutdown has stopped publication:
	// the accepted RunEnded is still drained, its derived update is now a no-op.
	for {
		_, err := b.Query(bus.GetSessionCost{})
		if errors.Is(err, bus.ErrClosed) {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("bus did not close")
		}
		time.Sleep(time.Millisecond)
	}
	release()
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("close did not drain")
	}
	loaded, err := store.Load(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := loaded.Metadata["session_cost_usd"].(float64)
	if math.Abs(got-.50) > 1e-9 {
		t.Fatalf("paid .50, accepted RunEnded drained by Close, but persisted session_cost_usd=%v (metadata=%v); final cost was lost by Flush-before-Close", got, loaded.Metadata)
	}
}
