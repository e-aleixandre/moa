package subagent

import (
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// Children inherit the compaction threshold in force in the parent session.
// Before this they were built with no threshold at all, so a delegated task ran
// to the brim of the model window while the session that spawned it compacted
// early — the child is a separate agent, nothing reaches it implicitly.
func TestNewChildAgentInheritsCompactAt(t *testing.T) {
	newChild := func(t *testing.T, cfg Config) int {
		t.Helper()
		child, err := newChildAgent(
			cfg, newMockProvider(textResponse("hi")), core.Model{ID: "m", Provider: "mock"},
			"medium", 0, "sys", core.NewRegistry(), "job-test",
		)
		if err != nil {
			t.Fatal(err)
		}
		return child.EffectiveCompactAt()
	}

	t.Run("inherits the resolved parent threshold", func(t *testing.T) {
		cfg := Config{InheritedCompactAt: func() int { return 120_000 }}
		if got := newChild(t, cfg); got != 120_000 {
			t.Fatalf("child EffectiveCompactAt = %d, want 120000 inherited from the parent", got)
		}
	})

	t.Run("no threshold anywhere leaves the child on the model window", func(t *testing.T) {
		if got := newChild(t, Config{}); got != 0 {
			t.Fatalf("child EffectiveCompactAt = %d, want 0 (model window)", got)
		}
	})

	t.Run("inherited value is a default, never the child's own choice", func(t *testing.T) {
		cfg := Config{InheritedCompactAt: func() int { return 90_000 }}
		child, err := newChildAgent(
			cfg, newMockProvider(textResponse("hi")), core.Model{ID: "m", Provider: "mock"},
			"medium", 0, "sys", core.NewRegistry(), "job-test",
		)
		if err != nil {
			t.Fatal(err)
		}
		if got := child.CompactAt(); got != 0 {
			t.Fatalf("child CompactAt = %d, want 0: an inherited value must not read back as a per-session setting", got)
		}
	})

	t.Run("read at spawn time, not captured at session start", func(t *testing.T) {
		current := 50_000
		cfg := Config{InheritedCompactAt: func() int { return current }}
		if got := newChild(t, cfg); got != 50_000 {
			t.Fatalf("child EffectiveCompactAt = %d, want 50000", got)
		}
		current = 150_000
		if got := newChild(t, cfg); got != 150_000 {
			t.Fatalf("child EffectiveCompactAt = %d, want 150000 after the parent moved its limit", got)
		}
	})
}
