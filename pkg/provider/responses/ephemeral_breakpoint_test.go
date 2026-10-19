package responses

import (
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// A request-only block (the open-questions and news inventory) is gone from
// the next request, so a breakpoint on it would mark a prefix nobody reads
// again. The ordinary user messages keep theirs.
func TestEphemeralUserMessageGetsNoCacheBreakpoint(t *testing.T) {
	eph := core.NewUserMessage("<open_questions>q</open_questions>")
	eph.Ephemeral = true
	req := core.Request{
		Model:    core.Model{ID: "gpt-5.6-terra"},
		System:   "sys",
		Messages: []core.Message{core.NewUserMessage("hello"), eph},
	}
	body := decodeBody(t, req, gpt56Dialect())
	input, _ := body["input"].([]any)
	if len(input) != 3 {
		t.Fatalf("input len = %d, want developer + 2 users", len(input))
	}
	part := func(i int) map[string]any {
		user, _ := input[i].(map[string]any)
		parts, _ := user["content"].([]any)
		p, _ := parts[0].(map[string]any)
		return p
	}
	if breakpointMode(part(1)) == "" {
		t.Fatalf("the ordinary user message lost its breakpoint: %v", part(1))
	}
	if _, ok := part(2)["prompt_cache_breakpoint"]; ok {
		t.Fatalf("the request-only block carried a breakpoint: %v", part(2))
	}
}
