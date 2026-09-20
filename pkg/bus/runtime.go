package bus

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/e-aleixandre/moa/pkg/askuser"
	"github.com/e-aleixandre/moa/pkg/checkpoint"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/goal"
	"github.com/e-aleixandre/moa/pkg/permission"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/sessioncheckpoint"
	"github.com/e-aleixandre/moa/pkg/tasks"
	"github.com/e-aleixandre/moa/pkg/tool"
)

// RuntimeConfig holds all dependencies for creating a SessionRuntime.
type RuntimeConfig struct {
	SessionID         string
	Ctx               context.Context
	Bus               EventBus // optional pre-created bus; if nil, a new LocalBus is created
	Agent             AgentController
	Subscriber        AgentSubscriber // nil = use Agent if it implements AgentSubscriber
	TaskStore         *tasks.Store
	Checkpoints       *checkpoint.Store
	SessionCheckpoint *sessioncheckpoint.Slot
	Goal              *goal.Goal
	Gate              *permission.Gate
	PathPolicy        *tool.PathPolicy
	AskBridge         *askuser.Bridge
	ProviderFactory   func(core.Model) (core.Provider, error)
	BaseSystemPrompt  string
	Persister         SessionPersister
	SteerFilter       func(text string) bool

	CWD        string // workspace directory
	AutoVerify bool   // run verify after edit runs

	// GateConfig preserves allow/deny/rules/headless config for gate reconstruction
	// when switching between permission modes at runtime.
	GateConfig permission.Config

	// InitialMessages/InitialCompactionEpoch load saved state into the agent
	// at construction time (before any handlers fire). Used by session restore.
	InitialMessages        []core.AgentMessage
	InitialCompactionEpoch int
	InitialMetadata        map[string]any

	// InitialEntries/InitialLeafID load a v2 session tree.
	// When set, the tree is reconstructed and agent state is derived from BuildContext.
	// InitialMessages is ignored when InitialEntries is set.
	InitialEntries []session.Entry
	InitialLeafID  string
}

// SessionRuntime is a fully wired session: bus + state machine + bridge +
// handlers + persistence. Created via NewSessionRuntime.
type SessionRuntime struct {
	ID    string
	Bus   EventBus
	State *StateMachine

	sctx              *SessionContext
	unsub             func()
	closeOnce         sync.Once
	persisterAttached atomic.Bool

	// persister is the attached persister, retained so Flush can persist
	// synchronously (bypassing the async event chain) on shutdown.
	persisterMu sync.Mutex
	persister   SessionPersister
}

