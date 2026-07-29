package tui

import (
	"strings"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/mcp"
)

func mcpStatuses() []mcp.ControllerStatus {
	return []mcp.ControllerStatus{
		{ServerStatus: mcp.ServerStatus{Name: "github", State: mcp.StateReady, ToolCount: 12}, Enabled: true, DesiredEnabled: true},
		{ServerStatus: mcp.ServerStatus{Name: "playwright", State: mcp.StateDisabled}, Enabled: false, DesiredEnabled: false, DisabledScopes: []core.MCPDisableScope{core.MCPScopeGlobal}},
	}
}

func TestMCPPickerOpenDefaultsToSessionScope(t *testing.T) {
	var p mcpPicker
	p.Open(mcpStatuses(), true)
	if !p.active {
		t.Fatal("should be active after Open")
	}
	if p.scope != core.MCPScopeSession {
		t.Fatalf("scope = %q, want session (safe default on open)", p.scope)
	}
	if p.cursor != 0 {
		t.Fatalf("cursor = %d, want 0", p.cursor)
	}
}

func TestMCPPickerCycleScope(t *testing.T) {
	var p mcpPicker
	p.Open(mcpStatuses(), true)
	want := []core.MCPDisableScope{core.MCPScopeProject, core.MCPScopeGlobal, core.MCPScopeSession}
	for i, w := range want {
		p.CycleScope()
		if p.scope != w {
			t.Fatalf("cycle %d: scope = %q, want %q", i, p.scope, w)
		}
	}
}

func TestMCPPickerScopeBadges(t *testing.T) {
	st := mcp.ControllerStatus{DisabledScopes: []core.MCPDisableScope{core.MCPScopeSession, core.MCPScopeGlobal}}
	// Stable order is global, project, session -> [G][S].
	if got := scopeBadges(st); got != "[G][S]" {
		t.Fatalf("badges = %q, want [G][S]", got)
	}
	if got := scopeBadges(mcp.ControllerStatus{}); got != "" {
		t.Fatalf("no vetoes should render no badges, got %q", got)
	}
	pendingBadge := scopeBadges(mcp.ControllerStatus{
		DisabledScopes: []core.MCPDisableScope{core.MCPScopeProject},
		PendingAction:  "disable",
	})
	if pendingBadge != "[P] (pending)" {
		t.Fatalf("pending badge = %q, want [P] (pending)", pendingBadge)
	}
}

func TestScopeVetoes(t *testing.T) {
	st := mcp.ControllerStatus{DisabledScopes: []core.MCPDisableScope{core.MCPScopeGlobal}}
	if !scopeVetoes(st, core.MCPScopeGlobal) {
		t.Fatal("global veto should be detected")
	}
	if scopeVetoes(st, core.MCPScopeSession) {
		t.Fatal("session should not be vetoed")
	}
}

func TestMCPPickerRenderIncludesScopeAndServers(t *testing.T) {
	var p mcpPicker
	p.Open(mcpStatuses(), true)
	out := p.Render(80)
	for _, want := range []string{"MCP servers", "scope: SESSION", "github", "ready · 12 tools", "playwright", "disabled", "[G]", "s scope", "space toggle"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
}

func TestMCPPickerRenderConfirmPrompt(t *testing.T) {
	var p mcpPicker
	p.Open(mcpStatuses(), true)
	p.pendingConfirm = mcpPendingToggle{name: "github", scope: core.MCPScopeGlobal, disabled: true}
	out := p.Render(80)
	if !strings.Contains(out, "Confirm disable") || !strings.Contains(out, "y/N") {
		t.Fatalf("confirm prompt not rendered:\n%s", out)
	}
}
