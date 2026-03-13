package agent

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

// --- test helpers ---

// schedHooks is a minimal Hooks implementation for schedule tests.
type schedHooks struct{}

func (h schedHooks) FireBeforeAgentStart(context.Context) []core.AgentMessage        { return nil }
func (h schedHooks) FireToolCall(context.Context, string, map[string]any) *core.ToolCallDecision {
	return nil
}
func (h schedHooks) FireToolResult(_ context.Context, _ string, r core.Result, _ bool) core.Result {
	return r
}
func (h schedHooks) FireContext(_ context.Context, msgs []core.AgentMessage) []core.AgentMessage {
	return msgs
}
func (h schedHooks) FireObserver(core.AgentEvent) {}

// schedBarrier is a mock tool execution that signals when it starts and
// waits for permission to finish. This lets tests assert concurrency /
// ordering deterministically without time.Sleep.
type schedBarrier struct {
	started chan struct{}
	proceed chan struct{}
}

func newBarrier() *schedBarrier {
	return &schedBarrier{
		started: make(chan struct{}),
		proceed: make(chan struct{}),
	}
}

func (b *schedBarrier) execute(_ context.Context, _ map[string]any, _ func(core.Result)) (core.Result, error) {
	close(b.started)
	<-b.proceed
	return core.TextResult("ok"), nil
}

// release lets the barrier finish and returns immediately.
func (b *schedBarrier) release() { close(b.proceed) }

// makeCfg builds a minimal loopConfig for schedule tests.
func makeCfg(tools *core.Registry) *loopConfig {
	return &loopConfig{
		tools:   tools,
		hooks:   schedHooks{},
		emitter: NewEmitter(slog.Default()),
		state:   &AgentState{},
	}
}

// makeToolCall creates a tool_call Content.
func makeToolCall(id, name string, args map[string]any) core.Content {
	return core.ToolCallContent(id, name, args)
}

// runExecuteTools calls executeTools in a goroutine and returns a channel
// that closes when it completes.
func runExecuteTools(ctx context.Context, cfg *loopConfig, calls []core.Content) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		executeTools(ctx, cfg, calls)
	}()
	return done
}

// assertStarted waits for a barrier to start within a timeout.
func assertStarted(t *testing.T, b *schedBarrier, label string) {
	t.Helper()
	select {
	case <-b.started:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s: did not start within timeout", label)
	}
}

// assertNotStarted checks a barrier has NOT started yet.
func assertNotStarted(t *testing.T, b *schedBarrier, label string) {
	t.Helper()
	select {
	case <-b.started:
		t.Fatalf("%s: started unexpectedly", label)
	case <-time.After(50 * time.Millisecond):
		// good — not started
	}
}

// --- tests ---

func TestSchedule_TwoReadsParallel(t *testing.T) {
	b1, b2 := newBarrier(), newBarrier()
	reg := core.NewRegistry()
	reg.Register(core.Tool{Name: "r1", Effect: core.EffectReadOnly, Execute: b1.execute})
	reg.Register(core.Tool{Name: "r2", Effect: core.EffectReadOnly, Execute: b2.execute})

	cfg := makeCfg(reg)
	calls := []core.Content{
		makeToolCall("1", "r1", nil),
		makeToolCall("2", "r2", nil),
	}

	done := runExecuteTools(context.Background(), cfg, calls)

	// Both should start concurrently.
	assertStarted(t, b1, "r1")
	assertStarted(t, b2, "r2")

	b1.release()
	b2.release()
	<-done
}

func TestSchedule_TwoWritesSamePath_Sequential(t *testing.T) {
	b1, b2 := newBarrier(), newBarrier()
	reg := core.NewRegistry()
	reg.Register(core.Tool{
		Name: "w", Effect: core.EffectWritePath,
		LockKey: func(args map[string]any) string { return "/a.go" },
		Execute: b1.execute,
	})
	// Second instance — same tool name, same key. We need two different
	// executions, so register one tool but use two barriers via an atomic counter.
	var callCount atomic.Int32
	reg.Unregister("w")
	reg.Register(core.Tool{
		Name: "w", Effect: core.EffectWritePath,
		LockKey: func(args map[string]any) string { return "/a.go" },
		Execute: func(ctx context.Context, args map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			n := callCount.Add(1)
			if n == 1 {
				return b1.execute(ctx, args, onUpdate)
			}
			return b2.execute(ctx, args, onUpdate)
		},
	})

	cfg := makeCfg(reg)
	calls := []core.Content{
		makeToolCall("1", "w", nil),
		makeToolCall("2", "w", nil),
	}

	done := runExecuteTools(context.Background(), cfg, calls)

	assertStarted(t, b1, "w-first")
	assertNotStarted(t, b2, "w-second (should wait)")

	b1.release()
	assertStarted(t, b2, "w-second (should start after first)")

	b2.release()
	<-done
}

