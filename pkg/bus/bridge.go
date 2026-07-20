package bus

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/ealeixandre/moa/pkg/askuser"
	"github.com/ealeixandre/moa/pkg/checkpoint"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/goal"
	"github.com/ealeixandre/moa/pkg/permission"
	"github.com/ealeixandre/moa/pkg/planmode"
	"github.com/ealeixandre/moa/pkg/session"
	"github.com/ealeixandre/moa/pkg/sessioncheckpoint"
	"github.com/ealeixandre/moa/pkg/tasks"
	"github.com/ealeixandre/moa/pkg/tool"
)

// ---------------------------------------------------------------------------
// Narrow interfaces — pkg/bus depends on behaviour, not on *agent.Agent.
// *agent.Agent satisfies both implicitly.
// ---------------------------------------------------------------------------

// AgentSubscriber allows subscribing to agent events.
type AgentSubscriber interface {
	Subscribe(fn func(core.AgentEvent)) func()
}

// AgentController is the command surface of an agent session.
type AgentController interface {
	// Commands
	Abort()
	Steer(it core.SteerItem) bool
	CancelSteer()
	DrainSteers() []core.SteerItem
	DrainUntilBarrier() []core.SteerItem
	PushSteersFront(items []core.SteerItem)
	PeekQueueHead() (core.SteerItem, bool)
	PopQueueBarrier(id string) bool
	SendItems(ctx context.Context, items []core.SteerItem, msgIDs []string) ([]core.AgentMessage, []string, error)
	SetModel(provider core.Provider, model core.Model) error
	SetThinkingLevel(level string) error
	SetSystemPrompt(prompt string) error
	SetCompactAt(tokens int) error
	SetMaxBudget(v float64) error
	Reset() error
	Compact(ctx context.Context) (*core.CompactionPayload, error)
	Send(ctx context.Context, prompt string) ([]core.AgentMessage, error)
	SendWithMsgID(ctx context.Context, prompt, msgID string) ([]core.AgentMessage, error)
	SendWithCustom(ctx context.Context, prompt string, custom map[string]any) ([]core.AgentMessage, error)
	SendWithContent(ctx context.Context, content []core.Content) ([]core.AgentMessage, error)
	AppendMessage(msg core.AgentMessage) error
	SetPermissionCheck(fn func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision) error
	LoadState(msgs []core.AgentMessage, compactionEpoch int) error

	// Queries
	Messages() []core.AgentMessage
	Model() core.Model
	SystemPrompt() string
	ThinkingLevel() string
	CompactAt() int
	MaxBudget() float64
	CompactionEpoch() int
	IsRunning() bool
	PendingSteers() []core.SteerItem
	QueueLen() int
	NativeDocBytesUndelivered() int64
	ReserveNativeDocBytes(n int64)
	ReleaseNativeDocBytes(n int64)
}

// ---------------------------------------------------------------------------
// SessionContext — per-session dependency aggregate
// ---------------------------------------------------------------------------

