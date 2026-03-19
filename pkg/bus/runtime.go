package bus

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/ealeixandre/moa/pkg/askuser"
	"github.com/ealeixandre/moa/pkg/checkpoint"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/permission"
	"github.com/ealeixandre/moa/pkg/planmode"
	"github.com/ealeixandre/moa/pkg/tasks"
	"github.com/ealeixandre/moa/pkg/tool"
)

// RuntimeConfig holds all dependencies for creating a SessionRuntime.
type RuntimeConfig struct {
	SessionID        string
	Ctx              context.Context
	Bus              EventBus        // optional pre-created bus; if nil, a new LocalBus is created
	Agent            AgentController
	Subscriber       AgentSubscriber // nil = use Agent if it implements AgentSubscriber
	TaskStore        *tasks.Store
	Checkpoints      *checkpoint.Store
	PlanMode         *planmode.PlanMode
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
	if cfg.InitialMessages != nil {
		if err := cfg.Agent.LoadState(cfg.InitialMessages, cfg.InitialCompactionEpoch); err != nil {
			return nil, fmt.Errorf("bus: LoadState: %w", err)
		}
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

	RegisterHandlers(sctx)
	unsub := Bridge(sctx, cfg.Subscriber)

	rt := &SessionRuntime{
		ID:    cfg.SessionID,
		Bus:   b,
		State: sm,
		sctx:  sctx,
		unsub: unsub,
	}

	if cfg.Persister != nil {
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
	RegisterPersistenceReactor(r.Bus, r.sctx, p)
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
