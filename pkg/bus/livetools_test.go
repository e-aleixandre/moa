package bus

import (
	"fmt"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func toolCallStart(id, name string) core.AgentEvent {
	return core.AgentEvent{
		Type:           core.AgentEventMessageUpdate,
		AssistantEvent: &core.AssistantEvent{Type: core.ProviderEventToolCallStart, ToolCallID: id, ToolName: name},
	}
}

func toolCallDelta(id string, args map[string]any) core.AgentEvent {
	return core.AgentEvent{
		Type:           core.AgentEventMessageUpdate,
		AssistantEvent: &core.AssistantEvent{Type: core.ProviderEventToolCallDelta, ToolCallID: id, PartialArgs: args},
	}
}

// Regression for "switching conversations degrades a live tool row to
// 'Calling'": while a tool call streams its arguments or executes, it lives in
// no message history, so the reconnect snapshot must carry it — with its real
// name — or the client rebuilds a nameless row.
func TestBridgeEvent_LiveToolRegistry(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)

	if got := sctx.LiveTools(); len(got) != 0 {
		t.Fatalf("registry not empty before any tool call: %+v", got)
	}

	// Phase 1: the model streams the call's name, then its arguments.
	bridgeEvent(sctx, toolCallStart("tc1", "edit"))
	got := sctx.LiveTools()
	if len(got) != 1 || got[0].ToolName != "edit" || got[0].Phase != LiveToolPhaseGenerating {
		t.Fatalf("after tool_call_start: %+v, want one generating edit", got)
	}
	if got[0].StartedAt.IsZero() {
		t.Fatal("StartedAt not stamped: the client's elapsed timer would restart on reconnect")
	}
	firstSeen := got[0].StartedAt

	bridgeEvent(sctx, toolCallDelta("tc1", map[string]any{"path": "pkg/serve/ws.go"}))
	got = sctx.LiveTools()
	if len(got) != 1 || got[0].Args["path"] != "pkg/serve/ws.go" {
		t.Fatalf("after tool_call_delta: %+v, want the partial args folded in", got)
	}
	if got[0].ToolName != "edit" {
		t.Fatalf("delta erased the tool name: %+v", got)
	}

	// Phase 2: execution starts. Same ID → same row, advanced, same anchor.
	bridgeEvent(sctx, core.AgentEvent{
		Type:       core.AgentEventToolExecStart,
		ToolCallID: "tc1",
		ToolName:   "edit",
		Args:       map[string]any{"path": "pkg/serve/ws.go", "oldText": "a"},
	})
	got = sctx.LiveTools()
	if len(got) != 1 {
		t.Fatalf("exec start duplicated the row: %+v", got)
	}
	if got[0].Phase != LiveToolPhaseRunning || got[0].Args["oldText"] != "a" {
		t.Fatalf("row not advanced to running with full args: %+v", got)
	}
	if !got[0].StartedAt.Equal(firstSeen) {
		t.Fatalf("StartedAt moved on the generating→running transition: %v → %v", firstSeen, got[0].StartedAt)
	}

	// Phase 3: the tool ends → the row leaves the registry (it is history now).
	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventToolExecEnd, ToolCallID: "tc1", ToolName: "edit"})
	if got := sctx.LiveTools(); len(got) != 0 {
		t.Fatalf("registry not cleaned after tool_end: %+v", got)
	}
}

// A tool call that starts executing without ever streaming (subagent-forwarded
// events, providers that emit no partial args) must still register.
func TestBridgeEvent_LiveToolExecOnly(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)

	bridgeEvent(sctx, core.AgentEvent{
		Type: core.AgentEventToolExecStart, ToolCallID: "tc1", ToolName: "bash",
		Args: map[string]any{"command": "go test ./..."},
	})
	got := sctx.LiveTools()
	if len(got) != 1 || got[0].ToolName != "bash" || got[0].Phase != LiveToolPhaseRunning {
		t.Fatalf("exec-only call not registered: %+v", got)
	}
}

