package serve

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"sync"
	"time"
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

// Terminal subagent cards are collapsed summaries, not conversation body, so
// they get their own — much tighter — budget than historyContentMaxBytes: the
// card shows a preview and the full text is one tap away through
// GET /api/sessions/{id}/subagents/{jobID}. A session accumulates outcomes for
// its whole life, so an unbounded per-outcome budget turned the init payload
// into megabytes of text nobody reads on arrival.
const (
	subagentOutcomeExcerptMaxBytes = 2 << 10
	// subagentTaskMaxBytes bounds the model-authored task echoed on every card.
	subagentTaskMaxBytes = 1 << 10
	// initSubagentOutcomeLimit keeps the newest outcomes by completion time.
	// This is a LOSSY cap, not a display preference: an outcome dropped here may
	// have no other representation in the payload. A card whose result was
	// consumed by subagent_wait never produced a parent notification message, so
	// nothing in the message history stands in for it — the client simply will
	// not show that job until it is opened by ID. The cap is accepted as the
	// fail-closed side of a bounded payload: sessions in production carried 200+
	// outcomes of up to 108 KiB each.
	initSubagentOutcomeLimit = 50
)

// Live children share one transcript budget in the init payload. Granting each
// running subagent a full initHistoryMaxBytes meant ten of them could add ten
// megabytes on their own.
//
// The two constants CONFLICT above 16 concurrent children (16 × 32 KiB = 512
// KiB), and the floor deliberately wins: a child whose transcript is cut to
// nothing is a useless row in the Live Dock, and this section is not the one
// that made the payload explode. So the maximum is a TARGET, not a guarantee —
// the guarantee is per child. The real ceiling is therefore
// max(initSubagentHistoryMaxBytes, liveChildren × initSubagentHistoryMinBytes),
// which the aggregate init budget test exercises with 10 and 20 children.
// Beyond that many live children the Live Dock has bigger problems than bytes.
const (
	initSubagentHistoryMaxBytes = 512 << 10
	initSubagentHistoryMinBytes = 32 << 10
)

// outcomeResendGrace widens the delta cutoff backwards. The anchor is a message
// timestamp in whole seconds, and a card can go terminal in the same second the
// client's last cached message was created, so an exact comparison could drop a
// card the client never saw. Re-sending a few seconds of already-known outcomes
// is cheap; losing one until the next full snapshot is not.
const outcomeResendGrace = 5 * time.Second

// initPayloadMaxBytes is the aggregate budget asserted by
// TestBuildInitData_TotalPayloadStaysWithinBudget. It is not enforced at
// runtime — a hard cut would silently drop state — but every section added to
// InitData must bound itself so the encoded snapshot stays under it.
const initPayloadMaxBytes = 2 << 20

