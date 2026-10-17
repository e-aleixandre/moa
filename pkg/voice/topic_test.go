package voice

import (
	"strings"
	"testing"
)

// When the owner brings up a topic, the delegate recovers its context before
// giving an opinion, and a recorded constraint is never offered back as an
// open option (Cellaria call, 17/10). The voice model and the handoff backend
// both get the rule.
func TestTopicContextRuleReachesVoiceAndBackend(t *testing.T) {
	for name, prompt := range map[string]string{"common": CommonInstructions, "handoff": HandoffInstructions} {
		if !strings.Contains(prompt, InstructionTopicContext) {
			t.Errorf("%s instructions lack the topic-context rule", name)
		}
	}
	for _, want := range []string{"book_read", "ask_session", "not reopened", "still to decide", "rules out"} {
		if !strings.Contains(InstructionTopicContext, want) {
			t.Errorf("topic-context rule lacks %q", want)
		}
	}
}
