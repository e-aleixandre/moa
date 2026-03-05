package extension

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/ealeixandre/go-agent/pkg/core"
)

// Deadlines for blocking hooks.
const (
	shortDeadline   = 200 * time.Millisecond
	contextDeadline = 500 * time.Millisecond
)

// Host manages loaded extensions and dispatches hooks with deadlines and panic recovery.
type Host struct {
	beforeAgentStart []BeforeAgentStartHook
	toolCall         []ToolCallHook
	toolResult       []ToolResultHook
	contextHooks     []ContextHook
	observers        map[string][]ObserverHook
	tools            *core.Registry
	logger           *slog.Logger
	mu               sync.RWMutex
}

// NewHost creates an extension host.
func NewHost(tools *core.Registry, logger *slog.Logger) *Host {
	if logger == nil {
		logger = slog.Default()
	}
	return &Host{
		observers: make(map[string][]ObserverHook),
		tools:     tools,
		logger:    logger,
	}
}

// Load initializes an extension, giving it access to register hooks and tools.
func (h *Host) Load(ext Extension) error {
	api := &hostAPI{host: h}
	return ext.Init(api)
}

// FireBeforeAgentStart calls all before_agent_start hooks, collects injected messages.
// Each hook runs with a deadline. Panics are recovered and logged.
func (h *Host) FireBeforeAgentStart(ctx context.Context) []core.AgentMessage {
	h.mu.RLock()
	hooks := h.beforeAgentStart
	h.mu.RUnlock()

	var all []core.AgentMessage
	for _, fn := range hooks {
		msgs := h.runBeforeAgentStart(ctx, fn)
		all = append(all, msgs...)
	}
	return all
}

// FireToolCall runs tool_call hooks. Returns first blocking decision, or nil.
func (h *Host) FireToolCall(ctx context.Context, name string, args map[string]any) *ToolCallDecision {
	h.mu.RLock()
	hooks := h.toolCall
	h.mu.RUnlock()

	for _, fn := range hooks {
		decision := h.runToolCall(ctx, fn, name, args)
		if decision != nil && decision.Block {
			return decision
		}
	}
	return nil
}

// FireToolResult runs tool_result hooks. Returns modified result (or original).
func (h *Host) FireToolResult(ctx context.Context, name string, result core.Result, isError bool) core.Result {
	h.mu.RLock()
	hooks := h.toolResult
	h.mu.RUnlock()

	current := result
	for _, fn := range hooks {
		modified := h.runToolResult(ctx, fn, name, current, isError)
		if modified != nil {
			current = *modified
		}
	}
	return current
}

// FireContext runs context hooks in order. Each receives and returns the message list.
func (h *Host) FireContext(ctx context.Context, msgs []core.AgentMessage) []core.AgentMessage {
	h.mu.RLock()
	hooks := h.contextHooks
	h.mu.RUnlock()

	current := msgs
	for _, fn := range hooks {
		modified := h.runContext(ctx, fn, current)
		if modified != nil {
			current = modified
		}
	}
	return current
}

// FireObserver dispatches async observer hooks. Does not block.
// Each handler runs in its own goroutine with panic recovery.
func (h *Host) FireObserver(event core.AgentEvent) {
	h.mu.RLock()
	hooks := h.observers[event.Type]
	h.mu.RUnlock()

	for _, fn := range hooks {
		fn := fn
		go func() {
			defer func() {
				if r := recover(); r != nil {
					h.logger.Error("observer hook panic", "event", event.Type, "error", r)
				}
			}()
			fn(context.Background(), event)
		}()
	}
}

// --- Internal: run hooks with deadline + recovery ---

// runWithTimeout executes fn in a goroutine with a deadline.
// If fn panics, recovers and returns zero value.
// If fn doesn't return before deadline, returns zero value and logs warning.
// Note: if fn ignores context and blocks forever, the goroutine leaks.
// Go has no mechanism to kill goroutines; this is a documented limitation.
func runWithTimeout[T any](ctx context.Context, deadline time.Duration, logger *slog.Logger, label string, fn func(context.Context) T) T {
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	ch := make(chan T, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("hook panic", "hook", label, "error", r)
				var zero T
				ch <- zero
			}
		}()
		ch <- fn(ctx)
	}()

	select {
	case result := <-ch:
		return result
	case <-ctx.Done():
		logger.Warn("hook timed out", "hook", label)
		var zero T
		return zero
	}
}