// newWsReactor subscribes to all bus events and session context cancellation.
// cwd is the session working directory, used to resolve relative file paths
// when enriching edit tool_start events with real line numbers.
// Returns the reactor and a read-only channel for the WS writer loop.
func newWsReactor(b bus.EventBus, sessionCtx context.Context, cwd string) *wsReactor {
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

	r.unsubs = append(r.unsubs, b.SubscribeAllSeq(func(seq uint64, event any) {
		if wsEvent, ok := wsEventFromBus(event); ok {
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
		// The same classifier as the tracker decides whether this terminal event
		// is an attention occurrence. Failed and cancelled runs still travel to
		// the client, but must not be treated as a second occurrence.
		attention := attentionEvent(e)
		return Event{Type: "run_end", Data: RunEndData{
			Text: e.FinalText, RunGen: e.RunGen,
			Cancelled: e.Cancelled, HasError: !attention && e.Err != nil,
		}}, true
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
			Fast: e.Fast, FastSupported: e.FastSupported, FastNote: e.FastNote,
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
			ID: e.ID, RunGen: e.RunGen, ToolName: e.ToolName, Args: e.Args,
			AllowPattern: e.AllowPattern,
		}}, true
	case bus.PermissionResolved:
		return Event{Type: "permission_resolved", Data: PromptResolvedData{ID: e.ID}}, true
	case bus.AskUserRequested:
		return Event{Type: "ask_user", Data: map[string]any{
			"id": e.ID, "run_gen": e.RunGen, "questions": e.Questions,
		}}, true
	case bus.AskUserResolved:
		return Event{Type: "ask_resolved", Data: PromptResolvedData{ID: e.ID}}, true
	case bus.SubagentCountChanged:
		return Event{Type: "subagent_count", Data: SubagentCountData{Count: e.Count}}, true
	case bus.SubagentCompleted:
		return Event{Type: "subagent_complete", Data: SubagentCompleteData{
			JobID: e.JobID, Task: e.Task, Status: e.Status, Text: e.Text,
		}}, true
	case bus.SubagentStarted:
		data := SubagentStartData{
			JobID: e.JobID, OriginToolCallID: e.OriginToolCallID, Task: e.Task, Title: e.Title, Model: e.Model, Thinking: e.Thinking, Async: e.Async, AccentIndex: e.AccentIndex,
		}
		if !e.StartedAt.IsZero() {
			data.StartedAtMs = e.StartedAt.UnixMilli()
		}
		return Event{Type: "subagent_start", Data: data}, true
	case bus.SubagentTitleChanged:
		return Event{Type: "subagent_title", Data: map[string]string{"job_id": e.JobID, "title": e.Title}}, true
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
			JobID: e.JobID, Task: truncateSubagentTask(e.Task), Async: e.Async, Status: e.Status,
			Result: truncateSubagentOutcome(e.Result), Error: truncateSubagentOutcome(e.Error),
			InputTokens: inputTok, OutputTokens: outputTok, CostUSD: e.CostUSD,
		}
		data.Excerpt = data.Result != e.Result || data.Error != e.Error
		if !e.FinishedAt.IsZero() {
			data.FinishedAtMs = e.FinishedAt.UnixMilli()
		}
		return Event{Type: "subagent_end", Data: data}, true
	case bus.SubagentEvent:
		inner, ok := wsEventFromBus(e.Inner)
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
		data := CompactionEndData{}
		if e.Marker != nil {
			marker, _ := sanitizeHistoryMessage(*e.Marker)
			data.Marker = &marker
		}
		return Event{Type: "compaction_end", Data: data}, true
	default:
		return Event{}, false
	}
}

