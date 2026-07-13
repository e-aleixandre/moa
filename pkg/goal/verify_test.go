package goal

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

func TestParseVerdict(t *testing.T) {
	tests := []struct {
		name          string
		in            string
		wantSatisfied bool
		wantFeedback  string
	}{
		{"clean json satisfied", `{"satisfied": true, "feedback": "all tests pass"}`, true, "all tests pass"},
		{"clean json not satisfied", `{"satisfied": false, "feedback": "3 tests still failing"}`, false, "3 tests still failing"},
		{"fenced json", "```json\n{\"satisfied\": true, \"feedback\": \"ok\"}\n```", true, "ok"},
		{"prose wrapped", "Here is my verdict:\n{\"satisfied\": false, \"feedback\": \"missing docs\"}\nThanks", false, "missing docs"},
		{"unparseable falls back to not-satisfied", "I think it looks good honestly", false, "I think it looks good honestly"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := parseVerdict(tt.in)
			if v.Satisfied != tt.wantSatisfied {
				t.Errorf("satisfied: got %v want %v", v.Satisfied, tt.wantSatisfied)
			}
			if v.Feedback != tt.wantFeedback {
				t.Errorf("feedback: got %q want %q", v.Feedback, tt.wantFeedback)
			}
		})
	}
}

// scriptedProvider plays a fixed sequence of responses, one per Stream call.
// A step either streams a text message or emits a single tool call.
type scriptStep struct {
	text     string
	toolName string
	toolArgs map[string]any
}

type scriptedProvider struct {
	mu       sync.Mutex
	steps    []scriptStep
	call     int
	requests []core.Request // captured requests, for assertions
	err      error          // if set, Stream returns this error on every call
}

func (p *scriptedProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	p.mu.Lock()
	if p.err != nil {
		p.mu.Unlock()
		return nil, p.err
	}
	p.requests = append(p.requests, req)
	idx := p.call
	p.call++
	// Clamp to the last step so a runaway loop keeps getting the final step
	// (used to exercise MaxTurns exhaustion with a repeating tool call).
	if idx >= len(p.steps) {
		idx = len(p.steps) - 1
	}
	step := p.steps[idx]
	p.mu.Unlock()

	ch := make(chan core.AssistantEvent, 4)
	go func() {
		defer close(ch)
		var msg core.Message
		if step.toolName != "" {
			msg = core.Message{
				Role: "assistant",
				Content: []core.Content{
					core.ToolCallContent(fmt.Sprintf("call_%d", idx), step.toolName, step.toolArgs),
				},
				StopReason: "tool_use",
				Timestamp:  time.Now().Unix(),
				Usage:      &core.Usage{Input: 10, Output: 5, TotalTokens: 15},
			}
		} else {
			msg = core.Message{
				Role:       "assistant",
				Content:    []core.Content{core.TextContent(step.text)},
				StopReason: "end_turn",
				Timestamp:  time.Now().Unix(),
				Usage:      &core.Usage{Input: 10, Output: 5, TotalTokens: 15},
			}
		}
		ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
		if step.toolName == "" {
			ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: step.text}
		}
		ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
	}()
	return ch, nil
}

func baseCfg(factory ProviderFactory, objective string) VerifyConfig {
	return VerifyConfig{
		Factory:   factory,
		Objective: objective,
		Evidence:  "git status: clean",
	}
}

func TestVerify_NilFactory(t *testing.T) {
	if _, _, err := Verify(context.Background(), baseCfg(nil, "obj")); err == nil {
		t.Fatal("Verify should error on nil factory")
	}
}

func TestVerify_UnknownModel(t *testing.T) {
	factory := func(core.Model) (core.Provider, error) { return &scriptedProvider{}, nil }
	cfg := baseCfg(factory, "obj")
	cfg.VerifierSpec = "no-such-model-xyz"
	if _, _, err := Verify(context.Background(), cfg); err == nil {
		t.Fatal("Verify should error when the verifier model can't be resolved")
	}
}

func TestVerify_ProviderFactoryError(t *testing.T) {
	factory := func(core.Model) (core.Provider, error) { return nil, fmt.Errorf("boom") }
	if _, _, err := Verify(context.Background(), baseCfg(factory, "obj")); err == nil {
		t.Fatal("Verify should propagate provider-factory errors")
	}
}

