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
	// On overflow, structural events disconnect the slow consumer; lossy events
	// (streaming deltas, including those wrapped in subagent_event) are dropped
	// instead, so a slow client can't be disconnected just by a burst of deltas.
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
			if isLossyWsEvent(e) {
				return // drop this delta, keep the connection
			}
			r.cleanup()
		}
	}

	r.unsubs = append(r.unsubs, b.SubscribeAll(func(event any) {
		if wsEvent, ok := wsEventFromBus(event); ok {
			send(wsEvent)
		}
	}))

	// Watch session context cancellation. Also select on r.done so this goroutine
	// exits when the reactor is cleaned up early (slow-consumer drop, or a WS
	// reconnect replacing it) instead of leaking until the whole session ends —
	// each mobile reconnect (30s keepalive anticipates flaps) would otherwise
	// strand a goroutine plus its 512-slot channel.
	go func() {
		select {
		case <-sessionCtx.Done():
			r.cleanup()
		case <-r.done:
		}
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
	case bus.RateLimitUpdated:
		rl := e.RateLimit
		return Event{Type: "ratelimit", Data: RateLimitData{
			Status:              rl.Status,
			RepresentativeClaim: rl.RepresentativeClaim,
			OnOverage:           rl.OnOverage(),
			FiveHourPct:         pctOf(rl.FiveHourUtil),
			SevenDayPct:         pctOf(rl.SevenDayUtil),
			OveragePct:          pctOf(rl.OverageUtil),
		}}, true
	case bus.ConfigChanged:
		return Event{Type: "config_change", Data: ConfigChangeData{
			Model: e.Model, Thinking: e.Thinking,
			PermissionMode: e.PermissionMode, PathScope: e.PathScope,
		}}, true
	case bus.PlanModeChanged:
		return Event{Type: "plan_mode", Data: PlanModeData{
			Mode: e.Mode, PlanFile: e.PlanFile,
		}}, true
	case bus.GoalChanged:
		return Event{Type: "goal_change", Data: GoalChangeData{
			Active: e.Active, Objective: e.Objective,
			Iteration: e.Iteration, Stalled: e.Stalled,
		}}, true
	case bus.GoalIterationEnded:
		return Event{Type: "goal_iteration", Data: GoalIterationData{
			Iteration: e.Iteration, Satisfied: e.Satisfied, Feedback: e.Feedback,
		}}, true
	case bus.GoalEnded:
		return Event{Type: "goal_end", Data: GoalEndData{Reason: e.Reason}}, true
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
	case bus.SubagentStarted:
		return Event{Type: "subagent_start", Data: SubagentStartData{
			JobID: e.JobID, Task: e.Task, Model: e.Model, Async: e.Async,
		}}, true
	case bus.SubagentEnded:
		var inputTok, outputTok int
		if e.Usage != nil {
			inputTok = e.Usage.Input
			outputTok = e.Usage.Output
		}
		return Event{Type: "subagent_end", Data: SubagentEndData{
			JobID: e.JobID, Status: e.Status,
			InputTokens: inputTok, OutputTokens: outputTok, CostUSD: e.CostUSD,
		}}, true
	case bus.SubagentEvent:
		inner, ok := wsEventFromBus(e.Inner)
		if !ok {
			return Event{}, false
		}
		return Event{Type: "subagent_event", Data: SubagentEventData{
			JobID: e.JobID, Event: &inner,
		}}, true
	case bus.CompactionStarted:
		return Event{Type: "compaction_start"}, true
	case bus.CompactionEnded:
		return Event{Type: "compaction_end"}, true
	default:
		return Event{}, false
	}
}

// wsLossyEventTypes are streaming deltas that may be dropped under backpressure
// without corrupting UI state (the authoritative message_end/tool_end follows).
var wsLossyEventTypes = map[string]bool{
	"text_delta":      true,
	"thinking_delta":  true,
	"tool_update":     true,
	"tool_call_delta": true,
}

// isLossyWsEvent reports whether e can be safely dropped on channel overflow.
// A subagent_event is lossy iff the event it wraps is lossy.
func isLossyWsEvent(e Event) bool {
	if e.Type == "subagent_event" {
		if d, ok := e.Data.(SubagentEventData); ok && d.Event != nil {
			return isLossyWsEvent(*d.Event)
		}
		return false
	}
	return wsLossyEventTypes[e.Type]
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
	subagents, _ := bus.QueryTyped[bus.GetSubagents, []bus.SubagentSnapshot](b, bus.GetSubagents{})
	goalInfo, _ := bus.QueryTyped[bus.GetGoal, bus.GoalInfo](b, bus.GetGoal{})

	data := InitData{
		Messages:       msgs,
		State:          state,
		ContextPercent: ctxPct,
		PermissionMode: permMode,
		Tasks:          taskList,
		PathScope:      pathInfo.Scope,
	}

	if len(subagents) > 0 {
		data.Subagents = make([]SubagentInitData, len(subagents))
		for i, sa := range subagents {
			data.Subagents[i] = SubagentInitData{
				JobID:    sa.JobID,
				Task:     sa.Task,
				Model:    sa.Model,
				Status:   sa.Status,
				Async:    sa.Async,
				Messages: sa.Messages,
			}
		}
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
	if goalInfo.Active {
		data.GoalActive = true
		data.GoalObjective = goalInfo.Objective
		data.GoalIteration = goalInfo.Iteration
		data.GoalStalled = goalInfo.Stalled
	}

	return data
}
