package mcp

import (
	"context"
	"sort"
	"sync"

	"github.com/ealeixandre/moa/pkg/core"
)

// Controller coordinates one session's MCP manager, its tool registry, and the
// disable policy, so neither the web nor the TUI frontend has to orchestrate
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

// Status returns the manager's server snapshots decorated with policy: whether
// each server is enabled by policy (desired), and which scopes veto it.
func (c *Controller) Status() []ControllerStatus {
	c.mu.Lock()
	policy := c.policy
	c.mu.Unlock()

	raw := c.mgr.Status()
	out := make([]ControllerStatus, 0, len(raw))
	for _, st := range raw {
		res := core.ResolveMCPDisabled(st.Name, policy)
		out = append(out, ControllerStatus{
			ServerStatus:   st,
			DesiredEnabled: !res.Disabled,
			DisabledScopes: res.Scopes,
		})
	}
	return out
}

// ControllerStatus is a manager ServerStatus plus the policy view: what the
// configuration wants (DesiredEnabled) and why (DisabledScopes), independent of
// the applied runtime State (which may still be catching up, e.g. failed).
type ControllerStatus struct {
	ServerStatus
	DesiredEnabled bool                   `json:"desired_enabled"`
	DisabledScopes []core.MCPDisableScope `json:"disabled_scopes,omitempty"`
}

// Restart restarts one server and re-syncs its tools by exact name. It refuses a
// disabled server (ErrServerDisabled) — restart must not bypass policy.
func (c *Controller) Restart(ctx context.Context, name string) (ServerStatus, error) {
	c.op.Lock()
	defer c.op.Unlock()

	st, err := c.mgr.RestartServer(ctx, name)
	if err != nil {
		return st, err
	}
	c.resyncServer(name)
	c.refresh()
	return st, nil
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
	policy := c.policy
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
