package serve

import (
	"testing"

	"github.com/e-aleixandre/moa/pkg/bus"
)

// TestApplyDefaultCompactAtReachesLoadedSessions covers the second half of the
// production bug: saving config.json only affects sessions built afterwards.
// The owner's server had been up for days with resident sessions, so a new
// threshold never reached the conversations he was actually using — one
// subagent ran to 37% of a 1M window ($29) without compacting.
func TestApplyDefaultCompactAtReachesLoadedSessions(t *testing.T) {
	mgr := newTestManager(t, t.Context(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	// The session must not gain an explicit choice of its own: the global
	// default is what changed, and it has to keep tracking future changes.
	before, err := bus.QueryTyped[bus.GetCompactAt, int](sess.runtime.Bus, bus.GetCompactAt{})
	if err != nil {
		t.Fatal(err)
	}

	if eff, err := bus.QueryTyped[bus.GetEffectiveCompactAt, int](sess.runtime.Bus, bus.GetEffectiveCompactAt{}); err != nil {
		t.Fatal(err)
	} else if eff != 0 {
		t.Fatalf("precondition: effective compact_at = %d, want 0", eff)
	}

	mgr.applyDefaultCompactAt(300_000)

	eff, err := bus.QueryTyped[bus.GetEffectiveCompactAt, int](sess.runtime.Bus, bus.GetEffectiveCompactAt{})
	if err != nil {
		t.Fatal(err)
	}
	if eff != 300_000 {
		t.Fatalf("effective compact_at = %d, want 300000: a session already loaded must pick up the new global threshold, not keep the one captured when it was built", eff)
	}

	after, err := bus.QueryTyped[bus.GetCompactAt, int](sess.runtime.Bus, bus.GetCompactAt{})
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("session compact_at changed from %d to %d: a global default must not become the session's own choice", before, after)
	}
}