// SessionContext holds all session-scoped dependencies needed by handlers and
// the agent event bridge. Created once per session.
//
// Bus is per-session (not shared between sessions). The SessionID in events
// and commands is metadata for logging/serialization, not routing.
type SessionContext struct {
	SessionID  string
	SessionCtx context.Context // session lifetime context; cancelled on destroy
	Bus        EventBus
	Agent      AgentController
	State      *StateMachine    // may be nil for backward compat
	Approvals  *ApprovalManager // manages pending permissions/asks; may be nil
	Tree       *session.Tree    // session entry tree; may be nil during migration

	PlanMode          *planmode.PlanMode      // may be nil
	Goal              *goal.Goal              // may be nil
	TaskStore         *tasks.Store            // may be nil
	Checkpoints       *checkpoint.Store       // may be nil
	SessionCheckpoint *sessioncheckpoint.Slot // ephemeral pre-compaction state
	PathPolicy        *tool.PathPolicy        // may be nil
	AskBridge         *askuser.Bridge         // may be nil
	PersistNow        func() error            // synchronous checkpoint-safe save

	ProviderFactory  func(core.Model) (core.Provider, error)
	BaseSystemPrompt string

	// goalPrevCompactAt is the CompactAt threshold captured when goal mode
	// started, restored when it ends. Written by EnterGoal before any goal run,
	// read by stopGoal afterward.
	goalPrevCompactAt int

	// goalPrevMaxBudget is the per-run MaxBudget captured when goal mode started.
	// The driver lowers the per-run budget to the remaining total each iteration
	// so the loop's cumulative cost can't exceed the configured budget; stopGoal
	// restores this value.
	goalPrevMaxBudget float64

	// cancelGoalVerify aborts an in-flight goal verification (evidence build +
	// verifier call). Set by RegisterHandlers; called on a new user run or when
	// the goal stops so stale checks don't run against fresh edits. May be nil.
	cancelGoalVerify func()

	// GateConfig is used to reconstruct a Gate when switching from yolo
	// to ask/auto. Preserves allow/deny patterns, rules, headless, etc.
	GateConfig permission.Config

	CWD        string // workspace directory for tools/verify
	AutoVerify bool   // run verify automatically after edit runs

	// SteerFilter returns false to suppress a steer event (e.g. subagent
	// completion text in serve). If nil, all steers are published.
	SteerFilter func(text string) bool

	// Gate is swapped atomically by SetPermissionMode command.
	// The Gate object itself is immutable between swaps — only the pointer changes.
	gate atomic.Pointer[permission.Gate]

	// compacting is the authoritative "a compaction is in progress" flag, so a
	// reconnect snapshot can restore (or clear) the compacting spinner. It is
	// set true before publishing CompactionStarted and cleared before
	// publishing CompactionEnded (and defensively on run end/error), so the
	// snapshot boundary cut (subscribe → LastSeq → query) always observes a
	// value consistent with the events streamed after the cut.
	compacting atomic.Bool

	// streamMu guards the authoritative in-flight streaming aggregate below.
	// The agent appends an assistant message to state only after the provider
	// turn completes, so mid-stream the partial text/thinking lives only in the
	// deltas already sent. A reconnect during generation would otherwise miss
	// everything streamed before the cut and render the reply "from the middle".
	// bridgeEvent maintains this aggregate serially (in the subscriber
	// goroutine), holding streamMu across both the mutation and the derived
	// publish so SnapshotStreamingWithCut can pair it with the sequence cut for
	// the reconnect snapshot.
	streamMu       sync.Mutex
	streamText     string
	streamThinking string
	streamMsgID    string

	// persistPaused suppresses persistence-reactor snapshots while a session is
	// being restored in place. The final complete state is saved explicitly by
	// SessionRuntime.SwitchSession.
	persistPaused atomic.Bool
	// persistMu prevents a snapshot already in progress from crossing the
	// persister rebind during a session switch.
	persistMu sync.RWMutex

	// treeSyncer is set by RegisterTreeSyncer; nil in tests that don't register
	// one. GetDisplayMessages uses it to append the in-flight turn (agent
	// messages not yet synced to the tree) so a mid-run reconnect snapshot is
	// complete.
	treeSyncer *TreeSyncer

	// RunGenAtomic is the current run generation, readable without locks.
	// Stamped on agent-lifecycle events by the bridge. Written by startRun
	// (under runMu), read atomically by the bridge.
	RunGenAtomic atomic.Uint64

	// sessionCost accumulates the session's USD spend (main run cost from
	// RunEnded plus each subagent's cost from SubagentEnded). Reset to 0 on
	// clear / clean-context plan execution / session load. Guarded by costMu.
	costMu      sync.Mutex
	sessionCost float64

	// Run context management — used by SendPrompt handler.
	// Protected by runMu.
	runMu      sync.Mutex
	runCancel  context.CancelFunc // cancels the current run context; nil when idle
	runGen     uint64             // incremented each run; used to avoid clearing a newer run's cancel
	runStatsMu sync.Mutex
	runStats   runStats

	// Background work that can outlive a foreground RunEnded. Headless callers
	// use it to wait for the complete autonomous chain, not merely the first
	// maker turn.
	quiescenceMu      sync.Mutex
	autoVerifyRunning int
	goalVerifyRunning int
	activeSubagents   map[string]struct{}
	activeBashJobs    map[string]struct{}

	// Queue pump coalescing. The pump drains the agent's unified queue rail at
	// each idle point (RunEnded / CompactionEnded), executing barrier commands
	// and starting runs for trailing steers. Two idle signals arrive on two
	// subscriber goroutines, and a barrier the pump executes can itself emit an
	// idle signal, so pumps must never overlap: pumpActive serializes them and
	// pumpRerun coalesces a request that arrives while a pump is running into
	// one more loop, instead of spawning a concurrent pump.
	pumpMu     sync.Mutex
	pumpActive bool
	pumpRerun  bool
}

