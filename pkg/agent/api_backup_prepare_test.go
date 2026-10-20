package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// Preparing an API-backup dispatch saves the turn before the paid request but
// adds nothing to the conversation: the status line is the only signal.
func TestPrepareBackupSavesWithoutAConversationRecord(t *testing.T) {
	var events []string
	var prepared int
	provider := NewMockProvider(func(req core.Request) (<-chan core.AssistantEvent, error) {
		for i := 0; i < 2; i++ { // a retry inside one request prepares once
			if err := req.Options.OnProviderPrepare(context.Background(), core.ProviderSource{Kind: "api_backup"}); err != nil {
				t.Errorf("prepare: %v", err)
			}
		}
		return simpleTextResponse("ok")(req)
	})
	ag := newTestAgent(provider)
	ag.SetProviderWaitSave((&recordingSave{events: &events}).save)
	var mu sync.Mutex
	var live []core.AgentEvent
	ag.Subscribe(func(e core.AgentEvent) {
		if e.Type == core.AgentEventUserMessage {
			mu.Lock()
			live = append(live, e)
			mu.Unlock()
		}
	})
	if _, err := ag.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	for _, m := range ag.Messages() {
		if m.Role == "session_event" || m.Custom["type"] == "provider_source_note" {
			prepared++
		}
	}
	if prepared != 0 {
		t.Fatalf("%d session_event records after preparing the backup, want 0", prepared)
	}
	for _, e := range live {
		if e.Message.Role == "session_event" {
			t.Fatalf("live session_event %q emitted, want none", e.Message.Custom["type"])
		}
	}
	if len(events) != 1 || events[0] != "save" {
		t.Fatalf("events = %v, want exactly one save before the paid request", events)
	}
}
