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