// NewSessionRuntime creates a fully wired session runtime.
// Returns error if required config fields are missing.
func NewSessionRuntime(cfg RuntimeConfig) (*SessionRuntime, error) {
	if cfg.Agent == nil {
		return nil, fmt.Errorf("bus: RuntimeConfig.Agent is required")
	}
	if cfg.Ctx == nil {
		cfg.Ctx = context.Background()
	}
	if cfg.SessionID == "" {
		cfg.SessionID = "default"
	}

	// If Subscriber not provided, try to use Agent.
	if cfg.Subscriber == nil {
		sub, ok := cfg.Agent.(AgentSubscriber)
		if !ok {
			return nil, fmt.Errorf("bus: RuntimeConfig.Subscriber is required (Agent does not implement AgentSubscriber)")
		}
		cfg.Subscriber = sub
	}

	var b EventBus
	if cfg.Bus != nil {
		b = cfg.Bus
	} else {
		b = NewLocalBus()
	}
	sm := NewStateMachine(b, cfg.SessionID)
	am := NewApprovalManager(b, sm, cfg.SessionID)

	sctx := &SessionContext{
		SessionID:         cfg.SessionID,
		SessionCtx:        cfg.Ctx,
		Bus:               b,
		Agent:             cfg.Agent,
		State:             sm,
		Approvals:         am,
		TaskStore:         cfg.TaskStore,
		Checkpoints:       cfg.Checkpoints,
		SessionCheckpoint: cfg.SessionCheckpoint,
		Goal:              cfg.Goal,
		PathPolicy:        cfg.PathPolicy,
		AskBridge:         cfg.AskBridge,
		ProviderFactory:   cfg.ProviderFactory,
		BaseSystemPrompt:  cfg.BaseSystemPrompt,
		CWD:               cfg.CWD,
		AutoVerify:        cfg.AutoVerify,
		SteerFilter:       cfg.SteerFilter,
		GateConfig:        cfg.GateConfig,
	}
	sctx.SetGate(cfg.Gate)
	// Let the approval manager stamp pending requests with the current run
	// generation so ClearPending can spare a newer run's live approvals.
	am.runGen = &sctx.RunGenAtomic

	// Compose the permission check with the session gate.
	permCheck := func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision {
		if g := sctx.GetGate(); g != nil {
			return g.Check(ctx, name, args)
		}
		return nil
	}
	if err := cfg.Agent.SetPermissionCheck(permCheck); err != nil {
		return nil, fmt.Errorf("bus: SetPermissionCheck: %w", err)
	}

	// Load initial state (session restore).
	if len(cfg.InitialEntries) > 0 {
		// V2 session: reconstruct tree and derive agent state from it
		tree, err := session.NewTreeFromEntries(cfg.InitialEntries, cfg.InitialLeafID)
		if err != nil {
			return nil, fmt.Errorf("bus: tree reconstruction: %w", err)
		}
		sctx.Tree = tree
		msgs, epoch := tree.BuildContext()
		if err := cfg.Agent.LoadState(msgs, epoch); err != nil {
			return nil, fmt.Errorf("bus: LoadState from tree: %w", err)
		}
		if err := restoreTrimWatermark(cfg.Agent, tree); err != nil {
			return nil, err
		}
	} else if cfg.InitialMessages != nil {
		if err := cfg.Agent.LoadState(cfg.InitialMessages, cfg.InitialCompactionEpoch); err != nil {
			return nil, fmt.Errorf("bus: LoadState: %w", err)
		}
	}
	// Ensure tree exists (even for new/v1 sessions)
	if sctx.Tree == nil {
		sctx.Tree = session.NewTree()
	}
	if sctx.SessionCheckpoint == nil {
		sctx.SessionCheckpoint = sessioncheckpoint.New()
	}
	if cfg.InitialMetadata != nil {
		sctx.SessionCheckpoint.Restore(cfg.InitialMetadata)
	}

	// Goal mode: rebuild system prompt (inject/remove directive) and announce.
	if cfg.Goal != nil {
		cfg.Goal.SetOnChange(func(active bool) {
			// Goal transitions happen between runs; a refusal is not actionable
			// from a change callback.
			_ = rebuildSystemPrompt(sctx)
			sctx.Bus.Publish(goalChangedEvent(sctx.SessionID, cfg.Goal.Info()))
		})
	}

	RegisterHandlers(sctx)
	unsub := Bridge(sctx, cfg.Subscriber)
	RegisterTreeSyncer(b, sctx)

	rt := &SessionRuntime{
		ID:    cfg.SessionID,
		Bus:   b,
		State: sm,
		sctx:  sctx,
		unsub: unsub,
	}
	if cfg.Persister != nil {
		sctx.PersistNow = rt.Flush
	}
	if cfg.Persister != nil {
		rt.persister = cfg.Persister
		RegisterPersistenceReactor(b, sctx, cfg.Persister)
		rt.persisterAttached.Store(true)
	}

	// Start approval bridges.
	if cfg.Gate != nil {
		am.StartPermissionBridge(cfg.Ctx, cfg.Gate)
	}
	if cfg.AskBridge != nil {
		am.StartAskBridge(cfg.Ctx, cfg.AskBridge)
	}

	return rt, nil
}

// Close tears down the runtime. Idempotent.
// Aborts any running agent, cancels the run context, stops approval bridges,
// unsubscribes from agent events, and closes the bus.
func (r *SessionRuntime) Close() {
	r.closeOnce.Do(func() {
		// Cancel run context FIRST so runCtx.Err() != nil before Agent.Abort()
		// causes runFn to return. Prevents misclassifying abort as real error.
		r.sctx.cancelRun()
		// Abort running agent to prevent dangling goroutines.
		r.sctx.Agent.Abort()
		// Stop approval bridges (auto-denies pending permissions).
		if r.sctx.Approvals != nil {
			r.sctx.Approvals.Stop()
		}
		// Unsubscribe from agent events.
		if r.unsub != nil {
			r.unsub()
		}
		// Close bus — subscribers drain and exit.
		r.Bus.Close()
	})
}

// AttachPersister registers a persistence reactor on this runtime.
// Must be called at most once — panics on double call.
func (r *SessionRuntime) AttachPersister(p SessionPersister) {
	if !r.persisterAttached.CompareAndSwap(false, true) {
		panic("bus: AttachPersister called more than once")
	}
	r.persisterMu.Lock()
	r.persister = p
	r.persisterMu.Unlock()
	r.sctx.PersistNow = r.Flush
	RegisterPersistenceReactor(r.Bus, r.sctx, p)
}

