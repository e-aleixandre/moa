// Package permission mediates tool execution approvals between the agent
// loop and the TUI. Three modes: yolo (auto-approve all), ask (auto-approve
// reads, confirm writes), auto (AI decides, with user-provided rules).
package permission

import (
	"context"
	"sync"

	"github.com/ealeixandre/moa/pkg/core"
)

// Mode controls how tool permissions are evaluated.
type Mode string

const (
	ModeYolo Mode = "yolo" // Everything auto-approved
	ModeAsk  Mode = "ask"  // Read tools auto-approved, writes require confirmation
	ModeAuto Mode = "auto" // AI evaluator decides; falls back to ask if no evaluator
)

// Response carries the user's decision back to the agent loop.
type Response struct {
	Approved bool
	Feedback string // optional: denial reason or approval note
	Allow    string // non-empty: add this glob pattern to the allow list
}

// Request is sent to the UI when user approval is needed.
// The receiver must send exactly one value on Response.
type Request struct {
	ToolName string
	Args     map[string]any
	Response chan<- Response
}

// readOnly tools never require approval (even in ask/auto mode).
var readOnly = map[string]bool{
	"read": true, "ls": true, "grep": true, "find": true,
}

// Gate mediates tool permissions. Created once, shared between agent and TUI.
type Gate struct {
	mu        sync.RWMutex
	mode      Mode
	reqCh     chan Request
	allow     []string   // glob patterns auto-approved in ask mode
	deny      []string   // glob patterns always denied
	rules     []string   // natural language rules for auto mode
	evaluator *Evaluator // AI evaluator for auto mode (nil = fallback to ask)
}

// Config holds the gate's initial settings from merged config files.
type Config struct {
	Allow     []string   // glob patterns: "Bash(npm:*)", "edit"
	Deny      []string   // glob patterns always denied
	Rules     []string   // natural language rules for auto mode
	Evaluator *Evaluator // AI evaluator (nil in ask mode)
}

// New creates a Gate with the given mode and config.
func New(mode Mode, cfg Config) *Gate {
	return &Gate{
		mode:      mode,
		reqCh:     make(chan Request),
		allow:     cfg.Allow,
		deny:      cfg.Deny,
		rules:     cfg.Rules,
		evaluator: cfg.Evaluator,
	}
}

// Mode returns the active permission mode.
func (g *Gate) Mode() Mode {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.mode
}

// SetMode changes the permission mode at runtime.
func (g *Gate) SetMode(mode Mode) {
	g.mu.Lock()
	g.mode = mode
	g.mu.Unlock()
}

// Requests returns the channel the UI listens on for approval requests.
func (g *Gate) Requests() <-chan Request { return g.reqCh }

// Rules returns the current rule set (for AI evaluator).
func (g *Gate) Rules() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	cp := make([]string, len(g.rules))
	copy(cp, g.rules)
	return cp
}

// Allow returns the current allow patterns.
func (g *Gate) AllowPatterns() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	cp := make([]string, len(g.allow))
	copy(cp, g.allow)
	return cp
}

// AddAllow appends a glob allow pattern (for ask mode "always allow").
func (g *Gate) AddAllow(pattern string) {
	g.mu.Lock()
	g.allow = append(g.allow, pattern)
	g.mu.Unlock()
}

// SetEvaluator replaces the AI evaluator (for runtime mode switches).
func (g *Gate) SetEvaluator(e *Evaluator) {
	g.mu.Lock()
	g.evaluator = e
	g.mu.Unlock()
}

// AddRule appends a natural language rule (for auto mode).
func (g *Gate) AddRule(rule string) {
	g.mu.Lock()
	g.rules = append(g.rules, rule)
	g.mu.Unlock()
}

// Check decides whether a tool call may proceed. May block waiting for user
// approval. Returns nil to approve, or a blocking ToolCallDecision to reject.
// Called from the agent loop goroutine.
//
// ask mode: readOnly → deny globs → allow globs → ask user
// auto mode: readOnly → AI evaluator (rules) → ask user (fallback)
func (g *Gate) Check(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision {
	g.mu.RLock()
	mode := g.mode
	allow := append([]string(nil), g.allow...)
	deny := append([]string(nil), g.deny...)
	rules := append([]string(nil), g.rules...)
	evaluator := g.evaluator
	g.mu.RUnlock()

	if mode == ModeYolo {
		return nil
	}

	if readOnly[name] {
		return nil
	}

	switch mode {
	case ModeAsk:
		if matchPolicy(deny, name, args) {
			return &core.ToolCallDecision{Block: true, Reason: "denied by policy"}
		}
		if matchPolicy(allow, name, args) {
			return nil
		}

	case ModeAuto:
		if evaluator != nil {
			switch evaluator.Evaluate(ctx, name, args, rules) {
			case DecisionApprove:
				return nil
			case DecisionDeny:
				return &core.ToolCallDecision{Block: true, Reason: "denied by AI evaluator"}
			case DecisionAsk:
				// fall through to user prompt
			}
		}
	}

	return g.askUser(ctx, name, args)
}

// askUser sends a request to the UI and blocks until the user responds.
func (g *Gate) askUser(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision {
	respCh := make(chan Response, 1)

	select {
	case g.reqCh <- Request{ToolName: name, Args: args, Response: respCh}:
	case <-ctx.Done():
		return &core.ToolCallDecision{Block: true, Reason: "cancelled"}
	}

	select {
	case resp := <-respCh:
		if resp.Allow != "" {
			g.AddAllow(resp.Allow)
		}
		if resp.Approved {
			return nil
		}
		reason := "denied by user"
		if resp.Feedback != "" {
			reason = resp.Feedback
		}
		return &core.ToolCallDecision{Block: true, Reason: reason}
	case <-ctx.Done():
		return &core.ToolCallDecision{Block: true, Reason: "cancelled"}
	}
}
