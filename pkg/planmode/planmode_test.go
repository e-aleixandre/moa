package planmode

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/tasks"
)

func newTestRegistry() *core.Registry {
	reg := core.NewRegistry()
	for _, name := range []string{"read", "write", "edit", "bash", "grep", "find", "ls", "web_search", "subagent"} {
		n := name
		reg.Register(core.Tool{
			Name:  n,
			Label: n,
			Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
				return core.TextResult("ok"), nil
			},
		})
	}
	return reg
}

func newTestPlanMode(t *testing.T) *PlanMode {
	t.Helper()
	dir := t.TempDir()
	reg := newTestRegistry()
	// Register the tasks tool globally (same as main.go does).
	ts := tasks.NewStore()
	reg.Register(tasks.NewTool(ts))
	return New(Config{
		Registry:   reg,
		SessionDir: dir,
		TaskStore:  ts,
	})
}

func TestEnterExit(t *testing.T) {
	pm := newTestPlanMode(t)

	if pm.Mode() != ModeOff {
		t.Fatalf("expected ModeOff, got %s", pm.Mode())
	}

	path, err := pm.Enter()
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if path == "" {
		t.Fatal("expected non-empty plan file path")
	}
	if pm.Mode() != ModePlanning {
		t.Fatalf("expected ModePlanning, got %s", pm.Mode())
	}

	if !strings.Contains(path, "/plans/") {
		t.Fatalf("plan path not under plans/: %s", path)
	}

	pm.Exit()
	if pm.Mode() != ModeOff {
		t.Fatalf("expected ModeOff after Exit, got %s", pm.Mode())
	}
}

func TestToolSwitching(t *testing.T) {
	dir := t.TempDir()
	reg := newTestRegistry()
	ts := tasks.NewStore()
	reg.Register(tasks.NewTool(ts))
	originalCount := reg.Count()

	pm := New(Config{Registry: reg, SessionDir: dir, TaskStore: ts})

	_, err := pm.Enter()
	if err != nil {
		t.Fatal(err)
	}

	// In planning mode, subagent should be gone.
	if _, ok := reg.Get("subagent"); ok {
		t.Error("subagent should be unregistered in planning mode")
	}
	// Planning tools should be present (tasks is in allowlist).
	for _, name := range []string{"read", "grep", "find", "ls", "bash", "write", "edit", "submit_plan", "tasks"} {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("expected %s to be available in planning mode", name)
		}
	}

	pm.Exit()

	// After exit, original tools should be restored.
	if reg.Count() < originalCount-1 { // tasks may have been unregistered/restored
		t.Errorf("expected at least %d tools after exit, got %d", originalCount-1, reg.Count())
	}
	// subagent should be back.
	if _, ok := reg.Get("subagent"); !ok {
		t.Error("subagent should be restored after exit")
	}
	// submit_plan should be gone.
	if _, ok := reg.Get("submit_plan"); ok {
		t.Error("submit_plan should be unregistered after exit")
	}
}

func TestFilterToolCall_Planning(t *testing.T) {
	pm := newTestPlanMode(t)
	path, _ := pm.Enter()

	if ok, _ := pm.FilterToolCall("read", map[string]any{"path": "main.go"}); !ok {
		t.Error("read should be allowed")
	}
	if ok, _ := pm.FilterToolCall("write", map[string]any{"path": path}); !ok {
		t.Error("write to plan file should be allowed")
	}
	if ok, _ := pm.FilterToolCall("write", map[string]any{"path": "main.go"}); ok {
		t.Error("write to non-plan file should be blocked")
	}
	if ok, _ := pm.FilterToolCall("bash", map[string]any{"command": "ls -la"}); !ok {
		t.Error("safe bash should be allowed")
	}
	if ok, _ := pm.FilterToolCall("bash", map[string]any{"command": "rm -rf /"}); ok {
		t.Error("dangerous bash should be blocked")
	}
}

func TestFilterToolCall_NonPlanning(t *testing.T) {
	pm := newTestPlanMode(t)

	if ok, _ := pm.FilterToolCall("write", map[string]any{"path": "main.go"}); !ok {
		t.Error("write should be allowed in ModeOff")
	}

	_, _ = pm.Enter()
	pm.StartExecution()

	if ok, _ := pm.FilterToolCall("bash", map[string]any{"command": "rm -rf /"}); !ok {
		t.Error("bash should be allowed in ModeExecuting")
	}
}

