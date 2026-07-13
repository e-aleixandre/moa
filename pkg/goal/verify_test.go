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

// TestVerify_ParsesJSONEmbeddedInProse: the verifier's final turn may wrap the
// JSON verdict in prose or fences; extractJSONObject recovers it without a
// dedicated reprompt (which we deliberately dropped to avoid re-granting caps).
func TestVerify_ParsesJSONEmbeddedInProse(t *testing.T) {
	dir := t.TempDir()
	prov := &scriptedProvider{steps: []scriptStep{
		{text: "After reviewing everything, here is my verdict:\n```json\n{\"satisfied\": true, \"feedback\": \"confirmed\"}\n```\nDone."},
	}}
	factory := func(core.Model) (core.Provider, error) { return prov, nil }
	cfg := baseCfg(factory, "obj")
	cfg.WorkDir = dir

	v, _, err := Verify(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if !v.Satisfied || v.Feedback != "confirmed" {
		t.Fatalf("expected the embedded JSON to be parsed, got %+v", v)
	}
	// Exactly one provider call — no reprompt.
	if prov.call != 1 {
		t.Fatalf("expected a single provider call (no reprompt), got %d", prov.call)
	}
}

// TestVerify_ConservativeFallback: no clean JSON in the final turn → not
// satisfied, raw text as feedback, no error (no reprompt is attempted).
func TestVerify_ConservativeFallback(t *testing.T) {
	dir := t.TempDir()
	prov := &scriptedProvider{steps: []scriptStep{
		{text: "looks fine to me"},
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

// TestVerifierRegistry_SandboxedToWorkDir asserts the verifier builds its OWN
// restricted path policy: reads inside WorkDir succeed, reads outside it are
// denied — even though the caller never passes a policy (P4: never reuse the
// session's possibly-unrestricted policy).
func TestVerifierRegistry_SandboxedToWorkDir(t *testing.T) {
	workDir := t.TempDir()
	inside := filepath.Join(workDir, "in.txt")
	if err := os.WriteFile(inside, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outside, []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg, err := newVerifierRegistry(workDir)
	if err != nil {
		t.Fatal(err)
	}
	readTool, ok := reg.Get("read")
	if !ok {
		t.Fatal("read tool missing from verifier registry")
	}

	// Inside the sandbox: succeeds.
	res, err := readTool.Execute(context.Background(), map[string]any{"path": inside}, nil)
	if err != nil || res.IsError {
		t.Fatalf("reading inside WorkDir should succeed, got err=%v isErr=%v", err, res.IsError)
	}

	// Outside the sandbox: denied (either an error return or IsError result).
	res, err = readTool.Execute(context.Background(), map[string]any{"path": outside}, nil)
	if err == nil && !res.IsError {
		t.Fatalf("reading outside WorkDir must be denied, got %+v", res)
	}

	// Write/exec tools must not exist at all.
	for _, banned := range []string{"edit", "write", "bash", "subagent"} {
		if _, ok := reg.Get(banned); ok {
			t.Fatalf("verifier registry must not expose %q", banned)
		}
	}
}

// TestVerify_OneShotStatsCost: the legacy one-shot path reports usage/cost so
// the driver can charge it against the goal budget (P3).
func TestVerify_OneShotStatsCost(t *testing.T) {
	prov := &scriptedProvider{steps: []scriptStep{
		{text: `{"satisfied": true, "feedback": "ok"}`},
	}}
	factory := func(core.Model) (core.Provider, error) { return prov, nil }
	cfg := baseCfg(factory, "obj")
	cfg.OneShot = true

	_, stats, err := Verify(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Usage == nil || stats.Usage.TotalTokens == 0 {
		t.Fatalf("one-shot must report usage, got %+v", stats.Usage)
	}
	if stats.CostUSD <= 0 {
		t.Fatalf("one-shot must report a positive cost, got %v", stats.CostUSD)
	}
}

// TestVerify_TimeoutIsTotal: the wall-clock timeout bounds the WHOLE call. A
// provider that always errors would otherwise retry; with a tiny timeout the
// call returns promptly rather than running the full retry budget (P1).
func TestVerify_TimeoutIsTotal(t *testing.T) {
	dir := t.TempDir()
	prov := &slowProvider{delay: 200 * time.Millisecond}
	factory := func(core.Model) (core.Provider, error) { return prov, nil }
	cfg := baseCfg(factory, "obj")
	cfg.WorkDir = dir
	cfg.Timeout = 50 * time.Millisecond

	start := time.Now()
	v, _, err := Verify(context.Background(), cfg)
	elapsed := time.Since(start)
	// A single shared 50ms deadline: even with retries, we must finish well
	// before N×(provider delay). Generous bound to avoid CI flakiness.
	if elapsed > 400*time.Millisecond {
		t.Fatalf("total timeout not enforced across retries: took %v", elapsed)
	}
	// Running out of the total budget is a cap, not an infrastructure failure:
	// a conservative not-satisfied verdict with no error, so the goal keeps going.
	if err != nil {
		t.Fatalf("a total-timeout cap must not error, got %v", err)
	}
	if v.Satisfied {
		t.Fatal("a timed-out verifier must return not-satisfied")
	}
	if !strings.Contains(strings.ToLower(v.Feedback), "time") {
		t.Fatalf("feedback should mention running out of time, got %q", v.Feedback)
	}
}

// slowProvider blocks on ctx until its delay elapses, then returns ctx.Err().
// It never yields a real turn, so it exercises the deadline path.
type slowProvider struct {
	delay time.Duration
}

func (p *slowProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	ch := make(chan core.AssistantEvent, 1)
	go func() {
		defer close(ch)
		select {
		case <-time.After(p.delay):
			ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: fmt.Errorf("slow")}
		case <-ctx.Done():
			ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: ctx.Err()}
		}
	}()
	return ch, nil
}

// TestVerify_OneShotTimeoutIsCap: two fast one-shot failures then a slow one
// that hits the shared deadline must yield a capped not-satisfied verdict, NOT
// an infrastructure error that pauses the goal (P1, one-shot path).
func TestVerify_OneShotTimeoutIsCap(t *testing.T) {
	prov := &oneShotSeqProvider{fastFails: 2, slowDelay: 500 * time.Millisecond}
	factory := func(core.Model) (core.Provider, error) { return prov, nil }
	cfg := baseCfg(factory, "obj")
	cfg.OneShot = true
	cfg.Timeout = 60 * time.Millisecond

	v, _, err := Verify(context.Background(), cfg)
	if err != nil {
		t.Fatalf("a one-shot total-timeout cap must not error, got %v", err)
	}
	if v.Satisfied {
		t.Fatal("a timed-out one-shot verifier must return not-satisfied")
	}
	if !strings.Contains(strings.ToLower(v.Feedback), "time") {
		t.Fatalf("feedback should mention running out of time, got %q", v.Feedback)
	}
	// The cap must have been reached on the LAST retry, not short-circuited on an
	// earlier one — otherwise the pre-3624e74 bug (deadline on the last attempt
	// returned as an infra error) wouldn't be exercised.
	if got := prov.calls(); got != oneShotMaxAttempts {
		t.Fatalf("expected the deadline to be hit on the last of %d attempts, got %d calls", oneShotMaxAttempts, got)
	}
}

// TestVerify_OneShotChargesFailedAttempts: an attempt that fails with a billed
// empty-response error still contributes its cost to the returned stats, so the
// goal is charged for retries (P3, one-shot path).
func TestVerify_OneShotChargesFailedAttempts(t *testing.T) {
	failedUsage := &core.Usage{Input: 100, Output: 0, TotalTokens: 100}
	successUsage := &core.Usage{Input: 10, Output: 5, TotalTokens: 15}
	prov := &oneShotSeqProvider{
		emptyErrUsage: failedUsage,
		successAfter:  1, // first attempt fails (billed), second succeeds
		successText:   `{"satisfied": true, "feedback": "ok"}`,
		successUsage:  successUsage,
	}
	factory := func(core.Model) (core.Provider, error) { return prov, nil }
	cfg := baseCfg(factory, "obj")
	cfg.OneShot = true

	v, stats, err := Verify(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Satisfied {
		t.Fatalf("expected satisfied verdict from the succeeding attempt, got %+v", v)
	}
	if prov.calls() != 2 {
		t.Fatalf("expected exactly 2 attempts (1 billed failure + 1 success), got %d", prov.calls())
	}
	// Cost must cover BOTH the billed failed attempt and the successful one — not
	// just the success (which alone would pass a naive > 0 check).
	model, _ := core.ResolveModel(DefaultVerifierSpec)
	want := model.Pricing.Cost(*failedUsage) + model.Pricing.Cost(*successUsage)
	if diff := stats.CostUSD - want; diff < -1e-9 || diff > 1e-9 {
		t.Fatalf("cost should sum both attempts: got %v want %v", stats.CostUSD, want)
	}
	// And strictly more than the successful attempt alone.
	if stats.CostUSD <= model.Pricing.Cost(*successUsage) {
		t.Fatalf("cost must exceed the success-only cost, got %v", stats.CostUSD)
	}
}

// oneShotSeqProvider scripts a sequence of one-shot (tool-less) attempts:
//   - the first `fastFails` calls fail immediately;
//   - if slowDelay > 0, the next call blocks until ctx is done (deadline);
//   - if emptyErrUsage is set, calls before successAfter fail with a billed
//     EmptyResponseError; the call at index successAfter streams successText.
type oneShotSeqProvider struct {
	mu            sync.Mutex
	call          int
	fastFails     int
	slowDelay     time.Duration
	emptyErrUsage *core.Usage
	successAfter  int
	successText   string
	successUsage  *core.Usage
}

// calls returns how many times Stream has been invoked (thread-safe).
func (p *oneShotSeqProvider) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.call
}

func (p *oneShotSeqProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	p.mu.Lock()
	idx := p.call
	p.call++
	p.mu.Unlock()

	ch := make(chan core.AssistantEvent, 2)
	go func() {
		defer close(ch)
		// Billed-failure-then-success mode.
		if p.emptyErrUsage != nil {
			if idx < p.successAfter {
				ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: &core.EmptyResponseError{Usage: p.emptyErrUsage}}
				return
			}
			msg := core.Message{Role: "assistant", Content: []core.Content{core.TextContent(p.successText)}, StopReason: "end_turn", Usage: p.successUsage}
			ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: p.successText}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
			return
		}
		// Fast-fails-then-slow mode.
		if idx < p.fastFails {
			ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: fmt.Errorf("fast fail %d", idx)}
			return
		}
		select {
		case <-ctx.Done():
			ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: ctx.Err()}
		case <-time.After(p.slowDelay):
			ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: fmt.Errorf("slow fail")}
		}
	}()
	return ch, nil
}

