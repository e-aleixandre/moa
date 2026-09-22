package serve

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/e-aleixandre/moa/pkg/session"
)

// The budget is pinned by synthetic tails elsewhere, but the failure happened
// on a real conversation: two days of Spanish, code, paths and one-word turns,
// which no generated text imitates faithfully. This runs the real trimming over
// a real saved session when one is pointed at, and asserts the payload it would
// send is within budget. It never reaches the network and never writes to the
// session directory.
//
//	MOA_VOICE_LIVE_SESSIONS_DIR=~/.config/moa/sessions \
//	MOA_VOICE_LIVE_SESSION_ID=<id> go test ./pkg/serve/ -run RealSession -v
//
// MOA_VOICE_LIVE_DUMP=<path> additionally writes the exact tail as JSON, so it
// can be counted with the provider's tokenizer outside the test.
func TestVoiceLiveInputFitsARealSession(t *testing.T) {
	dir, id := os.Getenv("MOA_VOICE_LIVE_SESSIONS_DIR"), os.Getenv("MOA_VOICE_LIVE_SESSION_ID")
	if dir == "" || id == "" {
		t.Skip("set MOA_VOICE_LIVE_SESSIONS_DIR and MOA_VOICE_LIVE_SESSION_ID to run against a real session")
	}
	// Read-only on purpose: FindSessionReadOnly neither initialises state nor
	// migrates a legacy file, so pointing this at a live config cannot alter it.
	saved, _, err := session.FindSessionReadOnly(dir, id)
	if err != nil {
		t.Fatalf("the session could not be read: %v", err)
	}
	projection, _, err := (&Manager{}).savedConversationMessages(saved)
	if err != nil {
		t.Fatalf("the conversation could not be projected: %v", err)
	}
	eligible := 0
	for _, message := range projection.messages {
		if message.Role == "user" || message.Role == "assistant" {
			eligible++
		}
	}
	input := voiceLiveInput(projection.messages, "")
	cost, bytes := voiceLiveInputCost(input), 0
	for _, item := range input {
		bytes += len(item.Content[0].Text)
	}
	t.Logf("session %s: %d projected items, %d eligible turns; tail = %d messages, %d bytes, %d estimated tokens of a %d budget and a %d limit",
		id, len(projection.messages), eligible, len(input), bytes, cost, voiceLiveInputBudget, voiceLiveInputTokens)
	if cost > voiceLiveInputBudget {
		t.Fatalf("the real tail costs %d tokens, over the %d budget", cost, voiceLiveInputBudget)
	}
	if len(input) > voiceLiveInputMessages {
		t.Fatalf("the real tail carries %d messages, over the %d cap", len(input), voiceLiveInputMessages)
	}
	if eligible > 0 && len(input) == 0 {
		t.Fatal("a conversation with turns produced an empty tail")
	}
	if dump := os.Getenv("MOA_VOICE_LIVE_DUMP"); dump != "" {
		items := make([]map[string]string, 0, len(input))
		for _, item := range input {
			items = append(items, map[string]string{"role": item.Role, "text": item.Content[0].Text})
		}
		encoded, err := json.Marshal(items)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dump, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