// Flush synchronously persists the current session state to disk, bypassing the
// async RunEnded→TreeSynced→save event chain. It first folds any not-yet-synced
// agent messages (the last or in-flight turn) into the tree, then snapshots
// through the attached persister. No-op if no persister is attached.
//
// Used on server shutdown: the async chain may not drain before the process
// exits, which would lose a turn that finished moments before. Flush is
// idempotent and safe to call once activity has quiesced.
func (r *SessionRuntime) Flush() error {
	r.persisterMu.Lock()
	p := r.persister
	r.persisterMu.Unlock()
	if p == nil {
		return nil
	}

	// Fold the last/in-flight turn into the tree so the snapshot is complete.
	// Idempotent: a no-op if the TreeSyncer already synced this turn.
	if r.sctx.treeSyncer != nil {
		r.sctx.treeSyncer.syncMessages()
	}

	meta := collectMetadata(r.sctx)
	if tp, ok := p.(TreePersister); ok && r.sctx.Tree != nil {
		entries, leafID := r.sctx.Tree.Snapshot()
		return tp.SnapshotTree(entries, leafID, meta)
	}
	msgs := r.sctx.Agent.Messages()
	epoch := r.sctx.Agent.CompactionEpoch()
	return p.Snapshot(msgs, epoch, meta)
}

// WaitSettled blocks until the session leaves the active states (running or
// waiting on a permission) AND the terminal event of the run that was in
// flight has been published — or ctx is done.
//
// The second condition is not redundant. A run transitions to idle before it
// publishes RunEnded, so a caller that only watched the state could return in
// that gap, while the outcome no subscriber has seen yet. On shutdown that gap
// is a lost turn: the flush happens, the session is torn down, and the
// RunEnded that would have produced a report is never observed. The run's
// start anchor — written when the generation is reserved, cleared only after
// RunEnded is on the bus — is what closes it.
//
// It reads the state machine directly (the authoritative source) and is woken
// by StateChanged events and by the terminal barrier rather than busy-polling.
// Returns true if the session settled, false if ctx expired first.
func (r *SessionRuntime) WaitSettled(ctx context.Context) bool {
	settled := func() bool {
		s := r.State.Current()
		if s == StateRunning || s == StatePermission {
			return false
		}
		return !r.sctx.runInFlight()
	}
	if settled() {
		return true
	}

	woke := make(chan struct{}, 1)
	unsub := r.Bus.Subscribe(func(StateChanged) {
		select {
		case woke <- struct{}{}:
		default:
		}
	})
	defer unsub()

	// Re-check after subscribing: a transition may have landed between the
	// first check and the subscription taking effect.
	for {
		// The barrier is taken BEFORE the condition is tested, so a run that
		// settles in between wakes this waiter instead of leaving it asleep.
		barrier := r.sctx.settleBarrier()
		if settled() {
			return true
		}
		select {
		case <-woke:
		case <-barrier:
		case <-ctx.Done():
			return settled()
		}
	}
}

// BackgroundWork is how much autonomous work is still outstanding: async
// subagents, background bash jobs, auto-verify and goal verifiers. Same lock
// and same sources as the quiescence check, so a caller that stops waiting can
// say exactly what it stopped waiting for.
func (r *SessionRuntime) BackgroundWork() int {
	return r.sctx.BackgroundWork()
}

// DoIfQuiescent runs fn atomically with respect to run-start if the session is
// quiescent, returning whether it ran. It holds the state lock across fn (via
// StateMachine.DoIfIdle) so a run cannot begin between the quiescence check and
// fn — closing the check-then-act race for a live tool-set mutation. Background
// work is also required to be absent; that part is a snapshot (background jobs
// don't flip a tool set mid-fn), but the run-start edge, which does, is
// serialized. fn must not call back into the state machine.
//
// An admitted generation that has not published its RunEnded yet also refuses:
// a run reaches StateIdle before its terminal event reaches subscribers, and a
// close admitted in that gap would tear the runtime down with the outcome
// still unseen — the very loss this whole path exists to prevent.
func (r *SessionRuntime) DoIfQuiescent(fn func()) bool {
	if r.sctx.hasBackgroundWork() || r.sctx.runInFlight() {
		return false
	}
	return r.State.DoIfIdle(fn)
}

// AdmitCloseIfQuiescent closes run admission and runs fn while holding the
// state lock, but only when there is no work left that close would discard.
// It is intentionally separate from DoIfQuiescent: MCP operations need only
// an idle run boundary, while close must also permanently prevent a new run
// from claiming the slot after it has been admitted.
//
// fn must not call back into the state machine.
func (r *SessionRuntime) AdmitCloseIfQuiescent(fn func()) bool {
	admitted := false
	r.State.DoIfIdle(func() {
		if r.sctx.Agent.QueueLen() != 0 || r.sctx.hasBackgroundWork() || r.sctx.runInFlight() {
			return
		}
		r.sctx.runAdmissionClosed.Store(true)
		fn()
		admitted = true
	})
	return admitted
}

