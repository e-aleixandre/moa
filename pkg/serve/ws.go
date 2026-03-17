package serve

import (
	"context"
	"sync"

	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/tasks"
)

// wsReactor bridges typed bus events to a per-WebSocket Event channel.
// Safe to use from multiple goroutines. Cleanup is idempotent.
//
// Ordering: each bus event type has its own subscriber goroutine, all writing
// to the shared ch. Cross-type ordering is NOT guaranteed — e.g. tool_start
// and permission_request may arrive in any order. Within a single event type,
// ordering is preserved. Frontends must be resilient to cross-type reordering.
//
// TODO: to restore strict agent-event ordering, subscribe to a single
// "agent event batch" bus event type and translate in one goroutine.
type wsReactor struct {
	ch     chan Event
	done   chan struct{} // closed on cleanup; guards sends to ch
	once   sync.Once
	unsubs []func()
}

const wsReactorBuffer = 512 // per-WS event channel capacity

// newWsReactor subscribes to all bus events and session context cancellation.
// Returns the reactor and a read-only channel for the WS writer loop.
func newWsReactor(b bus.EventBus, sessionCtx context.Context) *wsReactor {
	r := &wsReactor{
		ch:   make(chan Event, wsReactorBuffer),
		done: make(chan struct{}),
	}

	// Helper: try-send with done-channel guard (prevents send-on-closed panic).
	// On overflow, triggers cleanup (disconnects slow consumer).
	send := func(e Event) {
		select {
		case <-r.done:
			return // already cleaned up
		default:
		}
		select {
		case r.ch <- e:
		case <-r.done:
			return
		default:
			// buffer full — slow consumer
			r.cleanup()
		}
	}

	// Subscribe to all bus event types.
	r.unsubs = append(r.unsubs,
		b.Subscribe(func(e bus.StateChanged) {
			send(Event{Type: "state_change", Data: StateChangeData{
				State: e.State, Error: e.Error,
			}})
		}),
		b.Subscribe(func(e bus.TurnStarted) {
			send(Event{Type: "turn_start"})
		}),
		b.Subscribe(func(e bus.TurnEnded) {
			send(Event{Type: "turn_end"})
		}),
		b.Subscribe(func(e bus.MessageStarted) {
			send(Event{Type: "message_start"})
		}),
		b.Subscribe(func(e bus.TextDelta) {
			send(Event{Type: "text_delta", Data: DeltaData{Delta: e.Delta}})
		}),
		b.Subscribe(func(e bus.ThinkingDelta) {
			send(Event{Type: "thinking_delta", Data: DeltaData{Delta: e.Delta}})
		}),
		b.Subscribe(func(e bus.MessageEnded) {
			send(Event{Type: "message_end", Data: MessageEndData{Text: e.FullText}})
		}),
		b.Subscribe(func(e bus.ToolExecStarted) {
			send(Event{Type: "tool_start", Data: ToolStartData{
				ToolCallID: e.ToolCallID, ToolName: e.ToolName, Args: e.Args,
			}})
		}),
		b.Subscribe(func(e bus.ToolExecUpdate) {
			send(Event{Type: "tool_update", Data: ToolUpdateData{
				ToolCallID: e.ToolCallID, Delta: e.Delta,
			}})
		}),
		b.Subscribe(func(e bus.ToolExecEnded) {
			send(Event{Type: "tool_end", Data: ToolEndData{
				ToolCallID: e.ToolCallID, ToolName: e.ToolName,
				IsError: e.IsError, Rejected: e.Rejected, Result: e.Result,
			}})
		}),
		b.Subscribe(func(e bus.TasksUpdated) {
			send(Event{Type: "tasks_update", Data: TasksUpdateData{Tasks: e.Tasks}})
		}),
		b.Subscribe(func(e bus.RunEnded) {
			if e.FinalText != "" {
				send(Event{Type: "run_end", Data: RunEndData{Text: e.FinalText}})
			}
		}),
		b.Subscribe(func(e bus.ContextUpdated) {
			send(Event{Type: "context_update", Data: ContextUpdateData{ContextPercent: e.Percent}})
		}),
		b.Subscribe(func(e bus.ConfigChanged) {
			send(Event{Type: "config_change", Data: ConfigChangeData{
				Model: e.Model, Thinking: e.Thinking,
				PermissionMode: e.PermissionMode, PathScope: e.PathScope,
			}})
		}),
		b.Subscribe(func(e bus.PlanModeChanged) {
			send(Event{Type: "plan_mode", Data: PlanModeData{
				Mode: e.Mode, PlanFile: e.PlanFile,
			}})
		}),
		b.Subscribe(func(e bus.CommandExecuted) {
			send(Event{Type: "command", Data: CommandData{
				Command: e.Command, Messages: e.Messages,
			}})
		}),
		b.Subscribe(func(e bus.Steered) {
			send(Event{Type: "steer", Data: SteerData{Text: e.Text}})
		}),
		b.Subscribe(func(e bus.PermissionRequested) {
			send(Event{Type: "permission_request", Data: PermissionData{
				ID: e.ID, ToolName: e.ToolName, Args: e.Args,
				AllowPattern: e.AllowPattern,
			}})
		}),
		b.Subscribe(func(e bus.PermissionResolved) {
			send(Event{Type: "permission_resolved", Data: map[string]any{"id": e.ID}})
		}),
		b.Subscribe(func(e bus.AskUserRequested) {
			send(Event{Type: "ask_user", Data: map[string]any{
				"id": e.ID, "questions": e.Questions,
			}})
		}),
		b.Subscribe(func(e bus.AskUserResolved) {
			send(Event{Type: "ask_resolved", Data: map[string]any{"id": e.ID}})
		}),
		b.Subscribe(func(e bus.SubagentCountChanged) {
			send(Event{Type: "subagent_count", Data: SubagentCountData{Count: e.Count}})
		}),
		b.Subscribe(func(e bus.SubagentCompleted) {
			send(Event{Type: "subagent_complete", Data: SubagentCompleteData{
				JobID: e.JobID, Task: e.Task, Status: e.Status, Text: e.Text,
			}})
		}),
		b.Subscribe(func(e bus.CompactionStarted) {
			send(Event{Type: "compaction_start"})
		}),
		b.Subscribe(func(e bus.CompactionEnded) {
			send(Event{Type: "compaction_end"})
		}),
	)

	// Watch session context cancellation.
	go func() {
		<-sessionCtx.Done()
		r.cleanup()
	}()

	return r
}