// The registry must survive MessageEnd: the assistant message closes BEFORE its
// tool calls run, so clearing there would blank the rows for a two-minute bash
// that is about to start — the exact case this feature exists for.
func TestBridgeEvent_LiveToolSurvivesMessageEnd(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)

	bridgeEvent(sctx, toolCallStart("tc1", "bash"))
	bridgeEvent(sctx, core.AgentEvent{
		Type:    core.AgentEventMessageEnd,
		Message: core.AgentMessage{Message: core.Message{Role: "assistant", MsgID: "m1"}},
	})
	if got := sctx.LiveTools(); len(got) != 1 {
		t.Fatalf("MessageEnd cleared the pending tool calls: %+v", got)
	}
	bridgeEvent(sctx, core.AgentEvent{
		Type: core.AgentEventToolExecStart, ToolCallID: "tc1", ToolName: "bash",
		Args: map[string]any{"command": "sleep 120"},
	})
	if got := sctx.LiveTools(); len(got) != 1 || got[0].Phase != LiveToolPhaseRunning {
		t.Fatalf("row lost between message end and exec start: %+v", got)
	}
}

// Cleanup must be unconditional: a call whose end never arrives (cancelled run,
// abort, a capped response whose tool calls are never executed) would otherwise
// leave a phantom live row on every future reconnect.
func TestBridgeEvent_LiveToolClearedAtRunBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event core.AgentEvent
	}{
		{"turn end", core.AgentEvent{Type: core.AgentEventTurnEnd}},
		{"run end", core.AgentEvent{Type: core.AgentEventEnd}},
		{"run error", core.AgentEvent{Type: core.AgentEventError, Error: fmt.Errorf("boom")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := NewLocalBus()
			defer b.Close()
			sctx := newTestSessionContext(b, nil)

			bridgeEvent(sctx, toolCallStart("tc1", "bash"))
			bridgeEvent(sctx, core.AgentEvent{
				Type: core.AgentEventToolExecStart, ToolCallID: "tc2", ToolName: "read",
			})
			if got := sctx.LiveTools(); len(got) != 2 {
				t.Fatalf("setup: %+v, want two live calls", got)
			}
			bridgeEvent(sctx, tc.event)
			if got := sctx.LiveTools(); len(got) != 0 {
				t.Fatalf("registry not cleared on %s: %+v", tc.name, got)
			}
		})
	}
}

// A rejected call (permission denied, blocked, validation error) still emits
// tool_end, so it must leave the registry like any other terminal path.
func TestBridgeEvent_LiveToolClearedOnRejection(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)

	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventToolExecStart, ToolCallID: "tc1", ToolName: "bash"})
	bridgeEvent(sctx, core.AgentEvent{
		Type: core.AgentEventToolExecEnd, ToolCallID: "tc1", ToolName: "bash",
		IsError: true, Rejected: true,
	})
	if got := sctx.LiveTools(); len(got) != 0 {
		t.Fatalf("registry not cleaned after a rejected call: %+v", got)
	}
}

// Concurrent calls (executeTools runs tools in parallel) each keep their own
// row, and only the one that ends is removed.
func TestBridgeEvent_LiveToolConcurrentCalls(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)

	for _, id := range []string{"a", "b", "c"} {
		bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventToolExecStart, ToolCallID: id, ToolName: "read"})
	}
	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventToolExecEnd, ToolCallID: "b", ToolName: "read"})
	got := sctx.LiveTools()
	if len(got) != 2 || got[0].ToolCallID != "a" || got[1].ToolCallID != "c" {
		t.Fatalf("live calls after one ended: %+v, want a and c in order", got)
	}
}

