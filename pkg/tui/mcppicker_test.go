package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/mcp"
)

// fakeMCPControl is an in-memory mcpControl for handler tests: it records scope
// toggles and restarts and serves a mutable status list.
type fakeMCPControl struct {
	servers    []mcp.ControllerStatus
	scopeCalls []string // "scope:name:disabled"
	restarts   []string
	restartErr error
	reconciled int
}

func (f *fakeMCPControl) Status() []mcp.ControllerStatus { return f.servers }

func (f *fakeMCPControl) SetScopeDisabled(scope core.MCPDisableScope, name string, disabled bool) {
	f.scopeCalls = append(f.scopeCalls, string(scope)+":"+name+":"+boolStr(disabled))
	for i := range f.servers {
		if f.servers[i].Name != name {
			continue
		}
		has := scopeVetoes(f.servers[i], scope)
		if disabled && !has {
			f.servers[i].DisabledScopes = append(f.servers[i].DisabledScopes, scope)
		} else if !disabled && has {
			var kept []core.MCPDisableScope
			for _, s := range f.servers[i].DisabledScopes {
				if s != scope {
					kept = append(kept, s)
				}
			}
			f.servers[i].DisabledScopes = kept
		}
	}
}

func (f *fakeMCPControl) Reconcile(context.Context) []mcp.ControllerStatus {
	f.reconciled++
	return f.servers
}