// Events returns the read-only channel for the WS writer loop.
func (r *wsReactor) Events() <-chan Event {
	return r.ch
}

// Done returns a channel that's closed when the reactor shuts down.
// Use in select alongside Events() to detect shutdown.
func (r *wsReactor) Done() <-chan struct{} {
	return r.done
}

// cleanup unsubscribes from all events. Idempotent.
// We close done (stops sends) then unsubscribe (stops new events).
// We do NOT close ch — the reader exits via <-r.done in the select.
// Closing ch would race with concurrent sends and cause panics.
func (r *wsReactor) cleanup() {
	r.once.Do(func() {
		close(r.done)
		for _, unsub := range r.unsubs {
			unsub()
		}
	})
}

// buildInitData constructs the WS init payload from bus queries.
func buildInitData(sess *ManagedSession) InitData {
	b := sess.runtime.Bus

	msgs, _ := bus.QueryTyped[bus.GetMessages, []core.AgentMessage](b, bus.GetMessages{})
	state, _ := bus.QueryTyped[bus.GetSessionState, string](b, bus.GetSessionState{})
	ctxPct, _ := bus.QueryTyped[bus.GetContextUsage, int](b, bus.GetContextUsage{})
	permMode, _ := bus.QueryTyped[bus.GetPermissionMode, string](b, bus.GetPermissionMode{})
	pending, _ := bus.QueryTyped[bus.GetPendingApproval, bus.PendingApprovalInfo](b, bus.GetPendingApproval{})
	taskList, _ := bus.QueryTyped[bus.GetTasks, []tasks.Task](b, bus.GetTasks{})
	pathInfo, _ := bus.QueryTyped[bus.GetPathPolicy, bus.PathPolicyInfo](b, bus.GetPathPolicy{})
	planInfo, _ := bus.QueryTyped[bus.GetPlanMode, bus.PlanModeInfo](b, bus.GetPlanMode{})

	data := InitData{
		Messages:       msgs,
		State:          state,
		ContextPercent: ctxPct,
		PermissionMode: permMode,
		Tasks:          taskList,
		PathScope:      pathInfo.Scope,
	}

	if pending.Permission != nil {
		data.PendingPermission = &PermissionData{
			ID:           pending.Permission.ID,
			ToolName:     pending.Permission.ToolName,
			Args:         pending.Permission.Args,
			AllowPattern: pending.Permission.AllowPattern,
		}
	}
	if pending.Ask != nil {
		data.PendingAsk = &AskData{
			ID:        pending.Ask.ID,
			Questions: pending.Ask.Questions,
		}
	}
	if planInfo.Mode != "off" {
		data.PlanMode = planInfo.Mode
		data.PlanFile = planInfo.PlanFile
	}

	return data
}
