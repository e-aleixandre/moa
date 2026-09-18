package responses

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func daybreakTurn() core.Message {
	return core.Message{
		Role:     "assistant",
		Provider: "openai",
		// What the provider reports for a gpt-daybreak-blue-latest request.
		Model: "gpt-5.6-sol",
		Content: []core.Content{
			{Type: "text", Text: "checking", TextSignature: "msg_real_1"},
			{Type: "tool_call", ToolCallID: "call_1", ToolName: "bash",
				ToolCallItemID: "fc_real_1", Arguments: map[string]any{}},
			{Type: "thinking", ThinkingSignature: `{"type":"reasoning","id":"rs_1","encrypted_content":"ENC"}`},
		},
	}
}

// An alias's own history must replay in full. The provider answers a Daybreak
// request under gpt-5.6-sol, so comparing ids literally made every previous
// turn look cross-model: the encrypted reasoning was dropped and the real item
// ids replaced, which OpenAI documents as a cause of early stopping.
func TestAliasReplaysItsOwnReasoning(t *testing.T) {
	items := convertAssistantMessageForDialect(daybreakTurn(), "openai", "gpt-daybreak-blue-latest", 7)
	got, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(got)
	if !strings.Contains(s, "encrypted_content") {
		t.Errorf("alias dropped its own encrypted reasoning: %s", s)
	}
	if !strings.Contains(s, "fc_real_1") {
		t.Errorf("alias dropped its own function_call id: %s", s)
	}
	if !strings.Contains(s, "msg_real_1") {
		t.Errorf("alias dropped its own message id: %s", s)
	}
}

// The relation is one-way: asking for the target must not absorb state
// produced for the alias. They are different products, and a real provider
// fallback has to stay visible.
func TestTargetDoesNotAdoptAliasState(t *testing.T) {
	msg := daybreakTurn()
	msg.Model = "gpt-daybreak-blue-latest"
	items := convertAssistantMessageForDialect(msg, "openai", "gpt-5.6-sol", 7)
	got, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if s := string(got); strings.Contains(s, "encrypted_content") {
		t.Errorf("gpt-5.6-sol replayed the alias's reasoning: %s", s)
	}
}

// A genuine cross-model switch must still discard model-bound state.
func TestUnrelatedModelStillForeign(t *testing.T) {
	items := convertAssistantMessageForDialect(daybreakTurn(), "openai", "gpt-6-astra", 7)
	got, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(got)
	if strings.Contains(s, "encrypted_content") {
		t.Errorf("cross-model replay kept encrypted reasoning: %s", s)
	}
	if strings.Contains(s, "fc_real_1") {
		t.Errorf("cross-model replay kept the fc_ id: %s", s)
	}
}