func TestFilterToolCall_ReadyAndReviewing(t *testing.T) {
	pm := newTestPlanMode(t)
	path, _ := pm.Enter()

	pm.mu.Lock()
	pm.state.PlanSubmitted = true
	pm.mu.Unlock()
	pm.OnPlanSubmitted()

	if ok, _ := pm.FilterToolCall("write", map[string]any{"path": "main.go"}); ok {
		t.Error("write to non-plan file should be blocked in ModeReady")
	}
	if ok, _ := pm.FilterToolCall("write", map[string]any{"path": path}); !ok {
		t.Error("write to plan file should be allowed in ModeReady")
	}
	if ok, _ := pm.FilterToolCall("bash", map[string]any{"command": "rm -rf /"}); ok {
		t.Error("dangerous bash should be blocked in ModeReady")
	}

	pm.StartReview()
	if ok, _ := pm.FilterToolCall("write", map[string]any{"path": "main.go"}); ok {
		t.Error("write to non-plan file should be blocked in ModeReviewing")
	}
}

func TestStateTransitions(t *testing.T) {
	pm := newTestPlanMode(t)

	_, _ = pm.Enter()
	if pm.Mode() != ModePlanning {
		t.Fatalf("expected ModePlanning")
	}

	pm.mu.Lock()
	pm.state.PlanSubmitted = true
	pm.mu.Unlock()
	if !pm.OnPlanSubmitted() {
		t.Fatal("expected OnPlanSubmitted to return true")
	}
	if pm.Mode() != ModeReady {
		t.Fatalf("expected ModeReady, got %s", pm.Mode())
	}
	if pm.OnPlanSubmitted() {
		t.Fatal("expected OnPlanSubmitted to return false on second call")
	}

	pm.StartReview()
	if pm.Mode() != ModeReviewing {
		t.Fatalf("expected ModeReviewing")
	}

	pm.ReviewDone()
	if pm.Mode() != ModeReady {
		t.Fatalf("expected ModeReady")
	}

	pm.ContinueRefining()
	if pm.Mode() != ModePlanning {
		t.Fatalf("expected ModePlanning")
	}

	pm.mu.Lock()
	pm.state.PlanSubmitted = true
	pm.mu.Unlock()
	pm.OnPlanSubmitted()
	pm.StartExecution()
	if pm.Mode() != ModeExecuting {
		t.Fatalf("expected ModeExecuting")
	}

	pm.Exit()
	if pm.Mode() != ModeOff {
		t.Fatalf("expected ModeOff")
	}
}

func TestOnChangeCallback(t *testing.T) {
	pm := newTestPlanMode(t)

	var transitions []Mode
	var mu sync.Mutex
	pm.SetOnChange(func(m Mode) {
		mu.Lock()
		transitions = append(transitions, m)
		mu.Unlock()
	})

	_,_ = pm.Enter()
	pm.Exit()

	mu.Lock()
	defer mu.Unlock()
	if len(transitions) != 2 {
		t.Fatalf("expected 2 transitions, got %d: %v", len(transitions), transitions)
	}
	if transitions[0] != ModePlanning || transitions[1] != ModeOff {
		t.Fatalf("unexpected transitions: %v", transitions)
	}
}

func TestStatePersistence(t *testing.T) {
	pm := newTestPlanMode(t)
	_,_ = pm.Enter()

	meta := pm.SaveState()

	pm2 := newTestPlanMode(t)
	pm2.RestoreState(meta)

	if pm2.Mode() != ModePlanning {
		t.Fatalf("expected ModePlanning after restore, got %s", pm2.Mode())
	}
	if pm2.PlanFilePath() == "" {
		t.Fatal("expected non-empty plan file path after restore")
	}
}