func (h *Host) runBeforeAgentStart(ctx context.Context, fn BeforeAgentStartHook) []core.AgentMessage {
	return runWithTimeout(ctx, shortDeadline, h.logger, "before_agent_start", func(ctx context.Context) []core.AgentMessage {
		msgs, err := fn(ctx)
		if err != nil {
			h.logger.Error("before_agent_start hook error", "error", err)
			return nil
		}
		return msgs
	})
}

func (h *Host) runToolCall(ctx context.Context, fn ToolCallHook, name string, args map[string]any) *ToolCallDecision {
	return runWithTimeout(ctx, shortDeadline, h.logger, "tool_call:"+name, func(ctx context.Context) *ToolCallDecision {
		return fn(ctx, name, args)
	})
}

func (h *Host) runToolResult(ctx context.Context, fn ToolResultHook, name string, res core.Result, isError bool) *core.Result {
	return runWithTimeout(ctx, shortDeadline, h.logger, "tool_result:"+name, func(ctx context.Context) *core.Result {
		modified, err := fn(ctx, name, res, isError)
		if err != nil {
			h.logger.Error("tool_result hook error", "tool", name, "error", err)
			return nil
		}
		return modified
	})
}

func (h *Host) runContext(ctx context.Context, fn ContextHook, msgs []core.AgentMessage) []core.AgentMessage {
	return runWithTimeout(ctx, contextDeadline, h.logger, "context", func(ctx context.Context) []core.AgentMessage {
		modified, err := fn(ctx, msgs)
		if err != nil {
			h.logger.Error("context hook error", "error", err)
			return nil
		}
		return modified
	})
}

// --- hostAPI implements API for extensions ---

type hostAPI struct {
	host *Host
}

func (a *hostAPI) OnBeforeAgentStart(fn BeforeAgentStartHook) {
	a.host.mu.Lock()
	a.host.beforeAgentStart = append(a.host.beforeAgentStart, fn)
	a.host.mu.Unlock()
}

func (a *hostAPI) OnAgentEnd(fn ObserverHook) {
	a.host.mu.Lock()
	a.host.observers[core.AgentEventEnd] = append(a.host.observers[core.AgentEventEnd], fn)
	a.host.mu.Unlock()
}

func (a *hostAPI) OnTurnStart(fn ObserverHook) {
	a.host.mu.Lock()
	a.host.observers[core.AgentEventTurnStart] = append(a.host.observers[core.AgentEventTurnStart], fn)
	a.host.mu.Unlock()
}

func (a *hostAPI) OnTurnEnd(fn ObserverHook) {
	a.host.mu.Lock()
	a.host.observers[core.AgentEventTurnEnd] = append(a.host.observers[core.AgentEventTurnEnd], fn)
	a.host.mu.Unlock()
}

func (a *hostAPI) OnToolCall(fn ToolCallHook) {
	a.host.mu.Lock()
	a.host.toolCall = append(a.host.toolCall, fn)
	a.host.mu.Unlock()
}

func (a *hostAPI) OnToolResult(fn ToolResultHook) {
	a.host.mu.Lock()
	a.host.toolResult = append(a.host.toolResult, fn)
	a.host.mu.Unlock()
}

func (a *hostAPI) OnContext(fn ContextHook) {
	a.host.mu.Lock()
	a.host.contextHooks = append(a.host.contextHooks, fn)
	a.host.mu.Unlock()
}

func (a *hostAPI) RegisterTool(t core.Tool) {
	a.host.tools.Register(t)
}

func (a *hostAPI) UnregisterTool(name string) {
	a.host.tools.Unregister(name)
}

func (a *hostAPI) Logger() *slog.Logger {
	return a.host.logger
}

// ensure hostAPI implements API
var _ API = (*hostAPI)(nil)
