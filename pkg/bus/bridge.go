package bus

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/e-aleixandre/moa/pkg/askuser"
	"github.com/e-aleixandre/moa/pkg/checkpoint"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/goal"
	"github.com/e-aleixandre/moa/pkg/permission"
	"github.com/e-aleixandre/moa/pkg/planmode"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/sessioncheckpoint"
	"github.com/e-aleixandre/moa/pkg/tasks"
	"github.com/e-aleixandre/moa/pkg/tool"
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
	CancelSteer() []core.SteerItem
	DrainSteers() []core.SteerItem
	DrainUntilBarrier() []core.SteerItem
	PushSteersFront(items []core.SteerItem)
	PeekQueueHead() (core.SteerItem, bool)
	PopQueueBarrier(id string) bool
	SendItems(ctx context.Context, items []core.SteerItem, msgIDs []string, announce func()) ([]core.AgentMessage, []string, error)
	SetModel(provider core.Provider, model core.Model) error
	SetThinkingLevel(level string) error
	SetSystemPrompt(prompt string) error
	SetCompactAt(tokens int) error
	SetMaxBudget(v float64) error
	Reset() error
	Compact(ctx context.Context, focus string) (*core.CompactionPayload, error)
	Send(ctx context.Context, prompt string) ([]core.AgentMessage, error)
	SendWithMsgID(ctx context.Context, prompt, msgID string) ([]core.AgentMessage, error)
	SendWithCustom(ctx context.Context, prompt string, custom map[string]any) ([]core.AgentMessage, error)
	SendWithCustomAnnounced(ctx context.Context, prompt string, custom map[string]any) ([]core.AgentMessage, error)
	SendWithContent(ctx context.Context, content []core.Content) ([]core.AgentMessage, error)
	SendWithContentMsgID(ctx context.Context, content []core.Content, msgID string) ([]core.AgentMessage, error)
	// SendWithContentAnnounced is SendWithContentMsgID that also announces the
	// appended user message (core.AgentEventUserMessage) from the append point,
	// so the announcement can never race a concurrent history snapshot.
	SendWithContentAnnounced(ctx context.Context, content []core.Content, msgID string) ([]core.AgentMessage, error)
	AppendMessage(msg core.AgentMessage) error
	SetPermissionCheck(fn func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision) error
	LoadState(msgs []core.AgentMessage, compactionEpoch int) error

	// Queries
	Messages() []core.AgentMessage
	Model() core.Model
	SystemPrompt() string
	ThinkingLevel() string
	CompactAt() int
	CompactAtFloor() int
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

	// streamMu guards the authoritative in-flight state below: the streaming
	// aggregate AND the live tool-call registry. The agent appends an assistant
	// message to state only after the provider turn completes, so mid-stream the
	// partial text/thinking lives only in the deltas already sent. A reconnect
	// during generation would otherwise miss everything streamed before the cut
	// and render the reply "from the middle".
	// bridgeEvent maintains both serially (in the subscriber goroutine), holding
	// streamMu across the mutation and the derived publish so
	// SnapshotInFlightWithCut can pair them with the sequence cut for the
	// reconnect snapshot.
	streamMu       sync.Mutex
	streamText     string
	streamThinking string
	streamMsgID    string

	// liveTools is the registry of tool calls that exist but are not yet
	// finished: either the model is still streaming their arguments, or they
	// are executing. Such a call may not be represented in the history a
	// reconnect snapshot is rebuilt from — it is written there only when its
	// assistant message closes, and its result only when the tool ends — so
	// without this the client re-renders a live row it can't name ("Calling")
	// or loses it entirely. It also carries the phase and the start anchor,
	// which history never holds, so a restored row resumes its timer. Kept in creation order (the order the rows render in); the
	// cardinality is a turn's concurrent tool calls, so linear scans are the
	// cheapest correct thing here.
	liveTools []LiveToolCall

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

	// historyMu protects the legacy no-TreeSyncer history path. TreeSyncer has
	// its own mutex because it also protects its sync baseline.
	historyMu sync.RWMutex

	// RunGenAtomic is the current run generation, readable without locks.
	// Stamped on agent-lifecycle events by the bridge. Written by startRun
	// (under runMu), read atomically by the bridge.
	RunGenAtomic atomic.Uint64
	// runStartedAnchor is written synchronously with reserving a run, before
	// RunStarted is published. The immutable pair lets a finishing generation
	// clear only its own anchor without racing a newly reserved run.
	runStartedAnchor atomic.Pointer[runStartAnchor]

	// sessionCost accumulates the session's USD spend (main run cost from
	// RunEnded plus each subagent's cost from SubagentEnded). Reset to 0 on
	// clear / clean-context plan execution / session load. Guarded by costMu.
	costMu      sync.Mutex
	sessionCost float64

	// runTokens tracks the current run's logical traffic. runTokenBaseline is
	// the first message belonging to that run in Agent.Messages; the totals
	// exclude resent context and provider cache usage. Guarded by runTokenMu.
	runTokenMu       sync.Mutex
	runTokenBaseline int
	runTokensUp      int
	runTokensDown    int
	runTokensGen     uint64

	// Run context management — used by SendPrompt handler.
	// Protected by runMu.
	// abortMu serializes AbortRun against commands that can enqueue a steer or
	// claim a new run. Without it, a late enqueue can be cleared by abort cleanup
	// without ever being returned to the client that needs to recall it.
	abortMu    sync.Mutex
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

	// msgIDMu guards reserveMsgID: it makes "is this ID free?" atomic with
	// "this ID is now taken", closing the window between a caller's uniqueness
	// check and the append that makes the ID visible in history. See
	// reserveMsgID.
	msgIDMu       sync.Mutex
	reservedMsgID map[string]struct{}
}

