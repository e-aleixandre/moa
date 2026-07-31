package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/mcp"
)

// mcpControl is the narrow surface the picker needs from the shared MCP
// controller. Passing an interface (not the concrete *mcp.Controller) keeps the
// TUI decoupled and testable, and mirrors Sol's recommendation to hand the TUI
// a controller/coordinator rather than a fan of per-scope callbacks.
type mcpControl interface {
	Status() []mcp.ControllerStatus
	SetScopeDisabled(scope core.MCPDisableScope, name string, disabled bool)
	Reconcile(ctx context.Context) []mcp.ControllerStatus
	Restart(ctx context.Context, name string) (mcp.ServerStatus, error)
	// SessionDisabled returns the server names vetoed in SESSION scope, and
	// SetSessionDisabled replaces that set wholesale. The TUI uses these to keep
	// the session (process-memory) scope per-conversation: on a conversation
	// switch it saves the outgoing set and restores the incoming one, so a
	// session-scope veto set in one conversation doesn't leak into another.
	SessionDisabled() []string
	SetSessionDisabled(names []string)
}

// mcpScopeOrder is the cycle order for the 's' key, defaulting to session.
var mcpScopeOrder = []core.MCPDisableScope{
	core.MCPScopeSession,
	core.MCPScopeProject,
	core.MCPScopeGlobal,
}

// mcpPicker is the /mcp overlay: one row per configured server with its state,
// tool count and the scopes vetoing it, plus a visible scope mode the user
// cycles with 's'. Space toggles the selected server's veto in the visible
// scope only; r restarts an enabled server. Broad scopes (project/global) ask
// for a y/N confirmation the first time, remembered while the picker stays open.
type mcpPicker struct {
	active  bool
	cursor  int
	scope   core.MCPDisableScope
	servers []mcp.ControllerStatus

	// projectTrusted gates project-scope writes; when false the scope is shown
	// but its toggles are refused with an explanation (capability stays visible).
	projectTrusted bool

	// confirmed records which broad scopes were confirmed this open, so the
	// frequent action isn't gated twice.
	confirmed map[core.MCPDisableScope]bool
	// pendingConfirm holds a toggle awaiting y/N; empty name means none.
	pendingConfirm mcpPendingToggle

	// status is a transient one-line message (e.g. "still disabled globally").
	status string

	// busy marks an in-flight lifecycle action (reconcile/restart) running off
	// the UI goroutine; it blocks further toggles/restarts until the result
	// message arrives, and is shown in the footer.
	busy bool
}

// mcpPendingToggle is a toggle captured while a broad-scope confirm is showing.
type mcpPendingToggle struct {
	name     string
	scope    core.MCPDisableScope
	disabled bool
}

func (p *mcpPicker) Open(servers []mcp.ControllerStatus, projectTrusted bool) {
	p.servers = servers
	p.cursor = 0
	p.scope = core.MCPScopeSession // safe default each open
	p.projectTrusted = projectTrusted
	p.confirmed = map[core.MCPDisableScope]bool{}
	p.pendingConfirm = mcpPendingToggle{}
	p.status = ""
	p.busy = false
	p.active = true
}

func (p *mcpPicker) Close() { p.active = false }

func (p *mcpPicker) MoveUp() {
	if p.cursor > 0 {
		p.cursor--
	}
}

func (p *mcpPicker) MoveDown() {
	if p.cursor < len(p.servers)-1 {
		p.cursor++
	}
}

// CycleScope advances the visible scope SESSION → PROJECT → GLOBAL → SESSION.
func (p *mcpPicker) CycleScope() {
	for i, s := range mcpScopeOrder {
		if s == p.scope {
			p.scope = mcpScopeOrder[(i+1)%len(mcpScopeOrder)]
			p.status = ""
			return
		}
	}
	p.scope = core.MCPScopeSession
}

func (p *mcpPicker) selected() (mcp.ControllerStatus, bool) {
	if p.cursor >= 0 && p.cursor < len(p.servers) {
		return p.servers[p.cursor], true
	}
	return mcp.ControllerStatus{}, false
}

// scopeVetoes reports whether a server is vetoed in the given scope.
func scopeVetoes(st mcp.ControllerStatus, scope core.MCPDisableScope) bool {
	for _, s := range st.DisabledScopes {
		if s == scope {
			return true
		}
	}
	return false
}

