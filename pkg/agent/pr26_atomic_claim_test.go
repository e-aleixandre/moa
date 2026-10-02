package agent

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// A tool's end event is observable before another read-only tool has joined.
// Once that end says "new user message", the matching prefix is committed:
// recall cannot remove it and Stop cannot turn it into a discard.
func TestPR26DRecallVersusClaimAtLogicalStart(t *testing.T) {
	for _, order := range []string{"recall_before_start", "claim_before_recall", "stop_before_claim", "stop_after_claim"} {
		t.Run(order, func(t *testing.T) {
			active, scheduling := newAdmissionGate(t), newAdmissionGate(t)
			var executions [3]atomic.Int32
			var activeCancelled atomic.Bool
			var scheduled atomic.Int32
			requests := make(chan core.Request, 1)
			provider := NewMockProvider(toolBatchResponse(
				core.ToolCallContent("a", "a", nil), core.ToolCallContent("b", "writer", nil), core.ToolCallContent("c", "writer", nil),
			), func(req core.Request) (<-chan core.AssistantEvent, error) {
				requests <- req
				return simpleTextResponse("done")(req)
			})
			ag := newTestAgent(provider,
				core.Tool{Name: "a", Parameters: []byte(`{"type":"object"}`), Effect: core.EffectReadOnly,
					Execute: func(ctx context.Context, _ map[string]any, _ func(core.Result)) (core.Result, error) {
						executions[0].Add(1)
						close(active.entered)
						<-active.release
						activeCancelled.Store(ctx.Err() != nil)
						return core.TextResult("a completed"), nil
					}},
				core.Tool{Name: "writer", Parameters: []byte(`{"type":"object"}`), Effect: core.EffectWritePath,
					LockKey: func(map[string]any) string {
						if scheduled.Add(1) == 1 {
							close(scheduling.entered)
							<-scheduling.release
						}
						return "/pr26/fixture"
					},
					Execute: func(ctx context.Context, _ map[string]any, _ func(core.Result)) (core.Result, error) {
						id := core.ToolCallIDFromContext(ctx)
						if id == "b" {
							executions[1].Add(1)
						} else {
							executions[2].Add(1)
						}
						return core.TextResult(id + " completed"), nil
					}},
			)
			bEnded, cEnded := make(chan struct{}), make(chan struct{})
			var mu sync.Mutex
			var delivered []core.AgentEvent
			var unwindIDs []string
			unsubscribe := ag.Subscribe(func(e core.AgentEvent) {
				mu.Lock()
				switch e.Type {
				case core.AgentEventSteer:
					delivered = append(delivered, e)
				case core.AgentEventSteersCanceled:
					unwindIDs = append(unwindIDs, e.SteerIDs...)
				}
				mu.Unlock()
				if e.Type == core.AgentEventToolExecEnd {
					if e.ToolCallID == "b" {
						close(bEnded)
					}
					if e.ToolCallID == "c" {
						close(cEnded)
					}
				}
			})
			t.Cleanup(unsubscribe)
			// This unrelated reservation must survive recall and claimed-batch
			// settlement: resetting the ledger globally would hide a quota bug.
			const reservation int64 = 7
			ag.ReserveNativeDocBytes(reservation)
			done := startAdmissionRun(t, ag)
			awaitBarrier(t, active.entered, "active read-only tool")
			awaitBarrier(t, scheduling.entered, "writer before logical start")
			item := core.SteerItem{ID: "s-claim", Text: "change direction", Custom: map[string]any{"pr26_id": "s-claim"},
				Content: []core.Content{core.TextContent("change direction"), core.ImageContent("0123456789ABCDEF", "image/png")}}
			if err := ag.TrySteer(item); err != nil {
				t.Fatal(err)
			}
			var recalled []core.SteerItem
			switch order {
			case "recall_before_start":
				recalled = ag.CancelSteer()
				if got := ids(recalled); !reflect.DeepEqual(got, []string{item.ID}) {
					t.Errorf("recall before frontier IDs=%v, want [%s]", got, item.ID)
				}
				scheduling.open()
			case "stop_before_claim":
				ag.Abort()
				scheduling.open()
			default:
				scheduling.open()
				awaitBarrier(t, bEnded, "first withdrawn writer, read still active")
				awaitBarrier(t, cEnded, "second withdrawn writer, read still active")
				if executions[1].Load() != 0 || executions[2].Load() != 0 {
					t.Errorf("withdrawal frontier executed writers: b=%d c=%d", executions[1].Load(), executions[2].Load())
				}
				ag.steers.mu.Lock()
				inflight := ag.steers.inflightNativeDocBytes
				ag.steers.mu.Unlock()
				if want := reservation + core.NativeDocBytes(item.Content); inflight != want {
					t.Errorf("claimed prefix native-byte ledger=%d, want %d before history append", inflight, want)
				}
				// Unlike the legacy RED, recall happens AFTER a committed skip
				// here and must return zero, not one. Do not return on failure:
				// still prove the missing history and the execution count.
				recalled = ag.CancelSteer()
				if len(recalled) != 0 {
					t.Errorf("recall after committed withdrawal returned IDs %v, want none", ids(recalled))
				}
				if got := ag.NativeDocBytesUndelivered(); got != reservation+core.NativeDocBytes(item.Content) {
					t.Errorf("recall removed claimed content from quota: undelivered=%d", got)
				}
				if order == "stop_after_claim" {
					ag.Abort()
				}
			}
			active.open()
			cancelled := strings.HasPrefix(order, "stop_")
			finishAdmissionRun(t, done, cancelled)
			ag.Drain(2 * time.Second)
			msgs := ag.Messages()
			results := toolResultsInOrder(t, msgs, "a", "b", "c")
			wantExecutions := int32(0)
			if order == "recall_before_start" {
				wantExecutions = 1
			}
			if executions[0].Load() != 1 || executions[1].Load() != wantExecutions || executions[2].Load() != wantExecutions {
				t.Errorf("Execute counts a/b/c=%d/%d/%d, want 1/%d/%d", executions[0].Load(), executions[1].Load(), executions[2].Load(), wantExecutions, wantExecutions)
			}
			wantDelivery := order == "claim_before_recall" || order == "stop_after_claim"
			for _, r := range results[1:] {
				if isNewMessageResult(r) != wantDelivery {
					t.Errorf("%s result=%q, committed delivery=%v", r.ToolCallID, messageText(r), wantDelivery)
				}
				if order == "stop_before_claim" && (!r.IsError || !strings.Contains(messageText(r), "cancelled")) {
					t.Errorf("Stop before claim must yield cancellation, not withdrawal: %q", messageText(r))
				}
			}
			if !cancelled && activeCancelled.Load() {
				t.Error("steer/recall cancelled an already-started ordinary tool")
			}
			mu.Lock()
			gotDelivered := append([]core.AgentEvent(nil), delivered...)
			gotDiscarded := append([]string(nil), unwindIDs...)
			mu.Unlock()
			gotDiscarded = append(gotDiscarded, ids(recalled)...)
			pr26AssertMessagePartition(t, msgs, item.ID, wantDelivery, gotDelivered, gotDiscarded)
			wantRequests := 2
			if cancelled {
				wantRequests = 1
			}
			if got := providerCalls(provider); got != wantRequests {
				t.Errorf("provider requests=%d, want %d", got, wantRequests)
			}
			if !cancelled {
				req := <-requests
				wrapped := make([]core.AgentMessage, len(req.Messages))
				for i, m := range req.Messages {
					wrapped[i] = core.WrapMessage(m)
				}
				toolResultsInOrder(t, wrapped, "a", "b", "c")
				pr26AssertSteerAfterResults(t, req.Messages, item.Text, wantDelivery)
			}
			if got := ag.NativeDocBytesUndelivered(); got != reservation {
				t.Errorf("settled run ledger=%d, want only unrelated reservation %d", got, reservation)
			}
			ag.ReleaseNativeDocBytes(reservation)
			if got := ag.NativeDocBytesUndelivered(); got != 0 {
				t.Errorf("ledger after releasing unrelated reservation=%d", got)
			}
			t.Logf("order=%s Execute=%d/%d/%d delivery_events=%d discarded=%v requests=%d", order,
				executions[0].Load(), executions[1].Load(), executions[2].Load(), len(gotDelivered), gotDiscarded, providerCalls(provider))
		})
	}
}

