package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

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
	reg := core.NewRegistry()
	for _, tool := range parentTools {
		reg.Register(tool)
	}
	cfg.ParentTools = reg
	jobs := newJobStore()
	return newSubagent(cfg, jobs), newSubagentStatus(jobs), newSubagentCancel(jobs)
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
	parent.Register(core.Tool{Name: "read"})
	parent.Register(core.Tool{Name: "subagent"})
	parent.Register(core.Tool{Name: "subagent_status"})
	parent.Register(core.Tool{Name: "subagent_cancel"})
	parent.Register(core.Tool{Name: "grep"})

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
}

func TestBuildChildRegistryRejectsEmptyAndUnknownTools(t *testing.T) {
	parent := core.NewRegistry()
	parent.Register(core.Tool{Name: "read"})

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
	})

	_, err := sub.Execute(context.Background(), map[string]any{"task": "x", "thinking": "high"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if seenThinking != "high" {
		t.Fatalf("expected thinking override high, got %q", seenThinking)
	}
}

func TestSubagentThinkingFallsBackToCurrentValue(t *testing.T) {
	var seenThinking string
	sub, _, _ := newSubagentTools(t, Config{
		DefaultModel: core.Model{ID: "default", Provider: "mock"},
		CurrentThinkingLevel: func() string {
			return "minimal"
		},
		ProviderFactory: func(model core.Model) (core.Provider, error) {
			return newMockProvider(func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
				seenThinking = req.Options.ThinkingLevel
				return textResponse("ok")(ctx, req)
			}), nil
		},
	})

	_, err := sub.Execute(context.Background(), map[string]any{"task": "x"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if seenThinking != "minimal" {
		t.Fatalf("expected fallback thinking minimal, got %q", seenThinking)
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