// reserveMsgID returns the message ID a send must land under, claiming it for
// this session. A client-supplied ID is honored only when it is well-formed to
// the caller AND neither already in history nor already claimed; otherwise a
// fresh one is minted.
//
// Checking history alone is not enough: the append happens asynchronously in
// the run goroutine, so two concurrent sends carrying the same ID would both
// see it free and both persist under it — deduped live, doubled after a reload.
// Holding msgIDMu across check-and-claim is what makes the identity unique.
//
// The guarantee: an accepted ID is unique against everything in the session
// tree at acceptance time (all branches, not just the current one) plus the
// in-flight claims — sends already accepted whose message has not been appended
// yet. An ID whose message no longer exists anywhere in the tree may be reused
// harmlessly: there is no message left for a client to confuse it with, which
// is the only real threat (the SPA dedups by ID against messages it holds).
// That is why claims are transitory — taken here, dropped once the message is
// in the tree (releaseMsgID from the append announcement) or once the send is
// known never to have started. The map therefore holds only in-flight sends,
// and after a restart nothing needs rebuilding: the tree already is the truth.
func (sctx *SessionContext) reserveMsgID(msgID string) string {
	sctx.msgIDMu.Lock()
	defer sctx.msgIDMu.Unlock()
	if sctx.reservedMsgID == nil {
		sctx.reservedMsgID = make(map[string]struct{})
	}
	if msgID != "" {
		_, claimed := sctx.reservedMsgID[msgID]
		if !claimed && !sctx.msgIDInHistory(msgID) {
			sctx.reservedMsgID[msgID] = struct{}{}
			return msgID
		}
	}
	// Minted IDs are random, so a collision here would mean a repeat of
	// core.NewMsgID; claim it anyway to keep the map the single authority.
	fresh := core.NewMsgID()
	sctx.reservedMsgID[fresh] = struct{}{}
	return fresh
}