type runStats struct {
	gen       uint64
	finalText string
	hadEdits  bool
	costUSD   float64
}

func (sctx *SessionContext) beginAutoVerify() {
	sctx.quiescenceMu.Lock()
	sctx.autoVerifyRunning++
	sctx.quiescenceMu.Unlock()
}

func (sctx *SessionContext) endAutoVerify() {
	sctx.quiescenceMu.Lock()
	if sctx.autoVerifyRunning > 0 {
		sctx.autoVerifyRunning--
	}
	sctx.quiescenceMu.Unlock()
}

func (sctx *SessionContext) beginGoalVerify() {
	sctx.quiescenceMu.Lock()
	sctx.goalVerifyRunning++
	sctx.quiescenceMu.Unlock()
}

func (sctx *SessionContext) endGoalVerify() {
	sctx.quiescenceMu.Lock()
	if sctx.goalVerifyRunning > 0 {
		sctx.goalVerifyRunning--
	}
	sctx.quiescenceMu.Unlock()
}

func (sctx *SessionContext) trackBackgroundEvent(event any) {
	sctx.quiescenceMu.Lock()
	defer sctx.quiescenceMu.Unlock()
	if sctx.activeSubagents == nil {
		sctx.activeSubagents = make(map[string]struct{})
		sctx.activeBashJobs = make(map[string]struct{})
	}
	switch e := event.(type) {
	case SubagentStarted:
		sctx.activeSubagents[e.JobID] = struct{}{}
	case SubagentEnded:
		delete(sctx.activeSubagents, e.JobID)
	case BashJobStarted:
		sctx.activeBashJobs[e.JobID] = struct{}{}
	case BashJobSettled:
		delete(sctx.activeBashJobs, e.JobID)
	}
}

func (sctx *SessionContext) hasBackgroundWork() bool {
	sctx.quiescenceMu.Lock()
	defer sctx.quiescenceMu.Unlock()
	return sctx.autoVerifyRunning > 0 || sctx.goalVerifyRunning > 0 || len(sctx.activeSubagents) > 0 || len(sctx.activeBashJobs) > 0
}

// GoalVerifying reports whether a goal verifier is currently running, so a
// reconnect snapshot can restore the "verifying…" indicator.
func (sctx *SessionContext) GoalVerifying() bool {
	sctx.quiescenceMu.Lock()
	defer sctx.quiescenceMu.Unlock()
	return sctx.goalVerifyRunning > 0
}

// Compacting reports whether a compaction is currently in progress, so a
// reconnect snapshot can restore (or clear) the compacting spinner.
func (sctx *SessionContext) Compacting() bool {
	return sctx.compacting.Load()
}

// setCompacting sets the authoritative compacting flag. It must be called
// BEFORE publishing the corresponding CompactionStarted/CompactionEnded event
// so a concurrent snapshot cut observes a value consistent with the streamed
// events.
func (sctx *SessionContext) setCompacting(v bool) {
	sctx.compacting.Store(v)
}

// StreamingAggregate returns the in-flight partial assistant text/thinking and
// the current message ID, for a reconnect snapshot during generation. Empty
// strings mean nothing is streaming right now.
func (sctx *SessionContext) StreamingAggregate() (text, thinking, msgID string) {
	sctx.streamMu.Lock()
	defer sctx.streamMu.Unlock()
	return sctx.streamText, sctx.streamThinking, sctx.streamMsgID
}

