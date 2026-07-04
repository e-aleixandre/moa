package tool

import (
	"bytes"
	"strings"
	"sync"
)

// maxEnvCapture caps the size of a captured env dump. If the exported
// environment exceeds this, the prior snapshot is kept instead — protects
// against pathological exports bloating memory.
const maxEnvCapture = 256 << 10 // 256 KB

// envDenylist names variables that must not be persisted. PWD/OLDPWD would
// contradict cmd.Dir, SHLVL would keep climbing, and "_" is the last-command
// hack — bash regenerates all of these. BASH_ENV/ENV are startup files bash -c
// sources before running the command: persisting one would execute arbitrary
// code on every future call, before the trap installs — a corruption a real
// interactive shell never suffers. Exported bash functions (BASH_FUNC_* keys)
// are stripped by prefix in parseNullSepEnv for the same reason (they could
// shadow the trap's own commands).
var envDenylist = map[string]bool{
	"PWD":      true,
	"OLDPWD":   true,
	"SHLVL":    true,
	"_":        true,
	"BASH_ENV": true,
	"ENV":      true,
}

// shellSnapshot is one agent's persisted shell state.
type shellSnapshot struct {
	cwd string   // "" until first capture
	env []string // nil until first capture; "K=V" entries
}

// BashState holds per-agent shell state (cwd + exported env) persisted across
// bash tool calls within one session. Keyed by agentID (read from ctx); "" is
// the root/parent agent. Subagents get an isolated copy seeded from their
// parent (subshell semantics: child changes never propagate back).
type BashState struct {
	mu     sync.Mutex
	states map[string]*shellSnapshot // agentID → snapshot
}

// NewBashState returns an empty BashState.
func NewBashState() *BashState {
	return &BashState{states: make(map[string]*shellSnapshot)}
}

// Snapshot returns the persisted cwd and a copy of the env for the given agent
// (nil-safe: unknown agent → "", nil).
func (s *BashState) Snapshot(agentID string) (cwd string, env []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap, ok := s.states[agentID]
	if !ok || snap == nil {
		return "", nil
	}
	return snap.cwd, cloneEnv(snap.env)
}

// Update replaces the given agent's persisted state after a successful capture.
func (s *BashState) Update(agentID, cwd string, env []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[agentID] = &shellSnapshot{cwd: cwd, env: cloneEnv(env)}
}

// Seed copies the parent agent's current snapshot into childID (called when a
// subagent starts). No-op if the parent has no snapshot yet.
func (s *BashState) Seed(childID, parentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	parent, ok := s.states[parentID]
	if !ok || parent == nil {
		return
	}
	s.states[childID] = &shellSnapshot{cwd: parent.cwd, env: cloneEnv(parent.env)}
}

// Drop removes an agent's snapshot (called when a subagent job finishes) so the
// map doesn't grow unbounded across many subagents.
func (s *BashState) Drop(agentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.states, agentID)
}

// cloneEnv returns a defensive copy of env (nil stays nil) so callers can't
// mutate the snapshot held under the mutex.
func cloneEnv(env []string) []string {
	if env == nil {
		return nil
	}
	out := make([]string, len(env))
	copy(out, env)
	return out
}

// parseNullSepEnv parses the NUL-separated output of `env -0` into "K=V"
// entries, dropping empty records, records without '=', and denylisted names.
func parseNullSepEnv(raw []byte) []string {
	parts := bytes.Split(raw, []byte{0})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if len(p) == 0 {
			continue
		}
		entry := string(p)
		eq := strings.IndexByte(entry, '=')
		if eq <= 0 { // no '=', or empty key
			continue
		}
		key := entry[:eq]
		// Exported bash functions surface as BASH_FUNC_name%%=() {...}; drop the
		// whole family by prefix (see envDenylist doc for why).
		if envDenylist[key] || strings.HasPrefix(key, "BASH_FUNC_") {
			continue
		}
		out = append(out, entry)
	}
	return out
}
