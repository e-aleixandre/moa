package serve

import (
	"errors"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/mcp"
)

// Errors returned by the MCP toggle path, mapped to HTTP status by the handler.
var (
	// ErrScopeInvalid is returned for a scope that isn't session/project/global.
	ErrScopeInvalid = errors.New("invalid scope")
)

// scopeAvailability describes whether a scope can be written for a session, and
// why not when it can't. It feeds the GET response's available_scopes so the UI
// can disable a control instead of failing a request.
type scopeAvailability struct {
	Writable bool   `json:"writable"`
	Reason   string `json:"reason,omitempty"`
}

// mcpAvailableScopes reports, for this session, which disable scopes are
// writable. All three are: the veto is the user's own preference, stored with
// their config rather than in the repository, so there is nothing about the
// project that can make it unwritable.
func (s *ManagedSession) mcpAvailableScopes() map[string]scopeAvailability {
	return map[string]scopeAvailability{
		"session": {Writable: true},
		"global":  {Writable: true},
		"project": {Writable: true},
	}
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
	// Write the desired policy AND reconcile under the lifecycle guard, so the
	// policy change is serialized with restart/reload: a restart can't snapshot a
	// stale (still-enabled) policy and then respawn a server this toggle just
	// vetoed. mcpApplyPolicy re-reads the live controller after taking the guard.
	applied, hasCtrl := s.mcpApplyPolicy(scope, server, disabled)
	if !hasCtrl {
		return false, ErrNoMCP
	}
	if applied {
		// Publish unconditionally: a scope change whose applied state is already
		// controlled by another veto makes Reconcile a manager no-op (no OnChange
		// fires), yet the scope badges of every affected session must refresh.
		s.publishMCPChanged()
		return true, nil
	}
	// Not quiescent: the preference is recorded; arm the deferred reconcile.
	s.armMCPReconcile()
	// Publish now so this and other affected sessions show the pending state
	// live — the deferred change triggers no manager transition of its own.
	s.publishMCPChanged()
	return false, nil
}

// mcpApplyPolicy records a scope veto and reconciles once, all under the MCP
// lifecycle guard so it is serialized with restart/reload. It returns whether
// the reconcile applied (applied=false when not quiescent — the caller then
// defers) and whether a controller exists (hasCtrl=false means no MCP for this
// session). The policy write happens even when not quiescent, so a deferred
// reconcile later applies the recorded preference.
func (s *ManagedSession) mcpApplyPolicy(scope core.MCPDisableScope, server string, disabled bool) (applied, hasCtrl bool) {
	s.mcpLifecycleMu.Lock()
	defer s.mcpLifecycleMu.Unlock()
	s.mu.Lock()
	ctrl := s.infra.mcpController
	ctx := s.infra.sessionCtx
	s.mu.Unlock()
	if ctrl == nil {
		return false, false
	}
	ctrl.SetScopeDisabled(scope, server, disabled)
	return s.runtime.DoIfQuiescent(func() { ctrl.Reconcile(ctx) }), true
}

// mcpReconcileNow reconciles the session's MCP policy once, serialized with all
// other MCP lifecycle operations (restart/reload) via mcpLifecycleMu and made
// run-atomic via DoIfQuiescent. It re-reads the live controller after acquiring
// the guard, so it never reconciles through a controller a concurrent reload
// has already swapped out. Returns whether it applied (applied=false when not
// quiescent) and whether a controller exists (hasCtrl=false means MCP is gone,
// so a deferred worker should stop retrying).
func (s *ManagedSession) mcpReconcileNow() (applied, hasCtrl bool) {
	s.mcpLifecycleMu.Lock()
	defer s.mcpLifecycleMu.Unlock()
	// A close was admitted: the MCP manager is being torn down, so report "no
	// controller" and let the deferred worker retire instead of spinning.
	if s.closing.Load() {
		return false, false
	}
	s.mu.Lock()
	ctrl := s.infra.mcpController
	ctx := s.infra.sessionCtx
	s.mu.Unlock()
	if ctrl == nil {
		return false, false
	}
	return s.runtime.DoIfQuiescent(func() { ctrl.Reconcile(ctx) }), true
}

// armMCPReconcile schedules a one-shot reconcile of this session's MCP policy at
// the next quiescence. Multiple pending toggles coalesce into a single deferred
// reconcile (it re-reads the current policy when it fires, so it always applies
// the latest desired state).
func (s *ManagedSession) armMCPReconcile() {
	// Mark that desired policy changed. If a worker is already running, it will
	// observe the dirty flag and loop; only start a new worker if none is alive.
	s.mcpReconcileDirty.Store(true)
	if !s.mcpReconcilePending.CompareAndSwap(false, true) {
		return // a worker is alive and will pick up the dirty flag
	}
	go func() {
		// Loop: WaitQuiescent then reconcile via mcpReconcileNow, which serializes
		// with restart/reload (mcpLifecycleMu) and is run-atomic (DoIfQuiescent).
		// If a run sneaks in between the wait and the reconcile, mcpReconcileNow
		// returns false and we wait again — the deferred reconcile never mutates
		// the tool set under a live run or concurrently with a manager swap.
		for {
			if !s.runtime.WaitQuiescent(s.infra.sessionCtx) {
				s.mcpReconcilePending.Store(false)
				return // context cancelled (session tearing down)
			}
			// Consume the dirty flag before reconciling: a toggle arriving after
			// this point re-sets it and is caught by the re-check below.
			s.mcpReconcileDirty.Store(false)
			applied, hasCtrl := s.mcpReconcileNow()
			if !hasCtrl {
				s.mcpReconcilePending.Store(false)
				return // MCP is gone (reload removed all servers, or teardown)
			}
			if !applied {
				// Lost the race to a run that just started; a toggle may also have
				// re-dirtied. Ensure we retry at the next quiescence.
				s.mcpReconcileDirty.Store(true)
				continue
			}
			s.publishMCPChanged()
			// Release the one-flight, then re-check dirty. If a toggle set dirty
			// between the reconcile and here, it may have seen pending still true
			// and declined to start a worker; re-acquire and loop so its change is
			// not dropped.
			s.mcpReconcilePending.Store(false)
			if !s.mcpReconcileDirty.Load() {
				return
			}
			if !s.mcpReconcilePending.CompareAndSwap(false, true) {
				return // another worker started and owns the dirty change
			}
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

	// A session being closed is having its MCP manager torn down: mutating its
	// live tool set now is at best wasted work. Sessions already dropped from
	// the manager are invisible to the fan-out, so only the anchor needs this.
	if anchor.closing.Load() {
		return mcpToggleResult{}, ErrNotFound
	}

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
		// No trust gate: this is the user's own preference for this workspace,
		// stored with their config rather than in the repository, so there is
		// nothing about the project being trusted to check.
		if err := core.SetProjectMCPServerDisabled(anchor.CWD, server, params.Disabled); err != nil {
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
