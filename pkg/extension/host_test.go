package extension

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ealeixandre/go-agent/pkg/core"
)

// testExtension is a simple extension for testing.
type testExtension struct {
	initFunc func(api API) error
}

func (e *testExtension) Init(api API) error {
	return e.initFunc(api)
}

func TestHost_Load(t *testing.T) {
	host := NewHost(core.NewRegistry(), nil)
	ext := &testExtension{initFunc: func(api API) error {
		api.OnTurnStart(func(ctx context.Context, event core.AgentEvent) {})
		return nil
	}}

	if err := host.Load(ext); err != nil {
		t.Fatal(err)
	}
}

func TestHost_ToolCallBlock(t *testing.T) {
	host := NewHost(core.NewRegistry(), nil)

	// Extension that blocks "bash" tool
	ext := &testExtension{initFunc: func(api API) error {
		api.OnToolCall(func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision {
			if name == "bash" {
				return &core.ToolCallDecision{Block: true, Reason: "bash is blocked"}
			}
			return nil
		})
		return nil
	}}
	host.Load(ext)

	// bash should be blocked
	decision := host.FireToolCall(context.Background(), "bash", map[string]any{"command": "rm -rf /"})
	if decision == nil || !decision.Block {
		t.Fatal("expected bash to be blocked")
	}
	if decision.Reason != "bash is blocked" {
		t.Errorf("reason: %q", decision.Reason)
	}

	// read should not be blocked
	decision = host.FireToolCall(context.Background(), "read", map[string]any{"path": "file.txt"})
	if decision != nil {
		t.Fatal("expected read to not be blocked")
	}
}

func TestHost_BeforeAgentStart_InjectMessages(t *testing.T) {
	host := NewHost(core.NewRegistry(), nil)

	ext := &testExtension{initFunc: func(api API) error {
		api.OnBeforeAgentStart(func(ctx context.Context) ([]core.AgentMessage, error) {
			return []core.AgentMessage{
				core.WrapMessage(core.NewUserMessage("injected context")),
			}, nil
		})
		return nil
	}}
	host.Load(ext)

	msgs := host.FireBeforeAgentStart(context.Background())
	if len(msgs) != 1 {
		t.Fatalf("expected 1 injected message, got %d", len(msgs))
	}
	if msgs[0].Content[0].Text != "injected context" {
		t.Errorf("injected text: %q", msgs[0].Content[0].Text)
	}
}

func TestHost_PanicRecovery(t *testing.T) {
	host := NewHost(core.NewRegistry(), nil)

	// Extension that panics
	ext := &testExtension{initFunc: func(api API) error {
		api.OnToolCall(func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision {
			panic("boom!")
		})
		return nil
	}}
	host.Load(ext)

	// Should not panic; should return nil (no block)
	decision := host.FireToolCall(context.Background(), "bash", nil)
	if decision != nil {
		t.Fatal("expected nil decision after panic")
	}
}

func TestHost_DeadlineTimeout(t *testing.T) {
	host := NewHost(core.NewRegistry(), nil)

	// Extension with a slow hook
	ext := &testExtension{initFunc: func(api API) error {
		api.OnToolCall(func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision {
			// Simulate slow work that respects context
			select {
			case <-time.After(5 * time.Second):
				return &core.ToolCallDecision{Block: true, Reason: "slow"}
			case <-ctx.Done():
				return nil
			}
		})
		return nil
	}}
	host.Load(ext)

	start := time.Now()
	decision := host.FireToolCall(context.Background(), "bash", nil)
	elapsed := time.Since(start)

	// Should return within ~200ms (the deadline), not 5s
	if elapsed > 1*time.Second {
		t.Fatalf("hook took too long: %v", elapsed)
	}
	// Decision should be nil (hook was cancelled)
	if decision != nil && decision.Block {
		t.Fatal("expected nil/non-blocking decision")
	}
}

func TestHost_ToolResult_Modify(t *testing.T) {
	host := NewHost(core.NewRegistry(), nil)

	ext := &testExtension{initFunc: func(api API) error {
		api.OnToolResult(func(ctx context.Context, name string, result core.Result, isError bool) (*core.Result, error) {
			if name == "bash" {
				modified := core.TextResult("modified output")
				return &modified, nil
			}
			return nil, nil
		})
		return nil
	}}
	host.Load(ext)

	original := core.TextResult("original output")
	result := host.FireToolResult(context.Background(), "bash", original, false)
	if len(result.Content) != 1 || result.Content[0].Text != "modified output" {
		t.Fatalf("expected modified result: %+v", result)
	}

	// Non-bash tool should keep original
	result = host.FireToolResult(context.Background(), "read", original, false)
	if result.Content[0].Text != "original output" {
		t.Fatalf("expected original result: %+v", result)
	}
}

func TestHost_Context_ModifyMessages(t *testing.T) {
	host := NewHost(core.NewRegistry(), nil)

	ext := &testExtension{initFunc: func(api API) error {
		api.OnContext(func(ctx context.Context, msgs []core.AgentMessage) ([]core.AgentMessage, error) {
			// Add a system-like message
			return append(msgs, core.WrapMessage(core.NewUserMessage("extra context"))), nil
		})
		return nil
	}}
	host.Load(ext)

	msgs := []core.AgentMessage{core.WrapMessage(core.NewUserMessage("original"))}
	result := host.FireContext(context.Background(), msgs)
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
}

