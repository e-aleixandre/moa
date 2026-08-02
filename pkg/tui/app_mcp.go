package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/mcp"
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

// switchMCPSessionScope saves the current conversation's SESSION-scope MCP
// vetoes and loads the incoming conversation's, keeping the process-memory
// session scope per-conversation on the shared controller. It reconciles when
// the set actually changes so the tool registry matches the new conversation.
// No-op without a controller.
func (m *appModel) switchMCPSessionScope(newSessionID string) {
	if m.mcpCtrl == nil {
		return
	}
	if m.mcpActiveSessionID == newSessionID {
		return
	}
	// Save the outgoing conversation's session vetoes.
	if m.mcpActiveSessionID != "" {
		m.mcpSessionVetoes[m.mcpActiveSessionID] = m.mcpCtrl.SessionDisabled()
	}
	// Load the incoming conversation's set (empty if never toggled).
	incoming := m.mcpSessionVetoes[newSessionID]
	before := m.mcpCtrl.SessionDisabled()
	m.mcpCtrl.SetSessionDisabled(incoming)
	m.mcpActiveSessionID = newSessionID
	// Reconcile only if the effective set changed (order-insensitive compare).
	if !sameStringSet(before, incoming) {
		m.mcpCtrl.Reconcile(m.baseCtx)
		m.updateMCPSegment()
	}
}