func pr26AssertMessagePartition(t *testing.T, msgs []core.AgentMessage, id string, delivered bool, events []core.AgentEvent, discarded []string) {
	t.Helper()
	var historyIDs []string
	lastResult, messageIndex := -1, -1
	for i, m := range msgs {
		if m.Role == "tool_result" {
			lastResult = i
		}
		if m.Role == "user" && m.Custom["pr26_id"] == id {
			historyIDs = append(historyIDs, m.MsgID)
			messageIndex = i
		}
	}
	var eventMsgIDs []string
	for _, e := range events {
		if e.SteerID == id {
			eventMsgIDs = append(eventMsgIDs, e.MsgID)
		}
	}
	discards := 0
	for _, dropped := range discarded {
		if dropped == id {
			discards++
		}
	}
	wantHistory, wantDiscard := 0, 1
	if delivered {
		wantHistory, wantDiscard = 1, 0
	}
	if len(historyIDs) != wantHistory || len(eventMsgIDs) != wantHistory || discards != wantDiscard {
		t.Errorf("steer %s partition: history MsgIDs=%v delivery MsgIDs=%v authoritative_discard_count=%d; want %d/%d/%d", id,
			historyIDs, eventMsgIDs, discards, wantHistory, wantHistory, wantDiscard)
	}
	if len(historyIDs) == 1 && (historyIDs[0] == "" || !reflect.DeepEqual(historyIDs, eventMsgIDs) || messageIndex <= lastResult) {
		t.Errorf("steer %s must have one stable announced MsgID after all results: history=%v events=%v userIndex=%d lastResult=%d", id,
			historyIDs, eventMsgIDs, messageIndex, lastResult)
	}
}

