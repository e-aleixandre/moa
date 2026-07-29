package serve

import (
	"errors"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/mcp"
)

// Errors returned by the MCP toggle path, mapped to HTTP status by the handler.
var (
	// ErrScopeInvalid is returned for a scope that isn't session/project/global.
	ErrScopeInvalid = errors.New("invalid scope")
	// ErrProjectUntrusted is returned when a project-scope write is requested for
	// a session whose project config path isn't trusted (409).
	ErrProjectUntrusted = errors.New("project config is untrusted")
)

// scopeAvailability describes whether a scope can be written for a session, and
// why not when it can't. It feeds the GET response's available_scopes so the UI
// can disable a control instead of failing a request.
type scopeAvailability struct {
	Writable bool   `json:"writable"`
	Reason   string `json:"reason,omitempty"`
}

// mcpAvailableScopes reports, for this session, which disable scopes are
// writable. Session and global are always writable; project depends on whether
// <cwd>/.moa/config.json is trusted (an untrusted project must be trusted first,
// same gate that governs whether its config is applied at all).
func (s *ManagedSession) mcpAvailableScopes() map[string]scopeAvailability {
	out := map[string]scopeAvailability{
		"session": {Writable: true},
		"global":  {Writable: true},
	}
	if s.mcpProjectTrusted() {
		out["project"] = scopeAvailability{Writable: true}
	} else {
		out["project"] = scopeAvailability{Writable: false, Reason: "project_config_untrusted"}
	}
	return out
}

// mcpProjectTrusted reports whether this session's project moa config is trusted,
// i.e. whether the project disable scope is writable and applicable.
func (s *ManagedSession) mcpProjectTrusted() bool {
	// The global config carries the trusted-project allowlist; resolve against
	// this session's cwd.
	return core.IsProjectPathTrusted(core.LoadGlobalConfig(), s.CWD)
}

// mcpToggleResult is the outcome of a toggle fan-out: how many open sessions had
// the change applied immediately, and how many deferred it until quiescence.
type mcpToggleResult struct {
	affected int
	pending  int
}

// SetMCPServerDisabled records a disable/enable preference for one server in one
// scope of THIS session and applies it to the running manager when idle. It does
// not persist (session scope is memory-only) or fan out; the Manager wraps it
// for project/global persistence and cross-session fan-out.
//
// When the session is quiescent the change is reconciled now; otherwise the
// preference is recorded and reconciliation is deferred to quiescence, so the
// caller never claims a server is off while a run is still holding its tools.
// Returns whether the change was applied (true) or left pending (false).
func (s *ManagedSession) setMCPServerDisabled(scope core.MCPDisableScope, server string, disabled bool) (applied bool, err error) {
	s.mu.Lock()
	ctrl := s.infra.mcpController
	s.mu.Unlock()
	if ctrl == nil {
		return false, ErrNoMCP
	}

	ctrl.SetScopeDisabled(scope, server, disabled)

	if !s.runtime.IsQuiescent() {
		// Record the preference and reconcile later; the deferred reconcile is
		// armed once (armMCPReconcile) and drains at the next quiescence.
		s.armMCPReconcile()
		return false, nil
	}
	ctrl.Reconcile(s.infra.sessionCtx)
	return true, nil
}

// armMCPReconcile schedules a one-shot reconcile of this session's MCP policy at
// the next quiescence. Multiple pending toggles coalesce into a single deferred
// reconcile (it re-reads the current policy when it fires, so it always applies
// the latest desired state).
func (s *ManagedSession) armMCPReconcile() {
	if !s.mcpReconcilePending.CompareAndSwap(false, true) {
		return // already armed; the pending reconcile will pick up this change too
	}
	go func() {
		if !s.runtime.WaitQuiescent(s.infra.sessionCtx) {
			s.mcpReconcilePending.Store(false)
			return // context cancelled (session tearing down)
		}
		s.mcpReconcilePending.Store(false)
		s.mu.Lock()
		ctrl := s.infra.mcpController
		ctx := s.infra.sessionCtx
		s.mu.Unlock()
		if ctrl != nil {
			ctrl.Reconcile(ctx)
		}
	}()
}

// mcpDisableParams is the validated PATCH body.
type mcpDisableParams struct {
	Scope    core.MCPDisableScope
	Disabled bool
}

// parseMCPScope validates a raw scope string into a scope value.
func parseMCPScope(raw string) (core.MCPDisableScope, error) {
	switch core.MCPDisableScope(raw) {
	case core.MCPScopeSession:
		return core.MCPScopeSession, nil
	case core.MCPScopeProject:
		return core.MCPScopeProject, nil
	case core.MCPScopeGlobal:
		return core.MCPScopeGlobal, nil
	default:
		return "", ErrScopeInvalid
	}
}