// sameStringSet reports whether two name slices contain the same set of names.
func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]struct{}, len(a))
	for _, x := range a {
		seen[x] = struct{}{}
	}
	for _, y := range b {
		if _, ok := seen[y]; !ok {
			return false
		}
	}
	return true
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
	servers := m.mcpCtrl.Status()
	if len(servers) == 0 {
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: "No MCP servers configured for this session"})
		m.updateViewport()
		return m, nil
	}
	// The picker opens even while the agent is running: a toggle made mid-run is
	// recorded and deferred to the next quiescence (parity with the web panel),
	// so a reconcile never mutates the live tool set under an active run.
	m.mcpPicker.Open(servers)
	// If an action started in a previous open is still running, keep the picker
	// busy so a second one can't start (single-writer to the controller).
	m.mcpPicker.busy = m.mcpActionPending
	m.input.SetEnabled(false)
	m.updateViewport()
	return m, nil
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
		case tea.KeyEsc, tea.KeyCtrlC:
			m.mcpPicker.pendingConfirm = mcpPendingToggle{}
			m.mcpPicker.status = "Cancelled."
			m.updateViewport()
			return m, nil
		}
		return m, nil
	}

	// While a lifecycle action is in flight, only Esc/Ctrl-C (close) is honored;
	// toggles/restarts are blocked to keep the controller single-writer.
	if m.mcpPicker.busy {
		if msg.Type == tea.KeyEsc || msg.Type == tea.KeyCtrlC {
			m.mcpPicker.Close()
			m.recomputeInputEnabled()
			m.updateViewport()
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
// applies immediately.
func (m appModel) mcpPickerToggle() (tea.Model, tea.Cmd) {
	st, ok := m.mcpPicker.selected()
	if !ok {
		return m, nil
	}
	scope := m.mcpPicker.scope
	nextDisabled := !scopeVetoes(st, scope)

	if (scope == core.MCPScopeProject || scope == core.MCPScopeGlobal) && !m.mcpPicker.confirmed[scope] {
		m.mcpPicker.pendingConfirm = mcpPendingToggle{name: st.Name, scope: scope, disabled: nextDisabled}
		m.updateViewport()
		return m, nil
	}
	return m.applyMCPToggle(scope, st.Name, nextDisabled)
}

// applyMCPToggle records the veto in the controller, persists project/global
// scopes, then reconciles OFF the UI goroutine (a reconcile can block for the
// server start timeout). The live row/segment refresh arrives via
// bus.MCPChanged; mcpActionResultMsg clears the busy state and reports the
// honest outcome. A persist failure aborts before touching controller memory.
func (m appModel) applyMCPToggle(scope core.MCPDisableScope, name string, disabled bool) (tea.Model, tea.Cmd) {
	var persist func() error
	switch scope {
	case core.MCPScopeGlobal:
		persist = func() error {
			return core.SaveGlobalConfig(func(c *core.MoaConfig) {
				core.SetMCPServerDisabled(c, name, disabled)
			})
		}
	case core.MCPScopeProject:
		// The project veto is this user's preference for this workspace, so it
		// is stored with their config instead of in <cwd>/.moa/config.json,
		// which belongs to the repository.
		cwd := m.cwd
		persist = func() error { return core.SetProjectMCPServerDisabled(cwd, name, disabled) }
	}
	if persist != nil {
		if err := persist(); err != nil {
			m.mcpPicker.status = "Could not save preference: " + err.Error()
			m.updateViewport()
			return m, nil
		}
	}

	ctrl := m.mcpCtrl
	ctx := m.baseCtx
	rt := m.runtime
	m.mcpActionGen++
	gen := m.mcpActionGen
	m.mcpActionPending = true
	m.mcpPicker.busy = true
	m.mcpPicker.status = ""
	m.updateViewport()
	return m, func() tea.Msg {
		ctrl.SetScopeDisabled(scope, name, disabled)
		// Reconcile atomically against run-start (parity with serve): if a run or
		// background job is active, defer — the preference is recorded and applied
		// at the next quiescence.
		applied := true
		if rt != nil {
			applied = rt.DoIfQuiescent(func() { ctrl.Reconcile(ctx) })
			if !applied {
				m.armTUIReconcile(ctrl, ctx, rt)
			}
		} else {
			ctrl.Reconcile(ctx)
		}
		return mcpActionResultMsg{gen: gen, action: "toggle", name: name, scope: scope, disabled: disabled, deferred: !applied}
	}
}

// armTUIReconcile drains a deferred MCP reconcile at the next quiescence,
// mirroring serve's armMCPReconcile. It is one-flight per controller/runtime and
// re-tries if a run sneaks in before it can reconcile under the state lock.
func (m *appModel) armTUIReconcile(ctrl mcpControl, ctx context.Context, rt *bus.SessionRuntime) {
	m.mcpReconcileDirty.Store(true)
	if !m.mcpReconcileArmed.CompareAndSwap(false, true) {
		return
	}
	go func() {
		for {
			if !rt.WaitQuiescent(ctx) {
				m.mcpReconcileArmed.Store(false)
				return // context cancelled
			}
			m.mcpReconcileDirty.Store(false)
			if !rt.DoIfQuiescent(func() { ctrl.Reconcile(ctx) }) {
				m.mcpReconcileDirty.Store(true)
				continue // lost the race to a run; retry at next quiescence
			}
			rt.Bus.Publish(bus.MCPChanged{})
			m.mcpReconcileArmed.Store(false)
			if !m.mcpReconcileDirty.Load() {
				return
			}
			if !m.mcpReconcileArmed.CompareAndSwap(false, true) {
				return // another worker owns the dirty change
			}
		}
	}()
}

// mcpPickerRestart restarts the selected server if it is enabled, off the UI
// goroutine (a restart can block for the server start timeout).
func (m appModel) mcpPickerRestart() (tea.Model, tea.Cmd) {
	st, ok := m.mcpPicker.selected()
	if !ok {
		return m, nil
	}
	if st.State == mcp.StateDisabled || !st.Enabled || !st.DesiredEnabled {
		m.mcpPicker.status = "Server is disabled; enable it first."
		m.updateViewport()
		return m, nil
	}
	if st.PendingAction != "" {
		m.mcpPicker.status = "Server is settling; try again in a moment."
		m.updateViewport()
		return m, nil
	}
	ctrl := m.mcpCtrl
	ctx := m.baseCtx
	name := st.Name
	m.mcpActionGen++
	gen := m.mcpActionGen
	m.mcpActionPending = true
	m.mcpPicker.busy = true
	m.mcpPicker.status = ""
	m.updateViewport()
	return m, func() tea.Msg {
		_, err := ctrl.Restart(ctx, name)
		return mcpActionResultMsg{gen: gen, action: "restart", name: name, err: err}
	}
}

// handleMCPActionResult applies the outcome of an async toggle/restart: it
// clears the busy flag, refreshes rows from the controller, and sets an honest
// status. Live refresh of unrelated rows/segment happens via bus.MCPChanged. A
// stale result (from an action superseded by a newer one, e.g. after close and
// reopen) is ignored so it can't clobber the current action's state.
func (m *appModel) handleMCPActionResult(msg mcpActionResultMsg) {
	if msg.gen != m.mcpActionGen {
		return // superseded; a newer action owns the busy state
	}
	m.mcpActionPending = false
	m.mcpPicker.busy = false
	if !m.mcpPicker.active {
		return // picker was closed while the action ran; nothing to display
	}
	m.refreshMCPPicker()

	if msg.err != nil {
		switch msg.action {
		case "restart":
			m.mcpPicker.status = "Restart failed: " + msg.err.Error()
		default:
			m.mcpPicker.status = "Action failed: " + msg.err.Error()
		}
		m.updateViewport()
		return
	}

	switch msg.action {
	case "restart":
		m.mcpPicker.status = msg.name + " restarted."
	default: // toggle
		if msg.deferred {
			verb := "enable"
			if msg.disabled {
				verb = "disable"
			}
			m.mcpPicker.status = "Will " + verb + " " + msg.name + " in " + mcpScopeLabel(msg.scope) + " when work finishes."
			break
		}
		st, _ := m.mcpPickerServer(msg.name)
		switch {
		case !msg.disabled && len(st.DisabledScopes) > 0:
			m.mcpPicker.status = mcpScopeLabel(msg.scope) + " override removed; still disabled by another scope."
		case msg.disabled:
			m.mcpPicker.status = msg.name + " disabled in " + mcpScopeLabel(msg.scope) + "."
		default:
			m.mcpPicker.status = msg.name + " enabled."
		}
	}
	m.updateViewport()
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
