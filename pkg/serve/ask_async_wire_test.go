package serve

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/bus"
)

// The additive wire fields the client reads: an async question says so in the
// snapshot and in the live event, and a synchronous one stays as it was.
func TestAskAsyncWireFlag(t *testing.T) {
	_, ask := pendingAttentionData(bus.PendingApprovalInfo{Ask: &bus.PendingAskInfo{ID: "q1", Async: true}})
	b, _ := json.Marshal(ask)
	if !json.Valid(b) || !strings.Contains(string(b), `"async":true`) {
		t.Fatalf("async ask snapshot lacks the flag: %s", b)
	}
	_, sync := pendingAttentionData(bus.PendingApprovalInfo{Ask: &bus.PendingAskInfo{ID: "q2"}})
	b, _ = json.Marshal(sync)
	if strings.Contains(string(b), "async") {
		t.Fatalf("sync ask snapshot must not carry the flag: %s", b)
	}
	ev, _ := wsEventFromBus(bus.AskUserRequested{ID: "q1", Async: true})
	if ev.Data.(map[string]any)["async"] != true {
		t.Fatalf("live ask_user event lacks the flag: %+v", ev.Data)
	}
}

// A reloaded transcript must still say that an acknowledgement is not an answer
// and who gave a late answer, from the record and not from the XML text.
func TestAskAsyncCustomCrossesTheHistoryBoundary(t *testing.T) {
	ack := projectWSMessageCustom(map[string]any{"question_contract": "async-v1", "question_id": "q1", "question_state": "open"})
	if ack["question_id"] != "q1" || ack["question_state"] != "open" {
		t.Fatalf("acknowledgement lost its question: %v", ack)
	}
	answer := projectWSMessageCustom(map[string]any{
		"source": "question_answer", "question_id": "q1", "event_id": "e1", "id": "e1",
		"answers":       []any{"staging"},
		"ask_responder": map[string]any{"kind": "owner", "name": "Serena"},
	})
	if answer["source"] != "question_answer" || answer["question_id"] != "q1" {
		t.Fatalf("answer lost its identity: %v", answer)
	}
	if got, _ := answer["answers"].([]string); len(got) != 1 || got[0] != "staging" {
		t.Fatalf("answers = %v", answer["answers"])
	}
	if _, ok := answer["ask_responder"]; !ok {
		t.Fatalf("answer lost its responder: %v", answer)
	}
}