func TestSubmitPlan(t *testing.T) {
	pm := newTestPlanMode(t)
	_,_ = pm.Enter()

	submitTool := pm.SubmitPlanTool()
	ctx := context.Background()

	result, err := submitTool.Execute(ctx, map[string]any{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("submit_plan returned error: %v", result.Content)
	}
	if !pm.OnPlanSubmitted() {
		t.Fatal("expected OnPlanSubmitted to return true after submit_plan")
	}

	result, err = submitTool.Execute(ctx, map[string]any{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("submit_plan should error when not in planning mode")
	}
}

func TestRequestReviewToolLifecycle(t *testing.T) {
	pm := newTestPlanMode(t)

	// ModeOff: no request_review.
	if _, ok := pm.registry.Get("request_review"); ok {
		t.Error("request_review should not exist in ModeOff")
	}

	// Planning: no request_review.
	_,_ = pm.Enter()
	if _, ok := pm.registry.Get("request_review"); ok {
		t.Error("request_review should not exist in ModePlanning")
	}

	// Executing: has request_review.
	pm.StartExecution()
	if _, ok := pm.registry.Get("request_review"); !ok {
		t.Error("request_review should exist in ModeExecuting")
	}
	// tasks should also be present (globally registered).
	if _, ok := pm.registry.Get("tasks"); !ok {
		t.Error("tasks should exist in ModeExecuting")
	}

	// Exit: no request_review.
	pm.Exit()
	if _, ok := pm.registry.Get("request_review"); ok {
		t.Error("request_review should not exist after Exit")
	}
}

func TestRequestReviewToolLifecycle_RestoreExecuting(t *testing.T) {
	pm := newTestPlanMode(t)
	_,_ = pm.Enter()
	pm.StartExecution()

	meta := pm.SaveState()

	pm2 := newTestPlanMode(t)
	pm2.RestoreState(meta)
	pm2.ApplyRestoredState()

	if _, ok := pm2.registry.Get("request_review"); !ok {
		t.Error("request_review should be registered after restore to ModeExecuting")
	}
}

func TestRequestReviewValidation(t *testing.T) {
	pm := newTestPlanMode(t)
	_,_ = pm.Enter()
	pm.StartExecution()

	tool, ok := pm.registry.Get("request_review")
	if !ok {
		t.Fatal("request_review tool not found")
	}
	ctx := context.Background()

	// Empty summary.
	r, err := tool.Execute(ctx, map[string]any{
		"summary":       "",
		"files_changed": []any{"a.go"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsError {
		t.Error("expected error for empty summary")
	}

	// Empty files_changed.
	r, err = tool.Execute(ctx, map[string]any{
		"summary":       "did stuff",
		"files_changed": []any{},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsError {
		t.Error("expected error for empty files_changed")
	}

	// Too many files (>50 unique).
	bigList := make([]any, 51)
	for i := range bigList {
		bigList[i] = fmt.Sprintf("file%d.go", i)
	}
	r, err = tool.Execute(ctx, map[string]any{
		"summary":       "did stuff",
		"files_changed": bigList,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsError {
		t.Error("expected error for >50 files")
	}

	// Non-executing mode.
	pm.Exit()
	_,_ = pm.Enter()
	r, err = requestReviewTool(pm).Execute(ctx, map[string]any{
		"summary":       "stuff",
		"files_changed": []any{"a.go"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsError {
		t.Error("expected error for non-executing mode")
	}
}

func TestParseVerdictStrict(t *testing.T) {
	tests := []struct {
		text     string
		approved bool
	}{
		{"blah blah\nVERDICT: APPROVED\n", true},
		{"blah blah\nVERDICT: CHANGES_REQUESTED\n", false},
		{"blah blah\nVERDICT: APPROVED", true},
		{"blah\n\n**Verdict:**\nAPPROVED\n", true},
		{"blah\n\nCHANGES REQUESTED\n", false},
		{"blah\n\n**CHANGES_REQUESTED**\n", false},
		{"blah\n\nNOT APPROVED\n", false},
	}
	for _, tt := range tests {
		r := parseVerdict(tt.text)
		if r.Approved != tt.approved {
			t.Errorf("parseVerdict(%q): got Approved=%v, want %v", tt.text, r.Approved, tt.approved)
		}
	}
}

func TestSlugGeneration(t *testing.T) {
	slugs := make(map[string]bool)
	for i := 0; i < 100; i++ {
		s := generateSlug()
		parts := strings.Split(s, "-")
		if len(parts) != 3 {
			t.Fatalf("expected 3-part slug, got %q", s)
		}
		slugs[s] = true
	}
	if len(slugs) < 95 {
		t.Fatalf("expected mostly unique slugs, got %d unique out of 100", len(slugs))
	}
}