// releaseMsgID drops a claim taken by reserveMsgID. Called both when the send
// never reached history (rejected before the run started) and when its message
// did land there: from then on the history itself, not the claim, is what makes
// the ID taken, so keeping the claim would only grow the map forever.
func (sctx *SessionContext) releaseMsgID(msgID string) {
	if msgID == "" {
		return
	}
	sctx.msgIDMu.Lock()
	delete(sctx.reservedMsgID, msgID)
	sctx.msgIDMu.Unlock()
}

// reservedMsgIDCount reports how many claims are currently held. Only in-flight
// sends should be counted: tests assert the map stays bounded instead of
// growing for the life of the session.
func (sctx *SessionContext) reservedMsgIDCount() int {
	sctx.msgIDMu.Lock()
	defer sctx.msgIDMu.Unlock()
	return len(sctx.reservedMsgID)
}

// msgIDReserved reports whether an ID was already claimed by an accepted send.
// Backs the MsgIDInUse query, so a caller probing uniqueness before it reaches
// the send path already sees claims whose message is not in history yet.
func (sctx *SessionContext) msgIDReserved(msgID string) bool {
	sctx.msgIDMu.Lock()
	defer sctx.msgIDMu.Unlock()
	_, ok := sctx.reservedMsgID[msgID]
	return ok
}

