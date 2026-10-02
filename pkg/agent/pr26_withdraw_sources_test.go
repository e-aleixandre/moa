package agent

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// Source shapes are the producer map supplied by Luna, not invented flags.
// Withdrawal policy is distinct from the existing interruptible-wait policy.
func TestPR26COnlyUserAndParentOwnerInstructionsWithdraw(t *testing.T) {
	cases := []struct {
		name     string
		internal bool
		custom   map[string]any
		withdraw bool
	}{
		{name: "user", withdraw: true},
		{name: "subagent_steer_parent_to_child", withdraw: true},
		{name: "sessions_send_owner_to_child", custom: map[string]any{"source": "owner"}, withdraw: true},
		{name: "task_notice", custom: map[string]any{"source": "event", "source_name": "tasks", "steer": true}},
		{name: "inbox", custom: map[string]any{"source": "event", "source_name": "Inbox"}},
		{name: "webhook", custom: map[string]any{"source": "event", "source_name": "webhook"}},
		{name: "schedule_due", custom: map[string]any{"source": "schedule"}},
		{name: "report", custom: map[string]any{"source": "report"}},
		{name: "report_internal", internal: true, custom: map[string]any{"source": "report"}},
		{name: "subagent_completion", internal: true, custom: map[string]any{"source": "subagent"}},
		{name: "bash_job_completion", internal: true, custom: map[string]any{"source": "bash_job"}},
		{name: "user_shell_internal", internal: true},
		{name: "unknown_automatic", custom: map[string]any{"source": "future_automation"}},
		{name: "nil_source_present", custom: map[string]any{"source": nil}},
		{name: "empty_string_source", custom: map[string]any{"source": ""}, withdraw: true},
		{name: "malformed_automatic_source", custom: map[string]any{"source": 17}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gate := newAdmissionGate(t)
			var secondExecutions atomic.Int32
			requests := make(chan core.Request, 1)
			provider := NewMockProvider(toolBatchResponse(core.ToolCallContent("a", "a", nil), core.ToolCallContent("b", "b", nil)),
				func(req core.Request) (<-chan core.AssistantEvent, error) {
					requests <- req
					return simpleTextResponse("done")(req)
				})
			ag := newTestAgent(provider,
				core.Tool{Name: "a", Parameters: []byte(`{"type":"object"}`), Effect: core.EffectShell,
					Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
						close(gate.entered)
						<-gate.release
						return core.TextResult("a done"), nil
					}},
				core.Tool{Name: "b", Parameters: []byte(`{"type":"object"}`), Effect: core.EffectShell,
					Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
						secondExecutions.Add(1)
						return core.TextResult("b done"), nil
					}},
			)
			done := startAdmissionRun(t, ag)
			awaitBarrier(t, gate.entered, "started shell before unstarted shell")
			if err := ag.TrySteer(core.SteerItem{ID: tc.name, Text: "source-specific input", Internal: tc.internal, Custom: tc.custom}); err != nil {
				t.Fatal(err)
			}
			gate.open()
			finishAdmissionRun(t, done, false)
			wantExec := int32(1)
			if tc.withdraw {
				wantExec = 0
			}
			if got := secondExecutions.Load(); got != wantExec {
				t.Errorf("Execute(b)=%d want=%d for Internal=%v Custom=%v", got, wantExec, tc.internal, tc.custom)
			}
			results := toolResultsInOrder(t, ag.Messages(), "a", "b")
			if got := isNewMessageResult(results[1]); got != tc.withdraw {
				t.Errorf("withdrawn=%v want=%v; result=%q", got, tc.withdraw, messageText(results[1]))
			}
			// Automatic input still reaches the next turn; classification must
			// not silently lose it or use Internal to suppress its real message.
			pr26AssertSteerAfterResults(t, (<-requests).Messages, "source-specific input", true)
			if providerCalls(provider) != 2 || ag.QueueLen() != 0 {
				t.Errorf("requests=%d queue=%d, want one post-batch delivery", providerCalls(provider), ag.QueueLen())
			}
			t.Logf("source=%v Internal=%v Execute(b)=%d withdraw=%v", tc.custom["source"], tc.internal, secondExecutions.Load(), isNewMessageResult(results[1]))
		})
	}
}
