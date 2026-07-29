package mcp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

// newTestController starts a manager for the given servers and wraps it in a
// Controller whose tools are registered into a fresh registry. It returns the
// controller, the registry, and a counter of prompt refreshes.
func newTestController(t *testing.T, servers map[string]core.MCPServer, initiallyDisabled map[string]bool, policy core.MCPDisablePolicy) (*Controller, *core.Registry, *int) {
	t.Helper()
	mgr := NewManager(nil, "")
	mgr.Start(context.Background(), servers, initiallyDisabled)
	reg := core.NewRegistry()
	for _, tl := range mgr.Tools() {
		core.RegisterOrLog(reg, tl)
	}
	refreshes := 0
	c := NewController(ControllerConfig{
		Manager:       mgr,
		Registry:      reg,
		Policy:        policy,
		RefreshPrompt: func() { refreshes++ },
	})
	t.Cleanup(c.Close)
	return c, reg, &refreshes
}

func regHasPrefix(reg *core.Registry, prefix string) int {
	n := 0
	for _, spec := range reg.Specs() {
		if strings.HasPrefix(spec.Name, prefix) {
			n++
		}
	}
	return n
}

// TestControllerResyncLongServerName is the regression test for Terra's finding:
// a server name long enough that sanitizeToolName truncates+hashes it. Prefix
// matching (ServerToolPrefix + HasPrefix) fails there, but the Controller
// re-syncs by exact tool name, so disabling must actually drop the tool.
func TestControllerResyncLongServerName(t *testing.T) {
	longName := strings.Repeat("srv", 30) // 90 chars, well over the 64 cap
	if len(ServerToolPrefix(longName)) != 64 {
		t.Fatalf("precondition: expected a truncated 64-char prefix, got %d", len(ServerToolPrefix(longName)))
	}

	c, reg, _ := newTestController(t, map[string]core.MCPServer{
		longName: helperServerConfig(""),
	}, nil, core.MCPDisablePolicy{})

	if !waitForState(t, c.mgr, longName, StateReady, 5*time.Second) {
		t.Fatal("server not ready")
	}
	// Confirm the prefix approach WOULD miss this tool (documents why we index
	// by exact name).
	if regHasPrefix(reg, ServerToolPrefix(longName)) != 0 {
		t.Fatal("precondition: prefix unexpectedly matched; test no longer exercises the long-name path")
	}
	// The tool is nonetheless registered (by its real, hashed name).
	if got := reg.Count(); got != 1 {
		t.Fatalf("registry has %d tools, want 1", got)
	}

	// Disable via policy: the Controller must remove the tool by exact name.
	c.SetPolicy(context.Background(), core.MCPDisablePolicy{
		Session: map[string]struct{}{longName: {}},
	})
	if got := reg.Count(); got != 0 {
		t.Fatalf("after disable, registry has %d tools, want 0 (prefix re-sync would have leaked it)", got)
	}
}

func TestControllerDisableEnableReconcile(t *testing.T) {
	c, reg, refreshes := newTestController(t, map[string]core.MCPServer{
		"ping": helperServerConfig(""),
	}, nil, core.MCPDisablePolicy{})

	if !waitForState(t, c.mgr, "ping", StateReady, 5*time.Second) {
		t.Fatal("not ready")
	}
	if reg.Count() != 1 {
		t.Fatalf("registry = %d, want 1", reg.Count())
	}
	before := *refreshes

	// Disable.
	c.SetPolicy(context.Background(), core.MCPDisablePolicy{
		Session: map[string]struct{}{"ping": {}},
	})
	if reg.Count() != 0 {
		t.Fatalf("after disable registry = %d, want 0", reg.Count())
	}
	if *refreshes <= before {
		t.Fatal("prompt refresh should fire when the tool set changes")
	}

	// Status reflects desired-disabled + the scope.
	for _, st := range c.Status() {
		if st.Name == "ping" {
			if st.DesiredEnabled {
				t.Fatal("ping should be desired-disabled")
			}
			if len(st.DisabledScopes) != 1 || st.DisabledScopes[0] != core.MCPScopeSession {
				t.Fatalf("scopes = %v, want [session]", st.DisabledScopes)
			}
		}
	}

	// Re-enable.
	c.SetPolicy(context.Background(), core.MCPDisablePolicy{})
	if !waitForState(t, c.mgr, "ping", StateReady, 5*time.Second) {
		t.Fatal("not ready after re-enable")
	}
	if reg.Count() != 1 {
		t.Fatalf("after enable registry = %d, want 1", reg.Count())
	}
}

func TestControllerReconcileIdempotent(t *testing.T) {
	c, _, refreshes := newTestController(t, map[string]core.MCPServer{
		"ping": helperServerConfig(""),
	}, nil, core.MCPDisablePolicy{})
	if !waitForState(t, c.mgr, "ping", StateReady, 5*time.Second) {
		t.Fatal("not ready")
	}
	// Reconcile with an unchanged (empty) policy: nothing to do, no refresh.
	before := *refreshes
	c.Reconcile(context.Background())
	if *refreshes != before {
		t.Fatal("no-op reconcile should not refresh the prompt")
	}
}

func TestControllerRestartRefusesDisabled(t *testing.T) {
	c, _, _ := newTestController(t, map[string]core.MCPServer{
		"ping": helperServerConfig(""),
	}, map[string]bool{"ping": true}, core.MCPDisablePolicy{
		Session: map[string]struct{}{"ping": {}},
	})
	if _, err := c.Restart(context.Background(), "ping"); err == nil {
		t.Fatal("restart of a disabled server should fail")
	}
}

func TestControllerStartsDisabledFromPolicy(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	// Server is initially disabled (bootstrap would compute this from policy).
	c, reg, _ := newTestController(t, map[string]core.MCPServer{
		"ping": helperServerConfig(pidFile),
	}, map[string]bool{"ping": true}, core.MCPDisablePolicy{
		Session: map[string]struct{}{"ping": {}},
	})

	// No tools, no process, status is disabled + desired-disabled.
	if reg.Count() != 0 {
		t.Fatalf("registry = %d, want 0", reg.Count())
	}
	found := false
	for _, st := range c.Status() {
		if st.Name == "ping" {
			found = true
			if st.State != StateDisabled || st.DesiredEnabled {
				t.Fatalf("status = %+v, want disabled + desired-disabled", st)
			}
		}
	}
	if !found {
		t.Fatal("disabled server missing from Status")
	}
}