func mcpScopeLabel(s core.MCPDisableScope) string {
	switch s {
	case core.MCPScopeSession:
		return "SESSION"
	case core.MCPScopeProject:
		return "PROJECT"
	case core.MCPScopeGlobal:
		return "GLOBAL"
	}
	return strings.ToUpper(string(s))
}

// Render draws the picker. Kept close to the branch picker's visual language.
func (p *mcpPicker) Render(width int) string {
	if !p.active {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "🔌 MCP servers%sscope: %s\n\n", strings.Repeat(" ", pad(width, len(mcpScopeLabel(p.scope)))), mcpScopeLabel(p.scope))

	if len(p.servers) == 0 {
		sb.WriteString("  No MCP servers configured for this session.\n")
		sb.WriteString("\n  esc close\n")
		return sb.String()
	}

	for i, st := range p.servers {
		prefix := "   "
		if i == p.cursor {
			prefix = " ▸ "
		}
		glyph := stateGlyph(st)
		badges := scopeBadges(st)
		detail := serverDetail(st)
		fmt.Fprintf(&sb, "%s%s %-22s %s %s\n", prefix, glyph, truncateDisplay(st.Name, 22), detail, badges)
	}

	if p.pendingConfirm.name != "" {
		verb := "disable"
		if !p.pendingConfirm.disabled {
			verb = "enable"
		}
		where := "every project and future session"
		if p.pendingConfirm.scope == core.MCPScopeProject {
			where = "this project's config and open sessions"
		}
		fmt.Fprintf(&sb, "\n  Confirm %s %q for %s scope — affects %s.  y/N\n",
			verb, p.pendingConfirm.name, mcpScopeLabel(p.pendingConfirm.scope), where)
	} else if p.busy {
		sb.WriteString("\n  working…\n")
	} else if p.status != "" {
		fmt.Fprintf(&sb, "\n  %s\n", p.status)
	}

	sb.WriteString("\n  ↑/↓ move   s scope   space toggle   r restart   esc close\n")
	return sb.String()
}

func pad(width, used int) int {
	n := width - 16 - used
	if n < 2 {
		return 2
	}
	if n > 40 {
		return 40
	}
	return n
}

// stateGlyph is a compact status marker: ● ready, ○ disabled, ! failed/exited,
// … starting/disabling.
func stateGlyph(st mcp.ControllerStatus) string {
	switch st.State {
	case mcp.StateReady:
		return "●"
	case mcp.StateDisabled:
		return "○"
	case mcp.StateFailed, mcp.StateExited:
		return "!"
	default:
		return "…"
	}
}

func serverDetail(st mcp.ControllerStatus) string {
	switch st.State {
	case mcp.StateReady:
		if st.ToolCount == 1 {
			return "ready · 1 tool"
		}
		return fmt.Sprintf("ready · %d tools", st.ToolCount)
	case mcp.StateDisabled:
		return "disabled"
	case mcp.StateFailed:
		if st.Error != "" {
			return "failed · " + truncateDisplay(st.Error, 40)
		}
		return "failed"
	case mcp.StateExited:
		return "exited"
	case mcp.StateStarting:
		return "starting…"
	case mcp.StateDisabling:
		return "disabling…"
	}
	if st.PendingAction != "" {
		return "pending " + st.PendingAction
	}
	return string(st.State)
}

// scopeBadges renders [G][P][S] for the scopes vetoing a server, stable order.
func scopeBadges(st mcp.ControllerStatus) string {
	if len(st.DisabledScopes) == 0 {
		return ""
	}
	order := map[core.MCPDisableScope]int{core.MCPScopeGlobal: 0, core.MCPScopeProject: 1, core.MCPScopeSession: 2}
	letter := map[core.MCPDisableScope]string{core.MCPScopeGlobal: "G", core.MCPScopeProject: "P", core.MCPScopeSession: "S"}
	scopes := append([]core.MCPDisableScope{}, st.DisabledScopes...)
	sort.Slice(scopes, func(i, j int) bool { return order[scopes[i]] < order[scopes[j]] })
	var b strings.Builder
	for _, s := range scopes {
		fmt.Fprintf(&b, "[%s]", letter[s])
	}
	if st.PendingAction != "" {
		b.WriteString(" (pending)")
	}
	return b.String()
}
