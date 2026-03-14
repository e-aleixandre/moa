package bootstrap

import (
	"context"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/permission"
)

// nopProvider is a minimal provider that does nothing (for testing wiring only).
type nopProvider struct{}

func (nopProvider) Stream(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
	ch := make(chan core.AssistantEvent)
	close(ch) // immediately closes — agent won't actually run
	return ch, nil
}

func nopFactory(core.Model) (core.Provider, error) {
	return nopProvider{}, nil
}

func minimalConfig(t *testing.T) SessionConfig {
	t.Helper()
	return SessionConfig{
		CWD:             t.TempDir(),
		Model:           core.Model{ID: "test-model", Name: "test"},
		Provider:        nopProvider{},
		ProviderFactory: nopFactory,
		Ctx:             context.Background(),
	}
}

func TestBuildSession_Minimal(t *testing.T) {
	cfg := minimalConfig(t)
	sess, err := BuildSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Agent == nil {
		t.Fatal("expected Agent to be non-nil")
	}
	if sess.ToolReg == nil {
		t.Fatal("expected ToolReg to be non-nil")
	}

	// Check that key builtins are registered.
	for _, name := range []string{"bash", "read", "write", "edit", "grep", "find", "ls", "tasks"} {
		if _, ok := sess.ToolReg.Get(name); !ok {
			t.Errorf("expected tool %q to be registered", name)
		}
	}
}

func TestBuildSession_RequiredFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SessionConfig)
	}{
		{"missing CWD", func(c *SessionConfig) { c.CWD = "" }},
		{"missing Provider", func(c *SessionConfig) { c.Provider = nil }},
		{"missing ProviderFactory", func(c *SessionConfig) { c.ProviderFactory = nil }},
		{"missing Ctx", func(c *SessionConfig) { c.Ctx = nil }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := minimalConfig(t)
			tt.mutate(&cfg)
			_, err := BuildSession(cfg)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestBuildSession_WithAskUser(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.EnableAskUser = true
	sess, err := BuildSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if sess.AskBridge == nil {
		t.Error("expected AskBridge to be non-nil when EnableAskUser=true")
	}
	if _, ok := sess.ToolReg.Get("ask_user"); !ok {
		t.Error("expected ask_user tool to be registered")
	}
}

func TestBuildSession_PermissionModes(t *testing.T) {
	tests := []struct {
		mode     string
		wantGate bool
		wantMode permission.Mode
	}{
		{"yolo", false, ""},
		{"ask", true, permission.ModeAsk},
		{"auto", true, permission.ModeAuto},
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			cfg := minimalConfig(t)
			cfg.PermissionMode = tt.mode
			sess, err := BuildSession(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantGate {
				if sess.Gate == nil {
					t.Fatal("expected Gate to be non-nil")
				}
				if sess.Gate.Mode() != tt.wantMode {
					t.Errorf("expected mode %q, got %q", tt.wantMode, sess.Gate.Mode())
				}
			} else {
				if sess.Gate != nil {
					t.Error("expected Gate to be nil for yolo mode")
				}
			}
		})
	}
}

func TestBuildSession_BudgetPropagation(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.Model.Pricing = &core.Pricing{Input: 1.0, Output: 1.0}
	cfg.MaxBudget = 5.0
	sess, err := BuildSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Agent == nil {
		t.Fatal("expected Agent to be non-nil")
	}
}

func TestBuildSession_BeforeWriteHook(t *testing.T) {
	var captured string
	cfg := minimalConfig(t)
	cfg.BeforeWrite = func(path string) error {
		captured = path
		return nil
	}
	sess, err := BuildSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// The hook is wired through ToolConfig — we can't easily invoke write
	// without an actual agent run, so just verify the session was created.
	if sess.Agent == nil {
		t.Fatal("expected Agent")
	}
	_ = captured // used only to verify the closure compiles
}

func TestFormatSubagentNotification(t *testing.T) {
	tests := []struct {
		name       string
		status     string
		resultTail string
		wantEmpty  bool
	}{
		{"completed", "completed", "result text", false},
		{"failed", "failed", "error details", false},
		{"cancelled", "cancelled", "", false},
		{"unknown", "unknown_status", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatSubagentNotification("job-1", "do something", tt.status, tt.resultTail)
			if tt.wantEmpty && result != "" {
				t.Errorf("expected empty, got %q", result)
			}
			if !tt.wantEmpty && result == "" {
				t.Error("expected non-empty notification")
			}
		})
	}
}
