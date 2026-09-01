package mcp

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"github.com/e-aleixandre/moa/pkg/core"
)

// Controller coordinates one session's MCP manager, its tool registry, and the
// disable policy, so the web frontend does not have to orchestrate
// process + registry + prompt by hand. It is the single place that:
//
//   - resolves the desired enabled/disabled set from the policy;
//   - reconciles the running manager to match (enable/disable/restart);
//   - keeps the tool registry in sync by EXACT tool names (never by a sanitized
//     textual prefix, which is unreliable for long server names);
//   - refreshes the base system prompt after the tool set changes, so the model
//     is never told about a tool that is no longer registered.
//
// The manager stays the authority on subprocess/SDK state; the Controller adds
// the policy and registry/prompt coordination on top.
type Controller struct {
	mgr *Manager
	reg *core.Registry

	// refreshPrompt is called (outside op) after the registry changes so the
	// runtime can rebuild the base system prompt from the registry's specs. Nil
	// is allowed (e.g. tests, or a caller that rebuilds elsewhere).
	refreshPrompt func()

	// op serializes whole reconcile operations: a reconcile tears down/starts
	// processes and rewrites the registry, so two concurrent ones would race
	// each other's registry edits and prompt refreshes.
	op sync.Mutex

	mu     sync.Mutex
	policy core.MCPDisablePolicy
	// serverTools is the exact set of tool names currently registered for each
	// server. It is the source of truth for re-sync, replacing prefix matching.
	serverTools map[string]map[string]struct{}
}

// ControllerConfig configures a Controller.
type ControllerConfig struct {
	Manager  *Manager
	Registry *core.Registry
	Policy   core.MCPDisablePolicy
	// RefreshPrompt rebuilds the base system prompt from the registry. Optional.
	RefreshPrompt func()
}

// NewController builds a Controller over an already-started manager and the
// registry its tools were registered into. It captures the current server→tool
// name index from the manager so later reconciles can re-sync by exact name.
func NewController(cfg ControllerConfig) *Controller {
	c := &Controller{
		mgr:           cfg.Manager,
		reg:           cfg.Registry,
		refreshPrompt: cfg.RefreshPrompt,
		policy:        cfg.Policy,
		serverTools:   map[string]map[string]struct{}{},
	}
	// Seed the index from what the manager currently exposes, so we own an
	// accurate picture of which tool names belong to which server.
	for _, st := range c.mgr.Status() {
		set := map[string]struct{}{}
		for _, name := range currentToolNames(c.mgr, st.Name) {
			set[name] = struct{}{}
		}
		c.serverTools[st.Name] = set
	}
	return c
}

// SetRefreshPrompt sets the prompt-refresh hook after construction. The frontend
// creates the hook once its runtime exists (the runtime owns the base system
// prompt), which is later than when bootstrap builds the Controller. Guarded by
// op so it can't race an in-flight reconcile.
func (c *Controller) SetRefreshPrompt(fn func()) {
	c.op.Lock()
	c.refreshPrompt = fn
	c.op.Unlock()
}

// Status returns the manager's server snapshots decorated with policy: whether
// each server is enabled by policy (desired), which scopes veto it, the applied
// enabled state, and any pending action (desired differs from applied).
func (c *Controller) Status() []ControllerStatus {
	c.mu.Lock()
	policy := clonePolicy(c.policy)
	c.mu.Unlock()

	raw := c.mgr.Status()
	out := make([]ControllerStatus, 0, len(raw))
	for _, st := range raw {
		res := core.ResolveMCPDisabled(st.Name, policy)
		out = append(out, decorateStatus(st, res))
	}
	return out
}

// decorateStatus builds a ControllerStatus from a raw manager snapshot and a
// policy resolution. Applied-enabled means the running manager is not in the
// disabled state (a failed-but-enabled server is still "enabled"); pending is
// set only when the desired policy and the applied state disagree.
func decorateStatus(st ServerStatus, res core.MCPDisableResolution) ControllerStatus {
	appliedEnabled := st.State != StateDisabled
	desiredEnabled := !res.Disabled
	pending := ""
	if desiredEnabled && !appliedEnabled {
		pending = "enable"
	} else if !desiredEnabled && appliedEnabled {
		pending = "disable"
	}
	return ControllerStatus{
		ServerStatus:   st,
		Enabled:        appliedEnabled,
		DesiredEnabled: desiredEnabled,
		DisabledScopes: res.Scopes,
		PendingAction:  pending,
	}
}