// SnapshotStreamingWithCut atomically captures the in-flight streaming aggregate
// together with the current bus sequence, both under streamMu. bridgeEvent
// holds streamMu across the aggregate mutation AND the derived Bus.Publish, so
// this pairing gives a total order for the accumulative (non-idempotent)
// aggregate: a streamed delta is either already folded into the returned text
// AND at/below the returned cut, or absent AND published above it — never both.
// Without this atomicity a delta could be seeded into the reconnect snapshot and
// ALSO replayed live (seq > cut), double-rendering the partial reply.
func (sctx *SessionContext) SnapshotStreamingWithCut() (text, thinking, msgID string, cut uint64) {
	sctx.streamMu.Lock()
	defer sctx.streamMu.Unlock()
	return sctx.streamText, sctx.streamThinking, sctx.streamMsgID, sctx.Bus.LastSeq()
}

// The mutators below assume the caller already holds streamMu (bridgeEvent holds
// it across the aggregate update and the derived publish); they never lock.

// resetStreamingLocked clears the streaming aggregate. Called when a message
// completes (its text is now a real message in state) and defensively on
// turn/run end. Caller must hold streamMu.
func (sctx *SessionContext) resetStreamingLocked() {
	sctx.streamText = ""
	sctx.streamThinking = ""
	sctx.streamMsgID = ""
}

// setStreamMsgIDLocked records the ID of the assistant message currently
// streaming, resetting the accumulated deltas for the new message. Caller must
// hold streamMu.
func (sctx *SessionContext) setStreamMsgIDLocked(id string) {
	sctx.streamText = ""
	sctx.streamThinking = ""
	sctx.streamMsgID = id
}

// appendStreamTextLocked accumulates a streamed text delta. Caller must hold
// streamMu.
func (sctx *SessionContext) appendStreamTextLocked(delta string) {
	sctx.streamText += delta
}

// appendStreamThinkingLocked accumulates a streamed thinking delta. Caller must
// hold streamMu.
func (sctx *SessionContext) appendStreamThinkingLocked(delta string) {
	sctx.streamThinking += delta
}

// GetGate returns the current permission gate (may be nil for yolo mode).
func (sctx *SessionContext) GetGate() *permission.Gate {
	return sctx.gate.Load()
}

// addSessionCost adds delta to the accumulated session cost and returns the new
// total. Publishing SessionCostUpdated is left to the caller so the event can
// carry the triggering run's delta.
func (sctx *SessionContext) addSessionCost(delta float64) float64 {
	sctx.costMu.Lock()
	defer sctx.costMu.Unlock()
	sctx.sessionCost += delta
	return sctx.sessionCost
}

// resetSessionCost clears the accumulated session cost and publishes a
// SessionCostUpdated with a zero total. Called when the conversation context is
// reset (clear / clean-context plan execution / session load).
func (sctx *SessionContext) resetSessionCost() {
	sctx.clearSessionCost()
	sctx.Bus.Publish(SessionCostUpdated{SessionID: sctx.SessionID, TotalUSD: 0, RunUSD: 0})
}

// clearSessionCost resets the total without publishing an event. A transactional
// session switch uses it so observers see only the final SessionLoaded event.
func (sctx *SessionContext) clearSessionCost() {
	sctx.costMu.Lock()
	sctx.sessionCost = 0
	sctx.costMu.Unlock()
}

// sessionCostTotal returns the current accumulated session cost.
func (sctx *SessionContext) sessionCostTotal() float64 {
	sctx.costMu.Lock()
	defer sctx.costMu.Unlock()
	return sctx.sessionCost
}

// SetGate atomically replaces the permission gate.
func (sctx *SessionContext) SetGate(g *permission.Gate) {
	sctx.gate.Store(g)
}

// newRunContext creates a per-run context derived from SessionCtx.
// Returns the context and a generation token. Caller must hold runMu.
func (sctx *SessionContext) newRunContext() (context.Context, uint64) {
	ctx, cancel := context.WithCancel(sctx.SessionCtx)
	sctx.runCancel = cancel
	sctx.runGen++
	sctx.RunGenAtomic.Store(sctx.runGen)
	sctx.runStatsMu.Lock()
	sctx.runStats = runStats{gen: sctx.runGen}
	sctx.runStatsMu.Unlock()
	return ctx, sctx.runGen
}