// ToggleMCPServer applies a disable/enable preference for one server in one
// scope, persisting it (project/global) and fanning it out to every affected
// open session in the process:
//
//   - session: only the anchor session,
//   - project: every open session whose canonical cwd matches the anchor's,
//   - global:  every open session in the process.
//
// The persisted scope also governs future session startups. Returns how many
// sessions applied the change now vs deferred it to quiescence. anchor must be a
// session that has the server configured (the caller validates that via GET).
func (m *Manager) ToggleMCPServer(anchor *ManagedSession, params mcpDisableParams, server string) (mcpToggleResult, error) {
	// Serialize the whole mutation (persist + fan-out) so two concurrent toggles
	// can't lose a config update or interleave reconciles.
	m.mcpConfigMu.Lock()
	defer m.mcpConfigMu.Unlock()

	// Persist project/global scope before touching runtime, so a persistence
	// failure leaves the desired policy untouched (500, nothing applied).
	switch params.Scope {
	case core.MCPScopeGlobal:
		if err := core.SaveGlobalConfig(func(c *core.MoaConfig) {
			core.SetMCPServerDisabled(c, server, params.Disabled)
		}); err != nil {
			return mcpToggleResult{}, err
		}
	case core.MCPScopeProject:
		if !anchor.mcpProjectTrusted() {
			return mcpToggleResult{}, ErrProjectUntrusted
		}
		if err := core.SaveProjectConfig(anchor.CWD, func(c *core.MoaConfig) {
			core.SetMCPServerDisabled(c, server, params.Disabled)
		}); err != nil {
			return mcpToggleResult{}, err
		}
	case core.MCPScopeSession:
		// No persistence; memory-only.
	default:
		return mcpToggleResult{}, ErrScopeInvalid
	}

	targets := m.mcpFanoutTargets(anchor, params.Scope)

	var res mcpToggleResult
	for _, sess := range targets {
		applied, err := sess.setMCPServerDisabled(params.Scope, server, params.Disabled)
		if err != nil {
			// A target without this server configured is a no-op for it; other
			// scopes (an unmatched global name) simply record the preference.
			continue
		}
		res.affected++
		if !applied {
			res.pending++
		}
	}
	return res, nil
}

// mcpFanoutTargets returns the open sessions a scope's toggle should reach.
func (m *Manager) mcpFanoutTargets(anchor *ManagedSession, scope core.MCPDisableScope) []*ManagedSession {
	switch scope {
	case core.MCPScopeSession:
		return []*ManagedSession{anchor}
	case core.MCPScopeProject:
		anchorCWD := core.CanonicalOrRaw(anchor.CWD)
		m.mu.RLock()
		defer m.mu.RUnlock()
		var out []*ManagedSession
		for _, sess := range m.sessions {
			if core.CanonicalOrRaw(sess.CWD) == anchorCWD {
				out = append(out, sess)
			}
		}
		return out
	case core.MCPScopeGlobal:
		m.mu.RLock()
		defer m.mu.RUnlock()
		out := make([]*ManagedSession, 0, len(m.sessions))
		for _, sess := range m.sessions {
			out = append(out, sess)
		}
		return out
	default:
		return nil
	}
}

// mcpServerConfigured reports whether the anchor session has the named server
// among its configured MCP servers (matched or unmatched). Used to 404 a PATCH
// for a server the session doesn't know.
func (s *ManagedSession) mcpServerConfigured(server string) bool {
	for _, st := range s.MCPStatus() {
		if st.Name == server {
			return true
		}
	}
	return false
}

// mcpServerStatus reports the ControllerStatus snapshot of one server in the
// anchor session after a toggle, for the PATCH response body.
func (s *ManagedSession) mcpServerStatus(server string) (mcp.ControllerStatus, bool) {
	for _, st := range s.MCPStatus() {
		if st.Name == server {
			return st, true
		}
	}
	return mcp.ControllerStatus{}, false
}

// mcpUnmatchedDisabled reports this session's disabled preferences that match no
// configured server (empty if the session has no MCP controller).
func (s *ManagedSession) mcpUnmatchedDisabled() []mcp.UnmatchedDisabled {
	s.mu.Lock()
	ctrl := s.infra.mcpController
	s.mu.Unlock()
	if ctrl == nil {
		return nil
	}
	return ctrl.UnmatchedDisabled()
}
