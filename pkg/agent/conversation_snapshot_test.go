package agent

import (
	"context"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
)

func TestConversationSnapshotRestoresEphemeralMessages(t *testing.T) {
	ag := newTestAgent(&alwaysProvider{text: "ok"})
	original := []core.AgentMessage{{Message: core.Message{Role: "user", Content: []core.Content{core.TextContent("real")}}}}
	if err := ag.LoadState(original, 4); err != nil {
		t.Fatal(err)
	}
	before, epoch := ag.SnapshotConversation()
	if _, err := ag.SendWithCustom(context.Background(), "internal", map[string]any{"source": "prepare_compact"}); err != nil {
		t.Fatal(err)
	}
	if err := ag.RestoreConversation(before, epoch); err != nil {
		t.Fatal(err)
	}
	got := ag.Messages()
	if len(got) != 1 || got[0].Content[0].Text != "real" || ag.CompactionEpoch() != 4 {
		t.Fatalf("restored state = %#v epoch %d", got, ag.CompactionEpoch())
	}
}
