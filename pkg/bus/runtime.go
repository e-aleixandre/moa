package bus

import (
	"context"
	"fmt"
	"sync"

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
}

// SessionRuntime is a fully wired session: bus + state machine + bridge +
// handlers + persistence. Created via NewSessionRuntime.
type SessionRuntime struct {
	ID    string
	Bus   EventBus
	State *StateMachine

	sctx      *SessionContext
	unsub     func()
	closeOnce sync.Once
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

	b := NewLocalBus()
	sm := NewStateMachine(b, cfg.SessionID)

	sctx := &SessionContext{
		SessionID:        cfg.SessionID,
		SessionCtx:       cfg.Ctx,
		Bus:              b,
		Agent:            cfg.Agent,
		State:            sm,
		TaskStore:        cfg.TaskStore,
		Checkpoints:      cfg.Checkpoints,
		PlanMode:         cfg.PlanMode,
		Gate:             cfg.Gate,
		PathPolicy:       cfg.PathPolicy,
		AskBridge:        cfg.AskBridge,
		ProviderFactory:  cfg.ProviderFactory,
		BaseSystemPrompt: cfg.BaseSystemPrompt,
		SteerFilter:      cfg.SteerFilter,
	}

	RegisterHandlers(sctx)
	unsub := Bridge(sctx, cfg.Subscriber)

	if cfg.Persister != nil {
		registerPersistenceReactor(b, sctx, cfg.Persister)
	}

	return &SessionRuntime{
		ID:    cfg.SessionID,
		Bus:   b,
		State: sm,
		sctx:  sctx,
		unsub: unsub,
	}, nil
}

// Close tears down the runtime. Idempotent.
// Aborts any running agent, cancels the run context, unsubscribes from
// agent events, and closes the bus.
func (r *SessionRuntime) Close() {
	r.closeOnce.Do(func() {
		// Cancel run context FIRST so runCtx.Err() != nil before Agent.Abort()
		// causes runFn to return. Prevents misclassifying abort as real error.
		r.sctx.cancelRun()
		// Abort running agent to prevent dangling goroutines.
		r.sctx.Agent.Abort()
		// Unsubscribe from agent events.
		if r.unsub != nil {
			r.unsub()
		}
		// Close bus — subscribers drain and exit.
		r.Bus.Close()
	})
}

// Context returns the SessionContext. For testing and advanced use.
func (r *SessionRuntime) Context() *SessionContext {
	return r.sctx
}