// TestVerify_ReadsPlanThenVerdict exercises the agentic path: the verifier
// reads a plan file with a tool, then returns a clean verdict.
func TestVerify_ReadsPlanThenVerdict(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "PLAN.md")
	if err := os.WriteFile(planPath, []byte("Phase 1: add feature X\nPhase 2: tests"), 0o644); err != nil {
		t.Fatal(err)
	}

	prov := &scriptedProvider{steps: []scriptStep{
		{toolName: "read", toolArgs: map[string]any{"path": planPath}},
		{text: `{"satisfied": true, "feedback": "both phases present"}`},
	}}
	factory := func(core.Model) (core.Provider, error) { return prov, nil }

	cfg := baseCfg(factory, "implement the plan in PLAN.md")
	cfg.WorkDir = dir
	cfg.StatePath = filepath.Join(dir, "STATE.md")

	v, stats, err := Verify(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if !v.Satisfied || v.Feedback != "both phases present" {
		t.Fatalf("unexpected verdict: %+v", v)
	}
	if prov.call != 2 {
		t.Fatalf("expected 2 provider calls (tool + verdict), got %d", prov.call)
	}
	if stats.Turns != 2 {
		t.Fatalf("expected 2 assistant turns in stats, got %d", stats.Turns)
	}
	// The read tool must have been offered to the model.
	if len(prov.requests) == 0 || !hasTool(prov.requests[0], "read") {
		t.Fatal("expected the read tool to be available to the verifier")
	}
	if hasTool(prov.requests[0], "edit") || hasTool(prov.requests[0], "bash") {
		t.Fatal("verifier must not have write/exec tools")
	}
}

// TestVerify_StatePathInPrompt checks the state file path reaches the model.
func TestVerify_StatePathInPrompt(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "STATE.md")
	prov := &scriptedProvider{steps: []scriptStep{
		{text: `{"satisfied": false, "feedback": "not done"}`},
	}}
	factory := func(core.Model) (core.Provider, error) { return prov, nil }
	cfg := baseCfg(factory, "obj")
	cfg.WorkDir = dir
	cfg.StatePath = statePath

	if _, _, err := Verify(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if len(prov.requests) == 0 {
		t.Fatal("no request captured")
	}
	userText := lastUserText(prov.requests[0])
	if !strings.Contains(userText, statePath) {
		t.Fatalf("prompt should mention the state path %q, got:\n%s", statePath, userText)
	}
}

// TestVerify_MaxTurnsExhausted: a verifier that only ever calls tools runs out
// of turns and yields a not-satisfied verdict (not an error). Distinct tool
// calls avoid the doom-loop guard so we exercise the max-turns cap itself.
func TestVerify_MaxTurnsExhausted(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		if err := os.WriteFile(filepath.Join(dir, n+".txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prov := &scriptedProvider{steps: []scriptStep{
		{toolName: "read", toolArgs: map[string]any{"path": filepath.Join(dir, "a.txt")}},
		{toolName: "read", toolArgs: map[string]any{"path": filepath.Join(dir, "b.txt")}},
		{toolName: "read", toolArgs: map[string]any{"path": filepath.Join(dir, "c.txt")}},
		{toolName: "read", toolArgs: map[string]any{"path": filepath.Join(dir, "d.txt")}},
		{toolName: "read", toolArgs: map[string]any{"path": filepath.Join(dir, "e.txt")}},
		{toolName: "read", toolArgs: map[string]any{"path": filepath.Join(dir, "f.txt")}},
		{toolName: "read", toolArgs: map[string]any{"path": filepath.Join(dir, "g.txt")}},
		{toolName: "read", toolArgs: map[string]any{"path": filepath.Join(dir, "h.txt")}},
	}}
	factory := func(core.Model) (core.Provider, error) { return prov, nil }
	cfg := baseCfg(factory, "obj")
	cfg.WorkDir = dir
	cfg.MaxTurns = 3

	v, _, err := Verify(context.Background(), cfg)
	if err != nil {
		t.Fatalf("exhausting turns must not error, got %v", err)
	}
	if v.Satisfied {
		t.Fatal("a capped verifier must return not-satisfied")
	}
	if !strings.Contains(strings.ToLower(v.Feedback), "turn") {
		t.Fatalf("feedback should mention running out of turns, got %q", v.Feedback)
	}
}

