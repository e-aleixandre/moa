package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"
)

// ToolEffect classifies a tool's side effects for the conflict-aware scheduler.
// The zero value (EffectUnknown) is treated as a barrier — safe by default.
type ToolEffect int

const (
	EffectUnknown   ToolEffect = iota // zero value — serialized (conservative)
	EffectReadOnly                    // no side effects — safe to parallelize
	EffectWritePath                   // writes to a specific path via LockKey
	EffectShell                       // may write anywhere — acts as barrier
)

// Tool is a callable function with JSON Schema parameters.
type Tool struct {
	Name        string          `json:"name"`
	Label       string          `json:"label"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Execute     ExecuteFunc     `json:"-"`
	Effect      ToolEffect      `json:"-"` // scheduling hint for conflict-aware execution
	LockKey     LockKeyFunc     `json:"-"` // required when Effect is EffectWritePath
}

// LockKeyFunc returns a canonical path used as a lock key for scheduling.
// Returns empty string on failure, which causes fallback to shell scheduling.
type LockKeyFunc func(args map[string]any) string

// Spec returns a ToolSpec (definition without the execute function).
func (t Tool) Spec() ToolSpec {
	return ToolSpec{
		Name:        t.Name,
		Description: t.Description,
		Parameters:  t.Parameters,
	}
}

// ExecuteFunc runs a tool.
// onUpdate streams partial results (e.g., bash stdout lines).
type ExecuteFunc func(ctx context.Context, params map[string]any, onUpdate func(Result)) (Result, error)

// Result is what a tool returns to the LLM.
// Uses the same Content type as messages — no duplication.
type Result struct {
	Content []Content `json:"content"`
	IsError bool      `json:"is_error,omitempty"`
}

// TextResult creates a Result with a single text content block.
func TextResult(text string) Result {
	return Result{Content: []Content{TextContent(text)}}
}

// ErrorResult creates a Result representing an error message.
// Sets IsError=true so the agent loop can detect tool-level errors
// even when the tool returns (Result, nil) instead of (Result, error).
func ErrorResult(msg string) Result {
	return Result{Content: []Content{TextContent("Error: " + msg)}, IsError: true}
}

const (
	// ToolCallDecisionKindPermission marks user-facing permission denials.
	ToolCallDecisionKindPermission = "permission"
	// ToolCallDecisionKindPolicy marks non-permission policy/plan blocks.
	ToolCallDecisionKindPolicy = "policy"
)

// ToolCallDecision is returned by tool-call hooks to optionally block execution.
type ToolCallDecision struct {
	Block  bool
	Reason string
	Kind   string // optional classification (e.g. permission, policy)
}

// Registry holds registered tools. Thread-safe.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds or replaces a tool.
// Returns error if a WritePath tool is missing its LockKey function.
func (r *Registry) Register(t Tool) error {
	if t.Effect == EffectWritePath && t.LockKey == nil {
		return fmt.Errorf("tool %s: EffectWritePath requires LockKey", t.Name)
	}
	r.mu.Lock()
	r.tools[t.Name] = t
	r.mu.Unlock()
	return nil
}

// RegisterOrLog registers a tool and logs a warning on failure.
// Use for dynamic tool sources (MCP, extensions, plan mode) where
// a registration error shouldn't abort the caller.
func RegisterOrLog(reg *Registry, t Tool) {
	if err := reg.Register(t); err != nil {
		slog.Warn("tool registration failed", "tool", t.Name, "error", err)
	}
}

// Unregister removes a tool.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	delete(r.tools, name)
	r.mu.Unlock()
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock()
	return t, ok
}

// All returns all registered tools (snapshot), sorted by name for deterministic order.
func (r *Registry) All() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		result = append(result, t)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// Specs returns ToolSpecs for all registered tools (for sending to LLM), sorted by name.
func (r *Registry) Specs() []ToolSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]ToolSpec, 0, len(r.tools))
	for _, t := range r.tools {
		result = append(result, t.Spec())
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// Count returns the number of registered tools.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}
