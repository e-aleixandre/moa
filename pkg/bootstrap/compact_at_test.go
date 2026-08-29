package bootstrap

import (
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// The global threshold reaches a freshly built session. A session's own value
// is applied on top afterwards (SetCompactAt on resume), so what is asserted
// here is that the global one arrives as the DEFAULT and leaves CompactAt free
// for the session's own choice.
func TestBuildSession_GlobalCompactAt(t *testing.T) {
	cfg := minimalConfig(t)
	if err := core.SaveGlobalConfig(func(c *core.MoaConfig) { c.CompactAt = 90_000 }); err != nil {
		t.Fatal(err)
	}
	sess, err := BuildSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := sess.Agent.EffectiveCompactAt(); got != 90_000 {
		t.Fatalf("EffectiveCompactAt = %d, want the global 90000", got)
	}
	if got := sess.Agent.CompactAt(); got != 0 {
		t.Fatalf("CompactAt = %d, want 0: the global value is not the session's own setting", got)
	}

	// A session that sets its own threshold keeps it: the global is only the
	// fallback, and this is the resume path (SetCompactAt after build).
	if err := sess.Agent.SetCompactAt(130_000); err != nil {
		t.Fatal(err)
	}
	if got := sess.Agent.EffectiveCompactAt(); got != 130_000 {
		t.Fatalf("EffectiveCompactAt = %d, want the session's own 130000 to win over the global", got)
	}
}

func TestBuildSession_NoGlobalCompactAt(t *testing.T) {
	sess, err := BuildSession(minimalConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := sess.Agent.EffectiveCompactAt(); got != 0 {
		t.Fatalf("EffectiveCompactAt = %d, want 0 (compact at the model window)", got)
	}
}

// What a subagent inherits: the parent's own threshold when it set one, the
// global default otherwise. Before this, children were spawned with no
// threshold at all regardless of the session that opened them.
func TestInheritedCompactAt(t *testing.T) {
	sess, err := BuildSession(minimalConfig(t))
	if err != nil {
		t.Fatal(err)
	}

	if got := inheritedCompactAt(sess, 80_000); got != 80_000 {
		t.Fatalf("with no parent threshold the child should inherit the global: got %d, want 80000", got)
	}

	if err := sess.Agent.SetCompactAt(150_000); err != nil {
		t.Fatal(err)
	}
	if got := inheritedCompactAt(sess, 80_000); got != 150_000 {
		t.Fatalf("the parent's own threshold must win over the global: got %d, want 150000", got)
	}

	if err := sess.Agent.SetCompactAt(0); err != nil {
		t.Fatal(err)
	}
	if got := inheritedCompactAt(sess, 0); got != 0 {
		t.Fatalf("no threshold anywhere should leave the child on the model window: got %d", got)
	}
}
