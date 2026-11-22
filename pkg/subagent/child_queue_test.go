package subagent

import (
	"context"
	"sync"
	"testing"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

// childQueueEvents records the child-queue events the store emits, the ones a
// subagent view needs because no agent event announces them.
type childQueueEvents struct {
	mu     sync.Mutex
	events []any
}

func (c *childQueueEvents) record(_ string, inner any) {
	switch inner.(type) {
	case bus.SteerQueued, bus.SteersRedirected, bus.SteersCanceled:
		c.mu.Lock()
		c.events = append(c.events, inner)
		c.mu.Unlock()
	}
}

func (c *childQueueEvents) all() []any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]any(nil), c.events...)
}

// startGatedChild launches an async child held inside its first provider call,
// so its queue stays undelivered for the test's duration.
func startGatedChild(t *testing.T) (*jobStore, string, *childQueueEvents) {
	t.Helper()
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	provider := newMockProvider(gateResponse(started, release, "done"))
	sub, _, _, store := newSubagentToolsWithStore(t, Config{
		DefaultModel:    core.Model{ID: "default", Provider: "mock"},
		ProviderFactory: func(model core.Model) (core.Provider, error) { return provider, nil },
		AppCtx:          context.Background(),
	})
	rec := &childQueueEvents{}
	store.onChildEvent = rec.record
	res, err := sub.Execute(context.Background(), map[string]any{"task": "long", "async": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	jobID := jobIDFromResult(t, res)
	<-started
	return store, jobID, rec
}

func childOf(t *testing.T, store *jobStore, jobID string) *agent.Agent {
	t.Helper()
	jb, ok := store.get(jobID)
	if !ok || jb.getChildAgent() == nil {
		t.Fatalf("job %s has no child", jobID)
	}
	return jb.getChildAgent()
}

func TestSteerJobAnnouncesTheQueuedMessage(t *testing.T) {
	store, jobID, rec := startGatedChild(t)
	if ok, _ := store.steerJob(jobID, "from the owner", true); !ok {
		t.Fatal("a running child must accept a steer")
	}
	// The parent's tool goes through the same admission point.
	out, err := newSubagentSteer(store).Execute(context.Background(), map[string]any{"job_id": jobID, "message": "from the parent"}, nil)
	if err != nil || out.IsError {
		t.Fatalf("subagent_steer = %+v, %v", out, err)
	}
	pending := childOf(t, store, jobID).PendingSteers()
	events := rec.all()
	if len(events) != 2 || len(pending) != 2 {
		t.Fatalf("events = %+v, pending = %+v, want two of each", events, pending)
	}
	for i, want := range []struct {
		text     string
		redirect bool
	}{{"from the owner", true}, {"from the parent", false}} {
		q, ok := events[i].(bus.SteerQueued)
		if !ok || q.ID != pending[i].ID || q.Text != want.text || q.Redirect != want.redirect || !q.Recallable || q.SessionID != jobID {
			t.Errorf("event %d = %+v, want %q (redirect %v) under the queued ID %s", i, events[i], want.text, want.redirect, pending[i].ID)
		}
	}
}

func TestSteerJobRefusedAnnouncesNothing(t *testing.T) {
	store, _, rec := startGatedChild(t)
	if ok, _ := store.steerJob("sa-nope", "hello", false); ok {
		t.Fatal("unknown job accepted a steer")
	}
	if events := rec.all(); len(events) != 0 {
		t.Fatalf("events = %+v, want none for a refused steer", events)
	}
}

func TestRedirectJobQueueMarksTheChildQueue(t *testing.T) {
	store, jobID, rec := startGatedChild(t)
	store.steerJob(jobID, "one", false)
	store.steerJob(jobID, "two", false)
	jobs := &Jobs{store: store}

	ids, err := jobs.RedirectQueue(jobID)
	if err != nil || len(ids) != 2 {
		t.Fatalf("RedirectQueue = %v, %v, want both ids", ids, err)
	}
	for _, item := range childOf(t, store, jobID).PendingSteers() {
		if !item.Redirect {
			t.Errorf("item %+v not marked Redirect", item)
		}
	}
	events := rec.all()
	last, ok := events[len(events)-1].(bus.SteersRedirected)
	if !ok || last.SessionID != jobID || len(last.SteerIDs) != 2 {
		t.Fatalf("last event = %+v, want SteersRedirected for both", events[len(events)-1])
	}
	if _, err := jobs.RedirectQueue("sa-nope"); err != ErrUnknownJob {
		t.Fatalf("unknown job err = %v, want ErrUnknownJob", err)
	}
}

func TestRecallJobQueueEmptiesTheChildQueue(t *testing.T) {
	store, jobID, rec := startGatedChild(t)
	store.steerJob(jobID, "one", false)
	store.steerJob(jobID, "two", true)
	jobs := &Jobs{store: store}

	items, err := jobs.RecallQueue(jobID, "recall-1")
	if err != nil || len(items) != 2 || items[0].Text != "one" || items[1].Text != "two" {
		t.Fatalf("RecallQueue = %+v, %v, want both messages in order", items, err)
	}
	if pending := childOf(t, store, jobID).PendingSteers(); len(pending) != 0 {
		t.Fatalf("pending after recall = %+v, want empty", pending)
	}
	events := rec.all()
	last, ok := events[len(events)-1].(bus.SteersCanceled)
	if !ok || last.SessionID != jobID || last.RecallID != "recall-1" || len(last.SteerIDs) != 2 || last.SteerIDs[0] != items[0].ID {
		t.Fatalf("last event = %+v, want SteersCanceled naming both with the recall id", events[len(events)-1])
	}
	if _, err := jobs.RecallQueue("sa-nope", ""); err != ErrUnknownJob {
		t.Fatalf("unknown job err = %v, want ErrUnknownJob", err)
	}
}

func TestSnapshotCarriesTheRunningChildQueue(t *testing.T) {
	store, jobID, _ := startGatedChild(t)
	store.steerJob(jobID, "queued", true)
	for _, info := range (&Jobs{store: store}).Snapshot() {
		if info.JobID != jobID {
			continue
		}
		if len(info.PendingSteers) != 1 || info.PendingSteers[0].Text != "queued" || !info.PendingSteers[0].Redirect {
			t.Fatalf("PendingSteers = %+v, want the queued redirect", info.PendingSteers)
		}
		return
	}
	t.Fatal("job missing from snapshot")
}