func TestSchedule_TwoWritesDifferentPaths_Parallel(t *testing.T) {
	b1, b2 := newBarrier(), newBarrier()
	reg := core.NewRegistry()
	reg.Register(core.Tool{
		Name: "wa", Effect: core.EffectWritePath,
		LockKey: func(args map[string]any) string { return "/a.go" },
		Execute: b1.execute,
	})
	reg.Register(core.Tool{
		Name: "wb", Effect: core.EffectWritePath,
		LockKey: func(args map[string]any) string { return "/b.go" },
		Execute: b2.execute,
	})

	cfg := makeCfg(reg)
	calls := []core.Content{
		makeToolCall("1", "wa", nil),
		makeToolCall("2", "wb", nil),
	}

	done := runExecuteTools(context.Background(), cfg, calls)

	assertStarted(t, b1, "wa")
	assertStarted(t, b2, "wb")

	b1.release()
	b2.release()
	<-done
}

func TestSchedule_ShellWaitsForPriorWrite(t *testing.T) {
	bWrite, bShell := newBarrier(), newBarrier()
	reg := core.NewRegistry()
	reg.Register(core.Tool{
		Name: "w", Effect: core.EffectWritePath,
		LockKey: func(args map[string]any) string { return "/a.go" },
		Execute: bWrite.execute,
	})
	reg.Register(core.Tool{Name: "sh", Effect: core.EffectShell, Execute: bShell.execute})

	cfg := makeCfg(reg)
	calls := []core.Content{
		makeToolCall("1", "w", nil),
		makeToolCall("2", "sh", nil),
	}

	done := runExecuteTools(context.Background(), cfg, calls)

	assertStarted(t, bWrite, "write")
	assertNotStarted(t, bShell, "shell (should wait for write)")

	bWrite.release()
	assertStarted(t, bShell, "shell (should start after write)")

	bShell.release()
	<-done
}

func TestSchedule_WriteAfterShellWaits(t *testing.T) {
	bShell, bWrite := newBarrier(), newBarrier()
	reg := core.NewRegistry()
	reg.Register(core.Tool{Name: "sh", Effect: core.EffectShell, Execute: bShell.execute})
	reg.Register(core.Tool{
		Name: "w", Effect: core.EffectWritePath,
		LockKey: func(args map[string]any) string { return "/a.go" },
		Execute: bWrite.execute,
	})

	cfg := makeCfg(reg)
	calls := []core.Content{
		makeToolCall("1", "sh", nil),
		makeToolCall("2", "w", nil),
	}

	done := runExecuteTools(context.Background(), cfg, calls)

	assertStarted(t, bShell, "shell")
	assertNotStarted(t, bWrite, "write (should wait for shell)")

	bShell.release()
	assertStarted(t, bWrite, "write (should start after shell)")

	bWrite.release()
	<-done
}

func TestSchedule_MixedBashFirst(t *testing.T) {
	// [bash, write /a] → bash runs first, write waits.
	bBash, bWrite := newBarrier(), newBarrier()
	reg := core.NewRegistry()
	reg.Register(core.Tool{Name: "bash", Effect: core.EffectShell, Execute: bBash.execute})
	reg.Register(core.Tool{
		Name: "w", Effect: core.EffectWritePath,
		LockKey: func(args map[string]any) string { return "/a.go" },
		Execute: bWrite.execute,
	})

	cfg := makeCfg(reg)
	calls := []core.Content{
		makeToolCall("1", "bash", nil),
		makeToolCall("2", "w", nil),
	}

	done := runExecuteTools(context.Background(), cfg, calls)

	assertStarted(t, bBash, "bash")
	assertNotStarted(t, bWrite, "write (should wait for bash)")

	bBash.release()
	assertStarted(t, bWrite, "write (should start after bash)")

	bWrite.release()
	<-done
}

func TestSchedule_WriteBashWrite(t *testing.T) {
	// [write /a, bash, write /a] → write starts, bash waits, second write waits for bash.
	var callCount atomic.Int32
	b1, bBash, b2 := newBarrier(), newBarrier(), newBarrier()

	reg := core.NewRegistry()
	reg.Register(core.Tool{
		Name: "w", Effect: core.EffectWritePath,
		LockKey: func(args map[string]any) string { return "/a.go" },
		Execute: func(ctx context.Context, args map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			n := callCount.Add(1)
			if n == 1 {
				return b1.execute(ctx, args, onUpdate)
			}
			return b2.execute(ctx, args, onUpdate)
		},
	})
	reg.Register(core.Tool{Name: "bash", Effect: core.EffectShell, Execute: bBash.execute})

	cfg := makeCfg(reg)
	calls := []core.Content{
		makeToolCall("1", "w", nil),
		makeToolCall("2", "bash", nil),
		makeToolCall("3", "w", nil),
	}

	done := runExecuteTools(context.Background(), cfg, calls)

	assertStarted(t, b1, "write-1")
	assertNotStarted(t, bBash, "bash (should wait for write-1)")
	assertNotStarted(t, b2, "write-2 (should wait for bash)")

	b1.release()
	assertStarted(t, bBash, "bash (should start after write-1)")
	assertNotStarted(t, b2, "write-2 (should still wait for bash)")

	bBash.release()
	assertStarted(t, b2, "write-2 (should start after bash)")

	b2.release()
	<-done
}

