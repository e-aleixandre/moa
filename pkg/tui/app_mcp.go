package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/mcp"
)

// mcpControlOrNil converts a concrete controller to the mcpControl interface,
// returning a true nil interface when the controller is nil. This avoids the
// typed-nil trap where a nil *mcp.Controller stored in an interface is itself
// non-nil, which would make the /mcp picker think MCP is available.
func mcpControlOrNil(c *mcp.Controller) mcpControl {
	if c == nil {
		return nil
	}
	return c
}

// handleMCPCommand opens the /mcp picker. It refuses while the agent is running
// (a reconcile mutates the live tool set) and when no MCP servers are
// configured, so the command fails loudly instead of showing an empty overlay.
func (m appModel) handleMCPCommand() (tea.Model, tea.Cmd) {
	if m.mcpCtrl == nil {
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: "No MCP servers configured for this session"})
		m.updateViewport()
		return m, nil
	}
	if m.s.running {
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: "Cannot change MCP servers while agent is running"})
		m.updateViewport()
		return m, nil
	}
	servers := m.mcpCtrl.Status()
	if len(servers) == 0 {
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: "No MCP servers configured for this session"})
		m.updateViewport()
		return m, nil
	}
	m.mcpPicker.Open(servers, m.mcpProjectTrusted())
	m.input.SetEnabled(false)
	m.updateViewport()
	return m, nil
}

// mcpProjectTrusted reports whether this working directory's project moa config
// is trusted, which gates project-scope writes.
func (m appModel) mcpProjectTrusted() bool {
	return core.IsProjectPathTrusted(core.LoadGlobalConfig(), m.cwd)
}

// handleMCPPickerKey routes keys while the /mcp picker is open.
func (m appModel) handleMCPPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// A pending broad-scope confirmation intercepts y/N first.
	if m.mcpPicker.pendingConfirm.name != "" {
		switch msg.Type {
		case tea.KeyRunes:
			switch string(msg.Runes) {
			case "y", "Y":
				pc := m.mcpPicker.pendingConfirm
				m.mcpPicker.confirmed[pc.scope] = true
				m.mcpPicker.pendingConfirm = mcpPendingToggle{}
				return m.applyMCPToggle(pc.scope, pc.name, pc.disabled)
			default:
				m.mcpPicker.pendingConfirm = mcpPendingToggle{}
				m.mcpPicker.status = "Cancelled."
				m.updateViewport()
				return m, nil
			}
		case tea.KeyEsc:
			m.mcpPicker.pendingConfirm = mcpPendingToggle{}
			m.mcpPicker.status = "Cancelled."
			m.updateViewport()
			return m, nil
		}
		return m, nil
	}

	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.mcpPicker.Close()
		m.recomputeInputEnabled()
		m.updateViewport()
		return m, nil

	case tea.KeyUp:
		m.mcpPicker.MoveUp()
	case tea.KeyDown:
		m.mcpPicker.MoveDown()

	case tea.KeyRunes:
		switch string(msg.Runes) {
		case "j":
			m.mcpPicker.MoveDown()
		case "k":
			m.mcpPicker.MoveUp()
		case "s":
			m.mcpPicker.CycleScope()
		case " ":
			return m.mcpPickerToggle()
		case "r":
			return m.mcpPickerRestart()
		}
	case tea.KeySpace:
		return m.mcpPickerToggle()
	}
	m.updateViewport()
	return m, nil
}

// mcpPickerToggle flips the selected server's veto in the visible scope. Broad
// scopes (project/global) require a one-time y/N confirmation; session scope
// applies immediately. A project scope that isn't trusted is refused.
func (m appModel) mcpPickerToggle() (tea.Model, tea.Cmd) {
	st, ok := m.mcpPicker.selected()
	if !ok {
		return m, nil
	}
	scope := m.mcpPicker.scope
	if scope == core.MCPScopeProject && !m.mcpPicker.projectTrusted {
		m.mcpPicker.status = "Project config is not trusted; trust it first."
		m.updateViewport()
		return m, nil
	}
	nextDisabled := !scopeVetoes(st, scope)

	if (scope == core.MCPScopeProject || scope == core.MCPScopeGlobal) && !m.mcpPicker.confirmed[scope] {
		m.mcpPicker.pendingConfirm = mcpPendingToggle{name: st.Name, scope: scope, disabled: nextDisabled}
		m.updateViewport()
		return m, nil
	}
	return m.applyMCPToggle(scope, st.Name, nextDisabled)
}