// projectWSMessageCustom is a second transport boundary for callers that
// publish bus events directly. It mirrors bus.projectLiveCustom's allowlist,
// plus the keys only reconnect history carries: bus events project live user
// and steer messages, this one also projects tool results.
func projectWSMessageCustom(custom map[string]any) map[string]any {
	if marker, _ := custom["type"].(string); marker == "compaction_marker" {
		projected := map[string]any{"type": marker}
		if summary, ok := custom["summary"].(string); ok {
			projected["summary"] = truncateHistoryString(summary)
		}
		if tokens, ok := custom["tokens_before"].(int); ok {
			projected["tokens_before"] = tokens
		}
		for _, key := range []string{"read_files", "modified_files"} {
			if files, ok := custom[key].([]string); ok {
				projected[key] = append([]string(nil), files...)
			}
		}
		return projected
	}
	// A subagent launch result carries the job it spawned. The tool call ID is
	// the provider's, so this annotation is the only link from the launch row
	// to the child; without it a reconnected client cannot tell the launch and
	// the completion apart and draws both, the second one pointing at a
	// conversation it cannot open.
	jobID, _ := custom["subagent_job_id"].(string)
	source, _ := custom["source"].(string)
	if source == "" {
		if jobID == "" {
			return nil
		}
		return map[string]any{"subagent_job_id": jobID}
	}
	projected := map[string]any{"source": source}
	if jobID != "" {
		projected["subagent_job_id"] = jobID
	}
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
	if source == "event" {
		for _, key := range []string{"id", "source_name", "title"} {
			if value, ok := custom[key].(string); ok {
				projected[key] = value
			}
		}
		if autorun, ok := custom["autorun"].(bool); ok {
			projected["autorun"] = autorun
		}
		if steer, ok := custom["steer"].(bool); ok {
			projected["steer"] = steer
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
// aggregate and live tool calls are captured atomically with the sequence cut.
// sinceMsg, when non-empty, requests a validated delta resume from that entry.
func buildInitData(sess *ManagedSession, streaming bus.StreamingAggregate, liveTools []bus.LiveToolCall, sinceMsg string) InitData {
	b := sess.runtime.Bus

	// Use display messages (full history from tree) instead of agent messages.
	// A validated delta is used only when its entire suffix fits the same mobile
	// bounds as a normal init. Truncating a suffix would silently leave a hole in
	// the cached transcript, so it deliberately fails closed to a full snapshot.
	var msgs []core.AgentMessage
	var historyTruncated bool
	historyBefore := ""
	deltaBase := ""
	// outcomesSince dates the transcript the client already holds on a validated
	// delta, so only terminal subagent cards finished after it are re-sent. Zero
	// means "send them all" (full snapshot, or an anchor without a timestamp).
	var outcomesSince time.Time
	if sinceMsg != "" {
		if delta, err := bus.QueryTyped[bus.GetDisplayMessagesSince, bus.DisplayMessagesSince](b, bus.GetDisplayMessagesSince{EntryID: sinceMsg}); err == nil && delta.Valid {
			// A delta extends an already-present transcript, so it may legitimately
			// begin with results for calls in the client's prefix.
			if bounded, truncated := limitHistoryDelta(delta.Messages); !truncated {
				msgs, deltaBase = bounded, sinceMsg
				if !delta.EntryAt.IsZero() {
					outcomesSince = delta.EntryAt.Add(-outcomeResendGrace)
				}
			}
		}
	}
	if deltaBase == "" {
		full, _ := bus.QueryTyped[bus.GetDisplayMessages, []core.AgentMessage](b, bus.GetDisplayMessages{})
		msgs, historyTruncated = limitInitHistory(full)
		if historyTruncated && len(msgs) > 0 {
			historyBefore = msgs[0].MsgID
		}
	}
	state, _ := bus.QueryTyped[bus.GetSessionState, string](b, bus.GetSessionState{})
	ctxPct, _ := bus.QueryTyped[bus.GetContextUsage, int](b, bus.GetContextUsage{})
	compactAt, _ := bus.QueryTyped[bus.GetCompactAt, int](b, bus.GetCompactAt{})
	compactAtMin, _ := bus.QueryTyped[bus.GetCompactAtFloor, int](b, bus.GetCompactAtFloor{})
	initModel, _ := bus.QueryTyped[bus.GetModel, core.Model](b, bus.GetModel{})
	initFast := false
	if agent := sess.runtime.Context().Agent; agent != nil {
		initFast = agent.Fast()
	}
	permMode, _ := bus.QueryTyped[bus.GetPermissionMode, string](b, bus.GetPermissionMode{})
	pending, _ := bus.QueryTyped[bus.GetPendingApproval, bus.PendingApprovalInfo](b, bus.GetPendingApproval{})
	taskList, _ := bus.QueryTyped[bus.GetTasks, []tasks.Task](b, bus.GetTasks{})
	pathInfo, _ := bus.QueryTyped[bus.GetPathPolicy, bus.PathPolicyInfo](b, bus.GetPathPolicy{})
	// Read bash jobs before subagents. GetSubagents retains terminal owners of
	// its current bash snapshot, so this ordering keeps every bash included in
	// this init payload routable to a real owner.
	bashJobs, _ := bus.QueryTyped[bus.GetBashJobs, []bus.BashJobSnapshot](b, bus.GetBashJobs{})
	subagents, _ := bus.QueryTyped[bus.GetSubagents, []bus.SubagentSnapshot](b, bus.GetSubagents{})
	goalInfo, _ := bus.QueryTyped[bus.GetGoal, bus.GoalInfo](b, bus.GetGoal{})
	cost, _ := bus.QueryTyped[bus.GetSessionCost, float64](b, bus.GetSessionCost{})
	runTokens, _ := bus.QueryTyped[bus.GetRunTokens, bus.RunTokens](b, bus.GetRunTokens{})
	compacting, _ := bus.QueryTyped[bus.GetCompacting, bool](b, bus.GetCompacting{})
	autoVerifying, _ := bus.QueryTyped[bus.GetAutoVerifying, bool](b, bus.GetAutoVerifying{})
	pendingSteers, _ := bus.QueryTyped[bus.GetPendingSteers, []core.SteerItem](b, bus.GetPendingSteers{})

	data := InitData{
		ServerInstance:     sess.serverInstance,
		AttentionNamespace: sess.attentionNamespace,
		Messages:           msgs,
		HistoryTruncated:   historyTruncated,
		HistoryBefore:      historyBefore,
		DeltaBase:          deltaBase,
		State:              state,
		ContextPercent:     ctxPct,
		ContextWindow:      initModel.MaxInput,
		CompactAt:          compactAt,
		CompactAtMin:       compactAtMin,
		PermissionMode:     permMode,
		Fast:               initFast,
		FastSupported:      core.SupportsFast(initModel.ID),
		FastNote:           core.FastNote(initModel.ID),
		Tasks:              taskList,
		PathScope:          pathInfo.Scope,
		CostUSD:            cost,
		RunTokensUp:        runTokens.Up,
		RunTokensDown:      runTokens.Down,
		Compacting:         compacting,
		AutoVerifying:      autoVerifying,
		StreamingText:      truncateHistoryString(streaming.Text),
		StreamingThinking:  truncateHistoryString(streaming.Thinking),
		LiveTools:          liveToolInitData(liveTools),
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

	data.Subagents = liveSubagentInitData(subagents)
	// Terminal outcomes are persisted separately from parent model delivery:
	// an async result consumed by subagent_wait never creates a notification
	// prompt, but its completed/failed card must survive reload just the same.
	if sess.persister != nil {
		store := sess.persister.subagentStore(sess.ID)
		transcripts, err := store.ListSummaries()
		if err == nil && len(transcripts) > 0 {
			// A card is rendered against the launch row that spawned it. One
			// whose launch sits outside this payload has nothing to attach to,
			// so the client appends it after the last message instead — and in
			// a long session every card in the cap can miss at once, which
			// reads as a run of unrelated subagents at the end of the
			// transcript. The launch row is durable history, so skipping a card
			// here does not lose it: paging back to its turn renders it in
			// place.
			launched := launchedSubagentJobIDs(msgs)
			// ListSummaries is newest-finished first, so the cap is applied by
			// walking it in that order and stopping; the chronological order the
			// client expects is restored by sortSubagentOutcomes below.
			data.SubagentOutcomes = make([]SubagentEndData, 0, min(len(transcripts), initSubagentOutcomeLimit))
			for _, transcript := range transcripts {
				if len(data.SubagentOutcomes) >= initSubagentOutcomeLimit {
					break
				}
				if transcript.Status != "completed" && transcript.Status != "failed" && transcript.Status != "cancelled" {
					continue
				}
				if !launched[transcript.JobID] {
					continue
				}
				// On a validated delta the client already holds every card that
				// finished before its cached tail, so re-sending them is pure
				// waste on exactly the connection that can least afford it.
				if !outcomesSince.IsZero() && !transcript.FinishedAt.IsZero() && !transcript.FinishedAt.After(outcomesSince) {
					continue
				}
				result, resultErr := persistedSubagentOutcome(transcript)
				// Legacy completed transcripts did not persist Result. Retain
				// their historical final-assistant fallback while streaming past
				// all messages instead of rebuilding the entire child transcript.
				if transcript.Status == "completed" && transcript.Result == "" {
					legacyResult, err := store.LegacyResult(transcript.JobID)
					if err != nil {
						continue
					}
					result = legacyResult
				}
				outcome := SubagentEndData{
					JobID: transcript.JobID, Task: truncateSubagentTask(transcript.Task), Async: transcript.Async,
					Status: transcript.Status, Result: truncateSubagentOutcome(result), Error: truncateSubagentOutcome(resultErr),
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

	data.PendingPermission, data.PendingAsk = pendingAttentionData(pending)
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

func pendingAttentionData(pending bus.PendingApprovalInfo) (*PermissionData, *AskData) {
	var permissionData *PermissionData
	if pending.Permission != nil {
		permissionData = &PermissionData{
			ID:           pending.Permission.ID,
			ToolName:     pending.Permission.ToolName,
			Args:         pending.Permission.Args,
			AllowPattern: pending.Permission.AllowPattern,
		}
	}
	var askData *AskData
	if pending.Ask != nil {
		askData = &AskData{
			ID:        pending.Ask.ID,
			Questions: pending.Ask.Questions,
		}
	}
	return permissionData, askData
}

func launchedSubagentJobIDs(msgs []core.AgentMessage) map[string]bool {
	launched := make(map[string]bool)
	for _, msg := range msgs {
		if jobID, ok := msg.Custom["subagent_job_id"].(string); ok && jobID != "" {
			launched[jobID] = true
		}
	}
	return launched
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

// liveSubagentInitData projects the running children into the snapshot. Their
// transcripts share ONE aggregate budget rather than each receiving a full
// history's worth: ten running subagents used to be able to add ten megabytes
// on their own. The share is recomputed from what is left after each child, so
// a quiet child hands its unused bytes to the next one and the section total
// stays bounded no matter how many run. A child the user actually opens loads
// its complete transcript from the subagent endpoint.
func liveSubagentInitData(subagents []bus.SubagentSnapshot) []SubagentInitData {
	if len(subagents) == 0 {
		return nil
	}
	remainingBytes := initSubagentHistoryMaxBytes
	out := make([]SubagentInitData, len(subagents))
	for i, sa := range subagents {
		// The fair share of what is left, never more than a full ration: a quiet
		// child hands its unused bytes to the ones after it, but no single child
		// may absorb the entire remainder (which is what let the last child ship
		// hundreds of KB). The floor is the per-child guarantee and wins over
		// the aggregate target — see the constants' comment.
		share := max(min(remainingBytes/(len(subagents)-i), initSubagentHistoryMaxBytes/len(subagents)), initSubagentHistoryMinBytes)
		messages, _ := limitInitHistoryWithin(sa.Messages, share)
		remainingBytes = max(0, remainingBytes-encodedHistorySize(messages))
		sad := SubagentInitData{
			JobID:            sa.JobID,
			OriginToolCallID: sa.OriginToolCallID,
			Task:             truncateSubagentTask(sa.Task),
			Title:            truncateSubagentTask(sa.Title),
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
		out[i] = sad
	}
	return out
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

// encodedHistorySize is the JSON weight a projected message range adds to the
// payload. Used to charge a live subagent's transcript against the shared
// section budget, so the remaining children are sized by what is actually left.
func encodedHistorySize(messages []core.AgentMessage) int {
	if len(messages) == 0 {
		return 0
	}
	encoded, err := json.Marshal(messages)
	if err != nil {
		return 0
	}
	return len(encoded)
}

// limitInitHistory returns a bounded, recent display tail. It also removes
// large inline attachment payloads and bounds individual text blocks: sending
// a whole historic image or pasted file to a phone is neither useful nor safe.
func limitInitHistory(messages []core.AgentMessage) ([]core.AgentMessage, bool) {
	return limitInitHistoryWithin(messages, initHistoryMaxBytes)
}

// limitInitHistoryWithin is limitInitHistory with an explicit byte budget, used
// by the live-subagent section to share one budget across every child instead
// of granting each of them a full history's worth.
func limitInitHistoryWithin(messages []core.AgentMessage, maxBytes int) ([]core.AgentMessage, bool) {
	if len(messages) == 0 {
		return nil, false
	}
	start := boundedHistoryTailStart(messages, maxBytes)
	// Prefer including the call that owns a leading result run, but never at
	// the cost of dropping the newest transcript messages. Grow the range only
	// when it still fits the mobile bound in full.
	if aligned := historyPageStart(messages, len(messages), start); aligned < start && boundedHistoryTailStart(messages[aligned:], maxBytes) == 0 {
		start = aligned
	}
	return sanitizeHistoryRange(messages[start:], maxBytes), start > 0
}

// limitHistoryDelta bounds a durable suffix without trying to make it a
// standalone page: leading tool results can complete calls the client already
// has. A truncated delta is rejected by buildInitData rather than sent.
func limitHistoryDelta(messages []core.AgentMessage) ([]core.AgentMessage, bool) {
	if len(messages) == 0 {
		return nil, false
	}
	start := boundedHistoryTailStart(messages, initHistoryMaxBytes)
	return sanitizeHistoryRange(messages[start:], initHistoryMaxBytes), start > 0
}

// boundedHistoryTailStart returns the newest range that fits maxBytes and the
// init message count. Starting at the tail is essential: reconnect must never
// omit the transcript's newest message.
func boundedHistoryTailStart(messages []core.AgentMessage, maxBytes int) int {
	bytes := 0
	firstIndex := len(messages)
	for i := len(messages) - 1; i >= 0; i-- {
		_, size := sanitizeHistoryMessage(messages[i])
		// An oversized message is not dropped, it is degraded to the budget by
		// sanitizeHistoryRange, so charge it what it will actually weigh.
		size = min(size, maxBytes)
		if len(messages)-i > initHistoryMaxMessages || (i < len(messages)-1 && bytes+size > maxBytes) {
			break
		}
		firstIndex = i
		bytes += size
	}
	return firstIndex
}

func sanitizeHistoryRange(messages []core.AgentMessage, maxBytes int) []core.AgentMessage {
	bounded := make([]core.AgentMessage, 0, len(messages))
	for _, original := range messages {
		msg, size := sanitizeHistoryMessage(original)
		if size > maxBytes {
			msg = fitMessageWithin(msg, maxBytes)
		}
		bounded = append(bounded, msg)
	}
	return bounded
}

// fitMessageWithin degrades one oversized message to maxBytes instead of
// dropping it. Dropping is not an option for the newest message — a reconnect
// must never omit the tail of a transcript — and a message can exceed the
// budget while every individual block is under historyContentMaxBytes, simply
// by carrying many of them.
//
// Blocks are kept in order while they fit; the first block that does not is
// truncated to whatever room is left, and the remainder is replaced by one
// marker block so the client renders "there was more here" rather than a
// silently short message. A message with no room at all still travels as the
// marker, keeping its role and MsgID so the row stays addressable.
func fitMessageWithin(sanitized core.AgentMessage, maxBytes int) core.AgentMessage {
	// Charge the envelope (role, ids, timestamps) before any content, so the
	// budget bounds the encoded message and not just its text.
	envelope := sanitized
	envelope.Content = nil
	room := maxBytes - encodedMessageSize(envelope)

	kept := make([]core.Content, 0, len(sanitized.Content))
	truncatedAny := false
	// Reserve the marker block up front. It is appended after the loop, so its
	// own encoded weight has to come out of the budget before anything else is
	// admitted, or the result overshoots by exactly one block.
	marker := core.TextContent(truncationNotice)
	room -= encodedContentSize(marker)
	for _, block := range sanitized.Content {
		size := encodedContentSize(block)
		if size <= room {
			kept = append(kept, block)
			room -= size
			continue
		}
		// Only text is worth partially keeping; a half image or tool-call
		// argument map is not renderable.
		if block.Type == "text" {
			// truncateUTF8 appends "\n\n" + truncationNotice to whatever budget
			// it is given, so subtract that as well as the block envelope.
			overhead := encodedContentSize(core.TextContent("")) + len(truncationNotice) + 2
			if budget := room - overhead; budget > 0 {
				kept = append(kept, core.TextContent(truncateUTF8(block.Text, budget)))
			}
		}
		truncatedAny = true
		break
	}
	if truncatedAny {
		kept = append(kept, marker)
	}
	degraded := sanitized
	degraded.Content = kept
	// The arithmetic above is an estimate (JSON escaping can make an encoded
	// block larger than its raw text). Verify the real encoded size and, if the
	// estimate fell short, fall back to the marker alone — a bounded payload is
	// the invariant, and the client can still open the full transcript.
	if encodedMessageSize(degraded) > maxBytes {
		degraded.Content = []core.Content{marker}
	}
	return degraded
}

func encodedMessageSize(msg core.AgentMessage) int {
	encoded, err := json.Marshal(msg)
	if err != nil {
		return 0
	}
	return len(encoded)
}

func encodedContentSize(block core.Content) int {
	encoded, err := json.Marshal(block)
	if err != nil {
		return 0
	}
	return len(encoded)
}

// historyPageStart aligns a region's lower boundary to the assistant that
// immediately precedes a contiguous tool-result run. A malformed transcript
// with an intervening message deliberately degrades to an unmatched row.
func historyPageStart(messages []core.AgentMessage, end, start int) int {
	for start > 0 && messages[start].Role == "tool_result" {
		start--
	}
	if start == 0 {
		for start < end && messages[start].Role == "tool_result" {
			start++
		}
	}
	return start
}

// boundedHistoryRange applies the same aggregate bounds as an init payload.
// If a parallel tool-result run exceeds them, it keeps the assistant call and
// as many leading results as fit. The omitted results deliberately appear as
// pending calls in the existing frontend projection rather than as orphaned
// result rows or an unbounded response.
func boundedHistoryRange(messages []core.AgentMessage) []core.AgentMessage {
	selected := make([]core.AgentMessage, 0, len(messages))
	bytes := 0
	for _, original := range messages {
		msg, size := sanitizeHistoryMessage(original)
		if size > initHistoryMaxBytes {
			msg = core.WrapMessage(core.Message{Role: original.Role, MsgID: original.MsgID, Content: []core.Content{core.TextContent("[historic message too large to load on this device]")}})
			_, size = sanitizeHistoryMessage(msg)
		}
		if len(selected) > 0 && (len(selected) >= initHistoryMaxMessages || bytes+size > initHistoryMaxBytes) {
			break
		}
		selected = append(selected, msg)
		bytes += size
	}
	return selected
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
		// Provider round-trip signatures are opaque metadata the browser never
		// reads (0 references in the frontend) and can be large. Clear them on
		// THIS COPY only: the transcript kept in memory, persisted to disk and
		// replayed to the provider must keep them or Anthropic rejects the
		// thinking blocks they authenticate.
		content.TextSignature = ""
		content.ThinkingSignature = ""
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
	return truncateUTF8(value, historyContentMaxBytes)
}

// truncateSubagentOutcome bounds one terminal card's result/error text to the
// excerpt the collapsed card actually renders. The caller compares the result
// with its input to set SubagentEndData.Excerpt, which is what makes the UI
// label it "Result excerpt" and offer the full transcript.
//
// The SAME projection is used by the live subagent_end event and by the init
// snapshot on purpose: with different budgets a 10 KiB result showed whole when
// the child finished and shrank on the next reload, which reads as data loss.
func truncateSubagentOutcome(value string) string {
	return truncateUTF8(value, subagentOutcomeExcerptMaxBytes)
}

// truncateSubagentTask bounds the task description echoed on a subagent card.
// It is model-authored and therefore unbounded in principle: fifty cards, each
// carrying a full delegation brief, is a payload section of its own.
func truncateSubagentTask(value string) string {
	return truncateUTF8(value, subagentTaskMaxBytes)
}

// truncationNotice is the single marker every bounded projection appends, so a
// caller reserving room for it and the function writing it cannot disagree.
const truncationNotice = "[historic content truncated on this device]"

func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + "\n\n" + truncationNotice
}
