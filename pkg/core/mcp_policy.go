package core

import (
	"log/slog"
	"path/filepath"
	"sort"
)

// MCPDisableScope identifies the configuration level that vetoes an MCP server.
// The three scopes are the only values ever produced; this is a closed set, not
// an open enum persisted to disk (the persisted form is just a list of names
// per config level).
type MCPDisableScope string

const (
	// MCPScopeGlobal is the user's global moa config (~/.config/moa/config.json).
	MCPScopeGlobal MCPDisableScope = "global"
	// MCPScopeProject is the repo-local moa config (<cwd>/.moa/config.json),
	// honored only for trusted project paths.
	MCPScopeProject MCPDisableScope = "project"
	// MCPScopeSession is a temporary, in-memory veto for one conversation. It is
	// never persisted.
	MCPScopeSession MCPDisableScope = "session"
)

// MCPDisableSources is the disabled-server preference split by provenance, which
// the merged MoaConfig loses. Loaded once so startup, the controller, and the
// UI all agree on which scope vetoes a server.
type MCPDisableSources struct {
	// Global lists names vetoed by the global config.
	Global []string
	// Project lists names this user vetoed for this workspace. It comes from
	// their own project state, not from <cwd>/.moa/config.json: switching a
	// server off is a preference, while the project file declares which servers
	// exist and is meant to be committed.
	Project []string
	// ProjectTrusted reports whether <cwd>/.moa/config.json is trusted, i.e.
	// whether the project's own MCP definitions are loaded at all.
	ProjectTrusted bool
}

// LoadedMoaConfig is the merged config plus the disabled-server provenance the
// merge discards. LoadMoaConfig remains the simple entry point (returns .Config);
// scope-aware callers use LoadMoaConfigResolved.
type LoadedMoaConfig struct {
	Config      MoaConfig
	MCPDisabled MCPDisableSources
}

// LoadMoaConfigResolved loads and merges config exactly like LoadMoaConfig, but
// also returns the disabled-server preference split by scope. The project list
// is included only when cwd is a trusted project path — matching the trust gate
// that governs whether the project config is merged at all.
func LoadMoaConfigResolved(cwd string) LoadedMoaConfig {
	global := loadConfigFile(globalConfigPath())

	sources := MCPDisableSources{
		Global:         dedupeStrings(global.DisabledMCPServers),
		ProjectTrusted: IsProjectPathTrusted(global, cwd),
	}
	// The project veto is the user's own, so it applies whether or not the
	// project is trusted: trust governs reading the project's files, and this
	// preference no longer lives in one.
	//
	// A read failure is logged rather than ignored: dropping a veto silently
	// starts a server the user had switched off, which is the one outcome they
	// would not expect from an unreadable file.
	if state, err := LoadProjectState(cwd); err != nil {
		slog.Warn("project state: cannot read your MCP preferences; servers you disabled may start",
			"error", err)
	} else {
		sources.Project = dedupeStrings(state.DisabledMCPServers)
	}
	// A veto written into a trusted project's config by an older moa still
	// counts. Moa no longer puts one there, but silently starting a server
	// somebody had switched off is the wrong way to announce that.
	if sources.ProjectTrusted {
		legacy := loadConfigFile(filepath.Join(cwd, ".moa", "config.json")).DisabledMCPServers
		if len(legacy) > 0 {
			sources.Project = dedupeStrings(append(sources.Project, legacy...))
		}
	}

	return LoadedMoaConfig{
		Config:      LoadMoaConfig(cwd),
		MCPDisabled: sources,
	}
}

// MCPDisablePolicy is the resolved veto sets for one session, across all three
// scopes. The session set is process-lifetime and never persisted.
type MCPDisablePolicy struct {
	Global  map[string]struct{}
	Project map[string]struct{}
	Session map[string]struct{}
}

// NewMCPDisablePolicy builds a policy from loaded config sources. The session
// set starts empty; callers add to it at runtime.
func NewMCPDisablePolicy(sources MCPDisableSources) MCPDisablePolicy {
	return MCPDisablePolicy{
		Global:  toSet(sources.Global),
		Project: toSet(sources.Project),
		Session: map[string]struct{}{},
	}
}

// MCPDisableResolution is the outcome of resolving one server against a policy.
type MCPDisableResolution struct {
	// Disabled is true if any scope vetoes the server.
	Disabled bool
	// Scopes lists every scope that vetoes it, in stable order (global,
	// project, session). Empty when the server is enabled.
	Scopes []MCPDisableScope
}

// ResolveMCPDisabled resolves whether a server is disabled for a session. Vetoes
// accumulate: disabled = global OR project OR session. No scope can re-enable a
// veto from another scope, so the result reports every applicable scope, which
// lets the UI explain why a server stays disabled after one scope is cleared.
func ResolveMCPDisabled(name string, p MCPDisablePolicy) MCPDisableResolution {
	var scopes []MCPDisableScope
	if _, ok := p.Global[name]; ok {
		scopes = append(scopes, MCPScopeGlobal)
	}
	if _, ok := p.Project[name]; ok {
		scopes = append(scopes, MCPScopeProject)
	}
	if _, ok := p.Session[name]; ok {
		scopes = append(scopes, MCPScopeSession)
	}
	return MCPDisableResolution{Disabled: len(scopes) > 0, Scopes: scopes}
}

// DisabledSet returns the names disabled for a session across all scopes, ready
// to hand to Manager.Start as initiallyDisabled.
func (p MCPDisablePolicy) DisabledSet() map[string]bool {
	out := map[string]bool{}
	for name := range p.Global {
		out[name] = true
	}
	for name := range p.Project {
		out[name] = true
	}
	for name := range p.Session {
		out[name] = true
	}
	return out
}

// unionStrings returns the deduplicated union of two string slices in stable
// order (all of a's distinct values first, then b's new ones). It never returns
// an empty non-nil slice, so an all-empty union drops a JSON field with
// omitempty rather than serializing [].
func unionStrings(a, b []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range append(append([]string{}, a...), b...) {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// dedupeStrings removes empties and duplicates, preserving first-seen order.
func dedupeStrings(in []string) []string {
	return unionStrings(in, nil)
}

// toSet converts a slice to a membership set.
func toSet(in []string) map[string]struct{} {
	out := make(map[string]struct{}, len(in))
	for _, s := range in {
		if s != "" {
			out[s] = struct{}{}
		}
	}
	return out
}

// SetMCPServerDisabled adds or removes a server name from a config's disabled
// list, keeping it sorted and deduplicated. Removing the last entry sets the
// slice to nil so omitempty drops the field. It is the read-modify-write body
// callers pass to SaveGlobalConfig / SaveProjectConfig.
func SetMCPServerDisabled(cfg *MoaConfig, name string, disabled bool) {
	if name == "" {
		return
	}
	set := toSet(cfg.DisabledMCPServers)
	if disabled {
		set[name] = struct{}{}
	} else {
		delete(set, name)
	}
	if len(set) == 0 {
		cfg.DisabledMCPServers = nil
		return
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	cfg.DisabledMCPServers = out
}
