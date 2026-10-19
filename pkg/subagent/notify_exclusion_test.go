package subagent

import (
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// News is the parent's report to the user: a child never inherits notify,
// whether it takes every tool or names it explicitly.
func TestNotifyIsNotInheritedByChildren(t *testing.T) {
	parent := core.NewRegistry()
	_ = parent.Register(core.Tool{Name: "read"})
	_ = parent.Register(core.Tool{Name: "notify"})
	for name, params := range map[string]map[string]any{
		"default inheritance":    nil,
		"explicit allowed tools": {"tools": []any{"read", "notify"}},
	} {
		t.Run(name, func(t *testing.T) {
			reg, errRes := buildChildRegistry(parent, params)
			if errRes != nil {
				t.Fatalf("unexpected error: %s", textOf(*errRes))
			}
			if _, ok := reg.Get("read"); !ok {
				t.Fatal("control: child lost its ordinary tools")
			}
			if _, ok := reg.Get("notify"); ok {
				t.Error("child registry inherited notify")
			}
		})
	}
}
