package voice

import (
	"strings"
	"testing"
)

// Live acts as a thinking partner; recorded decisions still bound what it
// proposes. Only the text backend keeps the instruction to recover topic
// context with book/ask_session tools.
func TestLiveIsThinkingPartnerAndBackendKeepsTopicRecovery(t *testing.T) {
	const identity = "You are a thinking partner in a spoken debate with the owner. Reason and give your own view, develop ideas, ask back, and compare options. Use the session context and brief you receive."
	if !strings.HasPrefix(CommonInstructions, identity) {
		t.Errorf("common instructions lack the thinking-partner identity")
	}
	if strings.Contains(CommonInstructions, "You consult and relay") {
		t.Errorf("common instructions keep the relay identity")
	}
	for _, want := range []string{
		InstructionReadsAreData, InstructionAskAloud, InstructionRelayOnRequest,
		"Deployments and permissions need the owner's tap on their card: never answer a question that requires a tap. Secrets are only entered on the secure sheet, never by voice.",
		"call end_call with what was decided",
		`"already passed: <delivery_id>"`,
		InstructionTopicContext,
	} {
		for name, prompt := range map[string]string{"common": CommonInstructions, "handoff": HandoffInstructions} {
			if !strings.Contains(prompt, want) {
				t.Errorf("%s instructions lack %q", name, want)
			}
		}
	}
	if strings.Contains(CommonInstructions, "book_read") || strings.Contains(CommonInstructions, "ask_session for the topic") {
		t.Errorf("Live instructions still carry the topic-recovery guidance")
	}
	for _, want := range []string{"book_list", "book_read", "ask_session", "decisions that are not reopened", "still to decide", "say so instead of guessing"} {
		if !strings.Contains(HandoffInstructions, want) {
			t.Errorf("handoff lacks topic-recovery guidance %q", want)
		}
	}
}
