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
// waiting on a permission) — meaning any in-flight run has observed its
// context's cancellation and transitioned to idle/error — or ctx is done.
//
// It reads the state machine directly (the authoritative source) and is woken
// by StateChanged events rather than busy-polling. Returns true if the session
// settled, false if ctx expired while a run was still active. Used on shutdown
// so Flush snapshots a complete turn instead of a partial one.
func (r *SessionRuntime) WaitSettled(ctx context.Context) bool {
	settled := func() bool {
		s := r.State.Current()
		return s != StateRunning && s != StatePermission
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
		if settled() {
			return true
		}
		select {
		case <-woke:
		case <-ctx.Done():
			return settled()
		}
	}
}

// DoIfQuiescent runs fn atomically with respect to run-start if the session is
// quiescent, returning whether it ran. It holds the state lock across fn (via
// StateMachine.DoIfIdle) so a run cannot begin between the quiescence check and
// fn — closing the check-then-act race for a live tool-set mutation. Background
// work is also required to be absent; that part is a snapshot (background jobs
// don't flip a tool set mid-fn), but the run-start edge, which does, is
// serialized. fn must not call back into the state machine.
func (r *SessionRuntime) DoIfQuiescent(fn func()) bool {
	if r.sctx.hasBackgroundWork() {
		return false
	}
	return r.State.DoIfIdle(fn)
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
		// A RunEnded fan-out schedules the automatic reactors asynchronously.
		// Drain the currently accepted publication batch before inspecting the
		// counters, otherwise a caller observing RunEnded could win the race
		// just before the auto-verify/goal reactor marks itself active.
		r.Bus.Drain(2 * time.Second)
		if quiescent() {
			// One final drain closes the check-after-drain race for events emitted
			// by a reactor while its RunEnded handler was unwinding.
			r.Bus.Drain(2 * time.Second)
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