// TestVerify_EmptyWorkDirRejected: the agentic verifier refuses an empty
// WorkDir, since tool.safePath would treat "" as no sandbox (YOLO) and expose
// the filesystem to the read-only verifier (P4).
func TestVerify_EmptyWorkDirRejected(t *testing.T) {
	prov := &scriptedProvider{steps: []scriptStep{
		{text: `{"satisfied": true, "feedback": "ok"}`},
	}}
	factory := func(core.Model) (core.Provider, error) { return prov, nil }
	cfg := baseCfg(factory, "obj")
	cfg.WorkDir = "" // no sandbox root

	if _, _, err := Verify(context.Background(), cfg); err == nil {
		t.Fatal("agentic Verify must reject an empty WorkDir")
	}
	// One-shot has no tools, so it's exempt from the WorkDir requirement.
	cfg.OneShot = true
	if _, _, err := Verify(context.Background(), cfg); err != nil {
		t.Fatalf("one-shot Verify should not require a WorkDir, got %v", err)
	}
}

// TestVerify_InvalidWorkDirRejected: the agentic verifier also refuses a
// WorkDir that doesn't exist or isn't a directory, so it can't emit a verdict
// without ever inspecting the workspace.
func TestVerify_InvalidWorkDirRejected(t *testing.T) {
	prov := &scriptedProvider{steps: []scriptStep{
		{text: `{"satisfied": true, "feedback": "ok"}`},
	}}
	factory := func(core.Model) (core.Provider, error) { return prov, nil }

	// Non-existent path.
	cfg := baseCfg(factory, "obj")
	cfg.WorkDir = filepath.Join(t.TempDir(), "does-not-exist")
	if _, _, err := Verify(context.Background(), cfg); err == nil {
		t.Fatal("Verify must reject a non-existent WorkDir")
	}

	// A regular file, not a directory.
	f := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.WorkDir = f
	if _, _, err := Verify(context.Background(), cfg); err == nil {
		t.Fatal("Verify must reject a WorkDir that is a regular file")
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
