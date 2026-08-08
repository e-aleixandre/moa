package serve

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"sync"
	"unicode/utf8"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tasks"
	"github.com/e-aleixandre/moa/pkg/tool"
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

// A WebSocket init must be small enough for a mobile browser to parse and
// render without being killed by memory pressure. Older history remains safely
// persisted; the client receives a recent display tail on reconnect.
const (
	initHistoryMaxMessages = 150
	initHistoryMaxBytes    = 1 << 20
	historyContentMaxBytes = 64 << 10
)

// newWsReactor subscribes to all bus events and session context cancellation.
// cwd is the session working directory, used to resolve relative file paths
// when enriching edit tool_start events with real line numbers.
// Returns the reactor and a read-only channel for the WS writer loop.
func newWsReactor(b bus.EventBus, sessionCtx context.Context, cwd string, attentionGeneration ...func(uint64) uint64) *wsReactor {
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

	hasAttentionResolver := len(attentionGeneration) > 0 && attentionGeneration[0] != nil
	resolver := func(uint64) uint64 { return 0 }
	if hasAttentionResolver {
		resolver = attentionGeneration[0]
	}
	r.unsubs = append(r.unsubs, b.SubscribeAllSeq(func(seq uint64, event any) {
		attentionGen := uint64(0)
		if isAttentionEvent(event) {
			attentionGen = resolver(seq)
			if hasAttentionResolver && attentionGen == 0 {
				// Never serialize an attention occurrence with generation 0: the
				// client cannot acknowledge it, which would strand its roster dot.
				// Closing forces the explicit init attention_bound recovery path.
				r.cleanup()
				return
			}
		}
		if wsEvent, ok := wsEventFromBus(event, attentionGen); ok {
			wsEvent.Seq = seq
			send(enrichEditToolStart(wsEvent, cwd))
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

func isAttentionEvent(event any) bool {
	switch event := event.(type) {
	case bus.RunEnded, bus.PermissionRequested, bus.AskUserRequested:
		return true
	case bus.StateChanged:
		return event.State == string(bus.StateError)
	default:
		return false
	}
}

// enrichEditToolStart adds the real 1-based starting line number to edit
// tool_start events, so the frontend diff preview numbers lines like the
// final diff. The event fires before the edit executes, so the file still
// holds the pre-edit content. Degrades silently (StartLine stays 0) when the
// file can't be read or oldText isn't found.
func enrichEditToolStart(e Event, cwd string) Event {
	if e.Type != "tool_start" {
		return e
	}
	d, ok := e.Data.(ToolStartData)
	if !ok || d.ToolName != "edit" {
		return e
	}
	path, _ := d.Args["path"].(string)
	oldText, _ := d.Args["oldText"].(string)
	if path == "" || oldText == "" {
		return e
	}
	if !filepath.IsAbs(path) && cwd != "" {
		path = filepath.Join(cwd, path)
	}
	d.StartLine = tool.EditStartLineForFile(path, oldText)
	e.Data = d
	return e
}

func wsEventFromBus(event any, attentionGeneration ...uint64) (Event, bool) {
	attentionGen := uint64(0)
	if len(attentionGeneration) > 0 {
		attentionGen = attentionGeneration[0]
	}
	switch e := event.(type) {
	case bus.StateChanged:
		return Event{Type: "state_change", Data: StateChangeData{
			State: e.State, Error: e.Error, UnseenGen: attentionGen,
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
		var inputTok, outputTok int
		if e.Message.Usage != nil {
			// Input the model processed in this provider call includes cached
			// context replayed on every step.
			inputTok = e.Message.Usage.Input + e.Message.Usage.CacheRead + e.Message.Usage.CacheWrite
			outputTok = e.Message.Usage.Output
		}
		return Event{Type: "message_end", Data: MessageEndData{
			Text: truncateHistoryString(e.FullText), MsgID: e.Message.MsgID,
			InputTokens: inputTok, OutputTokens: outputTok,
		}}, true
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
		return Event{Type: "run_end", Data: RunEndData{Text: e.FinalText, RunGen: e.RunGen, UnseenGen: attentionGen}}, true
	case bus.ContextUpdated:
		return Event{Type: "context_update", Data: ContextUpdateData{ContextPercent: e.Percent}}, true
	case bus.MCPChanged:
		return Event{Type: "mcp_change", Data: MCPChangeData{
			Total: e.Total, Ready: e.Ready, Disabled: e.Disabled,
			Unhealthy: e.Unhealthy, Pending: e.Pending,
		}}, true
	case bus.RunTokensUpdated:
		return Event{Type: "run_tokens", Data: RunTokensData{Up: e.Up, Down: e.Down}}, true
	case bus.SessionCostUpdated:
		return Event{Type: "session_cost", Data: SessionCostData{CostUSD: e.TotalUSD}}, true
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
			Model: e.Model, Provider: e.Provider, Thinking: e.Thinking,
			PermissionMode: e.PermissionMode, PathScope: e.PathScope,
			CompactAt: e.CompactAt, ContextWindow: e.ContextWindow,
		}}, true
	case bus.PlanModeChanged:
		return Event{Type: "plan_mode", Data: PlanModeData{
			Mode: e.Mode, PlanFile: e.PlanFile,
		}}, true
	case bus.GoalChanged:
		return Event{Type: "goal_change", Data: GoalChangeData{
			Active: e.Active, Objective: e.Objective, WorkDir: e.WorkDir,
			Iteration: e.Iteration, Stalled: e.Stalled,
		}}, true
	case bus.GoalIterationEnded:
		return Event{Type: "goal_iteration", Data: GoalIterationData{
			Iteration: e.Iteration, Satisfied: e.Satisfied, Feedback: e.Feedback,
		}}, true
	case bus.GoalVerifyStarted:
		return Event{Type: "goal_verify", Data: map[string]any{
			"active": true, "iteration": e.Iteration,
		}}, true
	case bus.GoalVerifyEnded:
		return Event{Type: "goal_verify", Data: map[string]any{
			"active": e.Verifying, "iteration": e.Iteration,
		}}, true
	case bus.GoalEnded:
		return Event{Type: "goal_end", Data: GoalEndData{Reason: e.Reason}}, true
	case bus.CommandExecuted:
		messages, truncated := limitInitHistory(e.Messages)
		return Event{Type: "command", Data: CommandData{
			Command: e.Command, Messages: messages, HistoryTruncated: truncated,
		}}, true
	case bus.Steered:
		data := SteerData{ID: e.ID, MsgID: e.MsgID, Text: truncateHistoryString(e.Text), Custom: projectWSMessageCustom(e.Custom)}
		if len(e.Content) > 0 {
			// Same history projection as UserMessageAppended, so an attached
			// image travels bounded (inline payloads stripped to references)
			// instead of blowing up the frame.
			sanitized, _ := sanitizeHistoryMessage(core.WrapMessage(core.NewUserMessageWithContent(e.Content)))
			data.Content = sanitized.Content
		}
		return Event{Type: "steer", Data: data}, true
	case bus.UserMessageAppended:
		data := UserMessageData{MsgID: e.MsgID, Text: truncateHistoryString(e.Text), Custom: projectWSMessageCustom(e.Custom)}
		if len(e.Content) > 0 {
			// Reuse the history projection so inline attachment payloads and
			// oversized text are bounded exactly as on reconnect.
			sanitized, _ := sanitizeHistoryMessage(core.WrapMessage(core.NewUserMessageWithContent(e.Content)))
			data.Content = sanitized.Content
		}
		return Event{Type: "user_message", Data: data}, true
	case bus.CommandQueued:
		return Event{Type: "command_queued", Data: CommandQueuedData{ID: e.ID, Raw: e.Raw}}, true
	case bus.CommandDequeued:
		return Event{Type: "command_dequeued", Data: CommandDequeuedData{ID: e.ID, Raw: e.Raw, Executed: e.Executed, Err: e.Err}}, true
	case bus.SteersCanceled:
		return Event{Type: "steers_canceled"}, true
	case bus.AutoVerifyStarted:
		return Event{Type: "auto_verify_start", Data: map[string]any{
			"dir": e.Dir, "manual": e.Manual,
		}}, true
	case bus.AutoVerifyEnded:
		data := map[string]any{"all_pass": e.AllPass, "summary": e.Summary}
		if e.Err != nil {
			data["error"] = e.Err.Error()
		}
		return Event{Type: "auto_verify_end", Data: data}, true
	case bus.PermissionRequested:
		return Event{Type: "permission_request", Data: PermissionData{
			ID: e.ID, RunGen: e.RunGen, UnseenGen: attentionGen, ToolName: e.ToolName, Args: e.Args,
			AllowPattern: e.AllowPattern,
		}}, true
	case bus.PermissionResolved:
		return Event{Type: "permission_resolved", Data: map[string]any{"id": e.ID}}, true
	case bus.AskUserRequested:
		return Event{Type: "ask_user", Data: map[string]any{
			"id": e.ID, "run_gen": e.RunGen, "unseen_gen": attentionGen, "questions": e.Questions,
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
		data := SubagentStartData{
			JobID: e.JobID, OriginToolCallID: e.OriginToolCallID, Task: e.Task, Model: e.Model, Thinking: e.Thinking, Async: e.Async, AccentIndex: e.AccentIndex,
		}
		if !e.StartedAt.IsZero() {
			data.StartedAtMs = e.StartedAt.UnixMilli()
		}
		return Event{Type: "subagent_start", Data: data}, true
	case bus.SubagentUsage:
		var inputTok, outputTok int
		if e.Usage != nil {
			inputTok = e.Usage.Input
			outputTok = e.Usage.Output
		}
		return Event{Type: "subagent_usage", Data: SubagentUsageData{
			JobID: e.JobID, InputTokens: inputTok, OutputTokens: outputTok, CostUSD: e.CostUSD,
			ContextPercent: e.ContextPercent,
		}}, true
	case bus.SubagentEnded:
		var inputTok, outputTok int
		if e.Usage != nil {
			inputTok = e.Usage.Input
			outputTok = e.Usage.Output
		}
		data := SubagentEndData{
			JobID: e.JobID, Task: e.Task, Async: e.Async, Status: e.Status,
			Result: truncateHistoryString(e.Result), Error: truncateHistoryString(e.Error),
			InputTokens: inputTok, OutputTokens: outputTok, CostUSD: e.CostUSD,
		}
		data.Excerpt = data.Result != e.Result || data.Error != e.Error
		if !e.FinishedAt.IsZero() {
			data.FinishedAtMs = e.FinishedAt.UnixMilli()
		}
		return Event{Type: "subagent_end", Data: data}, true
	case bus.SubagentEvent:
		inner, ok := wsEventFromBus(e.Inner, attentionGen)
		if !ok {
			return Event{}, false
		}
		return Event{Type: "subagent_event", Data: SubagentEventData{
			JobID: e.JobID, Event: &inner,
		}}, true
	case bus.BashJobStarted:
		return Event{Type: "bash_job_start", Data: BashJobStartData{JobID: e.JobID, OwnerAgentID: e.OwnerAgentID, Command: e.Command, CWD: e.CWD}}, true
	case bus.BashJobOutput:
		return Event{Type: "bash_job_output", Data: BashJobOutputData{JobID: e.JobID, OwnerAgentID: e.OwnerAgentID, Delta: e.Delta}}, true
	case bus.BashJobEnded:
		return Event{Type: "bash_job_end", Data: BashJobEndData{JobID: e.JobID, OwnerAgentID: e.OwnerAgentID, Status: e.Status, Output: e.Output}}, true
	case bus.BashCompleted:
		return Event{Type: "bash_complete", Data: BashCompleteData{
			JobID: e.JobID, OwnerAgentID: e.OwnerAgentID, Command: e.Command, Status: e.Status, Text: e.Text,
		}}, true
	case bus.CompactionStarted:
		return Event{Type: "compaction_start"}, true
	case bus.CompactionEnded:
		return Event{Type: "compaction_end"}, true
	default:
		return Event{}, false
	}
}

// projectWSMessageCustom is a second transport boundary for callers that
// publish bus events directly. Keep it aligned with bus.projectLiveCustom.
func projectWSMessageCustom(custom map[string]any) map[string]any {
	source, _ := custom["source"].(string)
	if source == "" {
		return nil
	}
	projected := map[string]any{"source": source}
	if source == "secret_batch" {
		switch aliases := custom["secret_aliases"].(type) {
		case []string:
			projected["secret_aliases"] = append([]string(nil), aliases...)
		case []any:
			values := make([]string, 0, len(aliases))
			for _, alias := range aliases {
				if value, ok := alias.(string); ok {
					values = append(values, value)
				}
			}
			projected["secret_aliases"] = values
		}
	}
	return projected
}

// wsLossyEventTypes are streaming deltas that may be dropped under backpressure
// without corrupting UI state (the authoritative message_end/tool_end follows).
var wsLossyEventTypes = map[string]bool{
	"text_delta":      true,
	"thinking_delta":  true,
	"tool_update":     true,
	"tool_call_delta": true,
	"bash_job_output": true,
	"subagent_usage":  true,
}

// countImageContent returns how many image blocks a steer's content carries, so
// a reconnecting client can badge the chip (the base64 payload itself is not
// re-transported in the snapshot).
func countImageContent(content []core.Content) int {
	n := 0
	for _, c := range content {
		if c.Type == "image" {
			n++
		}
	}
	return n
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

// buildInitData constructs the WS init payload from bus queries. The streaming
// aggregate and the live tool calls are passed in (captured atomically with the
// sequence cut by the caller via SnapshotInFlightWithCut) rather than queried
// here, so in-flight state can't be both seeded into the snapshot and replayed
// live after the cut.
func buildInitData(sess *ManagedSession, streaming bus.StreamingAggregate, liveTools []bus.LiveToolCall) InitData {
	return buildInitDataAtAttentionGen(sess, streaming, liveTools, sess.attentionGen.Load())
}

func buildInitDataAtAttentionGen(sess *ManagedSession, streaming bus.StreamingAggregate, liveTools []bus.LiveToolCall, unseenGen uint64, sinceMsg ...string) InitData {
	b := sess.runtime.Bus

	// Use display messages (full history from tree) instead of agent messages.
	// A validated delta is used only when its entire suffix fits the same mobile
	// bounds as a normal init. Truncating a suffix would silently leave a hole in
	// the cached transcript, so it deliberately fails closed to a full snapshot.
	msgs := []core.AgentMessage(nil)
	deltaBase := ""
	if len(sinceMsg) > 0 && sinceMsg[0] != "" {
		if delta, err := bus.QueryTyped[bus.GetDisplayMessagesSince, bus.DisplayMessagesSince](b, bus.GetDisplayMessagesSince{EntryID: sinceMsg[0]}); err == nil && delta.Valid {
			if bounded, truncated := limitInitHistory(delta.Messages); !truncated {
				msgs, deltaBase = bounded, sinceMsg[0]
			}
		}
	}
	if deltaBase == "" {
		msgs, _ = bus.QueryTyped[bus.GetDisplayMessages, []core.AgentMessage](b, bus.GetDisplayMessages{})
	}
	state, _ := bus.QueryTyped[bus.GetSessionState, string](b, bus.GetSessionState{})
	ctxPct, _ := bus.QueryTyped[bus.GetContextUsage, int](b, bus.GetContextUsage{})
	compactAt, _ := bus.QueryTyped[bus.GetCompactAt, int](b, bus.GetCompactAt{})
	compactAtMin, _ := bus.QueryTyped[bus.GetCompactAtFloor, int](b, bus.GetCompactAtFloor{})
	initModel, _ := bus.QueryTyped[bus.GetModel, core.Model](b, bus.GetModel{})
	permMode, _ := bus.QueryTyped[bus.GetPermissionMode, string](b, bus.GetPermissionMode{})
	pending, _ := bus.QueryTyped[bus.GetPendingApproval, bus.PendingApprovalInfo](b, bus.GetPendingApproval{})
	taskList, _ := bus.QueryTyped[bus.GetTasks, []tasks.Task](b, bus.GetTasks{})
	pathInfo, _ := bus.QueryTyped[bus.GetPathPolicy, bus.PathPolicyInfo](b, bus.GetPathPolicy{})
	planInfo, _ := bus.QueryTyped[bus.GetPlanMode, bus.PlanModeInfo](b, bus.GetPlanMode{})
	// Read bash jobs before subagents. GetSubagents retains terminal owners of
	// its current bash snapshot, so this ordering keeps every bash included in
	// this init payload routable to a real owner.
	bashJobs, _ := bus.QueryTyped[bus.GetBashJobs, []bus.BashJobSnapshot](b, bus.GetBashJobs{})
	subagents, _ := bus.QueryTyped[bus.GetSubagents, []bus.SubagentSnapshot](b, bus.GetSubagents{})
	goalInfo, _ := bus.QueryTyped[bus.GetGoal, bus.GoalInfo](b, bus.GetGoal{})
	cost, _ := bus.QueryTyped[bus.GetSessionCost, float64](b, bus.GetSessionCost{})
	runTokens, _ := bus.QueryTyped[bus.GetRunTokens, bus.RunTokens](b, bus.GetRunTokens{})
	compacting, _ := bus.QueryTyped[bus.GetCompacting, bool](b, bus.GetCompacting{})
	pendingSteers, _ := bus.QueryTyped[bus.GetPendingSteers, []core.SteerItem](b, bus.GetPendingSteers{})

	msgs, historyTruncated := limitInitHistory(msgs)
	data := InitData{
		ServerInstance:    sess.serverInstance,
		UnseenGen:         unseenGen,
		Messages:          msgs,
		HistoryTruncated:  historyTruncated,
		DeltaBase:         deltaBase,
		State:             state,
		ContextPercent:    ctxPct,
		ContextWindow:     initModel.MaxInput,
		CompactAt:         compactAt,
		CompactAtMin:      compactAtMin,
		PermissionMode:    permMode,
		Tasks:             taskList,
		PathScope:         pathInfo.Scope,
		CostUSD:           cost,
		RunTokensUp:       runTokens.Up,
		RunTokensDown:     runTokens.Down,
		Compacting:        compacting,
		StreamingText:     truncateHistoryString(streaming.Text),
		StreamingThinking: truncateHistoryString(streaming.Thinking),
		LiveTools:         liveToolInitData(liveTools),
	}

	// Read the run anchor from the runtime's synchronous state. The cache-clock
	// subscriber is asynchronous, so using its copy here could omit an anchor
	// for a RunStarted already included by this snapshot's sequence cut.
	runStartedAt := sess.runtime.Context().RunStartedAt()
	if !runStartedAt.IsZero() && (state == string(StateRunning) || state == string(StatePermission)) {
		data.RunStartedAtMs = runStartedAt.UnixMilli()
	}

	if len(pendingSteers) > 0 {
		data.PendingSteers = make([]PendingSteerData, len(pendingSteers))
		for i, s := range pendingSteers {
			data.PendingSteers[i] = PendingSteerData{
				ID:      s.ID,
				Text:    s.Text,
				Command: s.IsBarrier(),
				Images:  countImageContent(s.Content),
			}
		}
	}

	if len(subagents) > 0 {
		data.Subagents = make([]SubagentInitData, len(subagents))
		for i, sa := range subagents {
			messages, _ := limitInitHistory(sa.Messages)
			sad := SubagentInitData{
				JobID:            sa.JobID,
				OriginToolCallID: sa.OriginToolCallID,
				Task:             sa.Task,
				Model:            sa.Model,
				Thinking:         sa.Thinking,
				Status:           sa.Status,
				Async:            sa.Async,
				Messages:         messages,
				ContextPercent:   sa.ContextPercent,
				AccentIndex:      sa.AccentIndex,
			}
			if !sa.StartedAt.IsZero() {
				sad.StartedAtMs = sa.StartedAt.UnixMilli()
			}
			if sa.Usage != nil {
				sad.InputTokens = sa.Usage.Input
				sad.OutputTokens = sa.Usage.Output
			}
			sad.CostUSD = sa.CostUSD
			data.Subagents[i] = sad
		}
	}
	// Terminal outcomes are persisted separately from parent model delivery:
	// an async result consumed by subagent_wait never creates a notification
	// prompt, but its completed/failed card must survive reload just the same.
	if sess.persister != nil {
		transcripts, err := sess.persister.subagentStore(sess.ID).List()
		if err == nil && len(transcripts) > 0 {
			data.SubagentOutcomes = make([]SubagentEndData, 0, len(transcripts))
			for _, transcript := range transcripts {
				if transcript.Status != "completed" && transcript.Status != "failed" && transcript.Status != "cancelled" {
					continue
				}
				result, resultErr := persistedSubagentOutcome(transcript)
				outcome := SubagentEndData{
					JobID: transcript.JobID, Task: transcript.Task, Async: transcript.Async,
					Status: transcript.Status, Result: truncateHistoryString(result), Error: truncateHistoryString(resultErr),
					CostUSD: transcript.CostUSD,
				}
				outcome.Excerpt = outcome.Result != result || outcome.Error != resultErr
				if !transcript.FinishedAt.IsZero() {
					outcome.FinishedAtMs = transcript.FinishedAt.UnixMilli()
				}
				if transcript.Usage != nil {
					outcome.InputTokens = transcript.Usage.Input
					outcome.OutputTokens = transcript.Usage.Output
				}
				data.SubagentOutcomes = append(data.SubagentOutcomes, outcome)
			}
		}
	}
	if len(data.SubagentOutcomes) > 1 {
		sortSubagentOutcomes(data.SubagentOutcomes)
	}
	if len(bashJobs) > 0 {
		data.BashJobs = make([]BashJobInitData, len(bashJobs))
		for i, job := range bashJobs {
			data.BashJobs[i] = BashJobInitData{JobID: job.JobID, OwnerAgentID: job.OwnerAgentID, Command: job.Command, CWD: job.CWD, Status: job.Status, Output: job.Output}
		}
	}

	data.PendingPermission, data.PendingAsk = pendingAttentionData(pending, unseenGen)
	if planInfo.Mode != "off" {
		data.PlanMode = planInfo.Mode
		data.PlanFile = planInfo.PlanFile
	}
	if goalInfo.Active {
		data.GoalActive = true
		data.GoalObjective = goalInfo.Objective
		data.GoalWorkDir = goalInfo.WorkDir
		data.GoalIteration = goalInfo.Iteration
		data.GoalStalled = goalInfo.Stalled
		data.GoalVerifying = goalInfo.Verifying
	}

	return data
}

func pendingAttentionData(pending bus.PendingApprovalInfo, attentionGen uint64) (*PermissionData, *AskData) {
	var permissionData *PermissionData
	if pending.Permission != nil {
		permissionData = &PermissionData{
			ID:           pending.Permission.ID,
			UnseenGen:    attentionGen,
			ToolName:     pending.Permission.ToolName,
			Args:         pending.Permission.Args,
			AllowPattern: pending.Permission.AllowPattern,
		}
	}
	var askData *AskData
	if pending.Ask != nil {
		askData = &AskData{
			ID:        pending.Ask.ID,
			UnseenGen: attentionGen,
			Questions: pending.Ask.Questions,
		}
	}
	return permissionData, askData
}

func sortSubagentOutcomes(outcomes []SubagentEndData) {
	sort.SliceStable(outcomes, func(i, j int) bool {
		left, right := outcomes[i].FinishedAtMs, outcomes[j].FinishedAtMs
		if left == 0 {
			return false
		}
		if right == 0 {
			return true
		}
		return left < right
	})
}

// persistedSubagentOutcome reads the explicit terminal fields written by the
// structured lifecycle. Pre-change transcripts have neither: completed jobs
// can safely recover their result from the child's final assistant response;
// failed jobs deliberately remain empty because a partial assistant reply is
// not necessarily the failure error. The frontend retains any legacy parent
// notification text when this fallback is empty.
func persistedSubagentOutcome(transcript session.SubagentTranscript) (result, resultErr string) {
	result, resultErr = transcript.Result, transcript.Error
	if transcript.Status == "completed" && result == "" {
		result = core.ExtractFinalAssistantText(transcript.Messages)
	}
	return result, resultErr
}

// liveToolInitData projects the bus registry of in-flight tool calls into the
// snapshot payload, bounding what travels: only the args map is unbounded in
// principle (a write carries a whole file), so it is dropped wholesale when its
// encoding exceeds the same per-content budget the history projection uses.
// Dropping args still leaves the tool NAME, which is what the live row needs to
// stop saying "Calling"; shipping a megabyte of file body to a phone to render
// one line would not.
func liveToolInitData(calls []bus.LiveToolCall) []LiveToolInitData {
	if len(calls) == 0 {
		return nil
	}
	out := make([]LiveToolInitData, 0, len(calls))
	for _, c := range calls {
		if c.ToolCallID == "" {
			continue
		}
		d := LiveToolInitData{
			ToolCallID: c.ToolCallID,
			ToolName:   c.ToolName,
			Args:       boundedLiveToolArgs(c.Args),
			Status:     c.Phase,
		}
		if !c.StartedAt.IsZero() {
			d.StartedAtMs = c.StartedAt.UnixMilli()
		}
		out = append(out, d)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// boundedLiveToolArgs returns the args map only when it is small enough to be
// worth sending; oversized (or unserializable) args are replaced by a marker so
// the client renders the row without a stale/huge object. Mirrors
// boundedHistoryMap, which does the same for historic tool-call arguments.
func boundedLiveToolArgs(args map[string]any) map[string]any {
	if len(args) == 0 {
		return nil
	}
	encoded, err := json.Marshal(args)
	if err != nil || len(encoded) > historyContentMaxBytes {
		return map[string]any{"_truncated": true}
	}
	return args
}

// limitInitHistory returns a bounded, recent display tail. It also removes
// large inline attachment payloads and bounds individual text blocks: sending
// a whole historic image or pasted file to a phone is neither useful nor safe.
func limitInitHistory(messages []core.AgentMessage) ([]core.AgentMessage, bool) {
	if len(messages) == 0 {
		return nil, false
	}
	selected := make([]core.AgentMessage, 0, min(len(messages), initHistoryMaxMessages))
	bytes := 0
	truncated := false
	firstIndex := len(messages)
	for i := len(messages) - 1; i >= 0; i-- {
		msg, size := sanitizeHistoryMessage(messages[i])
		if size > initHistoryMaxBytes {
			msg = core.WrapMessage(core.Message{
				Role:    messages[i].Role,
				MsgID:   messages[i].MsgID,
				Content: []core.Content{core.TextContent("[historic message too large to load on this device]")},
			})
			size = len("[historic message too large to load on this device]") + 128
		}
		if len(selected) >= initHistoryMaxMessages || (len(selected) > 0 && bytes+size > initHistoryMaxBytes) {
			truncated = true
			break
		}
		selected = append(selected, msg)
		firstIndex = i
		bytes += size
	}
	if len(selected) < len(messages) {
		truncated = true
	}
	for left, right := 0, len(selected)-1; left < right; left, right = left+1, right-1 {
		selected[left], selected[right] = selected[right], selected[left]
	}
	// Do not begin a display tail with orphaned tool results: retain the
	// immediately preceding assistant/tool-call message when possible.
	if len(selected) > 0 && selected[0].Role == "tool_result" && firstIndex > 0 {
		previous := messages[firstIndex-1]
		if previous.Role == "assistant" {
			projected, _ := sanitizeHistoryMessage(previous)
			selected = append([]core.AgentMessage{projected}, selected...)
		} else {
			for len(selected) > 0 && selected[0].Role == "tool_result" {
				selected = selected[1:]
			}
		}
	}
	return selected, truncated
}

func sanitizeHistoryMessage(msg core.AgentMessage) (core.AgentMessage, int) {
	copyMsg := msg
	copyMsg.Content = append([]core.Content(nil), msg.Content...)
	// Reconnect history crosses the same public WS boundary as live message
	// events. Do not expose internal custom fields merely because this is an
	// init snapshot.
	copyMsg.Custom = projectWSMessageCustom(copyMsg.Custom)
	for i := range copyMsg.Content {
		content := &copyMsg.Content[i]
		switch content.Type {
		case "image", "document":
			if len(content.Data) > historyContentMaxBytes {
				content.Data = ""
				if content.Filename == "" {
					content.Filename = "attachment omitted from reconnect history"
				}
			}
		case "text":
			content.Text = truncateHistoryString(content.Text)
		case "thinking":
			content.Thinking = truncateHistoryString(content.Thinking)
		}
		content.Arguments = boundedHistoryMap(content.Arguments)
	}
	encoded, err := json.Marshal(copyMsg)
	if err != nil {
		return core.WrapMessage(core.Message{Role: msg.Role, Content: []core.Content{core.TextContent("[historic message unavailable on this device]")}}), 96
	}
	return copyMsg, len(encoded)
}

func boundedHistoryMap(values map[string]any) map[string]any {
	if len(values) == 0 {
		return values
	}
	encoded, err := json.Marshal(values)
	if err != nil || len(encoded) > historyContentMaxBytes {
		return map[string]any{"_truncated": true}
	}
	return values
}

func truncateHistoryString(value string) string {
	if len(value) <= historyContentMaxBytes {
		return value
	}
	end := historyContentMaxBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + "\n\n[historic content truncated on this device]"
}
