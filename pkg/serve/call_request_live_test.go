package serve

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

// A request_call's outcome record reaches the live tool_end and the reconnect
// live-tools snapshot, so the missed-call block shows without a reload.
func TestCallRequestReachesTheLiveToolEnd(t *testing.T) {
	record := map[string]any{"id": "r1", "outcome": "written", "reason": "deploy?"}
	result := core.Result{Content: []core.Content{core.TextContent("ok")}, Custom: map[string]any{"call_request": record}}
	events := bus.TranslateAgentEvent("s", 1, core.AgentEvent{Type: core.AgentEventToolExecEnd, ToolCallID: "c", ToolName: "request_call", Result: &result}, nil)
	ev, ok := wsEventFromBus(events[0])
	if !ok {
		t.Fatal("no tool_end")
	}
	data, _ := json.Marshal(ev.Data)
	var got struct {
		CallRequest map[string]any `json:"call_request"`
	}
	if err := json.Unmarshal(data, &got); err != nil || got.CallRequest["id"] != "r1" || got.CallRequest["outcome"] != "written" {
		t.Fatalf("tool_end lost call_request: %s", data)
	}

	call := bus.EndedLiveToolCall(core.AgentEvent{Type: core.AgentEventToolExecEnd, ToolCallID: "c", ToolName: "request_call", Result: &result})
	data, _ = json.Marshal(liveToolInitData([]bus.LiveToolCall{call}))
	if !strings.Contains(string(data), `"call_request"`) {
		t.Fatalf("init live_tools lost call_request: %s", data)
	}

	plain := core.Result{Content: []core.Content{core.TextContent("x")}}
	events = bus.TranslateAgentEvent("s", 1, core.AgentEvent{Type: core.AgentEventToolExecEnd, ToolCallID: "d", ToolName: "read", Result: &plain}, nil)
	ev, _ = wsEventFromBus(events[0])
	if data, _ := json.Marshal(ev.Data); strings.Contains(string(data), "call_request") {
		t.Fatalf("tool_end without a record carries one: %s", data)
	}
}
