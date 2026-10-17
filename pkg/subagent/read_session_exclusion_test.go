package subagent

import (
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// A child never inherits read_session: its grants belong to the parent's human
// conversation. The sentinel tool only populates the registry so the REAL
// buildChildRegistry filter is what is under test; the control checks prove
// the parent has it and the child still gets its other tools.
func TestReadSessionIsNotInheritedByChildren(t *testing.T) {
	parent := core.NewRegistry()
	_ = parent.Register(core.Tool{Name: "read"})
	_ = parent.Register(core.Tool{Name: "grep"})
	_ = parent.Register(core.Tool{Name: "read_session"})
	if _, ok := parent.Get("read_session"); !ok {
		t.Fatal("control: parent registry lacks read_session")
	}

	for name, params := range map[string]map[string]any{
		"default inheritance":    nil,
		"explicit allowed tools": {"tools": []any{"read", "read_session", "grep"}},
		"explicit mixed casing":  {"tools": []any{"Read", "READ_SESSION"}},
	} {
		t.Run(name, func(t *testing.T) {
			reg, errRes := buildChildRegistry(parent, params)
			if errRes != nil {
				t.Fatalf("unexpected error: %s", textOf(*errRes))
			}
			if _, ok := reg.Get("read"); !ok {
				t.Fatal("control: child lost its ordinary tools")
			}
			if _, ok := reg.Get("read_session"); ok {
				t.Error("child registry inherited read_session")
			}
		})
	}
}
