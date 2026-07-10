package bus

import (
	"context"
	"fmt"
	"strings"
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

// RuntimeConfig holds all dependencies for creating a SessionRuntime.
type RuntimeConfig struct {
	SessionID        string
	Ctx              context.Context
	Bus              EventBus // optional pre-created bus; if nil, a new LocalBus is created
	Agent            AgentController
	Subscriber       AgentSubscriber // nil = use Agent if it implements AgentSubscriber
	TaskStore        *tasks.Store
	Checkpoints      *checkpoint.Store
	PlanMode         *planmode.PlanMode
	Goal             *goal.Goal
	Gate             *permission.Gate
	PathPolicy       *tool.PathPolicy
	AskBridge        *askuser.Bridge
	ProviderFactory  func(core.Model) (core.Provider, error)
	BaseSystemPrompt string
	Persister        SessionPersister
	SteerFilter      func(text string) bool

	CWD        string // workspace directory
	AutoVerify bool   // run verify after edit runs

	// GateConfig preserves allow/deny/rules/headless config for gate reconstruction
	// when switching between permission modes at runtime.
	GateConfig permission.Config

	// InitialMessages/InitialCompactionEpoch load saved state into the agent
	// at construction time (before any handlers fire). Used by session restore.
	InitialMessages        []core.AgentMessage
	InitialCompactionEpoch int

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
	defaults          sessionRuntimeDefaults
	unsub             func()
	closeOnce         sync.Once
	persisterAttached atomic.Bool

	// persister is the attached persister, retained so Flush can persist
	// synchronously (bypassing the async event chain) on shutdown.
	persisterMu sync.Mutex
	persister   SessionPersister
}

// sessionRuntimeDefaults is the configuration present when a runtime starts.
// Missing persisted metadata restores these values, except path policy: old
// sessions with no path metadata are deliberately restored to workspace-only
// access rather than inheriting a potentially unrestricted previous session.
type sessionRuntimeDefaults struct {
	model          core.Model
	thinking       string
	permissionMode permission.Mode
	gateConfig     permission.Config
	tasks          tasks.State
	plan           planmode.State
}

// SessionRestoreState is the validated, typed runtime state decoded from a
// persisted session. It centralizes metadata parsing for in-place restores.
type SessionRestoreState struct {
	Model             core.Model
	HasModel          bool
	Thinking          string
	HasThinking       bool
	PermissionMode    permission.Mode
	HasPermissionMode bool
	Tasks             tasks.State
	HasTasks          bool
	Plan              planmode.State
	PathScope         string
	AllowedPaths      []string
	HasPathPolicy     bool
}

// NewSessionRestoreState decodes the metadata persisted on sess. Invalid
// values are treated as absent so callers fall back to runtime defaults.
func NewSessionRestoreState(sess *session.Session) SessionRestoreState {
	state := SessionRestoreState{Plan: planmode.State{Mode: planmode.ModeOff}}
	if sess == nil || sess.Metadata == nil {
		return state
	}

	meta := sess.Metadata
	if modelSpec, ok := meta[session.MetaModel].(string); ok && modelSpec != "" {
		if model, ok := core.ResolveModel(modelSpec); ok {
			state.Model = model
			state.HasModel = true
		}
	}
	if thinking, ok := meta[session.MetaThinking].(string); ok && core.IsValidThinkingLevel(thinking) {
		state.Thinking = thinking
		state.HasThinking = true
	}
	if mode, ok := meta[session.MetaPermissionMode].(string); ok {
		switch permission.Mode(strings.ToLower(mode)) {
		case permission.ModeYolo, permission.ModeAsk, permission.ModeAuto:
			state.PermissionMode = permission.Mode(strings.ToLower(mode))
			state.HasPermissionMode = true
		}
	}
	if taskState, ok := tasks.StateFromMetadata(meta); ok {
		state.Tasks = taskState
		state.HasTasks = true
	}
	state.Plan = planmode.RestoreFromMetadata(meta)
	if scope, paths := sess.PathMeta(); scope != "" {
		switch strings.ToLower(scope) {
		case "unrestricted":
			state.PathScope = "unrestricted"
			state.HasPathPolicy = true
		case "workspace":
			state.PathScope = "workspace"
			state.HasPathPolicy = true
		}
		if state.HasPathPolicy {
			state.AllowedPaths = append([]string(nil), paths...)
		}
	}
	return state
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
		SessionID:        cfg.SessionID,
		SessionCtx:       cfg.Ctx,
		Bus:              b,
		Agent:            cfg.Agent,
		State:            sm,
		Approvals:        am,
		TaskStore:        cfg.TaskStore,
		Checkpoints:      cfg.Checkpoints,
		PlanMode:         cfg.PlanMode,
		Goal:             cfg.Goal,
		PathPolicy:       cfg.PathPolicy,
		AskBridge:        cfg.AskBridge,
		ProviderFactory:  cfg.ProviderFactory,
		BaseSystemPrompt: cfg.BaseSystemPrompt,
		CWD:              cfg.CWD,
		AutoVerify:       cfg.AutoVerify,
		SteerFilter:      cfg.SteerFilter,
		GateConfig:       cfg.GateConfig,
	}
	sctx.SetGate(cfg.Gate)
	// Let the approval manager stamp pending requests with the current run
	// generation so ClearPending can spare a newer run's live approvals.
	am.runGen = &sctx.RunGenAtomic

	// Compose permission check: plan mode filter + gate check.
	permCheck := func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision {
		if sctx.PlanMode != nil {
			if allowed, reason := sctx.PlanMode.FilterToolCall(name, args); !allowed {
				return &core.ToolCallDecision{
					Block:  true,
					Reason: reason,
					Kind:   core.ToolCallDecisionKindPolicy,
				}
			}
		}
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

	// Take ownership of PlanMode's onChange callback:
	// 1. Rebuild system prompt (centralized, every transition)
	// 2. Publish PlanModeChanged event
	if cfg.PlanMode != nil {
		cfg.PlanMode.SetOnChange(func(mode planmode.Mode) {
			rebuildSystemPrompt(sctx)
			sctx.Bus.Publish(PlanModeChanged{
				SessionID: sctx.SessionID,
				Mode:      string(mode),
				PlanFile:  sctx.PlanMode.PlanFilePath(),
			})
		})
	}

	// Goal mode: rebuild system prompt (inject/remove directive) and announce.
	if cfg.Goal != nil {
		cfg.Goal.SetOnChange(func(active bool) {
			rebuildSystemPrompt(sctx)
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
	rt.defaults = sessionRuntimeDefaults{
		model:          cfg.Agent.Model(),
		thinking:       cfg.Agent.ThinkingLevel(),
		permissionMode: permission.ModeYolo,
		gateConfig:     clonePermissionConfig(cfg.GateConfig),
		tasks:          tasks.State{WidgetMode: tasks.WidgetAll},
		plan:           planmode.State{Mode: planmode.ModeOff},
	}
	if cfg.Gate != nil {
		rt.defaults.permissionMode = cfg.Gate.Mode()
		rt.defaults.gateConfig = clonePermissionConfig(cfg.Gate.SnapshotConfig())
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
	return r.flush(false)
}

// flush persists the current snapshot. Session switching uses force=true while
// persistence is paused so its one final snapshot cannot be preempted by an
// event-triggered save.
func (r *SessionRuntime) flush(force bool) error {
	if !force {
		r.sctx.persistMu.RLock()
		defer r.sctx.persistMu.RUnlock()
		if r.sctx.persistPaused.Load() {
			return nil
		}
	}
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

// SyncPlanMode rebuilds the system prompt and publishes PlanModeChanged
// for the current plan mode state. Call after restoring plan mode from
// persisted metadata (RestoreState/ApplyRestoredState happen before
// SetOnChange is wired).
func (r *SessionRuntime) SyncPlanMode() {
	if r.sctx.PlanMode == nil {
		return
	}
	rebuildSystemPrompt(r.sctx)
	mode := r.sctx.PlanMode.Mode()
	if mode != planmode.ModeOff {
		r.Bus.Publish(PlanModeChanged{
			SessionID: r.sctx.SessionID,
			Mode:      string(mode),
			PlanFile:  r.sctx.PlanMode.PlanFilePath(),
		})
	}
}

// Context returns the SessionContext. For testing and advanced use.
func (r *SessionRuntime) Context() *SessionContext {
	return r.sctx
}

// SwitchSession atomically restores sess into this long-lived runtime. It
// restores history, runtime metadata, the tree syncer, and cost before
// rebinding persistence; direct restoration intentionally emits no
// ConfigChanged events. The agent must be idle.
func (r *SessionRuntime) SwitchSession(sess *session.Session) error {
	if s := r.State.Current(); s == StateRunning || s == StatePermission {
		return fmt.Errorf("bus: cannot switch session while the agent is busy (%s)", s)
	}

	tree, msgs, epoch, err := sessionState(sess)
	if err != nil {
		return fmt.Errorf("bus: SwitchSession: %w", err)
	}
	restored := NewSessionRestoreState(sess)
	model := r.defaults.model
	if restored.HasModel {
		model = restored.Model
	}
	var provider core.Provider
	modelChanged := !sameModel(r.sctx.Agent.Model(), model)
	if modelChanged {
		if r.sctx.ProviderFactory == nil {
			return fmt.Errorf("bus: SwitchSession: model switching unavailable")
		}
		provider, err = r.sctx.ProviderFactory(model)
		if err != nil {
			return fmt.Errorf("bus: SwitchSession: provider for %s: %w", model.ID, err)
		}
	}

	r.sctx.persistPaused.Store(true)
	r.sctx.persistMu.Lock()
	defer func() {
		r.sctx.persistMu.Unlock()
		r.sctx.persistPaused.Store(false)
	}()

	if err := r.sctx.Agent.LoadState(msgs, epoch); err != nil {
		return fmt.Errorf("bus: SwitchSession LoadState: %w", err)
	}
	if modelChanged {
		if err := r.sctx.Agent.SetModel(provider, model); err != nil {
			return fmt.Errorf("bus: SwitchSession SetModel: %w", err)
		}
	}
	thinking := r.defaults.thinking
	if restored.HasThinking {
		thinking = restored.Thinking
	}
	if err := r.sctx.Agent.SetThinkingLevel(thinking); err != nil {
		return fmt.Errorf("bus: SwitchSession SetThinkingLevel: %w", err)
	}
	r.sctx.Tree = tree
	r.sctx.clearSessionCost()
	if r.sctx.treeSyncer != nil {
		r.sctx.treeSyncer.Reset(tree, len(r.sctx.Agent.Messages()))
	}
	if r.sctx.TaskStore != nil {
		if restored.HasTasks {
			r.sctx.TaskStore.RestoreState(restored.Tasks)
		} else {
			r.sctx.TaskStore.RestoreState(r.defaults.tasks)
		}
	}
	if r.sctx.PlanMode != nil {
		plan := r.defaults.plan
		if sess != nil && sess.Metadata != nil {
			plan = restored.Plan
		}
		r.sctx.PlanMode.Restore(plan)
		r.sctx.PlanMode.ApplyRestoredState()
	}
	if r.sctx.PathPolicy != nil {
		// Legacy sessions without explicit path metadata must not inherit the
		// previous session's unrestricted policy or extra directories.
		unrestricted := restored.HasPathPolicy && restored.PathScope == "unrestricted"
		paths := restored.AllowedPaths
		if !restored.HasPathPolicy {
			paths = nil
		}
		r.sctx.PathPolicy.Restore(paths, unrestricted)
	}
	mode := r.defaults.permissionMode
	if restored.HasPermissionMode {
		mode = restored.PermissionMode
	}
	r.restorePermissionMode(mode)
	rebuildSystemPrompt(r.sctx)

	// Re-point the persister at the new session so subsequent saves write there
	// instead of clobbering the previous session under its old ID.
	r.persisterMu.Lock()
	p := r.persister
	r.persisterMu.Unlock()
	if rb, ok := p.(SessionRebinder); ok {
		rb.RebindSession(sess)
	}

	loadedID := r.sctx.SessionID
	if sess != nil && sess.ID != "" {
		loadedID = sess.ID
	}
	r.Bus.Publish(SessionLoaded{SessionID: loadedID})
	if err := r.flush(true); err != nil {
		return fmt.Errorf("bus: SwitchSession Flush: %w", err)
	}
	return nil
}

// LoadSession is retained for callers that used the original in-place API.
// New callers should use SwitchSession.
func (r *SessionRuntime) LoadSession(sess *session.Session) error {
	return r.SwitchSession(sess)
}

func (r *SessionRuntime) restorePermissionMode(mode permission.Mode) {
	g := r.sctx.GetGate()
	if g == nil {
		g = permission.New(mode, clonePermissionConfig(r.defaults.gateConfig))
		r.sctx.SetGate(g)
		if r.sctx.Approvals != nil {
			r.sctx.Approvals.StartPermissionBridge(r.sctx.SessionCtx, g)
		}
		return
	}
	g.Restore(mode, clonePermissionConfig(r.defaults.gateConfig))
}

func sameModel(a, b core.Model) bool {
	return a.ID == b.ID && a.Provider == b.Provider
}

func clonePermissionConfig(cfg permission.Config) permission.Config {
	return permission.Config{
		Allow:     append([]string(nil), cfg.Allow...),
		Deny:      append([]string(nil), cfg.Deny...),
		Rules:     append([]string(nil), cfg.Rules...),
		Evaluator: cfg.Evaluator,
		Headless:  cfg.Headless,
	}
}

// sessionState derives the tree + agent state to load for a session snapshot.
// A v2 session (SessionVersion + entries) rebuilds the tree and derives messages
// via BuildContext; anything else (new or v1) yields an empty tree plus the flat
// v1 messages (nil for a brand-new session, which resets the agent).
func sessionState(sess *session.Session) (*session.Tree, []core.AgentMessage, int, error) {
	if sess != nil && sess.Version >= session.SessionVersion && len(sess.Entries) > 0 {
		tree, err := session.NewTreeFromEntries(sess.Entries, sess.LeafID)
		if err != nil {
			return nil, nil, 0, err
		}
		msgs, epoch := tree.BuildContext()
		return tree, msgs, epoch, nil
	}
	tree := session.NewTree()
	if sess != nil {
		return tree, sess.Messages, sess.CompactionEpoch, nil
	}
	return tree, nil, 0, nil
}
