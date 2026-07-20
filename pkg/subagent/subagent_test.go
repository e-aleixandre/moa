package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/agent"
	"github.com/ealeixandre/moa/pkg/core"
)

type mockProvider struct {
	mu       sync.Mutex
	calls    int
	handlers []func(context.Context, core.Request) (<-chan core.AssistantEvent, error)
}

func newMockProvider(handlers ...func(context.Context, core.Request) (<-chan core.AssistantEvent, error)) *mockProvider {
	return &mockProvider{handlers: handlers}
}

func (m *mockProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	m.mu.Lock()
	idx := m.calls
	m.calls++
	m.mu.Unlock()
	if idx >= len(m.handlers) {
		return nil, fmt.Errorf("no more handlers (%d)", idx)
	}
	return m.handlers[idx](ctx, req)
}

func textResponse(text string) func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
	return func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 10)
		go func() {
			defer close(ch)
			msg := core.Message{
				Role:       "assistant",
				Content:    []core.Content{core.TextContent(text)},
				StopReason: "end_turn",
				Timestamp:  time.Now().Unix(),
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventTextStart, ContentIndex: 0}
			ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, ContentIndex: 0, Delta: text}
			ch <- core.AssistantEvent{Type: core.ProviderEventTextEnd, ContentIndex: 0}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		}()
		return ch, nil
	}
}

func toolCallResponse(toolID, toolName string, args map[string]any) func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
	return func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 5)
		go func() {
			defer close(ch)
			msg := core.Message{
				Role: "assistant",
				Content: []core.Content{
					core.ToolCallContent(toolID, toolName, args),
				},
				StopReason: "tool_use",
				Timestamp:  time.Now().Unix(),
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		}()
		return ch, nil
	}
}

func cancellableResponse(started chan<- struct{}) func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
	return func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 2)
		go func() {
			defer close(ch)
			if started != nil {
				close(started)
			}
			<-ctx.Done()
			ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: ctx.Err()}
		}()
		return ch, nil
	}
}

func gateResponse(started chan<- struct{}, release <-chan struct{}, text string) func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
	return func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 10)
		go func() {
			defer close(ch)
			if started != nil {
				close(started)
			}
			select {
			case <-release:
			case <-ctx.Done():
				ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: ctx.Err()}
				return
			}
			msg := core.Message{
				Role:       "assistant",
				Content:    []core.Content{core.TextContent(text)},
				StopReason: "end_turn",
				Timestamp:  time.Now().Unix(),
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		}()
		return ch, nil
	}
}

func gatedToolCallResponse(release <-chan struct{}, toolID, toolName string, args map[string]any) func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
	return func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 5)
		go func() {
			defer close(ch)
			select {
			case <-release:
			case <-ctx.Done():
				ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: ctx.Err()}
				return
			}
			msg := core.Message{
				Role: "assistant",
				Content: []core.Content{
					core.ToolCallContent(toolID, toolName, args),
				},
				StopReason: "tool_use",
				Timestamp:  time.Now().Unix(),
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		}()
		return ch, nil
	}
}

func textOf(res core.Result) string {
	var parts []string
	for _, c := range res.Content {
		if c.Type == "text" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "")
}

func jobIDFromResult(t *testing.T, res core.Result) string {
	t.Helper()
	for _, line := range strings.Split(textOf(res), "\n") {
		if strings.HasPrefix(line, "Job ID: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Job ID: "))
		}
	}
	t.Fatalf("job id not found in result %q", textOf(res))
	return ""
}

func waitFor(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if fn() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timeout waiting for condition")
		}
		<-ticker.C
	}
}

func newSubagentTools(t *testing.T, cfg Config, parentTools ...core.Tool) (core.Tool, core.Tool, core.Tool) {
	t.Helper()
	sub, status, cancel, _ := newSubagentToolsWithStore(t, cfg, parentTools...)
	return sub, status, cancel
}

// newSubagentToolsWithStore is like newSubagentTools but also returns the
// underlying jobStore, for tests that need to promote a job by ID (there is
// no "subagent_promote" tool — promotion is triggered via Jobs.Promote /
// jobStore.promote, the same path pkg/serve and the bus command use).
func newSubagentToolsWithStore(t *testing.T, cfg Config, parentTools ...core.Tool) (core.Tool, core.Tool, core.Tool, *jobStore) {
	t.Helper()
	reg := core.NewRegistry()
	for _, tool := range parentTools {
		_ = reg.Register(tool)
	}
	cfg.ParentTools = reg
	if cfg.AppCtx == nil {
		// The sync path now derives its job ctx from cfg.AppCtx (same as
		// async), so it needs a non-nil AppCtx even in tests that don't
		// otherwise care about cancellation.
		cfg.AppCtx = context.Background()
	}
	jobs := newJobStore()
	return newSubagent(cfg, jobs), newSubagentStatus(jobs), newSubagentCancel(jobs), jobs
}

