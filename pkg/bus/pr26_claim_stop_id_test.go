package bus

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/core"
)

// The real AbortAndRecall command must preserve stop_id and restore only the
// queue it actually discards. A claimed instruction is delivered after the
// active read joins, not returned with the correlated Stop acknowledgement.
func TestPR26DStopRestoresOnlyUnclaimedIDs(t *testing.T) {
	for _, order := range []string{"stop_before_claim", "stop_after_claim"} {
		t.Run(order, func(t *testing.T) {
			active, releaseActive := make(chan struct{}), make(chan struct{})
			lockEntered, releaseLock := make(chan struct{}), make(chan struct{})
			var activeOnce, lockOnce sync.Once
			openActive := func() { activeOnce.Do(func() { close(releaseActive) }) }
			openLock := func() { lockOnce.Do(func() { close(releaseLock) }) }
			t.Cleanup(openActive)
			t.Cleanup(openLock)
			var writerExecutions atomic.Int32
			p := &r26Provider{stream: func(context.Context, int32) (<-chan core.AssistantEvent, error) {
				return r26Done(core.Message{Role: "assistant", StopReason: "tool_use", Content: []core.Content{
					core.ToolCallContent("a", "a", nil), core.ToolCallContent("b", "b", nil),
				}}), nil
			}}
			rt, ag := r26Runtime(t, p, []core.Tool{
				{Name: "a", Parameters: []byte(`{"type":"object"}`), Effect: core.EffectReadOnly,
					Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
						close(active)
						<-releaseActive
						return core.TextResult("a done"), nil
					}},
				{Name: "b", Parameters: []byte(`{"type":"object"}`), Effect: core.EffectWritePath,
					LockKey: func(map[string]any) string { close(lockEntered); <-releaseLock; return "/pr26/stop-id" },
					Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
						writerExecutions.Add(1)
						return core.TextResult("b done"), nil
					}},
			}, func(ag *agent.Agent) AgentSubscriber { return ag })
			ended := make(chan RunEnded, 1)
			rt.Bus.Subscribe(func(e RunEnded) { ended <- e })
			bEnded := make(chan struct{})
			rt.Bus.Subscribe(func(e ToolExecEnded) {
				if e.ToolCallID == "b" {
					close(bEnded)
				}
			})
			var mu sync.Mutex
			var cancellations []SteersCanceled
			var deliveries []string
			rt.Bus.Subscribe(func(e SteersCanceled) { mu.Lock(); cancellations = append(cancellations, e); mu.Unlock() })
			rt.Bus.Subscribe(func(e Steered) { mu.Lock(); deliveries = append(deliveries, e.ID); mu.Unlock() })
			if err := rt.Bus.Execute(SendPrompt{Text: "go"}); err != nil {
				t.Fatal(err)
			}
			r26Await(t, active, "active read")
			r26Await(t, lockEntered, "writer before frontier")
			if err := rt.Bus.Execute(SteerAgent{ID: "commit", Text: "committed instruction"}); err != nil {
				t.Fatal(err)
			}
			if order == "stop_after_claim" {
				openLock()
				r26Await(t, bEnded, "withdrawal published before Stop")
			}
			if err := rt.Bus.Execute(SteerAgent{ID: "suffix", Text: "not yet claimed"}); err != nil {
				t.Fatal(err)
			}
			var recalled []core.SteerItem
			if err := rt.Bus.Execute(AbortAndRecall{StopID: "pr26-stop", DiscardedSteers: &recalled}); err != nil {
				t.Fatal(err)
			}
			wantDiscard := []string{"commit", "suffix"}
			if order == "stop_after_claim" {
				wantDiscard = []string{"suffix"}
			}
			var recallIDs []string
			for _, it := range recalled {
				recallIDs = append(recallIDs, it.ID)
			}
			if !reflect.DeepEqual(recallIDs, wantDiscard) {
				t.Errorf("correlated Stop recall IDs=%v, want %v", recallIDs, wantDiscard)
			}
			openLock()
			openActive()
			r26Ended(t, rt, ended)
			ag.Drain(2 * time.Second)
			rt.Bus.Drain(2 * time.Second)
			mu.Lock()
			defer mu.Unlock()
			if len(cancellations) != 1 || cancellations[0].StopID != "pr26-stop" || !reflect.DeepEqual(cancellations[0].SteerIDs, wantDiscard) {
				t.Errorf("Stop events=%+v, want one stop_id=pr26-stop with IDs=%v", cancellations, wantDiscard)
			}
			wantDeliveries := []string(nil)
			if order == "stop_after_claim" {
				wantDeliveries = []string{"commit"}
			}
			if !reflect.DeepEqual(deliveries, wantDeliveries) {
				t.Errorf("delivery IDs=%v, want %v", deliveries, wantDeliveries)
			}
			var results, commits, suffixes int
			for _, m := range ag.Messages() {
				if m.Role == "tool_result" {
					results++
				}
				if m.Role == "user" && len(m.Content) != 0 {
					switch m.Content[0].Text {
					case "committed instruction":
						commits++
					case "not yet claimed":
						suffixes++
					}
				}
			}
			wantCommits := 0
			if order == "stop_after_claim" {
				wantCommits = 1
			}
			if results != 2 || commits != wantCommits || suffixes != 0 || writerExecutions.Load() != 0 || p.calls.Load() != 1 {
				t.Errorf("balanced results=%d history commit/suffix=%d/%d Execute(b)=%d provider_requests=%d", results, commits, suffixes, writerExecutions.Load(), p.calls.Load())
			}
			t.Logf("order=%s stop_id=%s discarded=%v delivered=%v history_commit=%d Execute(b)=%d", order,
				"pr26-stop", recallIDs, deliveries, commits, writerExecutions.Load())
		})
	}
}
