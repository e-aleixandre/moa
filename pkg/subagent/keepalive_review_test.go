package subagent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/core"
)

type criticalTimer struct {
	at             time.Time
	f              func()
	stopped, fired bool
}

type criticalChildClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*criticalTimer
}

func (c *criticalChildClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *criticalChildClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	tm := &criticalTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, tm)
	c.mu.Unlock()
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		was := !tm.stopped && !tm.fired
		tm.stopped = true
		return was
	}
}
func (c *criticalChildClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var fs []func()
	for _, tm := range c.timers {
		if !tm.stopped && !tm.fired && !tm.at.After(c.now) {
			tm.fired = true
			fs = append(fs, tm.f)
		}
	}
	c.mu.Unlock()
	for _, f := range fs {
		f()
	}
}

type criticalChildProvider struct{ pinged chan core.Request }

func (p *criticalChildProvider) Stream(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	usage := core.Usage{CacheRead: 150_000}
	msg := core.Message{Role: "assistant", Content: []core.Content{core.ToolCallContent("cleanup-1", "slow_cleanup", map[string]any{})}, StopReason: "tool_use", Usage: &usage,
		ProviderSource: &core.ProviderSource{Kind: "oauth", PrimaryGeneration: "fake-generation", Provider: "anthropic", Model: req.Model.ID}}
	ch := make(chan core.AssistantEvent, 2)
	ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
	close(ch)
	return ch, nil
}
func (p *criticalChildProvider) Prewarm(_ context.Context, req core.Request) (core.Usage, error) {
	p.pinged <- req
	return core.Usage{CacheRead: 150_000}, nil
}

func criticalReceive[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatalf("timeout waiting for %s", what)
		var zero T
		return zero
	}
}

// RED: the actual subagent_cancel entry point cancels jobCtx, but keep-alive
// has an unrelated Background context until runJob's deferred teardown.
func TestCriticalREDChildCancelStopsPingsBeforeToolCleanupEnds(t *testing.T) {
	clock := &criticalChildClock{now: time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)}
	provider := &criticalChildProvider{pinged: make(chan core.Request, 8)}
	toolStarted, toolCanceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	tool := core.Tool{Name: "slow_cleanup", Parameters: []byte(`{"type":"object"}`), Effect: core.EffectShell,
		Execute: func(ctx context.Context, _ map[string]any, _ func(core.Result)) (core.Result, error) {
			close(toolStarted)
			<-ctx.Done()
			close(toolCanceled)
			<-release
			return core.TextResult("cleaned up"), nil
		}}
	model, _ := core.ResolveModel("claude-opus-5-5")
	sub, _, cancelTool, jobs := newSubagentToolsWithStore(t, Config{
		DefaultModel:    model,
		ProviderFactory: func(core.Model) (core.Provider, error) { return provider, nil },
		ChildKeepAlive: func(string) *agent.KeepAliveConfig {
			return &agent.KeepAliveConfig{
				Role: core.KeepAliveRoleSubagent, Clock: clock, Policy: func() core.KeepAlivePolicy {
					p, _ := core.GetCacheKeepAlive(core.MoaConfig{CacheKeepAlive: &core.CacheKeepAliveConfig{Subagent: &core.KeepAliveHorizons{Enabled: true}}})
					return p
				},
			}
		},
	}, tool)
	res, err := sub.Execute(context.Background(), map[string]any{"task": "wait for cleanup", "async": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	id := jobIDFromResult(t, res)
	j, ok := jobs.get(id)
	if !ok {
		t.Fatal("missing job")
	}
	criticalReceive(t, toolStarted, "child tool")
	clock.advance(4*time.Minute + 29*time.Second)
	if len(provider.pinged) != 0 {
		t.Fatal("setup: ping before due time")
	}
	cancelDone := make(chan struct{})
	go func() {
		_, _ = cancelTool.Execute(context.Background(), map[string]any{"job_id": id}, nil)
		close(cancelDone)
	}()
	criticalReceive(t, toolCanceled, "job context cancellation")
	defer func() {
		close(release)
		criticalReceive(t, j.done, "job exit")
		criticalReceive(t, cancelDone, "cancel tool exit")
	}()
	clock.advance(time.Second)
	select {
	case req := <-provider.pinged:
		t.Fatalf("subagent_cancel had canceled the tool context, but a NEW %s prewarm was dispatched during tool cleanup", req.Model.ID)
	case <-time.After(100 * time.Millisecond):
	}
}
