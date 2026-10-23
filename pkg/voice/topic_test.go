package voice

import (
	"crypto/sha256"
	"fmt"
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

// The backend text model must not inherit Live's debate/delegation identity:
// its rendered prompt stays byte-identical to the pre-debate value (sha256 of
// HandoffInstructions at 1177c392).
func TestHandoffInstructionsPinnedToBaseRendering(t *testing.T) {
	const want = "b6c636fda9772f302f42640a9e62307ccbfc86c0d50ab218c079777c1e4a519d"
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(HandoffInstructions))); got != want {
		t.Errorf("HandoffInstructions changed: sha256 %s, want %s", got, want)
	}
	for _, liveOnly := range []string{"thinking partner", "give your own view", "ask back", "Delegate a backend lookup only when"} {
		if strings.Contains(HandoffInstructions, liveOnly) {
			t.Errorf("handoff carries Live-only guidance %q", liveOnly)
		}
	}
}
