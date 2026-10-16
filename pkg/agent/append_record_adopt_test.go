package agent

import (
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestKeepAppendedRecords(t *testing.T) {
	msg := func(id, role string) core.AgentMessage {
		return core.AgentMessage{Message: core.Message{MsgID: id, Role: role}}
	}
	captured := []core.AgentMessage{msg("a", "user"), msg("b", "assistant")}
	replacement := []core.AgentMessage{msg("s", "user"), msg("b", "assistant")}
	ids := func(ms []core.AgentMessage) string {
		out := ""
		for _, m := range ms {
			out += m.MsgID
		}
		return out
	}
	cur := append(append([]core.AgentMessage(nil), captured...), msg("r", "session_event"))
	if got := ids(keepAppendedRecords(replacement, captured, cur)); got != "sbr" {
		t.Fatalf("record appended after the capture = %q, want sbr", got)
	}
	if got := ids(keepAppendedRecords(replacement, captured, captured)); got != "sb" {
		t.Fatalf("nothing appended = %q, want sb", got)
	}
	other := []core.AgentMessage{msg("x", "user"), msg("y", "user"), msg("r", "session_event")}
	if got := ids(keepAppendedRecords(replacement, captured, other)); got != "sb" {
		t.Fatalf("history replaced by someone else = %q, want sb", got)
	}
	if got := ids(replacement); got != "sb" {
		t.Fatalf("replacement mutated: %q", got)
	}
}