// TestVerify_RepromptForCleanJSON: first text isn't JSON, the reprompt yields it.
func TestVerify_RepromptForCleanJSON(t *testing.T) {
	dir := t.TempDir()
	prov := &scriptedProvider{steps: []scriptStep{
		{text: "I believe the objective is complete, everything looks good."},
		{text: `{"satisfied": true, "feedback": "confirmed"}`},
	}}
	factory := func(core.Model) (core.Provider, error) { return prov, nil }
	cfg := baseCfg(factory, "obj")
	cfg.WorkDir = dir

	v, _, err := Verify(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if !v.Satisfied || v.Feedback != "confirmed" {
		t.Fatalf("expected reprompt to recover clean JSON, got %+v", v)
	}
}

// TestVerify_ConservativeFallback: no clean JSON even after reprompt → not
// satisfied, raw text as feedback, no error.
func TestVerify_ConservativeFallback(t *testing.T) {
	dir := t.TempDir()
	prov := &scriptedProvider{steps: []scriptStep{
		{text: "looks fine to me"},
		{text: "still no json here"},
	}}
	factory := func(core.Model) (core.Provider, error) { return prov, nil }
	cfg := baseCfg(factory, "obj")
	cfg.WorkDir = dir

	v, _, err := Verify(context.Background(), cfg)
	if err != nil {
		t.Fatalf("fallback must not error, got %v", err)
	}
	if v.Satisfied {
		t.Fatal("fallback must be not-satisfied")
	}
}

// TestVerify_DoomLoopIsCapped: a verifier stuck repeating the same tool call
// trips the doom-loop guard and yields a not-satisfied verdict, not an error.
func TestVerify_DoomLoopIsCapped(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	prov := &scriptedProvider{steps: []scriptStep{
		{toolName: "ls", toolArgs: map[string]any{"path": "."}}, // identical, repeats
	}}
	factory := func(core.Model) (core.Provider, error) { return prov, nil }
	cfg := baseCfg(factory, "obj")
	cfg.WorkDir = dir

	v, _, err := Verify(context.Background(), cfg)
	if err != nil {
		t.Fatalf("a doom loop must not error, got %v", err)
	}
	if v.Satisfied {
		t.Fatal("a stuck verifier must return not-satisfied")
	}
}

// TestVerify_OneShotMode uses the legacy tool-less path.
func TestVerify_OneShotMode(t *testing.T) {
	prov := &scriptedProvider{steps: []scriptStep{
		{text: `{"satisfied": true, "feedback": "one-shot ok"}`},
	}}
	factory := func(core.Model) (core.Provider, error) { return prov, nil }
	cfg := baseCfg(factory, "make build green")
	cfg.OneShot = true

	v, _, err := Verify(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Verify(one-shot) failed: %v", err)
	}
	if !v.Satisfied || v.Feedback != "one-shot ok" {
		t.Fatalf("unexpected verdict: %+v", v)
	}
	// One-shot must offer no tools.
	if len(prov.requests) == 0 || len(prov.requests[0].Tools) != 0 {
		t.Fatal("one-shot verifier must not expose tools")
	}
}

// TestVerify_OneShotRetriesTransient: one-shot retries a transient stream error.
func TestVerify_OneShotRetriesTransient(t *testing.T) {
	prov := &scriptedProvider{err: fmt.Errorf("transient blip")}
	factory := func(core.Model) (core.Provider, error) { return prov, nil }
	cfg := baseCfg(factory, "obj")
	cfg.OneShot = true

	if _, _, err := Verify(context.Background(), cfg); err == nil {
		t.Fatal("expected error after exhausting one-shot retries")
	}
	if prov.call != 0 && len(prov.requests) != 0 {
		// requests is only appended on success; with err set it stays empty.
		t.Fatalf("unexpected request capture: %d", len(prov.requests))
	}
}

func TestVerify_StatsCost(t *testing.T) {
	dir := t.TempDir()
	prov := &scriptedProvider{steps: []scriptStep{
		{text: `{"satisfied": true, "feedback": "ok"}`},
	}}
	factory := func(core.Model) (core.Provider, error) { return prov, nil }
	cfg := baseCfg(factory, "obj")
	cfg.WorkDir = dir

	_, stats, err := Verify(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Usage == nil || stats.Usage.TotalTokens == 0 {
		t.Fatalf("expected usage to be recorded, got %+v", stats.Usage)
	}
	// haiku has pricing, so a cost should be computed.
	if stats.CostUSD <= 0 {
		t.Fatalf("expected a positive cost, got %v", stats.CostUSD)
	}
}

// hasTool reports whether a request offered a tool by name.
func hasTool(req core.Request, name string) bool {
	for _, ts := range req.Tools {
		if ts.Name == name {
			return true
		}
	}
	return false
}

// lastUserText returns the text of the last user message in a request.
func lastUserText(req core.Request) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			var b strings.Builder
			for _, c := range req.Messages[i].Content {
				if c.Type == "text" {
					b.WriteString(c.Text)
				}
			}
			return b.String()
		}
	}
	return ""
}