func (sctx *SessionContext) addRunEvent(gen uint64, e core.AgentEvent) {
	if e.Type != core.AgentEventEnd && e.Type != core.AgentEventMessageEnd && e.Type != core.AgentEventToolExecEnd && e.Type != core.AgentEventCompactionEnd {
		return
	}
	sctx.runStatsMu.Lock()
	defer sctx.runStatsMu.Unlock()
	if sctx.runStats.gen != gen {
		return
	}
	var pricing *core.Pricing
	if sctx.Agent != nil {
		pricing = sctx.Agent.Model().Pricing
	}
	switch e.Type {
	case core.AgentEventEnd:
		// A cancelled stream can leave a partial assistant message without a
		// MessageEnd. AgentEventEnd carries the final state in emitter order,
		// so it is a safe fallback without relying on a mutable history offset.
		if sctx.runStats.finalText == "" {
			sctx.runStats.finalText = extractFinalAssistantText(e.Messages)
		}
	case core.AgentEventMessageEnd:
		if e.Message.Role == "assistant" {
			sctx.runStats.finalText = messageText(e.Message)
			if pricing != nil && e.Message.Usage != nil {
				sctx.runStats.costUSD += pricing.Cost(*e.Message.Usage)
			}
		}
	case core.AgentEventToolExecEnd:
		if !e.IsError && !e.Rejected && (e.ToolName == "edit" || e.ToolName == "write" || e.ToolName == "multiedit" || e.ToolName == "apply_patch") {
			sctx.runStats.hadEdits = true
		}
	case core.AgentEventCompactionEnd:
		if pricing != nil && e.Compaction != nil && e.Compaction.Usage != nil {
			sctx.runStats.costUSD += pricing.Cost(*e.Compaction.Usage)
		}
	}
}

func (sctx *SessionContext) snapshotRunStats(gen uint64) runStats {
	sctx.runStatsMu.Lock()
	defer sctx.runStatsMu.Unlock()
	if sctx.runStats.gen != gen {
		return runStats{}
	}
	return sctx.runStats
}

// cancelRun cancels the current run context if any. Safe to call multiple times.
func (sctx *SessionContext) cancelRun() {
	sctx.runMu.Lock()
	defer sctx.runMu.Unlock()
	if sctx.runCancel != nil {
		sctx.runCancel()
		sctx.runCancel = nil
	}
}

// clearRunCancel clears the run cancel func only if the generation matches.
// This prevents a finishing run from clearing a newer run's cancel.
func (sctx *SessionContext) clearRunCancel(gen uint64) {
	sctx.runMu.Lock()
	defer sctx.runMu.Unlock()
	if sctx.runGen == gen {
		sctx.runCancel = nil
	}
}

// ---------------------------------------------------------------------------
// Bridge — translates core.AgentEvent → typed bus events
// ---------------------------------------------------------------------------

// Bridge subscribes to an agent's event emitter and publishes typed bus events.
// Returns an unsubscribe function. Call it when the session is destroyed.
func Bridge(sctx *SessionContext, subscriber AgentSubscriber) func() {
	return subscriber.Subscribe(func(e core.AgentEvent) {
		bridgeEvent(sctx, e)
	})
}

