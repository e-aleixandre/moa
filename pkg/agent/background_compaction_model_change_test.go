package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// bgHeldFailure holds the first summary call until opened, then fails it.
type bgHeldFailure struct {
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	err     error
}

func (p *bgHeldFailure) open() { p.once.Do(func() { close(p.release) }) }
func (p *bgHeldFailure) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	if p.calls.Add(1) == 1 {
		close(p.entered)
		<-p.release
	}
	return nil, p.err
}

// A failed job cleaned up by its own worker cannot be invalidated by a later
// model change, so the boundary that owns it must still notice the live
// window: with a larger one the literal context fits and the run proceeds.
// Structural-event backpressure parks the hard boundary after it marks itself
// waiting and before it selects done.
func TestBackgroundCompaction_ModelChangeAfterFailedCleanup(t *testing.T) {
	for _, id := range []string{"bgt", "larger-model"} {
		t.Run(id, func(t *testing.T) {
			sum := &bgHeldFailure{entered: make(chan struct{}), release: make(chan struct{}), err: errors.New("summary failed")}
			prov := &bgtProvider{}
			ag := pr28Agent(t, sum, prov, 120000)
			defer sum.open()
			resume := make(chan struct{})
			var resumeOnce sync.Once
			resumeAll := func() { resumeOnce.Do(func() { close(resume) }) }
			defer resumeAll()
			sub := &subscriber{ch: make(chan core.AgentEvent), done: make(chan struct{}), idleCh: make(chan struct{}, 1)}
			sub.fn = func(e core.AgentEvent) {
				if e.Type == core.AgentEventBackgroundCompaction && e.BackgroundCompaction != nil && e.BackgroundCompaction.Active && !e.BackgroundCompaction.Waiting {
					<-resume
				}
			}
			ag.emitter.mu.Lock()
			ag.emitter.subs = append(ag.emitter.subs, sub)
			ag.emitter.mu.Unlock()
			go sub.loop(ag.emitter.logger)
			defer func() { sub.closed.Store(true); close(sub.done) }()
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			result := make(chan error, 1)
			go func() { _, err := ag.Send(ctx, "go"); result <- err }()
			select {
			case <-sum.entered:
			case <-ctx.Done():
				t.Fatal("summary did not start")
			}
			var job *backgroundCompactionJob
			for {
				ag.mu.Lock()
				job = ag.bgJob
				waiting := job != nil && job.waiting
				ag.mu.Unlock()
				if waiting {
					break
				}
				if ctx.Err() != nil {
					t.Fatal("boundary did not reach waiting")
				}
				time.Sleep(time.Millisecond)
			}
			sum.open()
			select {
			case <-job.done:
			case <-ctx.Done():
				t.Fatal("summary did not finish")
			}
			for {
				ag.mu.Lock()
				cleared := ag.bgJob == nil
				ag.mu.Unlock()
				if cleared {
					break
				}
				if ctx.Err() != nil {
					t.Fatal("worker did not clean its own failed job")
				}
				time.Sleep(time.Millisecond)
			}
			if job.ctx.Err() != nil {
				t.Fatal("own failed cleanup unexpectedly cancelled the job")
			}
			if err := ag.SetModel(nil, core.Model{ID: id, MaxInput: 200000}); err != nil {
				t.Fatal(err)
			}
			resumeAll()
			if err := <-result; err != nil {
				t.Fatalf("the live larger model fits the literal context, but the old failed job stopped Send: %v", err)
			}
			ag.WaitBackgroundCompaction()
			ag.Drain(bgtWait)
			if prov.calls.Load() != 1 {
				t.Fatalf("ordinary calls=%d, want exactly one request on the current model", prov.calls.Load())
			}
			if hasSummary(ag.Messages()) {
				t.Fatal("failed result was adopted")
			}
		})
	}
}