func (f *fakeMCPControl) Restart(_ context.Context, name string) (mcp.ServerStatus, error) {
	f.restarts = append(f.restarts, name)
	return mcp.ServerStatus{Name: name}, f.restartErr
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

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

func newMCPTestModel(ctrl mcpControl) appModel {
	m := newTestModel()
	m.statusBar = NewStatusLine(statusLineStyle)
	m.mcpCtrl = ctrl
	return m
}

func TestMCPControlOrNil(t *testing.T) {
	if mcpControlOrNil(nil) != nil {
		t.Fatal("nil controller must yield a true-nil interface (typed-nil trap)")
	}
}

// TestMCPSessionToggleAppliesImmediately: space in session scope starts the
// async action (no confirm), and the result message applies the honest status.
func TestMCPSessionToggleAppliesImmediately(t *testing.T) {
	f := &fakeMCPControl{servers: mcpStatuses()}
	m := newMCPTestModel(f)
	m.mcpPicker.Open(f.Status(), true) // cursor on github, scope SESSION

	model, cmd := m.mcpPickerToggle()
	m = model.(appModel)
	if m.mcpPicker.pendingConfirm.name != "" {
		t.Fatal("session scope must not require confirmation")
	}
	if !m.mcpPicker.busy || cmd == nil {
		t.Fatal("toggle should mark busy and return an async command")
	}
	// Run the async command and feed the result back.
	msg := cmd().(mcpActionResultMsg)
	if len(f.scopeCalls) != 1 || f.scopeCalls[0] != "session:github:true" {
		t.Fatalf("scope calls = %v, want one session:github:true", f.scopeCalls)
	}
	m.handleMCPActionResult(msg)
	if m.mcpPicker.busy {
		t.Fatal("busy should clear after result")
	}
	if !strings.Contains(m.mcpPicker.status, "disabled in SESSION") {
		t.Fatalf("status = %q, want disabled-in-session", m.mcpPicker.status)
	}
}

// TestMCPBroadScopeRequiresConfirm: global scope stages a confirm; y applies, n
// cancels without touching the controller.
func TestMCPBroadScopeRequiresConfirm(t *testing.T) {
	f := &fakeMCPControl{servers: mcpStatuses()}
	m := newMCPTestModel(f)
	m.mcpPicker.Open(f.Status(), true)
	m.mcpPicker.CycleScope() // PROJECT
	m.mcpPicker.CycleScope() // GLOBAL

	model, cmd := m.mcpPickerToggle()
	m = model.(appModel)
	if m.mcpPicker.pendingConfirm.name != "github" || cmd != nil {
		t.Fatal("global scope must stage a confirmation, not act")
	}
	if len(f.scopeCalls) != 0 {
		t.Fatal("controller must not be touched before confirmation")
	}
	// 'n' cancels.
	model, _ = m.handleMCPPickerKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = model.(appModel)
	if m.mcpPicker.pendingConfirm.name != "" || len(f.scopeCalls) != 0 {
		t.Fatalf("cancel should clear confirm and not call controller; calls=%v", f.scopeCalls)
	}
}

// TestMCPProjectUntrustedRefused: project scope with an untrusted project is
// refused with a reason, controller untouched.
func TestMCPProjectUntrustedRefused(t *testing.T) {
	f := &fakeMCPControl{servers: mcpStatuses()}
	m := newMCPTestModel(f)
	m.mcpPicker.Open(f.Status(), false) // projectTrusted=false
	m.mcpPicker.CycleScope()            // PROJECT

	model, cmd := m.mcpPickerToggle()
	m = model.(appModel)
	if cmd != nil || len(f.scopeCalls) != 0 {
		t.Fatal("untrusted project toggle must not act on the controller")
	}
	if !strings.Contains(m.mcpPicker.status, "not trusted") {
		t.Fatalf("status = %q, want a not-trusted reason", m.mcpPicker.status)
	}
}

// TestMCPRestartRefusesDisabled: r on a disabled row is refused; r on an enabled
// row runs the async restart.
func TestMCPRestartRefusesDisabled(t *testing.T) {
	f := &fakeMCPControl{servers: mcpStatuses()}
	m := newMCPTestModel(f)
	m.mcpPicker.Open(f.Status(), true)
	m.mcpPicker.cursor = 1 // playwright (disabled)

	model, cmd := m.mcpPickerRestart()
	m = model.(appModel)
	if cmd != nil || len(f.restarts) != 0 {
		t.Fatal("restart of a disabled server must be refused")
	}

	m.mcpPicker.cursor = 0 // github (enabled)
	model, cmd = m.mcpPickerRestart()
	m = model.(appModel)
	if cmd == nil || !m.mcpPicker.busy {
		t.Fatal("restart of an enabled server should run async and mark busy")
	}
	msg := cmd().(mcpActionResultMsg)
	if len(f.restarts) != 1 || f.restarts[0] != "github" {
		t.Fatalf("restarts = %v, want [github]", f.restarts)
	}
	m.handleMCPActionResult(msg)
	if !strings.Contains(m.mcpPicker.status, "restarted") {
		t.Fatalf("status = %q, want restarted", m.mcpPicker.status)
	}
}

// TestMCPActionResultAfterCloseIsSilent: a result arriving after the picker was
// closed must not touch closed-picker state.
func TestMCPActionResultAfterCloseIsSilent(t *testing.T) {
	f := &fakeMCPControl{servers: mcpStatuses()}
	m := newMCPTestModel(f)
	m.mcpPicker.busy = true // picker closed but action in flight
	m.handleMCPActionResult(mcpActionResultMsg{action: "restart", name: "github"})
	if m.mcpPicker.busy {
		t.Fatal("busy must clear even when picker is closed")
	}
	if m.mcpPicker.status != "" {
		t.Fatalf("closed picker must not gain a status, got %q", m.mcpPicker.status)
	}
}

// TestMCPReopenStaysBusyAndIgnoresStaleResult: an action started, then the
// picker is closed and reopened while it is still running. The reopened picker
// must stay busy (no second action), and the old result must be ignored so it
// can't clobber a newer one.
func TestMCPReopenStaysBusyAndIgnoresStaleResult(t *testing.T) {
	f := &fakeMCPControl{servers: mcpStatuses()}
	m := newMCPTestModel(f)
	m.mcpPicker.Open(f.Status(), true)

	// Start action gen 1 but do not deliver its result yet.
	model, cmd := m.mcpPickerToggle()
	m = model.(appModel)
	staleMsg := cmd().(mcpActionResultMsg)
	if !m.mcpActionPending {
		t.Fatal("action should be pending")
	}

	// Close and reopen: the picker must come back busy (action still running).
	m.mcpPicker.Close()
	model, _ = m.handleMCPCommand()
	m = model.(appModel)
	if !m.mcpPicker.busy {
		t.Fatal("reopened picker must stay busy while an action is in flight")
	}
	// A key press must not start a second action.
	before := len(f.scopeCalls)
	model, cmd2 := m.handleMCPPickerKey(tea.KeyMsg{Type: tea.KeySpace})
	m = model.(appModel)
	if cmd2 != nil || len(f.scopeCalls) != before {
		t.Fatal("no second action may start while busy")
	}

	// The stale result (gen 1) is still current here since no newer action ran;
	// simulate supersession by bumping the generation, then delivering it.
	m.mcpActionGen++
	m.handleMCPActionResult(staleMsg)
	if !m.mcpActionPending || !m.mcpPicker.busy {
		t.Fatal("a stale result must be ignored (pending/busy preserved)")
	}
}