// ReopenRunAdmission reverses a close admission after close failed before the
// runtime was torn down. Callers must also restore their own lifecycle state.
func (r *SessionRuntime) ReopenRunAdmission() {
	r.sctx.runAdmissionClosed.Store(false)
}

// WaitQuiescent waits for the complete autonomous session chain to finish.
// Unlike WaitSettled, it does not return in the gap after a foreground run
// becomes idle while auto-verify, a goal verifier, or an asynchronous child
// job can still publish work (and potentially start another run). It includes
// background bash jobs because their final output is likewise delivered after
// the foreground turn.
//
// A goal that is active but paused without work is quiescent. This makes the
// method usable for headless callers even when a goal stops on a verifier
// infrastructure failure and requires human intervention to resume.
func (r *SessionRuntime) WaitQuiescent(ctx context.Context) bool {
	woke := make(chan struct{}, 1)
	unsub := r.Bus.SubscribeAll(func(any) {
		select {
		case woke <- struct{}{}:
		default:
		}
	})
	defer unsub()

	quiescent := func() bool {
		state := r.State.Current()
		return state != StateRunning && state != StatePermission && !r.sctx.hasBackgroundWork()
	}

	for {
		// Honour the caller's deadline before doing anything expensive: a
		// caller that allotted 15s must not spend 15s plus two drains.
		if ctx.Err() != nil {
			return quiescent()
		}
		// A RunEnded fan-out schedules the automatic reactors asynchronously.
		// Drain the currently accepted publication batch before inspecting the
		// counters, otherwise a caller observing RunEnded could win the race
		// just before the auto-verify/goal reactor marks itself active. Each
		// drain is capped by what is left of the deadline, so the total wait
		// stays inside it.
		r.Bus.Drain(drainBudget(ctx))
		if ctx.Err() != nil {
			return quiescent()
		}
		if quiescent() {
			// One final drain closes the check-after-drain race for events emitted
			// by a reactor while its RunEnded handler was unwinding.
			r.Bus.Drain(drainBudget(ctx))
			if quiescent() {
				return true
			}
			continue
		}
		select {
		case <-woke:
		case <-ctx.Done():
			return quiescent()
		}
	}
}

// maxQuiescenceDrain bounds one drain when the caller set no deadline at all.
// It is the long-standing value; a deadline shortens it, never lengthens it.
const maxQuiescenceDrain = 2 * time.Second

// drainBudget is how long a drain may take without overrunning the caller's
// deadline. A caller who asked for 15 seconds gets 15 seconds in total, not 15
// plus however many drains happened to be in flight.
func drainBudget(ctx context.Context) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return maxQuiescenceDrain
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 0
	}
	if remaining > maxQuiescenceDrain {
		return maxQuiescenceDrain
	}
	return remaining
}

// Context returns the SessionContext. For testing and advanced use.
func (r *SessionRuntime) Context() *SessionContext {
	return r.sctx
}

// RefreshBaseSystemPrompt sets a freshly built base system prompt and re-applies
// it, composing goal fragments on top. Callers use it after the tool set
// changes at runtime (e.g. an MCP server is enabled or disabled) so the model is
// never told about a tool that is no longer registered.
//
// It reports whether the prompt reached the agent: SetSystemPrompt refuses while
// a run is in flight, and a caller that has already recorded the new state needs
// to know it did not take, or the change is silently lost. Callers that cannot
// fail meaningfully may ignore it.
func (r *SessionRuntime) RefreshBaseSystemPrompt(base string) error {
	if r.sctx.Agent == nil {
		return fmt.Errorf("session has no agent")
	}
	r.sctx.BaseSystemPrompt = base
	// rebuildSystemPrompt re-applies BaseSystemPrompt + goal fragments, but
	// it is a no-op when neither mode is active — in that case set the base
	// prompt directly so a plain session still picks up the new tool list.
	if r.sctx.Goal == nil {
		return r.sctx.Agent.SetSystemPrompt(base)
	}
	return rebuildSystemPrompt(r.sctx)
}

// restoreTrimWatermark tells the agent how far context trimming already reached
// on the tree's current branch. It travels beside the messages rather than
// inside them because it is a property of the BRANCH: without it, a reloaded or
// re-branched session would plan its next trim from scratch and re-elide a
// region already elided, possibly under rules that changed since.
//
// Optional by design: an AgentController that does not trim (a test double,
// another embedder) simply does not implement it.
func restoreTrimWatermark(agent AgentController, tree *session.Tree) error {
	setter, ok := agent.(interface{ SetTrimWatermark(string) error })
	if !ok || tree == nil {
		return nil
	}
	if err := setter.SetTrimWatermark(tree.TrimWatermark()); err != nil {
		return fmt.Errorf("bus: restore trim watermark: %w", err)
	}
	return nil
}