func TestSubagentSyncBasic(t *testing.T) {
	provider := newMockProvider(textResponse("child done"))
	sub, _, _ := newSubagentTools(t, Config{
		DefaultModel:    core.Model{ID: "default", Provider: "mock"},
		ProviderFactory: func(model core.Model) (core.Provider, error) { return provider, nil },
	})

	res, err := sub.Execute(context.Background(), map[string]any{"task": "do it"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := textOf(res); got != "child done" {
		t.Fatalf("expected final child text, got %q", got)
	}
}

func TestBuildChildRegistryFiltersNestedAndDedupes(t *testing.T) {
	parent := core.NewRegistry()
	_ = parent.Register(core.Tool{Name: "read"})
	_ = parent.Register(core.Tool{Name: "subagent"})
	_ = parent.Register(core.Tool{Name: "subagent_status"})
	_ = parent.Register(core.Tool{Name: "subagent_cancel"})
	_ = parent.Register(core.Tool{Name: "grep"})

	reg, errRes := buildChildRegistry(parent, map[string]any{"tools": []any{"read", "read", "grep"}})
	if errRes != nil {
		t.Fatalf("unexpected error: %s", textOf(*errRes))
	}
	if reg.Count() != 2 {
		t.Fatalf("expected 2 child tools, got %d", reg.Count())
	}
	if _, ok := reg.Get("subagent"); ok {
		t.Fatal("child registry should not include subagent")
	}

	// Explicitly requesting excluded tools should silently skip them, not error.
	reg2, errRes2 := buildChildRegistry(parent, map[string]any{
		"tools": []any{"read", "subagent", "subagent_status", "grep"},
	})
	if errRes2 != nil {
		t.Fatalf("expected excluded tools to be silently skipped, got error: %s", textOf(*errRes2))
	}
	if reg2.Count() != 2 {
		t.Fatalf("expected 2 tools (read+grep), got %d", reg2.Count())
	}
}

func TestBuildChildRegistryExcludesMemory(t *testing.T) {
	parent := core.NewRegistry()
	_ = parent.Register(core.Tool{Name: "read"})
	_ = parent.Register(core.Tool{Name: "memory"})
	_ = parent.Register(core.Tool{Name: "grep"})

	reg, errRes := buildChildRegistry(parent, map[string]any{"tools": []any{"read", "memory", "grep"}})
	if errRes != nil {
		t.Fatalf("unexpected error: %s", textOf(*errRes))
	}
	if _, ok := reg.Get("memory"); ok {
		t.Error("child registry should not include memory tool")
	}
	if reg.Count() != 2 {
		t.Fatalf("expected 2 tools (read+grep), got %d", reg.Count())
	}
}

func TestBuildChildRegistryCaseInsensitive(t *testing.T) {
	parent := core.NewRegistry()
	_ = parent.Register(core.Tool{Name: "read"})
	_ = parent.Register(core.Tool{Name: "bash"})
	_ = parent.Register(core.Tool{Name: "write"})

	// Model sends Claude Code casing ("Read", "Bash") — should still resolve.
	reg, errRes := buildChildRegistry(parent, map[string]any{
		"tools": []any{"Read", "Bash", "WRITE"},
	})
	if errRes != nil {
		t.Fatalf("unexpected error: %s", textOf(*errRes))
	}
	if reg.Count() != 3 {
		t.Fatalf("expected 3 tools, got %d", reg.Count())
	}
	for _, name := range []string{"read", "bash", "write"} {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("expected tool %q to be registered", name)
		}
	}
}

func TestBuildChildRegistryRejectsEmptyAndUnknownTools(t *testing.T) {
	parent := core.NewRegistry()
	_ = parent.Register(core.Tool{Name: "read"})

	_, errRes := buildChildRegistry(parent, map[string]any{"tools": []any{}})
	if errRes == nil || !strings.Contains(textOf(*errRes), "tools array cannot be empty") {
		t.Fatalf("expected empty tools error, got %v", errRes)
	}

	_, errRes = buildChildRegistry(parent, map[string]any{"tools": []any{"read", "missing"}})
	if errRes == nil || !strings.Contains(textOf(*errRes), "unknown tool: missing") {
		t.Fatalf("expected unknown tool error, got %v", errRes)
	}
}

func TestSubagentUsesCurrentModelWhenOmitted(t *testing.T) {
	current := core.Model{ID: "first", Provider: "mock"}
	var seen []string
	var mu sync.Mutex

	sub, _, _ := newSubagentTools(t, Config{
		DefaultModel: current,
		CurrentModel: func() core.Model { return current },
		ProviderFactory: func(model core.Model) (core.Provider, error) {
			mu.Lock()
			seen = append(seen, model.ID)
			mu.Unlock()
			return newMockProvider(textResponse("ok")), nil
		},
	})

	_, err := sub.Execute(context.Background(), map[string]any{"task": "one"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	current = core.Model{ID: "second", Provider: "mock"}
	_, err = sub.Execute(context.Background(), map[string]any{"task": "two"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[0] != "first" || seen[1] != "second" {
		t.Fatalf("unexpected models: %v", seen)
	}
}

func TestSubagentModelOverrideUsesRequestedModel(t *testing.T) {
	var got core.Model
	sub, _, _ := newSubagentTools(t, Config{
		DefaultModel: core.Model{ID: "default", Provider: "mock"},
		ProviderFactory: func(model core.Model) (core.Provider, error) {
			got = model
			return newMockProvider(textResponse("ok")), nil
		},
	})

	_, err := sub.Execute(context.Background(), map[string]any{"task": "x", "model": "haiku"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == "default" || got.ID == "" {
		t.Fatalf("expected override model, got %+v", got)
	}
}

func TestSubagentThinkingOverrideUsesRequestedValue(t *testing.T) {
	var seenThinking string
	var startedThinking string
	sub, _, _ := newSubagentTools(t, Config{
		DefaultModel: core.Model{ID: "default", Provider: "mock"},
		CurrentThinkingLevel: func() string {
			return "low"
		},
		ProviderFactory: func(model core.Model) (core.Provider, error) {
			return newMockProvider(func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
				seenThinking = req.Options.ThinkingLevel
				return textResponse("ok")(ctx, req)
			}), nil
		},
		OnChildStart: func(_ string, _ string, _ string, thinking string, _ bool, _ time.Time, _ int) {
			startedThinking = thinking
		},
	})

	_, err := sub.Execute(context.Background(), map[string]any{"task": "x", "thinking": "high"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if seenThinking != "high" {
		t.Fatalf("expected thinking override high, got %q", seenThinking)
	}
	if startedThinking != "high" {
		t.Fatalf("expected start thinking high, got %q", startedThinking)
	}
}

func TestSubagentThinkingFallsBackToCurrentValue(t *testing.T) {
	const defaultThinking = "medium"

	seenThinking := make(chan string, 1)
	startedThinking := make(chan string, 1)
	sub, _, _, jobs := newSubagentToolsWithStore(t, Config{
		DefaultModel: core.Model{ID: "default", Provider: "mock"},
		CurrentThinkingLevel: func() string {
			return defaultThinking
		},
		ProviderFactory: func(model core.Model) (core.Provider, error) {
			return newMockProvider(func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
				seenThinking <- req.Options.ThinkingLevel
				return textResponse("ok")(ctx, req)
			}), nil
		},
		OnChildStart: func(_ string, _ string, _ string, thinking string, _ bool, _ time.Time, _ int) {
			startedThinking <- thinking
		},
	})

	res, err := sub.Execute(context.Background(), map[string]any{"task": "x", "async": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-seenThinking:
		if got != defaultThinking {
			t.Fatalf("expected fallback provider thinking %q, got %q", defaultThinking, got)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for provider thinking")
	}
	select {
	case got := <-startedThinking:
		if got != defaultThinking {
			t.Fatalf("expected fallback start thinking %q, got %q", defaultThinking, got)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for start thinking")
	}
	jobID := jobIDFromResult(t, res)
	snap, ok := jobs.snapshot(jobID)
	if !ok {
		t.Fatalf("expected snapshot for job %q", jobID)
	}
	if snap.Thinking != defaultThinking {
		t.Fatalf("expected fallback snapshot thinking %q, got %q", defaultThinking, snap.Thinking)
	}
}

func TestSubagentInvalidThinkingFails(t *testing.T) {
	sub, _, _ := newSubagentTools(t, Config{
		DefaultModel:    core.Model{ID: "default", Provider: "mock"},
		ProviderFactory: func(model core.Model) (core.Provider, error) { return newMockProvider(textResponse("ok")), nil },
	})

	res, err := sub.Execute(context.Background(), map[string]any{"task": "x", "thinking": "turbo"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(textOf(res), "invalid thinking level") {
		t.Fatalf("expected invalid thinking error, got %q", textOf(res))
	}
}

func TestSubagentPermissionGetterFollowsRuntimeChanges(t *testing.T) {
	var executed int
	danger := core.Tool{
		Name:       "danger",
		Parameters: json.RawMessage(`{"type":"object"}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			executed++
			return core.TextResult("ran"), nil
		},
	}

	providerFactory := func(model core.Model) (core.Provider, error) {
		return newMockProvider(
			toolCallResponse("tc-1", "danger", map[string]any{}),
			textResponse("done"),
		), nil
	}

	var currentPerm func(context.Context, string, map[string]any) *core.ToolCallDecision
	sub, _, _ := newSubagentTools(t, Config{
		DefaultModel: core.Model{ID: "default", Provider: "mock"},
		CurrentPermissionCheck: func() func(context.Context, string, map[string]any) *core.ToolCallDecision {
			return currentPerm
		},
		ProviderFactory: providerFactory,
	}, danger)

	_, err := sub.Execute(context.Background(), map[string]any{"task": "run allowed"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if executed != 1 {
		t.Fatalf("expected tool to run once, got %d", executed)
	}

	currentPerm = func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision {
		if name == "danger" {
			return &core.ToolCallDecision{Block: true, Reason: "blocked"}
		}
		return nil
	}
	_, err = sub.Execute(context.Background(), map[string]any{"task": "run blocked"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if executed != 1 {
		t.Fatalf("permission getter did not update; executions=%d", executed)
	}
}

func TestSubagentPermissionGetterIsLiveWithinSingleRun(t *testing.T) {
	firstRan := make(chan struct{})
	secondTurn := make(chan struct{})
	var executed int
	danger := core.Tool{
		Name:       "danger",
		Parameters: json.RawMessage(`{"type":"object"}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			executed++
			if executed == 1 {
				close(firstRan)
			}
			return core.TextResult("ran"), nil
		},
	}

	provider := newMockProvider(
		toolCallResponse("tc-1", "danger", map[string]any{}),
		gatedToolCallResponse(secondTurn, "tc-2", "danger", map[string]any{}),
		textResponse("done"),
	)

	var currentPerm func(context.Context, string, map[string]any) *core.ToolCallDecision
	sub, _, _ := newSubagentTools(t, Config{
		DefaultModel: core.Model{ID: "default", Provider: "mock"},
		CurrentPermissionCheck: func() func(context.Context, string, map[string]any) *core.ToolCallDecision {
			return currentPerm
		},
		ProviderFactory: func(model core.Model) (core.Provider, error) { return provider, nil },
	}, danger)

	done := make(chan error, 1)
	go func() {
		_, err := sub.Execute(context.Background(), map[string]any{"task": "two turns"}, nil)
		done <- err
	}()

	<-firstRan
	currentPerm = func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision {
		if name == "danger" {
			return &core.ToolCallDecision{Block: true, Reason: "blocked later"}
		}
		return nil
	}
	close(secondTurn)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if executed != 1 {
		t.Fatalf("expected second tool call to be blocked after permission change, executions=%d", executed)
	}
}

func TestSubagentSyncOnUpdateForwardsProgress(t *testing.T) {
	release := make(chan struct{})
	readTool := core.Tool{
		Name:       "read",
		Parameters: json.RawMessage(`{"type":"object"}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			select {
			case <-release:
				return core.TextResult("ok"), nil
			case <-ctx.Done():
				return core.ErrorResult(ctx.Err().Error()), nil
			}
		},
	}
	provider := newMockProvider(
		toolCallResponse("tc-1", "read", map[string]any{"path": "x"}),
		textResponse("done"),
	)
	sub, _, _ := newSubagentTools(t, Config{
		DefaultModel:    core.Model{ID: "default", Provider: "mock"},
		ProviderFactory: func(model core.Model) (core.Provider, error) { return provider, nil },
	}, readTool)

	var (
		mu      sync.Mutex
		updates []string
		done    = make(chan error, 1)
	)
	go func() {
		_, err := sub.Execute(context.Background(), map[string]any{"task": "do it"}, func(res core.Result) {
			mu.Lock()
			updates = append(updates, textOf(res))
			mu.Unlock()
		})
		done <- err
	}()

	waitFor(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(strings.Join(updates, "\n"), "[subagent] Running read")
	})
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSubagentUnknownModelWithoutProviderFails(t *testing.T) {
	sub, _, _ := newSubagentTools(t, Config{
		DefaultModel:    core.Model{ID: "default", Provider: "mock"},
		ProviderFactory: func(model core.Model) (core.Provider, error) { return newMockProvider(textResponse("ok")), nil },
	})

	res, err := sub.Execute(context.Background(), map[string]any{"task": "x", "model": "totally-fake"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(textOf(res), "unknown model") {
		t.Fatalf("expected unknown model error, got %q", textOf(res))
	}
}

func TestAsyncSubagentCancelledCallerContextDoesNotSpawn(t *testing.T) {
	streamStarted := make(chan struct{}, 1)
	provider := &mockProvider{handlers: []func(context.Context, core.Request) (<-chan core.AssistantEvent, error){
		func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
			streamStarted <- struct{}{}
			return textResponse("should not run")(ctx, req)
		},
	}}

	factoryStarted := make(chan struct{})
	factoryRelease := make(chan struct{})
	sub, _, _ := newSubagentTools(t, Config{
		DefaultModel: core.Model{ID: "default", Provider: "mock"},
		ProviderFactory: func(model core.Model) (core.Provider, error) {
			close(factoryStarted)
			<-factoryRelease
			return provider, nil
		},
		AppCtx: context.Background(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan core.Result, 1)
	errCh := make(chan error, 1)
	go func() {
		res, err := sub.Execute(ctx, map[string]any{"task": "async", "async": true}, nil)
		resultCh <- res
		errCh <- err
	}()

	<-factoryStarted
	cancel()
	close(factoryRelease)

	var res core.Result
	select {
	case res = <-resultCh:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for cancelled async result")
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if !res.IsError || strings.Contains(textOf(res), "Job ID:") {
		t.Fatalf("expected cancelled error without job spawn, got %q", textOf(res))
	}
	select {
	case <-streamStarted:
		t.Fatal("provider stream should not start for cancelled async request")
	default:
	}
}

func TestAsyncSubagentStatusAndCompletion(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	provider := newMockProvider(gateResponse(started, release, "async done"))
	sub, statusTool, _ := newSubagentTools(t, Config{
		DefaultModel:    core.Model{ID: "default", Provider: "mock"},
		ProviderFactory: func(model core.Model) (core.Provider, error) { return provider, nil },
		AppCtx:          context.Background(),
	})

	res, err := sub.Execute(context.Background(), map[string]any{"task": "async", "async": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	jobID := jobIDFromResult(t, res)
	<-started

	statusRes, err := statusTool.Execute(context.Background(), map[string]any{"job_id": jobID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(textOf(statusRes), "Status: running") {
		t.Fatalf("expected running status, got %q", textOf(statusRes))
	}

	close(release)
	waitFor(t, time.Second, func() bool {
		res, _ := statusTool.Execute(context.Background(), map[string]any{"job_id": jobID}, nil)
		return strings.Contains(textOf(res), "Status: completed")
	})
}

func TestSubagentTimeoutSurfacesActionableMessage(t *testing.T) {
	// A provider that blocks until the context is cancelled, then reports the
	// context error — mimicking a real stream that outlives the child's own
	// MaxRunDuration budget.
	provider := newMockProvider(cancellableResponse(nil))
	var (
		mu             sync.Mutex
		notifiedStatus string
		notifiedTail   string
	)
	sub, statusTool, _ := newSubagentTools(t, Config{
		DefaultModel:        core.Model{ID: "default", Provider: "mock"},
		ProviderFactory:     func(model core.Model) (core.Provider, error) { return provider, nil },
		AppCtx:              context.Background(),
		ChildMaxRunDuration: 50 * time.Millisecond,
		OnAsyncComplete: func(jobID, task, status, resultTail string, truncated bool) {
			mu.Lock()
			notifiedStatus = status
			notifiedTail = resultTail
			mu.Unlock()
		},
	})

	res, err := sub.Execute(context.Background(), map[string]any{"task": "long", "async": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	jobID := jobIDFromResult(t, res)

	waitFor(t, 2*time.Second, func() bool {
		r, _ := statusTool.Execute(context.Background(), map[string]any{"job_id": jobID}, nil)
		return strings.Contains(textOf(r), "Status: failed")
	})
	r, _ := statusTool.Execute(context.Background(), map[string]any{"job_id": jobID}, nil)
	got := textOf(r)
	// Actionable message, not the cryptic raw error.
	if !strings.Contains(got, "timed out after 50ms") {
		t.Errorf("expected effective-duration timeout message, got %q", got)
	}
	if !strings.Contains(got, "max_duration") {
		t.Errorf("expected max_duration guidance, got %q", got)
	}
	if strings.Contains(got, "context deadline exceeded") {
		t.Errorf("should not leak the raw context error: %q", got)
	}

	// Blocker #2: the async notification path must deliver the failure message,
	// not an empty string (a failed job carries its text in Error, not Result).
	waitFor(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return notifiedStatus == statusFailed
	})
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(notifiedTail, "timed out after 50ms") {
		t.Errorf("OnAsyncComplete tail should carry the timeout message, got %q", notifiedTail)
	}
}

// An inherited parent deadline (AppCtx) that fires must NOT be misreported as
// the child exhausting its own much-larger MaxRunDuration budget.
func TestSubagentInheritedDeadlineNotReportedAsChildTimeout(t *testing.T) {
	provider := newMockProvider(cancellableResponse(nil))
	appCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	sub, statusTool, _ := newSubagentTools(t, Config{
		DefaultModel:        core.Model{ID: "default", Provider: "mock"},
		ProviderFactory:     func(model core.Model) (core.Provider, error) { return provider, nil },
		AppCtx:              appCtx,
		ChildMaxRunDuration: 10 * time.Minute, // far larger than the AppCtx deadline
	})

	res, err := sub.Execute(context.Background(), map[string]any{"task": "long", "async": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	jobID := jobIDFromResult(t, res)

	waitFor(t, 2*time.Second, func() bool {
		r, _ := statusTool.Execute(context.Background(), map[string]any{"job_id": jobID}, nil)
		s := textOf(r)
		return strings.Contains(s, "Status: failed") || strings.Contains(s, "Status: cancelled")
	})
	got := textOf(mustStatus(t, statusTool, jobID))
	// The child's 10m budget did NOT trip; must not claim it did.
	if strings.Contains(got, "timed out after 10m") {
		t.Errorf("inherited deadline misreported as child timeout: %q", got)
	}
}

// A genuine subagent_cancel racing the child's own deadline must be classified
// as cancelled, never as a timeout.
func TestSubagentCancelWinsOverTimeout(t *testing.T) {
	provider := newMockProvider(cancellableResponse(nil))
	sub, statusTool, cancelTool := newSubagentTools(t, Config{
		DefaultModel:        core.Model{ID: "default", Provider: "mock"},
		ProviderFactory:     func(model core.Model) (core.Provider, error) { return provider, nil },
		AppCtx:              context.Background(),
		ChildMaxRunDuration: 10 * time.Minute,
	})

	res, err := sub.Execute(context.Background(), map[string]any{"task": "long", "async": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	jobID := jobIDFromResult(t, res)

	if _, err := cancelTool.Execute(context.Background(), map[string]any{"job_id": jobID}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(textOf(mustStatus(t, statusTool, jobID)), "Status: cancelled")
	})
	got := textOf(mustStatus(t, statusTool, jobID))
	if strings.Contains(got, "timed out") {
		t.Errorf("cancel must not be reported as timeout: %q", got)
	}
}

func mustStatus(t *testing.T, statusTool core.Tool, jobID string) core.Result {
	t.Helper()
	r, err := statusTool.Execute(context.Background(), map[string]any{"job_id": jobID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAsyncSubagentSurvivesParentContextCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	provider := newMockProvider(gateResponse(started, release, "done later"))
	sub, statusTool, _ := newSubagentTools(t, Config{
		DefaultModel:    core.Model{ID: "default", Provider: "mock"},
		ProviderFactory: func(model core.Model) (core.Provider, error) { return provider, nil },
		AppCtx:          context.Background(),
	})

	parentCtx, cancel := context.WithCancel(context.Background())
	res, err := sub.Execute(parentCtx, map[string]any{"task": "async", "async": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	jobID := jobIDFromResult(t, res)
	cancel()
	<-started

	statusRes, err := statusTool.Execute(context.Background(), map[string]any{"job_id": jobID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(textOf(statusRes), "Status: running") {
		t.Fatalf("expected job to survive parent ctx cancellation, got %q", textOf(statusRes))
	}

	close(release)
	waitFor(t, time.Second, func() bool {
		res, _ := statusTool.Execute(context.Background(), map[string]any{"job_id": jobID}, nil)
		return strings.Contains(textOf(res), "Status: completed")
	})
}

func TestAsyncCancelSetsCancelledStatus(t *testing.T) {
	started := make(chan struct{})
	provider := newMockProvider(cancellableResponse(started))
	sub, statusTool, cancelTool := newSubagentTools(t, Config{
		DefaultModel:    core.Model{ID: "default", Provider: "mock"},
		ProviderFactory: func(model core.Model) (core.Provider, error) { return provider, nil },
		AppCtx:          context.Background(),
	})

	res, err := sub.Execute(context.Background(), map[string]any{"task": "async", "async": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	jobID := jobIDFromResult(t, res)
	<-started

	cancelRes, err := cancelTool.Execute(context.Background(), map[string]any{"job_id": jobID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(textOf(cancelRes), "cancel") {
		t.Fatalf("unexpected cancel result: %q", textOf(cancelRes))
	}

	waitFor(t, time.Second, func() bool {
		res, _ := statusTool.Execute(context.Background(), map[string]any{"job_id": jobID}, nil)
		return strings.Contains(textOf(res), "Status: cancelled")
	})
}

func TestAsyncStatusUnknownJobID(t *testing.T) {
	_, statusTool, cancelTool := newSubagentTools(t, Config{DefaultModel: core.Model{ID: "default", Provider: "mock"}})

	res, err := statusTool.Execute(context.Background(), map[string]any{"job_id": "missing"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected error result for unknown status job id")
	}

	res, err = cancelTool.Execute(context.Background(), map[string]any{"job_id": "missing"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected error result for unknown cancel job id")
	}
}

func TestJobStoreCleanupRemovesExpiredJobs(t *testing.T) {
	store := newJobStore()
	_, cancel := context.WithCancel(context.Background())
	j := store.create("task", "model", cancel)
	close(j.done)
	store.setCompleted(j.id, "result")

	j.mu.Lock()
	j.finishedAt = time.Now().Add(-2 * time.Hour)
	j.mu.Unlock()

	store.cleanup(time.Hour)

	if _, ok := store.get(j.id); ok {
		t.Fatal("expected expired job to be cleaned up")
	}
}

func TestJobStoreCleanupKeepsRecentJobs(t *testing.T) {
	store := newJobStore()
	_, cancel := context.WithCancel(context.Background())
	j := store.create("task", "model", cancel)
	close(j.done)
	store.setCompleted(j.id, "result")

	store.cleanup(time.Hour)

	if _, ok := store.get(j.id); !ok {
		t.Fatal("expected recent job to survive cleanup")
	}
}

func TestAsyncSubagentOnCompleteOnSuccess(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	provider := newMockProvider(gateResponse(started, release, "child result"))

	var (
		mu        sync.Mutex
		gotID     string
		gotTask   string
		gotStatus string
		gotResult string
		callCount int
	)

	sub, statusTool, _ := newSubagentTools(t, Config{
		DefaultModel:    core.Model{ID: "default", Provider: "mock"},
		ProviderFactory: func(model core.Model) (core.Provider, error) { return provider, nil },
		AppCtx:          context.Background(),
		OnAsyncComplete: func(jobID, task, status, resultTail string, truncated bool) {
			mu.Lock()
			gotID = jobID
			gotTask = task
			gotStatus = status
			gotResult = resultTail
			callCount++
			mu.Unlock()
		},
	})

	res, err := sub.Execute(context.Background(), map[string]any{"task": "my task", "async": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	jobID := jobIDFromResult(t, res)
	<-started
	close(release)

	// Wait for completion.
	waitFor(t, 2*time.Second, func() bool {
		res, _ := statusTool.Execute(context.Background(), map[string]any{"job_id": jobID}, nil)
		return strings.Contains(textOf(res), "Status: completed")
	})

	// Verify OnAsyncComplete was called.
	waitFor(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return callCount == 1
	})

	mu.Lock()
	defer mu.Unlock()
	if gotID != jobID {
		t.Fatalf("expected jobID %q, got %q", jobID, gotID)
	}
	if gotTask != "my task" {
		t.Fatalf("expected task 'my task', got %q", gotTask)
	}
	if gotStatus != "completed" {
		t.Fatalf("expected status 'completed', got %q", gotStatus)
	}
	if gotResult != "child result" {
		t.Fatalf("expected result 'child result', got %q", gotResult)
	}
}

func TestAsyncSubagentOnCompleteOnCancel(t *testing.T) {
	started := make(chan struct{})
	provider := newMockProvider(cancellableResponse(started))

	var (
		mu        sync.Mutex
		gotStatus string
		called    bool
	)

	sub, _, cancelTool := newSubagentTools(t, Config{
		DefaultModel:    core.Model{ID: "default", Provider: "mock"},
		ProviderFactory: func(model core.Model) (core.Provider, error) { return provider, nil },
		AppCtx:          context.Background(),
		OnAsyncComplete: func(jobID, task, status, resultTail string, truncated bool) {
			mu.Lock()
			gotStatus = status
			called = true
			mu.Unlock()
		},
	})

	res, err := sub.Execute(context.Background(), map[string]any{"task": "cancel me", "async": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	jobID := jobIDFromResult(t, res)
	<-started

	cancelTool.Execute(context.Background(), map[string]any{"job_id": jobID}, nil) //nolint:errcheck

	waitFor(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return called
	})

	mu.Lock()
	defer mu.Unlock()
	if gotStatus != "cancelled" {
		t.Fatalf("expected status 'cancelled', got %q", gotStatus)
	}
}

func TestConcurrentStatusPolling(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	provider := newMockProvider(gateResponse(started, release, "done"))
	sub, statusTool, _ := newSubagentTools(t, Config{
		DefaultModel:    core.Model{ID: "default", Provider: "mock"},
		ProviderFactory: func(model core.Model) (core.Provider, error) { return provider, nil },
		AppCtx:          context.Background(),
	})

	res, err := sub.Execute(context.Background(), map[string]any{"task": "async", "async": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	jobID := jobIDFromResult(t, res)
	<-started

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				res, err := statusTool.Execute(context.Background(), map[string]any{"job_id": jobID}, nil)
				if err != nil {
					t.Error(err)
					return
				}
				_ = textOf(res)
			}
		}()
	}
	wg.Wait()
	close(release)
}

func TestResolveChildGuardrailsDefaults(t *testing.T) {
	maxTurns, maxRunDuration := resolveChildGuardrails(Config{}, 0)
	if maxTurns != defaultChildMaxTurns {
		t.Errorf("maxTurns = %d, want default %d", maxTurns, defaultChildMaxTurns)
	}
	if maxRunDuration != defaultChildMaxRunDuration {
		t.Errorf("maxRunDuration = %v, want default %v", maxRunDuration, defaultChildMaxRunDuration)
	}
}

func TestResolveChildGuardrailsOverrides(t *testing.T) {
	cfg := Config{ChildMaxTurns: 5, ChildMaxRunDuration: 2 * time.Minute}
	maxTurns, maxRunDuration := resolveChildGuardrails(cfg, 0)
	if maxTurns != 5 {
		t.Errorf("maxTurns = %d, want 5", maxTurns)
	}
	if maxRunDuration != 2*time.Minute {
		t.Errorf("maxRunDuration = %v, want 2m", maxRunDuration)
	}
}

func TestNewChildAgentAppliesGuardrails(t *testing.T) {
	cfg := Config{ChildMaxTurns: 7, ChildMaxRunDuration: 3 * time.Minute}
	provider := newMockProvider(textResponse("hi"))
	reg := core.NewRegistry()
	child, err := newChildAgent(cfg, provider, core.Model{ID: "m", Provider: "mock"}, "medium", 0, "sys", reg)
	if err != nil {
		t.Fatal(err)
	}
	if child == nil {
		t.Fatal("expected non-nil child agent")
	}
	// MaxBudget is never set on children (no exported getter, but agent.New
	// would reject a negative budget — absence of error is enough coverage
	// here; guardrail values are covered by resolveChildGuardrails above).
}

func TestAsyncSubagentConcurrencyCap(t *testing.T) {
	started := make(chan struct{}, 10)
	release := make(chan struct{})
	provider := newMockProvider(
		gateResponse(started, release, "one"),
		gateResponse(started, release, "two"),
	)
	sub, _, _ := newSubagentTools(t, Config{
		DefaultModel:       core.Model{ID: "default", Provider: "mock"},
		ProviderFactory:    func(model core.Model) (core.Provider, error) { return provider, nil },
		AppCtx:             context.Background(),
		MaxConcurrentAsync: 1,
	})

	res, err := sub.Execute(context.Background(), map[string]any{"task": "first", "async": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error starting first job: %s", textOf(res))
	}
	<-started

	res2, err := sub.Execute(context.Background(), map[string]any{"task": "second", "async": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res2.IsError {
		t.Fatalf("expected concurrency cap error, got success: %s", textOf(res2))
	}
	if !strings.Contains(textOf(res2), "too many concurrent async subagents") {
		t.Fatalf("unexpected error message: %s", textOf(res2))
	}

	close(release)
}

func TestFormatStatusUsageLine(t *testing.T) {
	withUsage := jobSnapshot{
		Status:  statusCompleted,
		Task:    "task",
		Model:   "m",
		Result:  "done",
		Usage:   &core.Usage{Input: 100, Output: 50},
		CostUSD: 0.0123,
	}
	out := formatStatus(withUsage)
	if !strings.Contains(out, "Tokens: 100/50") || !strings.Contains(out, "Cost: $0.0123") {
		t.Fatalf("expected usage line in output, got: %s", out)
	}

	noUsage := jobSnapshot{
		Status: statusCompleted,
		Task:   "task",
		Model:  "m",
		Result: "done",
	}
	out2 := formatStatus(noUsage)
	if strings.Contains(out2, "Tokens:") || strings.Contains(out2, "Cost:") {
		t.Fatalf("did not expect usage line without usage, got: %s", out2)
	}
}

// TestSubagentTranscriptAccumulatesMidRun verifies that the job's stored
// messages grow as the child completes messages (via message_end), so a
// reconnecting client can fetch the transcript-so-far mid-run, rather than
// only after the child finishes. See P1 #3.
func TestSubagentTranscriptAccumulatesMidRun(t *testing.T) {
	releaseTool := make(chan struct{})
	releaseFinal := make(chan struct{})
	// Turn 1: child calls a tool (gated). Turn 2: child answers (gated).
	provider := newMockProvider(
		gatedToolCallResponse(releaseTool, "tc-1", "noop", map[string]any{}),
		gateResponse(nil, releaseFinal, "all done"),
	)

	noop := core.Tool{
		Name:        "noop",
		Description: "does nothing",
		Execute: func(ctx context.Context, args map[string]any, _ func(core.Result)) (core.Result, error) {
			return core.TextResult("ok"), nil
		},
	}

	reg := core.NewRegistry()
	_ = reg.Register(noop)
	jobs := newJobStore()
	sub := newSubagent(Config{
		DefaultModel:    core.Model{ID: "default", Provider: "mock"},
		ProviderFactory: func(model core.Model) (core.Provider, error) { return provider, nil },
		AppCtx:          context.Background(),
		ParentTools:     reg,
	}, jobs)

	res, err := sub.Execute(context.Background(), map[string]any{
		"task": "do it", "async": true, "tools": []any{"noop"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	jobID := jobIDFromResult(t, res)

	// Let turn 1 (tool call) complete → child records the assistant tool-call
	// message plus the tool result, so messages() should become non-empty
	// before the whole job finishes.
	close(releaseTool)
	waitFor(t, 2*time.Second, func() bool {
		return len(jobs.messages(jobID)) > 0
	})
	midCount := len(jobs.messages(jobID))
	if midCount == 0 {
		t.Fatal("expected transcript to accumulate mid-run, got 0 messages")
	}

	// Finish turn 2.
	close(releaseFinal)
	waitFor(t, 2*time.Second, func() bool {
		snap, ok := jobs.snapshot(jobID)
		return ok && snap.Status == statusCompleted
	})
	finalCount := len(jobs.messages(jobID))
	if finalCount < midCount {
		t.Fatalf("final transcript (%d) smaller than mid-run (%d)", finalCount, midCount)
	}
}

// TestSubagentOnChildUsageMidRun verifies that OnChildUsage fires as the child
// closes messages (message_end), so live UIs can show accumulated tokens/cost
// before the terminal OnChildEnd.
func TestSubagentOnChildUsageMidRun(t *testing.T) {
	releaseTool := make(chan struct{})
	releaseFinal := make(chan struct{})
	provider := newMockProvider(
		gatedToolCallResponse(releaseTool, "tc-1", "noop", map[string]any{}),
		gateResponse(nil, releaseFinal, "all done"),
	)

	noop := core.Tool{
		Name:        "noop",
		Description: "does nothing",
		Execute: func(ctx context.Context, args map[string]any, _ func(core.Result)) (core.Result, error) {
			return core.TextResult("ok"), nil
		},
	}

	reg := core.NewRegistry()
	_ = reg.Register(noop)

	var mu sync.Mutex
	usageCalls := 0
	jobs := newJobStore()
	sub := newSubagent(Config{
		DefaultModel:    core.Model{ID: "default", Provider: "mock"},
		ProviderFactory: func(model core.Model) (core.Provider, error) { return provider, nil },
		AppCtx:          context.Background(),
		ParentTools:     reg,
		OnChildUsage: func(jobID string, usage *core.Usage, costUSD float64) {
			mu.Lock()
			usageCalls++
			mu.Unlock()
		},
	}, jobs)

	res, err := sub.Execute(context.Background(), map[string]any{
		"task": "do it", "async": true, "tools": []any{"noop"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	jobID := jobIDFromResult(t, res)

	// After turn 1's message_end, OnChildUsage must have fired at least once
	// before the job as a whole finishes.
	close(releaseTool)
	waitFor(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return usageCalls > 0
	})

	close(releaseFinal)
	waitFor(t, 2*time.Second, func() bool {
		snap, ok := jobs.snapshot(jobID)
		return ok && snap.Status == statusCompleted
	})
}

func TestResolveMaxDuration(t *testing.T) {
	tests := []struct {
		name    string
		params  map[string]any
		want    time.Duration
		wantErr bool
	}{
		{"absent", map[string]any{}, 0, false},
		{"empty", map[string]any{"max_duration": ""}, 0, false},
		{"valid minutes", map[string]any{"max_duration": "20m"}, 20 * time.Minute, false},
		{"valid hour", map[string]any{"max_duration": "1h"}, time.Hour, false},
		{"invalid", map[string]any{"max_duration": "soon"}, 0, true},
		{"non-positive", map[string]any{"max_duration": "-5m"}, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, errRes := resolveMaxDuration(tc.params)
			if tc.wantErr {
				if errRes == nil {
					t.Fatalf("expected error result, got none")
				}
				return
			}
			if errRes != nil {
				t.Fatalf("unexpected error: %s", textOf(*errRes))
			}
			if got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSuggestLongerDuration(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want time.Duration
	}{
		{10 * time.Minute, 20 * time.Minute},
		{30 * time.Minute, time.Hour},
		{time.Hour, 2 * time.Hour},
		{20 * time.Second, time.Minute},      // 40s → round UP to 1m
		{90 * time.Second, 3 * time.Minute},  // 180s exact
		{40 * time.Second, 2 * time.Minute},  // 80s → round UP to 2m (never below the double)
		{100 * time.Second, 4 * time.Minute}, // 200s → round UP to 4m
		{math.MaxInt64 - 100, 0},             // overflow guard: no fixed want, invariants checked below
	}
	overflowIdx := len(tests) - 1
	for i, tc := range tests {
		got := suggestLongerDuration(tc.in)
		// The overflow case has no fixed expected value; assert only invariants.
		if i != overflowIdx && got != tc.want {
			t.Errorf("suggestLongerDuration(%v) = %v, want %v", tc.in, got, tc.want)
		}
		// Invariant: the suggestion is always a positive, whole-minute duration.
		if got <= 0 {
			t.Errorf("suggestLongerDuration(%v) = %v is not positive", tc.in, got)
		}
		if got%time.Minute != 0 {
			t.Errorf("suggestLongerDuration(%v) = %v is not a whole minute", tc.in, got)
		}
		if i == overflowIdx {
			// At the very ceiling of time.Duration we cannot round up without
			// wrapping, so we round down to the current whole minute — still
			// within a minute of the (astronomical) input, never the old
			// catastrophic wrap to a tiny/negative value.
			if tc.in-got >= time.Minute {
				t.Errorf("overflow: suggestLongerDuration(%v) = %v drifted too far below input", tc.in, got)
			}
			continue
		}
		// For all realistic inputs the suggestion never shrinks the budget...
		if got < tc.in {
			t.Errorf("suggestLongerDuration(%v) = %v is below the original budget", tc.in, got)
		}
		// ...and, when doubling doesn't overflow, is never below the real double.
		if tc.in >= time.Minute/2 && tc.in <= math.MaxInt64/2 && got < tc.in*2 {
			t.Errorf("suggestLongerDuration(%v) = %v is below the double %v", tc.in, got, tc.in*2)
		}
	}
}
func TestFormatDurationArg(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{20 * time.Minute, "20m"},
		{time.Hour, "1h"},
		{90 * time.Minute, "1h30m"},
		{2 * time.Hour, "2h"},
		{time.Minute, "1m"},
	}
	for _, tc := range tests {
		if got := formatDurationArg(tc.in); got != tc.want {
			t.Errorf("formatDurationArg(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTimeoutMessage(t *testing.T) {
	msg := timeoutMessage(10*time.Minute, "")
	if !strings.Contains(msg, "timed out after 10m0s") {
		t.Errorf("message should state the effective duration: %q", msg)
	}
	if !strings.Contains(msg, `"20m"`) {
		t.Errorf("message should suggest a larger max_duration: %q", msg)
	}
	if strings.Contains(msg, "Partial output") {
		t.Errorf("no partial should not mention partial output: %q", msg)
	}
	withPartial := timeoutMessage(10*time.Minute, "did half the work")
	if !strings.Contains(withPartial, "Partial output before the timeout:\ndid half the work") {
		t.Errorf("partial should be included: %q", withPartial)
	}
	// The actionable guidance must come AFTER the partial so it survives
	// tail-truncation on the async notification path.
	if strings.Index(withPartial, "did half the work") > strings.Index(withPartial, "timed out after") {
		t.Errorf("guidance should come after the partial, got %q", withPartial)
	}
}

func TestTimeoutPartialExcludesMarker(t *testing.T) {
	// The synthetic "(run timed out)" marker must not be surfaced as real output.
	marker := []core.AgentMessage{
		core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{core.TextContent(agent.MarkerRunTimedOut)}}),
	}
	if got := timeoutPartial(marker); got != "" {
		t.Errorf("marker-only should yield empty partial, got %q", got)
	}
	real := []core.AgentMessage{
		core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{core.TextContent("real work")}}),
	}
	if got := timeoutPartial(real); got != "real work" {
		t.Errorf("real text should pass through, got %q", got)
	}
}

func TestResolveChildGuardrailsPerCallDurationOverrides(t *testing.T) {
	cfg := Config{ChildMaxRunDuration: 10 * time.Minute}
	if _, d := resolveChildGuardrails(cfg, 30*time.Minute); d != 30*time.Minute {
		t.Fatalf("per-call override not applied: got %v", d)
	}
	if _, d := resolveChildGuardrails(cfg, 0); d != 10*time.Minute {
		t.Fatalf("configured default not used: got %v", d)
	}
	if _, d := resolveChildGuardrails(Config{}, 0); d != defaultChildMaxRunDuration {
		t.Fatalf("package default not used: got %v", d)
	}
}

func TestResolveResumeWithoutLoaderFails(t *testing.T) {
	_, errRes := resolveResume(Config{}, map[string]any{"resume": "job-1"})
	if errRes == nil {
		t.Fatal("expected error when TranscriptLoader is nil")
	}
}

func TestResolveResumeUnknownJobFails(t *testing.T) {
	cfg := Config{TranscriptLoader: func(string) ([]core.AgentMessage, error) {
		return nil, fmt.Errorf("not found")
	}}
	_, errRes := resolveResume(cfg, map[string]any{"resume": "nope"})
	if errRes == nil {
		t.Fatal("expected error for unknown job id")
	}
}

func TestResolveResumeAbsentReturnsNil(t *testing.T) {
	msgs, errRes := resolveResume(Config{}, map[string]any{})
	if errRes != nil {
		t.Fatalf("unexpected error: %s", textOf(*errRes))
	}
	if msgs != nil {
		t.Fatalf("expected nil seed messages, got %d", len(msgs))
	}
}

func TestResolveResumeStripsThinking(t *testing.T) {
	prior := []core.AgentMessage{
		core.WrapMessage(core.NewUserMessage("earlier task")),
		{Message: core.Message{Role: "assistant", Content: []core.Content{
			{Type: "thinking", Text: "secret reasoning"},
			core.TextContent("earlier answer"),
		}}},
	}
	cfg := Config{TranscriptLoader: func(string) ([]core.AgentMessage, error) { return prior, nil }}
	msgs, errRes := resolveResume(cfg, map[string]any{"resume": "job-1"})
	if errRes != nil {
		t.Fatalf("unexpected error: %s", textOf(*errRes))
	}
	for _, m := range msgs {
		for _, c := range m.Content {
			if c.Type == "thinking" {
				t.Fatal("thinking block was not stripped from resumed transcript")
			}
		}
	}
	// Original slice must be untouched.
	if prior[1].Content[0].Type != "thinking" {
		t.Fatal("sanitizeResumeTranscript mutated the input transcript")
	}
}

func TestSanitizeResumeTrimsOrphanToolCall(t *testing.T) {
	// A transcript cut off mid-turn: the last assistant emitted a tool_call that
	// never got its tool_result. Replaying it + a new user message is invalid.
	msgs := []core.AgentMessage{
		core.WrapMessage(core.NewUserMessage("task")),
		{Message: core.Message{Role: "assistant", Content: []core.Content{
			core.ToolCallContent("call-1", "read", map[string]any{"path": "x"}),
		}}},
		{Message: core.Message{Role: "tool_result", ToolCallID: "call-1", ToolName: "read",
			Content: []core.Content{core.TextContent("file contents")}}},
		{Message: core.Message{Role: "assistant", Content: []core.Content{
			core.ToolCallContent("call-2", "read", map[string]any{"path": "y"}),
		}}},
	}
	clean := sanitizeResumeTranscript(msgs)
	// The last assistant (call-2, unmatched) and everything after must be gone;
	// the satisfied call-1 turn stays.
	if len(clean) != 3 {
		t.Fatalf("expected 3 messages after trimming the orphan turn, got %d", len(clean))
	}
	last := clean[len(clean)-1]
	if last.Role != "tool_result" || last.ToolCallID != "call-1" {
		t.Fatalf("expected transcript to end at the satisfied tool_result, got %+v", last)
	}
}

func TestSanitizeResumeDropsThinkingOnlyAssistant(t *testing.T) {
	msgs := []core.AgentMessage{
		core.WrapMessage(core.NewUserMessage("task")),
		{Message: core.Message{Role: "assistant", Content: []core.Content{
			{Type: "thinking", Text: "only thinking, no visible output"},
		}}},
		{Message: core.Message{Role: "assistant", Content: []core.Content{
			core.TextContent("real answer"),
		}}},
	}
	clean := sanitizeResumeTranscript(msgs)
	for _, m := range clean {
		if m.Role == "assistant" && len(m.Content) == 0 {
			t.Fatal("an empty assistant message survived sanitization")
		}
	}
	// user + the non-empty assistant remain.
	if len(clean) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(clean))
	}
}

func TestSanitizeResumeStartsWithUser(t *testing.T) {
	// A transcript persisted mid-conversation could start with a tool_result or
	// assistant. Replay must begin with a user message.
	msgs := []core.AgentMessage{
		{Message: core.Message{Role: "tool_result", ToolCallID: "orphan", ToolName: "read",
			Content: []core.Content{core.TextContent("leading orphan")}}},
		{Message: core.Message{Role: "assistant", Content: []core.Content{core.TextContent("stray")}}},
		core.WrapMessage(core.NewUserMessage("the real start")),
		{Message: core.Message{Role: "assistant", Content: []core.Content{core.TextContent("answer")}}},
	}
	clean := sanitizeResumeTranscript(msgs)
	if len(clean) == 0 {
		t.Fatal("expected a non-empty replayable prefix")
	}
	if clean[0].Role != "user" {
		t.Fatalf("replay must start with a user message, got %q", clean[0].Role)
	}
	if len(clean) != 2 {
		t.Fatalf("expected user + assistant, got %d", len(clean))
	}
}

func TestSanitizeResumePartialToolCallsInOneAssistant(t *testing.T) {
	// One assistant emits two tool_calls but only one got a result — the whole
	// turn is unreplayable and must be trimmed.
	msgs := []core.AgentMessage{
		core.WrapMessage(core.NewUserMessage("task")),
		{Message: core.Message{Role: "assistant", Content: []core.Content{core.TextContent("ok")}}},
		{Message: core.Message{Role: "assistant", Content: []core.Content{
			core.ToolCallContent("a", "read", nil),
			core.ToolCallContent("b", "read", nil),
		}}},
		{Message: core.Message{Role: "tool_result", ToolCallID: "a", ToolName: "read",
			Content: []core.Content{core.TextContent("only a answered")}}},
	}
	clean := sanitizeResumeTranscript(msgs)
	// The partial-tool-call turn and its lone result are dropped; the earlier
	// clean assistant text turn remains.
	if len(clean) != 2 {
		t.Fatalf("expected 2 messages after trimming partial turn, got %d", len(clean))
	}
	last := clean[len(clean)-1]
	if last.Role != "assistant" || len(last.Content) == 0 || last.Content[0].Type != "text" {
		t.Fatalf("expected transcript to end at the clean assistant text turn, got %+v", last)
	}
}

func TestSubagentResumeReplaysHistory(t *testing.T) {
	prior := []core.AgentMessage{
		core.WrapMessage(core.NewUserMessage("first task")),
		{Message: core.Message{Role: "assistant", Content: []core.Content{core.TextContent("first answer")}}},
	}
	provider := newMockProvider(func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		// The resumed run must include the prior history plus the new task.
		if len(req.Messages) < 3 {
			return nil, fmt.Errorf("expected replayed history + new task, got %d messages", len(req.Messages))
		}
		return textResponse("resumed answer")(ctx, req)
	})
	sub, _, _ := newSubagentTools(t, Config{
		DefaultModel:    core.Model{ID: "default", Provider: "mock"},
		ProviderFactory: func(core.Model) (core.Provider, error) { return provider, nil },
		TranscriptLoader: func(jobID string) ([]core.AgentMessage, error) {
			if jobID != "job-1" {
				return nil, fmt.Errorf("unknown job %q", jobID)
			}
			return prior, nil
		},
	})

	res, err := sub.Execute(context.Background(), map[string]any{"task": "continue", "resume": "job-1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := textOf(res); got != "resumed answer" {
		t.Fatalf("expected resumed answer, got %q", got)
	}
}

// onlyJobID returns the id of the single job currently tracked in s. Fails
// the test if there isn't exactly one.
func onlyJobID(t *testing.T, s *jobStore) string {
	t.Helper()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.jobs) != 1 {
		t.Fatalf("expected exactly 1 job, got %d", len(s.jobs))
	}
	for id := range s.jobs {
		return id
	}
	return ""
}

func TestSyncSubagentPromotedDeliversViaAsyncLane(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	provider := newMockProvider(gateResponse(started, release, "promoted result"))
	type startEvent struct {
		thinking string
		async    bool
	}
	startEvents := make(chan startEvent, 2)

	var (
		mu          sync.Mutex
		completeID  string
		completeSt  string
		completeRes string
		completeN   int
	)
	sub, statusTool, _, jobs := newSubagentToolsWithStore(t, Config{
		DefaultModel:    core.Model{ID: "default", Provider: "mock"},
		ProviderFactory: func(model core.Model) (core.Provider, error) { return provider, nil },
		AppCtx:          context.Background(),
		OnChildStart: func(_ string, _ string, _ string, thinking string, async bool, _ time.Time, _ int) {
			startEvents <- startEvent{thinking: thinking, async: async}
		},
		OnAsyncComplete: func(jobID, task, status, resultTail string, truncated bool) {
			mu.Lock()
			completeID = jobID
			completeSt = status
			completeRes = resultTail
			completeN++
			mu.Unlock()
		},
	})

	resultCh := make(chan core.Result, 1)
	go func() {
		res, _ := sub.Execute(context.Background(), map[string]any{"task": "do it", "thinking": "high"}, nil)
		resultCh <- res
	}()

	<-started
	jobID := onlyJobID(t, jobs)
	if err := jobs.promote(jobID); err != nil {
		t.Fatalf("promote() error = %v", err)
	}

	// The parent must unblock immediately with a "promoted" message, not the
	// child's eventual result.
	var res core.Result
	select {
	case res = <-resultCh:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for parent to unblock after promotion")
	}
	if !strings.Contains(textOf(res), "promoted to background") {
		t.Fatalf("expected parent result to mention promotion, got %q", textOf(res))
	}
	if strings.Contains(textOf(res), "promoted result") {
		t.Fatal("parent result must not carry the child's eventual output")
	}
	for _, wantAsync := range []bool{false, true} {
		select {
		case event := <-startEvents:
			if event.thinking != "high" || event.async != wantAsync {
				t.Fatalf("OnChildStart = %+v, want thinking high and async=%v", event, wantAsync)
			}
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for OnChildStart async=%v", wantAsync)
		}
	}

	// Now let the child finish; its result must arrive via OnAsyncComplete,
	// not by any other channel.
	close(release)
	waitFor(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return completeN > 0
	})

	mu.Lock()
	defer mu.Unlock()
	if completeN != 1 {
		t.Fatalf("OnAsyncComplete called %d times, want 1", completeN)
	}
	if completeID != jobID {
		t.Fatalf("OnAsyncComplete jobID = %q, want %q", completeID, jobID)
	}
	if completeSt != statusCompleted {
		t.Fatalf("OnAsyncComplete status = %q, want %q", completeSt, statusCompleted)
	}
	if completeRes != "promoted result" {
		t.Fatalf("OnAsyncComplete result = %q, want %q", completeRes, "promoted result")
	}

	// subagent_status must also reflect completion, confirming the job store
	// itself has the final result (not just the callback).
	statusRes, err := statusTool.Execute(context.Background(), map[string]any{"job_id": jobID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(textOf(statusRes), "Status: completed") {
		t.Fatalf("expected completed status, got %q", textOf(statusRes))
	}
}

func TestSyncSubagentSurvivesParentCtxCancellationAfterPromotion(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	provider := newMockProvider(gateResponse(started, release, "survived"))

	completed := make(chan struct{})
	sub, statusTool, _, jobs := newSubagentToolsWithStore(t, Config{
		DefaultModel:    core.Model{ID: "default", Provider: "mock"},
		ProviderFactory: func(model core.Model) (core.Provider, error) { return provider, nil },
		AppCtx:          context.Background(),
		OnAsyncComplete: func(jobID, task, status, resultTail string, truncated bool) {
			close(completed)
		},
	})

	parentCtx, cancelParent := context.WithCancel(context.Background())
	resultCh := make(chan core.Result, 1)
	go func() {
		res, _ := sub.Execute(parentCtx, map[string]any{"task": "do it"}, nil)
		resultCh <- res
	}()

	<-started
	jobID := onlyJobID(t, jobs)
	if err := jobs.promote(jobID); err != nil {
		t.Fatalf("promote() error = %v", err)
	}
	<-resultCh // parent unblocked by promotion

	// Cancelling the parent's own context after promotion must NOT kill the
	// (now decoupled) child.
	cancelParent()

	statusRes, err := statusTool.Execute(context.Background(), map[string]any{"job_id": jobID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(textOf(statusRes), "Status: running") {
		t.Fatalf("expected job to survive parent ctx cancellation, got %q", textOf(statusRes))
	}

	close(release)
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for child to complete after parent ctx cancellation")
	}
	waitFor(t, time.Second, func() bool {
		res, _ := statusTool.Execute(context.Background(), map[string]any{"job_id": jobID}, nil)
		return strings.Contains(textOf(res), "Status: completed")
	})
}

// TestSyncSubagentPromoteRacesParentCtxCancel stresses the linker's decision
// when promotion and the parent's context cancellation happen concurrently:
// once promoted, the child must never be killed by the parent tool call
// returning (which cancels the parent ctx). Runs many iterations to shake out
// the pseudo-random select ordering the linker must guard against.
func TestSyncSubagentPromoteRacesParentCtxCancel(t *testing.T) {
	for i := 0; i < 50; i++ {
		started := make(chan struct{})
		release := make(chan struct{})
		provider := newMockProvider(gateResponse(started, release, "survived"))

		completed := make(chan string, 1)
		sub, _, _, jobs := newSubagentToolsWithStore(t, Config{
			DefaultModel:    core.Model{ID: "default", Provider: "mock"},
			ProviderFactory: func(model core.Model) (core.Provider, error) { return provider, nil },
			AppCtx:          context.Background(),
			OnAsyncComplete: func(jobID, task, status, resultTail string, truncated bool) {
				completed <- status
			},
		})

		parentCtx, cancelParent := context.WithCancel(context.Background())
		resultCh := make(chan core.Result, 1)
		go func() {
			res, _ := sub.Execute(parentCtx, map[string]any{"task": "do it"}, nil)
			resultCh <- res
		}()

		<-started
		jobID := onlyJobID(t, jobs)
		if err := jobs.promote(jobID); err != nil {
			t.Fatalf("iter %d: promote() error = %v", i, err)
		}
		// Cancel the parent ctx as close to the promotion as possible — this
		// is what the parent tool call returning does.
		cancelParent()
		<-resultCh

		// Let the child run to completion; it must NOT have been cancelled.
		close(release)
		select {
		case st := <-completed:
			if st != statusCompleted {
				t.Fatalf("iter %d: child finished with status %q, want completed (promotion should have decoupled it from the parent ctx)", i, st)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("iter %d: timeout — promoted child likely killed by parent ctx cancellation", i)
		}
	}
}

func TestSyncSubagentPromoteVsFinishRaceDeliversExactlyOnce(t *testing.T) {
	for i := 0; i < 50; i++ {
		provider := newMockProvider(textResponse("race result"))

		var (
			mu        sync.Mutex
			completeN int
		)
		sub, _, _, jobs := newSubagentToolsWithStore(t, Config{
			DefaultModel:    core.Model{ID: "default", Provider: "mock"},
			ProviderFactory: func(model core.Model) (core.Provider, error) { return provider, nil },
			AppCtx:          context.Background(),
			OnAsyncComplete: func(jobID, task, status, resultTail string, truncated bool) {
				mu.Lock()
				completeN++
				mu.Unlock()
			},
		})

		resultCh := make(chan core.Result, 1)
		go func() {
			res, _ := sub.Execute(context.Background(), map[string]any{"task": "do it"}, nil)
			resultCh <- res
		}()

		// Race a promote() call against the child finishing on its own: spin
		// until the job shows up in the store, then promote it immediately.
		// The provider responds near-instantly, so this genuinely races
		// jobStore.promote (guarded by j.mu) against setCompleted (also
		// guarded by j.mu) — exactly the race awaitSyncResult/runJob must
		// linearize into a single delivery lane.
		go func() {
			for {
				jobs.mu.RLock()
				n := len(jobs.jobs)
				var id string
				for k := range jobs.jobs {
					id = k
				}
				jobs.mu.RUnlock()
				if n == 1 {
					_ = jobs.promote(id)
					return
				}
			}
		}()

		res := <-resultCh

		// Poll briefly: if promote won the race, OnAsyncComplete fires
		// asynchronously from runJob's own goroutine and may not have run yet.
		promoted := strings.Contains(textOf(res), "promoted to background")
		if promoted {
			waitFor(t, time.Second, func() bool {
				mu.Lock()
				defer mu.Unlock()
				return completeN == 1
			})
		}

		mu.Lock()
		n := completeN
		mu.Unlock()

		if promoted && n != 1 {
			t.Fatalf("iteration %d: promoted parent result but OnAsyncComplete called %d times, want 1", i, n)
		}
		if !promoted && n != 0 {
			t.Fatalf("iteration %d: non-promoted parent result but OnAsyncComplete called %d times, want 0", i, n)
		}
	}
}

// TestSubagentWaitReturnsResult verifies subagent_wait blocks until an async
// job finishes and returns its completed status.
func TestSubagentWaitReturnsResult(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	provider := newMockProvider(gateResponse(started, release, "child result"))

	reg := core.NewRegistry()
	cfg := Config{
		DefaultModel:    core.Model{ID: "default", Provider: "mock"},
		ProviderFactory: func(model core.Model) (core.Provider, error) { return provider, nil },
		AppCtx:          context.Background(),
	}
	cfg.ParentTools = reg
	jobs := newJobStore()
	sub := newSubagent(cfg, jobs)
	wait := newSubagentWait(jobs)

	res, err := sub.Execute(context.Background(), map[string]any{"task": "t", "async": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	jobID := jobIDFromResult(t, res)
	<-started

	done := make(chan string, 1)
	go func() {
		r, _ := wait.Execute(context.Background(), map[string]any{"job_id": jobID}, nil)
		done <- textOf(r)
	}()
	time.Sleep(20 * time.Millisecond)
	close(release)

	select {
	case out := <-done:
		if !strings.Contains(out, "Status: completed") || !strings.Contains(out, "child result") {
			t.Fatalf("subagent_wait result = %q", out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subagent_wait did not return")
	}
}

// TestSubagentWaitSuppressesAsyncComplete verifies that when a subagent_wait is
// blocked on a job, OnAsyncComplete is NOT fired (the waiter consumes the
// result — single delivery lane).
func TestSubagentWaitSuppressesAsyncComplete(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	provider := newMockProvider(gateResponse(started, release, "child result"))

	var mu sync.Mutex
	completeN := 0
	reg := core.NewRegistry()
	cfg := Config{
		DefaultModel:    core.Model{ID: "default", Provider: "mock"},
		ProviderFactory: func(model core.Model) (core.Provider, error) { return provider, nil },
		AppCtx:          context.Background(),
		OnAsyncComplete: func(jobID, task, status, resultTail string, truncated bool) {
			mu.Lock()
			completeN++
			mu.Unlock()
		},
	}
	cfg.ParentTools = reg
	jobs := newJobStore()
	sub := newSubagent(cfg, jobs)
	wait := newSubagentWait(jobs)

	res, err := sub.Execute(context.Background(), map[string]any{"task": "t", "async": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	jobID := jobIDFromResult(t, res)
	<-started

	done := make(chan struct{})
	go func() {
		_, _ = wait.Execute(context.Background(), map[string]any{"job_id": jobID}, nil)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	close(release)
	<-done

	// Give any (erroneous) async-complete callback a chance to fire.
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if completeN != 0 {
		t.Fatalf("OnAsyncComplete fired %d times while a waiter was blocked, want 0", completeN)
	}
	j, ok := jobs.get(jobID)
	if !ok {
		t.Fatal("job disappeared")
	}
	j.mu.Lock()
	claimed := j.resultClaimed
	j.mu.Unlock()
	if !claimed {
		t.Fatal("blocked waiter did not claim terminal result")
	}
}

// TestSubagentWaitNoWaiterStillNotifies verifies OnAsyncComplete DOES fire
// when no subagent_wait is blocked (normal async reinjection path).
func TestSubagentWaitNoWaiterStillNotifies(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	provider := newMockProvider(gateResponse(started, release, "child result"))

	var mu sync.Mutex
	completeN := 0
	reg := core.NewRegistry()
	cfg := Config{
		DefaultModel:    core.Model{ID: "default", Provider: "mock"},
		ProviderFactory: func(model core.Model) (core.Provider, error) { return provider, nil },
		AppCtx:          context.Background(),
		OnAsyncComplete: func(jobID, task, status, resultTail string, truncated bool) {
			mu.Lock()
			completeN++
			mu.Unlock()
		},
	}
	cfg.ParentTools = reg
	jobs := newJobStore()
	sub := newSubagent(cfg, jobs)

	res, err := sub.Execute(context.Background(), map[string]any{"task": "t", "async": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	jobID := jobIDFromResult(t, res)
	<-started
	close(release)

	waitFor(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return completeN == 1
	})

	// Completion won the mutex before this fast-path wait, and delivered the
	// full result via OnAsyncComplete. The wait must report delivered=false so
	// the subagent_wait tool returns a brief ack instead of re-dumping the same
	// result the model already saw.
	result, delivered, err := jobs.wait(context.Background(), jobID, time.Second)
	if err != nil {
		t.Fatalf("wait result = %v", err)
	}
	if result.Status != statusCompleted || result.Result != "child result" {
		t.Fatalf("fast-path wait = %+v", result)
	}
	if delivered {
		t.Fatal("fast-path wait after async notification must report delivered=false")
	}
	mu.Lock()
	gotCompleteN := completeN
	mu.Unlock()
	if gotCompleteN != 1 {
		t.Fatalf("OnAsyncComplete called %d times after fast-path wait, want 1", gotCompleteN)
	}
}

// TestSubagentWaitFastPathToolReturnsAck verifies the subagent_wait TOOL emits
// a brief acknowledgment (not a re-dump of the result) when the async
// notification already delivered the result to the conversation.
func TestSubagentWaitFastPathToolReturnsAck(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	provider := newMockProvider(gateResponse(started, release, "child result"))

	var mu sync.Mutex
	completeN := 0
	reg := core.NewRegistry()
	cfg := Config{
		DefaultModel:    core.Model{ID: "default", Provider: "mock"},
		ProviderFactory: func(model core.Model) (core.Provider, error) { return provider, nil },
		AppCtx:          context.Background(),
		OnAsyncComplete: func(jobID, task, status, resultTail string, truncated bool) {
			mu.Lock()
			completeN++
			mu.Unlock()
		},
	}
	cfg.ParentTools = reg
	jobs := newJobStore()
	sub := newSubagent(cfg, jobs)
	wait := newSubagentWait(jobs)

	res, err := sub.Execute(context.Background(), map[string]any{"task": "t", "async": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	jobID := jobIDFromResult(t, res)
	<-started
	close(release)
	waitFor(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return completeN == 1
	})

	waitRes, err := wait.Execute(context.Background(), map[string]any{"job_id": jobID}, nil)
	if err != nil || waitRes.IsError {
		t.Fatalf("subagent_wait execute = %+v %v", waitRes, err)
	}
	text := textOf(waitRes)
	if !strings.Contains(text, "already finished") || strings.Contains(text, "child result") {
		t.Fatalf("expected brief ack without result re-dump, got %q", text)
	}
}

func TestSubagentWaitUnknownJob(t *testing.T) {
	jobs := newJobStore()
	wait := newSubagentWait(jobs)
	res, err := wait.Execute(context.Background(), map[string]any{"job_id": "sa-nope"}, nil)
	if err != nil || !res.IsError || !strings.Contains(textOf(res), "unknown job ID") {
		t.Fatalf("expected unknown-job error, got %+v %v", res, err)
	}
}