// bridgeEvent translates a single core.AgentEvent into typed bus event(s) and
// publishes them on sctx.Bus. Special-cases the steer filter (which needs
// sctx.SteerFilter, not just data) — everything else defers to
// TranslateAgentEvent.
func bridgeEvent(sctx *SessionContext, e core.AgentEvent) {
	if e.Type == core.AgentEventSteer && sctx.SteerFilter != nil && !sctx.SteerFilter(e.Text) {
		return
	}
	sid := sctx.SessionID
	gen := sctx.RunGenAtomic.Load()
	sctx.addRunEvent(gen, e)
	// Keep the authoritative compacting flag in lockstep with the events we are
	// about to publish. This runs serially in the bridge subscriber goroutine,
	// and the Store happens before Bus.Publish, so a concurrent snapshot cut
	// sees a value consistent with the streamed events. The run-end/error cases
	// are a safety net: a run that dies without a CompactionEnd must not leave
	// the spinner stuck.
	switch e.Type {
	case core.AgentEventCompactionStart:
		sctx.setCompacting(true)
	case core.AgentEventCompactionEnd, core.AgentEventEnd, core.AgentEventError:
		sctx.setCompacting(false)
	}

	translated := TranslateAgentEvent(sid, gen, e, sctx.TaskStore)

	// Maintain the authoritative in-flight streaming aggregate in lockstep with
	// the deltas we publish, so a reconnect snapshot during generation restores
	// the whole partial reply instead of only post-cut deltas. Cleared when the
	// message completes (now a real message in state) or the turn/run ends.
	//
	// The aggregate is accumulative (concatenated deltas), so — unlike the
	// idempotent compacting flag — the mutation and the publish of its derived
	// events must be atomic with respect to the snapshot cut: streamMu is held
	// across BOTH, and SnapshotStreamingWithCut reads the aggregate and
	// Bus.LastSeq under the same lock. That gives a total order so a streamed
	// delta is never both folded into the snapshot AND replayed live (seq>cut).
	if delta, mutates := streamAggregateDelta(e); mutates {
		sctx.streamMu.Lock()
		switch delta.kind {
		case streamKindStart:
			sctx.setStreamMsgIDLocked(delta.msgID)
		case streamKindText:
			sctx.appendStreamTextLocked(delta.text)
		case streamKindThinking:
			sctx.appendStreamThinkingLocked(delta.text)
		case streamKindReset:
			sctx.resetStreamingLocked()
		}
		for _, ev := range translated {
			sctx.Bus.Publish(ev)
		}
		sctx.streamMu.Unlock()
		return
	}
	for _, ev := range translated {
		sctx.Bus.Publish(ev)
	}
}

type streamDeltaKind int

const (
	streamKindStart streamDeltaKind = iota
	streamKindText
	streamKindThinking
	streamKindReset
)

type streamDelta struct {
	kind  streamDeltaKind
	msgID string
	text  string
}

// streamAggregateDelta reports how an AgentEvent mutates the in-flight streaming
// aggregate, and whether it mutates it at all. Only these events take streamMu
// in bridgeEvent; everything else publishes without it.
func streamAggregateDelta(e core.AgentEvent) (streamDelta, bool) {
	switch e.Type {
	case core.AgentEventMessageStart:
		return streamDelta{kind: streamKindStart, msgID: e.Message.MsgID}, true
	case core.AgentEventMessageUpdate:
		if e.AssistantEvent != nil {
			switch e.AssistantEvent.Type {
			case core.ProviderEventTextDelta:
				return streamDelta{kind: streamKindText, text: e.AssistantEvent.Delta}, true
			case core.ProviderEventThinkingDelta:
				return streamDelta{kind: streamKindThinking, text: e.AssistantEvent.Delta}, true
			}
		}
		return streamDelta{}, false
	case core.AgentEventMessageEnd, core.AgentEventTurnEnd, core.AgentEventEnd, core.AgentEventError:
		return streamDelta{kind: streamKindReset}, true
	}
	return streamDelta{}, false
}