// applyMCPToggle records the veto in the controller, persists project/global
// scopes, reconciles, and refreshes the picker rows. It sets a transient status
// that is honest about a removed override still leaving the server disabled by
// another scope.
func (m appModel) applyMCPToggle(scope core.MCPDisableScope, name string, disabled bool) (tea.Model, tea.Cmd) {
	// Persist first for durable scopes; a failure leaves memory untouched.
	if scope == core.MCPScopeProject || scope == core.MCPScopeGlobal {
		save := core.SaveGlobalConfig
		if scope == core.MCPScopeProject {
			cwd := m.cwd
			save = func(update func(*core.MoaConfig)) error { return core.SaveProjectConfig(cwd, update) }
		}
		if err := save(func(c *core.MoaConfig) { core.SetMCPServerDisabled(c, name, disabled) }); err != nil {
			m.mcpPicker.status = "Could not save preference: " + err.Error()
			m.updateViewport()
			return m, nil
		}
	}

	m.mcpCtrl.SetScopeDisabled(scope, name, disabled)
	m.mcpCtrl.Reconcile(m.baseCtx)
	m.refreshMCPPicker()

	// Honest transient status: removing one veto may leave others.
	st, _ := m.mcpPickerServer(name)
	switch {
	case !disabled && len(st.DisabledScopes) > 0:
		m.mcpPicker.status = mcpScopeLabel(scope) + " override removed; still disabled by another scope."
	case disabled:
		m.mcpPicker.status = name + " disabled in " + mcpScopeLabel(scope) + "."
	default:
		m.mcpPicker.status = name + " enabled."
	}
	m.updateViewport()
	m.updateMCPSegment()
	return m, nil
}

// mcpPickerRestart restarts the selected server if it is enabled.
func (m appModel) mcpPickerRestart() (tea.Model, tea.Cmd) {
	st, ok := m.mcpPicker.selected()
	if !ok {
		return m, nil
	}
	if st.State == mcp.StateDisabled || !st.Enabled {
		m.mcpPicker.status = "Server is disabled; enable it first."
		m.updateViewport()
		return m, nil
	}
	if _, err := m.mcpCtrl.Restart(m.baseCtx, st.Name); err != nil {
		m.mcpPicker.status = "Restart failed: " + err.Error()
		m.updateViewport()
		return m, nil
	}
	m.refreshMCPPicker()
	m.mcpPicker.status = st.Name + " restarted."
	m.updateViewport()
	m.updateMCPSegment()
	return m, nil
}

// refreshMCPPicker reloads the picker's server rows from the controller,
// clamping the cursor.
func (m *appModel) refreshMCPPicker() {
	if m.mcpCtrl == nil {
		return
	}
	m.mcpPicker.servers = m.mcpCtrl.Status()
	if m.mcpPicker.cursor >= len(m.mcpPicker.servers) {
		m.mcpPicker.cursor = len(m.mcpPicker.servers) - 1
	}
	if m.mcpPicker.cursor < 0 {
		m.mcpPicker.cursor = 0
	}
}

// mcpPickerServer returns the current status of one server by name.
func (m appModel) mcpPickerServer(name string) (mcp.ControllerStatus, bool) {
	for _, st := range m.mcpPicker.servers {
		if st.Name == name {
			return st, true
		}
	}
	return mcp.ControllerStatus{}, false
}

// updateMCPSegment recomputes the status-line MCP counts from the controller.
// Disabled is a neutral choice; only enabled servers that failed/exited count as
// unhealthy (red). No-op when there is no controller.
func (m *appModel) updateMCPSegment() {
	if m.mcpCtrl == nil {
		return
	}
	var total, unhealthy, disabled int
	for _, st := range m.mcpCtrl.Status() {
		total++
		switch st.State {
		case mcp.StateDisabled:
			disabled++
		case mcp.StateFailed, mcp.StateExited:
			unhealthy++
		}
	}
	m.statusBar.UpdateMCPSegment(total, unhealthy, disabled)
}