func pr26AssertSteerAfterResults(t *testing.T, msgs []core.Message, text string, delivered bool) {
	t.Helper()
	lastResult, userIndex, count := -1, -1, 0
	for i, m := range msgs {
		if m.Role == "tool_result" {
			lastResult = i
		}
		if m.Role == "user" && len(m.Content) != 0 && m.Content[0].Text == text {
			count++
			userIndex = i
		}
	}
	want := 0
	if delivered {
		want = 1
	}
	if count != want || (delivered && userIndex <= lastResult) {
		t.Errorf("next request: steer_count=%d want=%d userIndex=%d lastResult=%d", count, want, userIndex, lastResult)
	}
}

// A prefix may include an automatic notice before the user's instruction;
// claiming preserves FIFO but cannot consume a barrier or its trailing user.
// Stop after that claim discards only the live suffix, not the commitment.
func TestPR26DClaimedPrefixStopsAtBarrierAndSurvivesStop(t *testing.T) {
	active, scheduling := newAdmissionGate(t), newAdmissionGate(t)
	var writerExecutions atomic.Int32
	provider := NewMockProvider(toolBatchResponse(core.ToolCallContent("a", "a", nil), core.ToolCallContent("b", "writer", nil)))
	ag := newTestAgent(provider,
		core.Tool{Name: "a", Parameters: []byte(`{"type":"object"}`), Effect: core.EffectReadOnly,
			Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
				close(active.entered)
				<-active.release
				return core.TextResult("a done"), nil
			}},
		core.Tool{Name: "writer", Parameters: []byte(`{"type":"object"}`), Effect: core.EffectWritePath,
			LockKey: func(map[string]any) string { close(scheduling.entered); <-scheduling.release; return "/pr26/prefix" },
			Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
				writerExecutions.Add(1)
				return core.TextResult("b done"), nil
			}},
	)
	bEnded := make(chan struct{})
	var mu sync.Mutex
	var delivered []core.AgentEvent
	var discarded []string
	t.Cleanup(ag.Subscribe(func(e core.AgentEvent) {
		mu.Lock()
		switch e.Type {
		case core.AgentEventSteer:
			delivered = append(delivered, e)
		case core.AgentEventSteersCanceled:
			discarded = append(discarded, e.SteerIDs...)
		}
		mu.Unlock()
		if e.Type == core.AgentEventToolExecEnd && e.ToolCallID == "b" {
			close(bEnded)
		}
	}))
	done := startAdmissionRun(t, ag)
	awaitBarrier(t, active.entered, "active read")
	awaitBarrier(t, scheduling.entered, "writer scheduling")
	items := []core.SteerItem{
		{ID: "notice", Text: "automatic notice", Custom: map[string]any{"source": "event", "pr26_id": "notice"}},
		{ID: "instruction", Text: "new instruction", Custom: map[string]any{"pr26_id": "instruction"}},
		{ID: "barrier", Command: "/compact"},
		{ID: "after", Text: "after command", Custom: map[string]any{"pr26_id": "after"}},
	}
	for _, it := range items {
		if err := ag.TrySteer(it); err != nil {
			t.Fatal(err)
		}
	}
	scheduling.open()
	awaitBarrier(t, bEnded, "withdrawn writer")
	if got := ids(ag.PendingSteers()); !reflect.DeepEqual(got, []string{"barrier", "after"}) {
		t.Errorf("live queue after claim=%v, want [barrier after]", got)
	}
	ag.Abort()
	active.open()
	finishAdmissionRun(t, done, true)
	ag.Drain(2 * time.Second)
	msgs := ag.Messages()
	results := toolResultsInOrder(t, msgs, "a", "b")
	if writerExecutions.Load() != 0 || !isNewMessageResult(results[1]) {
		t.Errorf("writer executions=%d result=%q, want a committed withdrawal", writerExecutions.Load(), messageText(results[1]))
	}
	mu.Lock()
	defer mu.Unlock()
	for _, id := range []string{"notice", "instruction"} {
		pr26AssertMessagePartition(t, msgs, id, true, delivered, discarded)
	}
	pr26AssertMessagePartition(t, msgs, "after", false, delivered, discarded)
	if !reflect.DeepEqual(discarded, []string{"barrier", "after"}) {
		t.Errorf("Stop discard IDs=%v, want only live suffix [barrier after]", discarded)
	}
	var deliveryIDs []string
	for _, e := range delivered {
		deliveryIDs = append(deliveryIDs, e.SteerID)
	}
	if !reflect.DeepEqual(deliveryIDs, []string{"notice", "instruction"}) {
		t.Errorf("delivered FIFO IDs=%v", deliveryIDs)
	}
	if got := ag.NativeDocBytesUndelivered(); got != 0 {
		t.Errorf("ledger leak after stopped claim=%d", got)
	}
	if providerCalls(provider) != 1 {
		t.Error("Stop after claim launched a new provider request")
	}
}