func TestSchedule_UnknownEffectTreatedAsShell(t *testing.T) {
	bWrite, bUnknown := newBarrier(), newBarrier()
	reg := core.NewRegistry()
	reg.Register(core.Tool{
		Name: "w", Effect: core.EffectWritePath,
		LockKey: func(args map[string]any) string { return "/a.go" },
		Execute: bWrite.execute,
	})
	// Effect zero value = EffectUnknown — should serialize like shell.
	reg.Register(core.Tool{Name: "unk", Execute: bUnknown.execute})

	cfg := makeCfg(reg)
	calls := []core.Content{
		makeToolCall("1", "w", nil),
		makeToolCall("2", "unk", nil),
	}

	done := runExecuteTools(context.Background(), cfg, calls)

	assertStarted(t, bWrite, "write")
	assertNotStarted(t, bUnknown, "unknown (should wait like shell)")

	bWrite.release()
	assertStarted(t, bUnknown, "unknown (should start after write)")

	bUnknown.release()
	<-done
}

func TestSchedule_WritePathNilLockKey_FallsBackToShell(t *testing.T) {
	// WritePath with nil LockKey can't be registered (panics).
	// So this tests the runtime fallback when LockKey returns "".
	bWrite, bBad := newBarrier(), newBarrier()
	reg := core.NewRegistry()
	reg.Register(core.Tool{
		Name: "w", Effect: core.EffectWritePath,
		LockKey: func(args map[string]any) string { return "/a.go" },
		Execute: bWrite.execute,
	})
	reg.Register(core.Tool{
		Name: "bad", Effect: core.EffectWritePath,
		LockKey: func(args map[string]any) string { return "" }, // empty = fallback
		Execute: bBad.execute,
	})

	cfg := makeCfg(reg)
	calls := []core.Content{
		makeToolCall("1", "w", nil),
		makeToolCall("2", "bad", nil),
	}

	done := runExecuteTools(context.Background(), cfg, calls)

	assertStarted(t, bWrite, "write")
	assertNotStarted(t, bBad, "bad-lockkey (should fallback to shell)")

	bWrite.release()
	assertStarted(t, bBad, "bad-lockkey (should start after write)")

	bBad.release()
	<-done
}

func TestSchedule_RegisterWritePathNilLockKey_Panics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for WritePath with nil LockKey")
		}
	}()
	reg := core.NewRegistry()
	reg.Register(core.Tool{
		Name: "bad", Effect: core.EffectWritePath,
		// LockKey intentionally nil
		Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
			return core.TextResult("ok"), nil
		},
	})
}

func TestSchedule_ReadDoesNotBlockShell(t *testing.T) {
	bRead, bShell := newBarrier(), newBarrier()
	reg := core.NewRegistry()
	reg.Register(core.Tool{Name: "r", Effect: core.EffectReadOnly, Execute: bRead.execute})
	reg.Register(core.Tool{Name: "sh", Effect: core.EffectShell, Execute: bShell.execute})

	cfg := makeCfg(reg)
	calls := []core.Content{
		makeToolCall("1", "r", nil),
		makeToolCall("2", "sh", nil),
	}

	done := runExecuteTools(context.Background(), cfg, calls)

	// Both should start — shell doesn't wait for reads.
	assertStarted(t, bRead, "read")
	assertStarted(t, bShell, "shell (should not wait for read)")

	bRead.release()
	bShell.release()
	<-done
}

// TestSchedule_ResultsInOriginalOrder verifies Phase 3 collects results
// in the original tool call order regardless of execution completion order.
func TestSchedule_ResultsInOriginalOrder(t *testing.T) {
	started := make(chan struct{})
	proceed := make(chan struct{})

	reg := core.NewRegistry()
	// "slow" signals started and blocks; "fast" completes immediately.
	reg.Register(core.Tool{
		Name: "slow", Effect: core.EffectReadOnly,
		Execute: func(_ context.Context, _ map[string]any, _ func(core.Result)) (core.Result, error) {
			close(started)
			<-proceed
			return core.TextResult("slow"), nil
		},
	})
	reg.Register(core.Tool{
		Name: "fast", Effect: core.EffectReadOnly,
		Execute: func(_ context.Context, _ map[string]any, _ func(core.Result)) (core.Result, error) {
			return core.TextResult("fast"), nil
		},
	})

	cfg := makeCfg(reg)
	calls := []core.Content{
		makeToolCall("1", "slow", nil), // first in order, finishes last
		makeToolCall("2", "fast", nil), // second in order, finishes first
	}

	done := runExecuteTools(context.Background(), cfg, calls)

	<-started       // slow has started
	close(proceed)  // let slow finish
	<-done          // wait for executeTools to complete

	// Phase 3 always appends in original order, so messages should be:
	// [0] = slow result, [1] = fast result
	if len(cfg.state.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(cfg.state.Messages))
	}
	if cfg.state.Messages[0].Content[0].Text != "slow" {
		t.Errorf("first result should be 'slow', got %q", cfg.state.Messages[0].Content[0].Text)
	}
	if cfg.state.Messages[1].Content[0].Text != "fast" {
		t.Errorf("second result should be 'fast', got %q", cfg.state.Messages[1].Content[0].Text)
	}
}
