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
	Steer(msg string)
	CancelSteer()
	DrainSteers() []string
	SetModel(provider core.Provider, model core.Model) error
	SetThinkingLevel(level string) error
	SetSystemPrompt(prompt string) error
	SetCompactAt(tokens int) error
	SetMaxBudget(v float64) error
	Reset() error
	Compact(ctx context.Context) (*core.CompactionPayload, error)
	Send(ctx context.Context, prompt string) ([]core.AgentMessage, error)
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

	PlanMode    *planmode.PlanMode // may be nil
	Goal        *goal.Goal         // may be nil
	TaskStore   *tasks.Store       // may be nil
	Checkpoints *checkpoint.Store  // may be nil
	PathPolicy  *tool.PathPolicy   // may be nil
	AskBridge   *askuser.Bridge    // may be nil

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
	case BashJobEnded:
		delete(sctx.activeBashJobs, e.JobID)
	}
}

func (sctx *SessionContext) hasBackgroundWork() bool {
	sctx.quiescenceMu.Lock()
	defer sctx.quiescenceMu.Unlock()
	return sctx.autoVerifyRunning > 0 || sctx.goalVerifyRunning > 0 || len(sctx.activeSubagents) > 0 || len(sctx.activeBashJobs) > 0
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
	for _, ev := range TranslateAgentEvent(sid, gen, e, sctx.TaskStore) {
		sctx.Bus.Publish(ev)
	}
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
		return []any{Steered{SessionID: sid, RunGen: gen, Text: e.Text}}

	case core.AgentEventCompactionStart:
		return []any{CompactionStarted{SessionID: sid, RunGen: gen}}

	case core.AgentEventCompactionEnd:
		return []any{CompactionEnded{
			SessionID: sid,
			RunGen:    gen,
			Payload:   e.Compaction,
			Err:       e.Error,
		}}
	}
	return nil
}