// TranslateAgentEvent translates a single core.AgentEvent into 0..n typed bus
// events. It is a pure function (no publishing) so it can be reused both by
// the session Bridge and by the subagent event sink (namespaced per jobID).
//
// taskStore may be nil; when nil, the TasksUpdated side event for the "tasks"
// tool is skipped (used by callers, e.g. subagent children, that have no
// meaningful task store).
//
// Note: this does NOT apply SessionContext.SteerFilter — callers that care
// about filtering steer events (the session Bridge) must do so themselves
// before/around calling this function.
func TranslateAgentEvent(sid string, gen uint64, e core.AgentEvent, taskStore *tasks.Store) []any {
	switch e.Type {
	case core.AgentEventStart:
		return []any{AgentStarted{SessionID: sid, RunGen: gen}}

	case core.AgentEventEnd:
		return []any{AgentEnded{SessionID: sid, RunGen: gen, Messages: e.Messages}}

	case core.AgentEventError:
		return []any{AgentError{SessionID: sid, RunGen: gen, Err: e.Error}}

	case core.AgentEventTurnStart:
		return []any{TurnStarted{SessionID: sid, RunGen: gen}}

	case core.AgentEventTurnEnd:
		return []any{TurnEnded{SessionID: sid, RunGen: gen}}

	case core.AgentEventMessageStart:
		return []any{MessageStarted{SessionID: sid, RunGen: gen, Message: e.Message}}

	case core.AgentEventMessageUpdate:
		if e.AssistantEvent == nil {
			return nil
		}
		switch e.AssistantEvent.Type {
		case core.ProviderEventTextDelta:
			return []any{TextDelta{SessionID: sid, RunGen: gen, Delta: e.AssistantEvent.Delta}}
		case core.ProviderEventThinkingDelta:
			return []any{ThinkingDelta{SessionID: sid, RunGen: gen, Delta: e.AssistantEvent.Delta}}
		case core.ProviderEventToolCallStart:
			return []any{ToolCallStreaming{
				SessionID:  sid,
				RunGen:     gen,
				ToolCallID: e.AssistantEvent.ToolCallID,
				ToolName:   e.AssistantEvent.ToolName,
			}}
		case core.ProviderEventToolCallDelta:
			if e.AssistantEvent.PartialArgs != nil {
				return []any{ToolCallDelta{
					SessionID:  sid,
					RunGen:     gen,
					ToolCallID: e.AssistantEvent.ToolCallID,
					Args:       e.AssistantEvent.PartialArgs,
				}}
			}
			return nil
		case core.ProviderEventRateLimit:
			if e.AssistantEvent.RateLimit != nil {
				return []any{RateLimitUpdated{SessionID: sid, RunGen: gen, RateLimit: *e.AssistantEvent.RateLimit}}
			}
			return nil
		}
		return nil

	case core.AgentEventMessageEnd:
		var fullText string
		for _, c := range e.Message.Content {
			if c.Type == "text" {
				fullText += c.Text
			}
		}
		return []any{MessageEnded{SessionID: sid, RunGen: gen, Message: e.Message, FullText: fullText}}

	case core.AgentEventToolExecStart:
		return []any{ToolExecStarted{
			SessionID:  sid,
			RunGen:     gen,
			ToolCallID: e.ToolCallID,
			ToolName:   e.ToolName,
			Args:       e.Args,
		}}

	case core.AgentEventToolExecUpdate:
		var delta string
		if e.Result != nil {
			for _, c := range e.Result.Content {
				if c.Type == "text" {
					delta += c.Text
				}
			}
		}
		if delta == "" {
			return nil
		}
		return []any{ToolExecUpdate{
			SessionID:  sid,
			RunGen:     gen,
			ToolCallID: e.ToolCallID,
			Delta:      delta,
		}}

	case core.AgentEventToolExecEnd:
		var resultText string
		if e.Result != nil {
			for _, c := range e.Result.Content {
				if c.Type == "text" {
					resultText += c.Text
				}
			}
		}
		events := []any{ToolExecEnded{
			SessionID:  sid,
			RunGen:     gen,
			ToolCallID: e.ToolCallID,
			ToolName:   e.ToolName,
			Result:     resultText,
			IsError:    e.IsError,
			Rejected:   e.Rejected,
		}}
		// Emit task update on tool_end only (matches serve and TUI behavior).
		if e.ToolName == "tasks" && taskStore != nil {
			events = append(events, TasksUpdated{
				SessionID: sid,
				Tasks:     taskStore.Tasks(),
			})
		}
		return events

	case core.AgentEventSteer:
		return []any{Steered{SessionID: sid, RunGen: gen, ID: e.SteerID, MsgID: e.MsgID, Text: e.Text}}

	case core.AgentEventCompactionStart:
		return []any{CompactionStarted{SessionID: sid, RunGen: gen}}

	case core.AgentEventCompactionEnd:
		return []any{CompactionEnded{
			SessionID:         sid,
			RunGen:            gen,
			Payload:           e.Compaction,
			Err:               e.Error,
			CostIncludedInRun: true,
		}}
	}
	return nil
}
