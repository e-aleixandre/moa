package bootstrap

import (
	"testing"

	"github.com/ealeixandre/moa/pkg/askuser"
	"github.com/ealeixandre/moa/pkg/permission"
	"github.com/ealeixandre/moa/pkg/planmode"
	"github.com/ealeixandre/moa/pkg/tasks"
	"github.com/ealeixandre/moa/pkg/tool"
)

func TestRuntimeConfig_CommonFields(t *testing.T) {
	ts := tasks.NewStore()
	pm := planmode.New(planmode.Config{})
	pp := tool.NewPathPolicy("/tmp", nil, false)
	ab := askuser.NewBridge()
	bs := newTestBootstrapSession("medium")
	bs.TaskStore = ts
	bs.PlanMode = pm
	bs.PathPolicy = pp
	bs.AskBridge = ab

	rcfg := bs.RuntimeConfig()

	if rcfg.Agent != bs.Agent {
		t.Error("Agent mismatch")
	}
	if rcfg.TaskStore != ts {
		t.Error("TaskStore mismatch")
	}
	if rcfg.PlanMode != pm {
		t.Error("PlanMode mismatch")
	}
	if rcfg.PathPolicy != pp {
		t.Error("PathPolicy mismatch")
	}
	if rcfg.AskBridge != ab {
		t.Error("AskBridge mismatch")
	}
	// BaseSystemPrompt comes from Agent.SystemPrompt() (source of truth).
	if rcfg.BaseSystemPrompt != bs.Agent.SystemPrompt() {
		t.Errorf("BaseSystemPrompt: got %q, want %q", rcfg.BaseSystemPrompt, bs.Agent.SystemPrompt())
	}
}

func TestRuntimeConfig_GateConfig_WithGate(t *testing.T) {
	bs := newTestBootstrapSession("medium")
	bs.Gate = permission.New(permission.ModeAsk, permission.Config{
		Headless: true,
		Allow:    []string{"Bash(go:*)"},
	})

	rcfg := bs.RuntimeConfig()

	if rcfg.Gate != bs.Gate {
		t.Error("Gate mismatch")
	}
	if !rcfg.GateConfig.Headless {
		t.Error("GateConfig.Headless should be true (from gate snapshot)")
	}
	if len(rcfg.GateConfig.Allow) != 1 || rcfg.GateConfig.Allow[0] != "Bash(go:*)" {
		t.Errorf("GateConfig.Allow not preserved: %v", rcfg.GateConfig.Allow)
	}
}

func TestRuntimeConfig_GateConfig_NoGate_Headless(t *testing.T) {
	bs := newTestBootstrapSession("medium")
	bs.Gate = nil
	bs.Headless = true

	rcfg := bs.RuntimeConfig()

	if rcfg.Gate != nil {
		t.Error("Gate should be nil")
	}
	if !rcfg.GateConfig.Headless {
		t.Error("GateConfig.Headless should be true from Session.Headless fallback")
	}
}

func TestRuntimeConfig_GateConfig_NoGate_Interactive(t *testing.T) {
	bs := newTestBootstrapSession("medium")
	bs.Gate = nil
	bs.Headless = false

	rcfg := bs.RuntimeConfig()

	if rcfg.GateConfig.Headless {
		t.Error("GateConfig.Headless should be false for interactive sessions")
	}
}

func TestRuntimeConfig_CallerCanOverride(t *testing.T) {
	bs := newTestBootstrapSession("medium")
	rcfg := bs.RuntimeConfig()

	// Callers can override any field.
	rcfg.SessionID = "custom"
	rcfg.BaseSystemPrompt = ""

	if rcfg.SessionID != "custom" {
		t.Error("override failed")
	}
	if rcfg.BaseSystemPrompt != "" {
		t.Error("BaseSystemPrompt override failed")
	}
}