// msgIDInHistory reports whether a message with this ID exists anywhere in the
// session: any branch of the tree plus the in-flight turn not synced yet. The
// current branch's projection is not enough — a message the user branched away
// from still exists and is one /branch away from being on screen again.
func (sctx *SessionContext) msgIDInHistory(msgID string) bool {
	if msgID == "" {
		return false
	}
	if sctx.treeSyncer != nil {
		return sctx.treeSyncer.HasMsgID(msgID)
	}
	if sctx.Tree != nil {
		if sctx.Tree.HasMsgID(msgID) {
			return true
		}
	}
	for _, m := range sctx.Agent.Messages() {
		if m.MsgID == msgID {
			return true
		}
	}
	return false
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

// SnapshotInFlightWithCut atomically captures the in-flight streaming aggregate
// AND the live tool-call registry together with the current bus sequence, all
// under streamMu. bridgeEvent holds streamMu across the mutation AND the derived
// Bus.Publish, so this pairing gives a total order for state that is not
// idempotent under replay: a streamed delta is either already folded into the
// returned text AND at/below the returned cut, or absent AND published above it
// — never both. Without this atomicity a delta could be seeded into the
// reconnect snapshot and ALSO replayed live (seq > cut), double-rendering the
// partial reply.
//
// The tool registry rides the same gate for the same reason: a client dedups a
// restored row by tool_call_id, but the pair (snapshot, replayed events) must
// still be consistent — a call must not be *absent* from the snapshot while its
// only announcing events (tool_call_start / tool_start) sit at/below the cut and
// are therefore never replayed, which would resurrect the nameless "Calling"
// row this registry exists to kill.
func (sctx *SessionContext) SnapshotInFlightWithCut() (StreamingAggregate, []LiveToolCall, uint64) {
	sctx.streamMu.Lock()
	defer sctx.streamMu.Unlock()
	aggregate := StreamingAggregate{
		Text:     sctx.streamText,
		Thinking: sctx.streamThinking,
		MsgID:    sctx.streamMsgID,
	}
	liveTools := sctx.liveToolsSnapshotLocked()
	cut := sctx.Bus.CaptureSeq()
	return aggregate, liveTools, cut
}

// LiveTools returns the tool calls currently generating arguments or executing.
func (sctx *SessionContext) LiveTools() []LiveToolCall {
	sctx.streamMu.Lock()
	defer sctx.streamMu.Unlock()
	return sctx.liveToolsSnapshotLocked()
}

// liveToolsSnapshotLocked copies the registry slice so the caller can serialize
// it after releasing streamMu without racing the bridge goroutine's next
// mutation. The entries' args maps need no copy here: they were already deep-
// copied on the way in (see liveToolDelta) and are never mutated in place.
// Caller must hold streamMu.
func (sctx *SessionContext) liveToolsSnapshotLocked() []LiveToolCall {
	if len(sctx.liveTools) == 0 {
		return nil
	}
	out := make([]LiveToolCall, len(sctx.liveTools))
	copy(out, sctx.liveTools)
	return out
}

// liveToolsMax bounds the registry. A turn's concurrent tool calls are already
// capped by maxToolCallsPerTurn, so this is a belt-and-braces limit against a
// pathological provider: the registry must never become an unbounded per-session
// leak just because some call never reported an end.
const liveToolsMax = 64

// upsertLiveToolLocked records (or advances) a tool call in the live registry.
// The same call is announced twice — once when the model starts streaming its
// arguments, once when execution begins — so this is idempotent by ToolCallID
// and only ever moves a row forward: name/args are refined as they become known,
// and StartedAt is preserved from the first sighting so a reconnected client
// resumes the elapsed timer instead of restarting it. Caller must hold streamMu.
func (sctx *SessionContext) upsertLiveToolLocked(call LiveToolCall) {
	for i := range sctx.liveTools {
		if sctx.liveTools[i].ToolCallID != call.ToolCallID {
			continue
		}
		if call.ToolName != "" {
			sctx.liveTools[i].ToolName = call.ToolName
		}
		if call.Args != nil {
			sctx.liveTools[i].Args = call.Args
		}
		if call.Phase == LiveToolPhaseRunning {
			sctx.liveTools[i].Phase = call.Phase
		}
		return
	}
	// Evict the oldest rather than refusing the newest: what a client most
	// needs to see is what is happening now.
	if len(sctx.liveTools) >= liveToolsMax {
		sctx.liveTools = append(sctx.liveTools[:0], sctx.liveTools[len(sctx.liveTools)-liveToolsMax+1:]...)
	}
	call.StartedAt = time.Now()
	sctx.liveTools = append(sctx.liveTools, call)
}

// removeLiveToolLocked drops a finished tool call. Caller must hold streamMu.
func (sctx *SessionContext) removeLiveToolLocked(toolCallID string) {
	for i := range sctx.liveTools {
		if sctx.liveTools[i].ToolCallID == toolCallID {
			sctx.liveTools = append(sctx.liveTools[:i], sctx.liveTools[i+1:]...)
			return
		}
	}
}

// resetLiveToolsLocked clears the whole registry at a boundary where nothing can
// still be in flight (turn end, run end, run error). This is the safety net that
// makes cleanup unconditional: a call whose end event never arrives — cancelled
// context, aborted run, a capped response whose tool calls were never executed —
// would otherwise leave a phantom live row on every future reconnect. Caller
// must hold streamMu.
func (sctx *SessionContext) resetLiveToolsLocked() {
	sctx.liveTools = nil
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
	sctx.runStartedAnchor.Store(&runStartAnchor{gen: sctx.runGen, at: time.Now()})
	sctx.runStatsMu.Lock()
	sctx.runStats = runStats{gen: sctx.runGen}
	sctx.runStatsMu.Unlock()
	return ctx, sctx.runGen
}

// RunStartedAt returns the authoritative start anchor for the current run.
// It is zero once that generation has settled.
func (sctx *SessionContext) RunStartedAt() time.Time {
	anchor := sctx.runStartedAnchor.Load()
	if anchor == nil {
		return time.Time{}
	}
	return anchor.at
}

func (sctx *SessionContext) clearRunStartedAt(gen uint64) {
	for {
		anchor := sctx.runStartedAnchor.Load()
		if anchor == nil || anchor.gen != gen {
			return
		}
		if sctx.runStartedAnchor.CompareAndSwap(anchor, nil) {
			return
		}
	}
}

type runStartAnchor struct {
	gen uint64
	at  time.Time
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

// addInternalRunUsage records a provider call made by session machinery rather
// than the agent loop (currently /handoff) in the active run's accounting.
func (sctx *SessionContext) addInternalRunUsage(usage *core.Usage, pricing *core.Pricing) (gen uint64, up, down int) {
	if usage == nil {
		return
	}
	gen = sctx.RunGenAtomic.Load()
	sctx.runStatsMu.Lock()
	if sctx.runStats.gen == gen && pricing != nil {
		sctx.runStats.costUSD += pricing.Cost(*usage)
	}
	sctx.runStatsMu.Unlock()

	sctx.runTokenMu.Lock()
	if sctx.runTokensGen == gen {
		sctx.runTokensUp += usage.Input
		sctx.runTokensDown += usage.Output
		up, down = sctx.runTokensUp, sctx.runTokensDown
	}
	sctx.runTokenMu.Unlock()
	return gen, up, down
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
	sctx.clearRunStartedAt(gen)
}

// settleRunCancel atomically closes the AbortRun window for gen and reports
// whether it was cancelled before that terminal decision.
func (sctx *SessionContext) settleRunCancel(gen uint64, ctx context.Context) bool {
	sctx.runMu.Lock()
	defer sctx.runMu.Unlock()
	cancelled := ctx.Err() != nil
	if sctx.runGen == gen {
		sctx.runCancel = nil
	}
	return cancelled
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
// publishes them on sctx.Bus. Special-cases session-only queued-steer
// cancellations and the steer filter (which needs sctx.SteerFilter, not just
// data) — everything else defers to TranslateAgentEvent.
func bridgeEvent(sctx *SessionContext, e core.AgentEvent) {
	if e.Type == core.AgentEventSteer && sctx.SteerFilter != nil && !sctx.SteerFilter(e.Text) {
		return
	}
	// SteersCanceled applies to this session's queue. It must not go through
	// TranslateAgentEvent, which is also used to forward child-agent events.
	if e.Type == core.AgentEventSteersCanceled {
		sctx.Bus.Publish(SteersCanceled{SessionID: sctx.SessionID, AttachmentIDs: e.AttachmentIDs})
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

	// Maintain the authoritative in-flight state (streaming aggregate + live
	// tool-call registry) in lockstep with the events we publish, so a reconnect
	// snapshot during a run restores the whole partial reply and every tool row
	// that has no message-history representation yet.
	//
	// Both are non-idempotent under replay, so — unlike the idempotent
	// compacting flag — the mutation and the publish of the derived events must
	// be atomic with respect to the snapshot cut: streamMu is held across BOTH,
	// and SnapshotInFlightWithCut reads them and Bus.LastSeq under the same
	// lock. That gives a total order, so a streamed delta is never both folded
	// into the snapshot AND replayed live (seq>cut), and a live tool call is
	// never missing from the snapshot while the events that announce it are at
	// or below the cut (i.e. never replayed either).
	delta, mutatesStream := streamAggregateDelta(e)
	toolDelta, mutatesTools := liveToolDelta(e)
	if mutatesStream || mutatesTools {
		sctx.streamMu.Lock()
		if mutatesStream {
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
		}
		if mutatesTools {
			switch toolDelta.kind {
			case liveToolKindUpsert:
				sctx.upsertLiveToolLocked(toolDelta.call)
			case liveToolKindRemove:
				sctx.removeLiveToolLocked(toolDelta.call.ToolCallID)
			case liveToolKindReset:
				sctx.resetLiveToolsLocked()
			}
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

type liveToolDeltaKind int

const (
	liveToolKindUpsert liveToolDeltaKind = iota
	liveToolKindRemove
	liveToolKindReset
)

type liveToolMutation struct {
	kind liveToolDeltaKind
	call LiveToolCall
}

// liveToolDelta reports how an AgentEvent mutates the live tool-call registry,
// and whether it mutates it at all.
//
// Note what is deliberately NOT here: MessageEnd. The assistant message closes
// BEFORE its tool calls execute, so clearing the registry there would blank the
// rows for exactly the calls about to run. The turn (which ends after
// executeTools returns) and the run are the real boundaries at which nothing
// can still be in flight.
func liveToolDelta(e core.AgentEvent) (liveToolMutation, bool) {
	switch e.Type {
	case core.AgentEventMessageUpdate:
		if e.AssistantEvent == nil {
			return liveToolMutation{}, false
		}
		switch e.AssistantEvent.Type {
		case core.ProviderEventToolCallStart:
			return liveToolMutation{kind: liveToolKindUpsert, call: LiveToolCall{
				ToolCallID: e.AssistantEvent.ToolCallID,
				ToolName:   e.AssistantEvent.ToolName,
				Phase:      LiveToolPhaseGenerating,
			}}, true
		case core.ProviderEventToolCallDelta:
			// Partially parsed arguments: the live row's object ("Editing
			// pkg/serve/ws.go") comes from them, so a reconnect mid-generation
			// restores the same row the streaming client was showing.
			if e.AssistantEvent.PartialArgs == nil {
				return liveToolMutation{}, false
			}
			return liveToolMutation{kind: liveToolKindUpsert, call: LiveToolCall{
				ToolCallID: e.AssistantEvent.ToolCallID,
				Args:       core.CloneArgs(e.AssistantEvent.PartialArgs),
				Phase:      LiveToolPhaseGenerating,
			}}, true
		}
		return liveToolMutation{}, false

	case core.AgentEventToolExecStart:
		// The arguments map belongs to the agent's history and is mutated after
		// this event (the permission layer pops its feedback key from it), so
		// the registry keeps a deep copy instead of the live map: it is read by
		// the snapshot goroutine, and a nested map/slice left aliased would
		// still be mutable from under it.
		return liveToolMutation{kind: liveToolKindUpsert, call: LiveToolCall{
			ToolCallID: e.ToolCallID,
			ToolName:   e.ToolName,
			Args:       core.CloneArgs(e.Args),
			Phase:      LiveToolPhaseRunning,
		}}, true

	case core.AgentEventToolExecEnd:
		// Covers every terminal path — success, error, permission rejection,
		// blocked/validation rejection — because the loop emits ToolExecEnd for
		// all of them (see rejectToolCall).
		return liveToolMutation{kind: liveToolKindRemove, call: LiveToolCall{ToolCallID: e.ToolCallID}}, true

	case core.AgentEventTurnEnd, core.AgentEventEnd, core.AgentEventError:
		return liveToolMutation{kind: liveToolKindReset}, true
	}
	return liveToolMutation{}, false
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
		ev := Steered{SessionID: sid, RunGen: gen, ID: e.SteerID, MsgID: e.MsgID, Text: e.Text}
		ev.Custom = projectLiveCustom(e.Message.Custom)
		// A steer always carries its plain text, but one with attachments was
		// injected as content blocks: publish them too so clients render the
		// thumbnails live instead of only after a reload. Text-only steers keep
		// travelling as Text alone, so their shape is unchanged.
		if hasNonTextContent(e.Message.Content) {
			ev.Content = e.Message.Content
		}
		return []any{ev}

	case core.AgentEventUserMessage:
		ev := UserMessageAppended{SessionID: sid, RunGen: gen, MsgID: e.MsgID, Text: e.Text}
		ev.Custom = projectLiveCustom(e.Message.Custom)
		// A structured send carries its blocks; a plain-text prompt travels in
		// Text alone, so clients render exactly one shape per message.
		if e.Text == "" {
			ev.Content = e.Message.Content
		}
		return []any{ev}

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

// projectLiveCustom limits live transport metadata to the fields frontends
// render. Conversation Custom may grow internal fields; publishing it whole
// would silently turn each one into a WebSocket API field.
func projectLiveCustom(custom map[string]any) map[string]any {
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

// hasNonTextContent reports whether a message carries blocks that plain text
// can't express (images, documents). Used to decide when a steer must publish
// its full content: a text-only steer is fully described by its Text, so
// shipping its blocks would just fatten every WS frame for nothing.
func hasNonTextContent(content []core.Content) bool {
	for _, c := range content {
		if c.Type != "text" {
			return true
		}
	}
	return false
}