// The registry is per-session state, so it must never grow without bound just
// because some pathological stream never reports an end.
func TestBridgeEvent_LiveToolCardinalityBounded(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)

	total := liveToolsMax + 25
	for i := range total {
		bridgeEvent(sctx, toolCallStart(fmt.Sprintf("tc%d", i), "read"))
	}
	got := sctx.LiveTools()
	if len(got) != liveToolsMax {
		t.Fatalf("registry size = %d, want it capped at %d", len(got), liveToolsMax)
	}
	// The newest calls are the ones a client needs to see.
	if want := fmt.Sprintf("tc%d", total-1); got[len(got)-1].ToolCallID != want {
		t.Fatalf("newest call evicted: last is %s, want %s", got[len(got)-1].ToolCallID, want)
	}
}

// The registry must not alias maps owned by the agent's history: the permission
// layer pops its feedback key out of the very Args map ToolExecStart carried,
// which would otherwise mutate the snapshot under the reader.
func TestBridgeEvent_LiveToolArgsAreCopied(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)

	args := map[string]any{"command": "go test ./..."}
	bridgeEvent(sctx, core.AgentEvent{
		Type: core.AgentEventToolExecStart, ToolCallID: "tc1", ToolName: "bash", Args: args,
	})
	delete(args, "command")
	args["injected"] = true

	got := sctx.LiveTools()
	if len(got) != 1 || got[0].Args["command"] != "go test ./..." {
		t.Fatalf("registry aliased the caller's args map: %+v", got)
	}
	if _, ok := got[0].Args["injected"]; ok {
		t.Fatalf("later mutation leaked into the registry: %+v", got[0].Args)
	}
}

// Args can be JSON-shaped, so a top-level copy is not enough: a nested map or
// slice left aliased is still mutable from under the snapshot goroutine.
func TestBridgeEvent_LiveToolArgsAreDeepCopied(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)

	nested := map[string]any{"path": "pkg/serve/ws.go"}
	list := []any{"a"}
	args := map[string]any{"edit": nested, "globs": list}
	bridgeEvent(sctx, core.AgentEvent{
		Type: core.AgentEventToolExecStart, ToolCallID: "tc1", ToolName: "edit", Args: args,
	})
	nested["path"] = "mutated"
	list[0] = "mutated"

	got := sctx.LiveTools()
	gotNested, _ := got[0].Args["edit"].(map[string]any)
	if gotNested == nil || gotNested["path"] != "pkg/serve/ws.go" {
		t.Fatalf("nested map aliased the caller's args: %+v", got[0].Args)
	}
	gotList, _ := got[0].Args["globs"].([]any)
	if len(gotList) != 1 || gotList[0] != "a" {
		t.Fatalf("nested slice aliased the caller's args: %+v", got[0].Args)
	}
}

// Regression for the atomicity blocker, tool-call flavour: the snapshot must
// never omit a live call whose announcing events are at or below the cut. Those
// events are never replayed to the reconnecting client, so a snapshot taken
// "just after" them but built from a stale registry would restore a nameless
// row. bridgeEvent publishes exactly one event per call while holding streamMu,
// so seqs are deterministic: with L0 = LastSeq before the first start, call i
// takes seq L0+1+i. A snapshot at cut therefore implies exactly cut-L0 live
// calls — assert that, sampling concurrently with the producer.
func TestBridgeEvent_LiveToolSnapshotCutIsAtomic(t *testing.T) {
	const nCalls = 400

	for iter := 0; iter < 40; iter++ {
		b := NewLocalBus()
		l0 := b.LastSeq()
		sctx := newTestSessionContext(b, nil)

		done := make(chan struct{})
		go func() {
			defer close(done)
			for i := range nCalls {
				bridgeEvent(sctx, toolCallStart(fmt.Sprintf("tc%d", i), "read"))
			}
		}()

		var samples int
		for {
			_, live, cut := sctx.SnapshotInFlightWithCut()
			k := int(cut) - int(l0)
			if k >= 0 && k <= nCalls && k <= liveToolsMax {
				if len(live) != k {
					t.Fatalf("iter %d: cut=%d implies %d live calls, snapshot holds %d",
						iter, cut, k, len(live))
				}
				samples++
			}
			select {
			case <-done:
				if k >= nCalls || samples > nCalls {
					goto next
				}
			default:
			}
		}
	next:
		b.Close()
	}
}
