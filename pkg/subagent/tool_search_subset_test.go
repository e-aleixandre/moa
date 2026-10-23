package subagent

import (
	"context"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// The parent sees tool_search among its tools and may name it in a child's
// subset; the child gets its own from the MCP tools it receives.
func TestChildSubsetMayNameToolSearch(t *testing.T) {
	parent := core.NewRegistry()
	click := "mcp__playwright__browser_click"
	if err := parent.Register(core.Tool{Name: click, Deferrable: true, Parameters: []byte(`{"type":"object"}`),
		Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) { return core.TextResult("ok"), nil }}); err != nil {
		t.Fatal(err)
	}
	reg, errRes := buildChildRegistry(parent, map[string]any{"tools": []any{core.ToolSearchName, click}})
	if errRes != nil {
		t.Fatalf("subset with tool_search refused: %+v", errRes)
	}
	if got, ok := reg.Get(click); !ok || !got.Deferrable {
		t.Fatalf("child lacks the deferrable MCP tool: %+v", got)
	}
	if _, ok := reg.Get(core.ToolSearchName); ok {
		t.Fatal("tool_search must not be registered in the child")
	}
}