// ControllerStatus is a manager ServerStatus plus the policy view: what the
// configuration wants (DesiredEnabled) and why (DisabledScopes), the applied
// runtime enablement (Enabled), and PendingAction when the two disagree (the
// server is mid-transition or awaiting quiescence).
type ControllerStatus struct {
	ServerStatus
	Enabled        bool                   `json:"enabled"`
	DesiredEnabled bool                   `json:"desired_enabled"`
	DisabledScopes []core.MCPDisableScope `json:"disabled_scopes,omitempty"`
	PendingAction  string                 `json:"pending_action,omitempty"`
}

// UnmatchedDisabled reports disabled-server preferences that don't correspond to
// any server configured in this session, split by the scopes that veto each. A
// global name absent here may still exist in another project, so these are
// "unmatched", not globally "orphaned".
func (c *Controller) UnmatchedDisabled() []UnmatchedDisabled {
	c.mu.Lock()
	policy := clonePolicy(c.policy)
	c.mu.Unlock()

	configured := map[string]struct{}{}
	for _, st := range c.mgr.Status() {
		configured[st.Name] = struct{}{}
	}

	names := map[string]struct{}{}
	for n := range policy.Global {
		names[n] = struct{}{}
	}
	for n := range policy.Project {
		names[n] = struct{}{}
	}
	for n := range policy.Session {
		names[n] = struct{}{}
	}

	var out []UnmatchedDisabled
	for name := range names {
		if _, ok := configured[name]; ok {
			continue
		}
		res := core.ResolveMCPDisabled(name, policy)
		out = append(out, UnmatchedDisabled{Name: name, Scopes: res.Scopes})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// UnmatchedDisabled is a disabled-server preference with no matching configured
// server in the session, plus the scopes that veto it.
type UnmatchedDisabled struct {
	Name   string                 `json:"name"`
	Scopes []core.MCPDisableScope `json:"scopes,omitempty"`
}

// SetScopeDisabled adds or removes a server name from one scope of this
// Controller's in-memory policy. It mutates only the requested scope's set
// (vetoes in other scopes are independent). It does NOT reconcile or persist;
// the caller decides when to Reconcile (at quiescence) and whether to persist
// (project/global scopes). Session scope is process-lifetime only.
func (c *Controller) SetScopeDisabled(scope core.MCPDisableScope, name string, disabled bool) {
	if name == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var set map[string]struct{}
	switch scope {
	case core.MCPScopeGlobal:
		set = c.policy.Global
	case core.MCPScopeProject:
		set = c.policy.Project
	case core.MCPScopeSession:
		set = c.policy.Session
	default:
		return
	}
	if set == nil {
		set = map[string]struct{}{}
	}
	if disabled {
		set[name] = struct{}{}
	} else {
		delete(set, name)
	}
	switch scope {
	case core.MCPScopeGlobal:
		c.policy.Global = set
	case core.MCPScopeProject:
		c.policy.Project = set
	case core.MCPScopeSession:
		c.policy.Session = set
	}
}

// Policy returns a deep copy of the controller's current in-memory disable
// policy (all scopes). Callers use it to rebuild a replacement manager/controller
// on reload from the LIVE policy, not a stale bootstrap snapshot.
func (c *Controller) Policy() core.MCPDisablePolicy {
	c.mu.Lock()
	defer c.mu.Unlock()
	return clonePolicy(c.policy)
}

// clonePolicy deep-copies a disable policy so a snapshot can be traversed
// without the controller lock while SetScopeDisabled mutates the live maps.
func clonePolicy(p core.MCPDisablePolicy) core.MCPDisablePolicy {
	cp := func(m map[string]struct{}) map[string]struct{} {
		if m == nil {
			return nil
		}
		out := make(map[string]struct{}, len(m))
		for k := range m {
			out[k] = struct{}{}
		}
		return out
	}
	return core.MCPDisablePolicy{
		Global:  cp(p.Global),
		Project: cp(p.Project),
		Session: cp(p.Session),
	}
}

// Restart restarts one server and re-syncs its tools by exact name. It refuses a
// server the policy wants disabled — whether already applied (Manager returns
// ErrServerDisabled) or still pending a deferred reconcile — so restart never
// bypasses a desired disable and spawns a process that policy says should be off.
func (c *Controller) Restart(ctx context.Context, name string) (ServerStatus, error) {
	c.op.Lock()
	defer c.op.Unlock()

	// Honor the desired policy, not just the applied state: a busy toggle records
	// a veto (pending) while the process is still running, and a restart must not
	// revive it.
	c.mu.Lock()
	policy := clonePolicy(c.policy)
	c.mu.Unlock()
	if core.ResolveMCPDisabled(name, policy).Disabled {
		for _, st := range c.mgr.Status() {
			if st.Name == name {
				return st, ErrServerDisabled
			}
		}
		return ServerStatus{}, ErrServerDisabled
	}

	st, err := c.mgr.RestartServer(ctx, name)
	if err != nil {
		return st, err
	}
	c.syncServerLocked(name)
	return st, nil
}

// SyncServer rewrites one server's tools in the registry to match the manager.
// Used when a server finishes its initial connect in the background (Start no
// longer waits for the handshake). Safe to call from OnChange via a new
// goroutine so it does not deadlock against Restart/Reconcile holding c.op.
func (c *Controller) SyncServer(name string) {
	if c == nil {
		return
	}
	c.op.Lock()
	defer c.op.Unlock()
	c.syncServerLocked(name)
}

func (c *Controller) syncServerLocked(name string) {
	// Compare the full registry before/after this server's resync — not a
	// fingerprint taken at NewController, which runs in bootstrap before
	// skills/ask-user/subagents are registered. BuildSystemPrompt stamps
	// time.Now() and git status; on GPT-5.6 that misses the entire implicit
	// cache, so a restart that brings back the same tools must not refresh.
	before := specsFingerprint(c.reg.Specs())
	c.resyncServer(name)
	if specsFingerprint(c.reg.Specs()) != before {
		c.refresh()
	}
}

// SetPolicy replaces the disable policy and reconciles the manager to match.
func (c *Controller) SetPolicy(ctx context.Context, p core.MCPDisablePolicy) []ControllerStatus {
	c.mu.Lock()
	c.policy = p
	c.mu.Unlock()
	return c.Reconcile(ctx)
}

// Reconcile brings the manager in line with the current policy: every server
// whose desired-enabled differs from its applied state is enabled or disabled,
// and the registry + prompt are re-synced. It is safe to call repeatedly; a
// server already in the desired state is left untouched (SetServerEnabled is
// idempotent).
//
// Reconcile is where a caller must have already established quiescence: it
// mutates the live tool set, so it must not run under an in-flight model turn.
func (c *Controller) Reconcile(ctx context.Context) []ControllerStatus {
	c.op.Lock()
	defer c.op.Unlock()

	c.mu.Lock()
	policy := clonePolicy(c.policy)
	c.mu.Unlock()

	changed := false
	for _, st := range c.mgr.Status() {
		desiredEnabled := !core.ResolveMCPDisabled(st.Name, policy).Disabled
		appliedDisabled := st.State == StateDisabled
		if desiredEnabled == !appliedDisabled {
			continue // already in the desired state
		}
		if _, err := c.mgr.SetServerEnabled(ctx, st.Name, desiredEnabled); err != nil {
			continue
		}
		c.resyncServer(st.Name)
		changed = true
	}
	if changed {
		c.refresh()
	}
	return c.Status()
}

// Close shuts down the manager. The registry is left as-is; the session is
// being torn down.
func (c *Controller) Close() {
	c.mgr.Close()
}

// resyncServer rewrites the registry entries for one server to exactly match the
// manager's current tools for it: unregister the names we last registered for
// this server, then register whatever it now exposes, updating the index. This
// is correct for long server names because it never derives a prefix — it works
// from the exact tool names the manager reports. Caller holds c.op.
func (c *Controller) resyncServer(name string) {
	c.mu.Lock()
	old := c.serverTools[name]
	c.mu.Unlock()

	// Remove the previous generation's tools for this server.
	for toolName := range old {
		c.reg.Unregister(toolName)
	}

	// Register the current generation and rebuild the index.
	tools, _ := c.mgr.ToolsForServer(name)
	next := make(map[string]struct{}, len(tools))
	for _, t := range tools {
		core.RegisterOrLog(c.reg, t)
		next[t.Name] = struct{}{}
	}

	c.mu.Lock()
	if len(next) == 0 {
		delete(c.serverTools, name)
	} else {
		c.serverTools[name] = next
	}
	c.mu.Unlock()
}

// refresh invokes the prompt-refresh hook if set. Caller holds c.op.
func (c *Controller) refresh() {
	if c.refreshPrompt != nil {
		c.refreshPrompt()
	}
}

// specsFingerprint is a stable identity of the tool list the system prompt
// is built from. Parameters are canonicalized so equivalent JSON with
// different key order or whitespace does not look like a tool-set change.
func specsFingerprint(specs []core.ToolSpec) string {
	var b strings.Builder
	for _, s := range specs {
		b.WriteString(s.Name)
		b.WriteByte(0)
		b.WriteString(s.Description)
		b.WriteByte(0)
		b.WriteString(canonicalJSON(s.Parameters))
		b.WriteByte(0)
	}
	return b.String()
}

func canonicalJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(out)
}

// currentToolNames returns the exact registered tool names for one server, from
// the manager. Used only to seed the index at construction.
func currentToolNames(mgr *Manager, server string) []string {
	tools, ok := mgr.ToolsForServer(server)
	if !ok {
		return nil
	}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	return names
}
