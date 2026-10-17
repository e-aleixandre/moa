package serve

import (
	"strings"
	"testing"
)

// The Cellaria brief listed "el contacto del restaurante" as a pending
// decision although a recorded decision already excluded identities. The
// brief and the topic-context answer give constraints as settled.
func TestVoiceSessionPromptsKeepConstraintsSettled(t *testing.T) {
	for name, prompt := range map[string]string{"brief": voiceBriefPrompt, "question": voiceQuestionPrompt("Topic context for Cellaria")} {
		if !strings.Contains(prompt, "never present something a decision already rules out as") {
			t.Errorf("%s prompt does not keep constraints settled:\n%s", name, prompt)
		}
	}
	q := voiceQuestionPrompt("Topic context for Cellaria")
	for _, want := range []string{"goal", "not reopened", "data or documents", "still to decide"} {
		if !strings.Contains(q, want) {
			t.Errorf("question prompt lacks the topic-context format %q", want)
		}
	}
}
