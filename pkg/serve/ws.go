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
// Ordering: the reactor uses one SubscribeAll handler so events are translated
// and sent in the same order they were published on the session bus. This is
// important for streaming UI state: text_delta, tool_call_start, message_end,
// tool_end, state_change, and run_end must not overtake one another.
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

	r.unsubs = append(r.unsubs, b.SubscribeAll(func(event any) {
		if wsEvent, ok := wsEventFromBus(event); ok {
			send(wsEvent)
		}
	}))

	// Watch session context cancellation.
	go func() {
		<-sessionCtx.Done()
		r.cleanup()
	}()

	return r
}

func wsEventFromBus(event any) (Event, bool) {
	switch e := event.(type) {
	case bus.StateChanged:
		return Event{Type: "state_change", Data: StateChangeData{
			State: e.State, Error: e.Error,
		}}, true
	case bus.TurnStarted:
		return Event{Type: "turn_start"}, true
	case bus.TurnEnded:
		return Event{Type: "turn_end"}, true
	case bus.MessageStarted:
		return Event{Type: "message_start"}, true
	case bus.TextDelta:
		return Event{Type: "text_delta", Data: DeltaData{Delta: e.Delta}}, true
	case bus.ThinkingDelta:
		return Event{Type: "thinking_delta", Data: DeltaData{Delta: e.Delta}}, true
	case bus.MessageEnded:
		return Event{Type: "message_end", Data: MessageEndData{Text: e.FullText}}, true
	case bus.ToolCallStreaming:
		return Event{Type: "tool_call_start", Data: ToolCallStreamingData{
			ToolCallID: e.ToolCallID, ToolName: e.ToolName,
		}}, true
	case bus.ToolCallDelta:
		return Event{Type: "tool_call_delta", Data: ToolCallDeltaData{
			ToolCallID: e.ToolCallID, Args: e.Args,
		}}, true
	case bus.ToolExecStarted:
		return Event{Type: "tool_start", Data: ToolStartData{
			ToolCallID: e.ToolCallID, ToolName: e.ToolName, Args: e.Args,
		}}, true
	case bus.ToolExecUpdate:
		return Event{Type: "tool_update", Data: ToolUpdateData{
			ToolCallID: e.ToolCallID, Delta: e.Delta,
		}}, true
	case bus.ToolExecEnded:
		return Event{Type: "tool_end", Data: ToolEndData{
			ToolCallID: e.ToolCallID, ToolName: e.ToolName,
			IsError: e.IsError, Rejected: e.Rejected, Result: e.Result,
		}}, true
	case bus.TasksUpdated:
		return Event{Type: "tasks_update", Data: TasksUpdateData{Tasks: e.Tasks}}, true
	case bus.RunEnded:
		return Event{Type: "run_end", Data: RunEndData{Text: e.FinalText}}, true
	case bus.ContextUpdated:
		return Event{Type: "context_update", Data: ContextUpdateData{ContextPercent: e.Percent}}, true
	case bus.ConfigChanged:
		return Event{Type: "config_change", Data: ConfigChangeData{
			Model: e.Model, Thinking: e.Thinking,
			PermissionMode: e.PermissionMode, PathScope: e.PathScope,
		}}, true
	case bus.PlanModeChanged:
		return Event{Type: "plan_mode", Data: PlanModeData{
			Mode: e.Mode, PlanFile: e.PlanFile,
		}}, true
	case bus.CommandExecuted:
		return Event{Type: "command", Data: CommandData{
			Command: e.Command, Messages: e.Messages,
		}}, true
	case bus.Steered:
		return Event{Type: "steer", Data: SteerData{Text: e.Text}}, true
	case bus.AutoVerifyStarted:
		return Event{Type: "auto_verify_start"}, true
	case bus.AutoVerifyEnded:
		data := map[string]any{"all_pass": e.AllPass, "summary": e.Summary}
		if e.Err != nil {
			data["error"] = e.Err.Error()
		}
		return Event{Type: "auto_verify_end", Data: data}, true
	case bus.PermissionRequested:
		return Event{Type: "permission_request", Data: PermissionData{
			ID: e.ID, ToolName: e.ToolName, Args: e.Args,
			AllowPattern: e.AllowPattern,
		}}, true
	case bus.PermissionResolved:
		return Event{Type: "permission_resolved", Data: map[string]any{"id": e.ID}}, true
	case bus.AskUserRequested:
		return Event{Type: "ask_user", Data: map[string]any{
			"id": e.ID, "questions": e.Questions,
		}}, true
	case bus.AskUserResolved:
		return Event{Type: "ask_resolved", Data: map[string]any{"id": e.ID}}, true
	case bus.SubagentCountChanged:
		return Event{Type: "subagent_count", Data: SubagentCountData{Count: e.Count}}, true
	case bus.SubagentCompleted:
		return Event{Type: "subagent_complete", Data: SubagentCompleteData{
			JobID: e.JobID, Task: e.Task, Status: e.Status, Text: e.Text,
		}}, true
	case bus.CompactionStarted:
		return Event{Type: "compaction_start"}, true
	case bus.CompactionEnded:
		return Event{Type: "compaction_end"}, true
	default:
		return Event{}, false
	}
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

	// Use display messages (full history from tree) instead of agent messages.
	msgs, _ := bus.QueryTyped[bus.GetDisplayMessages, []core.AgentMessage](b, bus.GetDisplayMessages{})
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