func TestHost_Observer_Async(t *testing.T) {
	host := NewHost(core.NewRegistry(), nil)
	var called atomic.Int32

	ext := &testExtension{initFunc: func(api API) error {
		api.OnTurnStart(func(ctx context.Context, event core.AgentEvent) {
			called.Add(1)
		})
		return nil
	}}
	host.Load(ext)

	host.FireObserver(core.AgentEvent{Type: core.AgentEventTurnStart})

	// Give goroutine time to run
	time.Sleep(50 * time.Millisecond)

	if called.Load() != 1 {
		t.Fatalf("expected observer to be called once, got %d", called.Load())
	}
}

func TestHost_RegisterTool(t *testing.T) {
	reg := core.NewRegistry()
	host := NewHost(reg, nil)

	ext := &testExtension{initFunc: func(api API) error {
		api.RegisterTool(core.Tool{Name: "custom", Description: "A custom tool"})
		return nil
	}}
	host.Load(ext)

	if _, ok := reg.Get("custom"); !ok {
		t.Fatal("expected custom tool to be registered")
	}
}

func TestHook_NonCooperativeTimeout(t *testing.T) {
	host := NewHost(core.NewRegistry(), nil)

	// Extension with a hook that ignores context and blocks
	ext := &testExtension{initFunc: func(api API) error {
		api.OnToolCall(func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision {
			time.Sleep(5 * time.Second) // ignores ctx
			return &core.ToolCallDecision{Block: true, Reason: "slow"}
		})
		return nil
	}}
	host.Load(ext)

	start := time.Now()
	decision := host.FireToolCall(context.Background(), "bash", nil)
	elapsed := time.Since(start)

	// Should return within ~300ms (deadline + margin), not 5s
	if elapsed > 1*time.Second {
		t.Fatalf("hook took too long: %v", elapsed)
	}
	// Decision should be nil (hook was cancelled/timed out)
	if decision != nil && decision.Block {
		t.Fatal("expected nil/non-blocking decision after timeout")
	}
}

func TestHook_RepeatedTimeouts(t *testing.T) {
	host := NewHost(core.NewRegistry(), nil)

	ext := &testExtension{initFunc: func(api API) error {
		api.OnToolCall(func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision {
			time.Sleep(5 * time.Second) // non-cooperative
			return &core.ToolCallDecision{Block: true, Reason: "slow"}
		})
		return nil
	}}
	host.Load(ext)

	start := time.Now()
	for i := 0; i < 10; i++ {
		decision := host.FireToolCall(context.Background(), "bash", nil)
		if decision != nil && decision.Block {
			t.Fatal("expected timeout, not block")
		}
	}
	elapsed := time.Since(start)

	// 10 calls at ~200ms each = ~2s max. Should not be 50s.
	if elapsed > 5*time.Second {
		t.Fatalf("repeated hooks took too long: %v", elapsed)
	}
}

func TestHost_FirstBlockerWins(t *testing.T) {
	host := NewHost(core.NewRegistry(), nil)

	// Two extensions, both block, first one should win
	ext1 := &testExtension{initFunc: func(api API) error {
		api.OnToolCall(func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision {
			return &core.ToolCallDecision{Block: true, Reason: "blocker 1"}
		})
		return nil
	}}
	ext2 := &testExtension{initFunc: func(api API) error {
		api.OnToolCall(func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision {
			return &core.ToolCallDecision{Block: true, Reason: "blocker 2"}
		})
		return nil
	}}
	host.Load(ext1)
	host.Load(ext2)

	decision := host.FireToolCall(context.Background(), "bash", nil)
	if decision == nil || decision.Reason != "blocker 1" {
		t.Fatalf("expected first blocker to win: %+v", decision)
	}
}

func TestHost_Load_RollbackOnError(t *testing.T) {
	reg := core.NewRegistry()
	host := NewHost(reg, nil)

	// Extension that registers hooks then fails
	failExt := &testExtension{initFunc: func(api API) error {
		api.OnToolCall(func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision {
			return &core.ToolCallDecision{Block: true, Reason: "should be rolled back"}
		})
		api.OnBeforeAgentStart(func(ctx context.Context) ([]core.AgentMessage, error) {
			return nil, nil
		})
		api.RegisterTool(core.Tool{Name: "phantom"})
		return fmt.Errorf("init failed")
	}}

	err := host.Load(failExt)
	if err == nil {
		t.Fatal("expected error from Load")
	}

	// Hooks should be rolled back — tool call should return nil (no blockers)
	decision := host.FireToolCall(context.Background(), "bash", nil)
	if decision != nil {
		t.Fatalf("expected no tool call hook after rollback, got %+v", decision)
	}

	// before_agent_start should be empty
	msgs := host.FireBeforeAgentStart(context.Background())
	if len(msgs) > 0 {
		t.Fatalf("expected no injected messages after rollback, got %d", len(msgs))
	}
}
