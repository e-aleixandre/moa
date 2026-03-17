package bus

import (
	"context"
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
	SetModel(provider core.Provider, model core.Model) error
	SetThinkingLevel(level string) error
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
	ThinkingLevel() string
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
	State      *StateMachine      // may be nil for backward compat
	Approvals  *ApprovalManager   // manages pending permissions/asks; may be nil

	PlanMode    *planmode.PlanMode  // may be nil
	TaskStore   *tasks.Store        // may be nil
	Checkpoints *checkpoint.Store   // may be nil
	PathPolicy  *tool.PathPolicy    // may be nil
	AskBridge   *askuser.Bridge     // may be nil

	ProviderFactory  func(core.Model) (core.Provider, error)
	BaseSystemPrompt string

	// GateConfig is used to reconstruct a Gate when switching from yolo
	// to ask/auto. Preserves allow/deny patterns, rules, headless, etc.
	GateConfig permission.Config

	// SteerFilter returns false to suppress a steer event (e.g. subagent
	// completion text in serve). If nil, all steers are published.
	SteerFilter func(text string) bool

	// Gate is swapped atomically by SetPermissionMode command.
	// The Gate object itself is immutable between swaps — only the pointer changes.
	gate atomic.Pointer[permission.Gate]

	// Run context management — used by SendPrompt handler.
	// Protected by runMu.
	runMu     sync.Mutex
	runCancel context.CancelFunc // cancels the current run context; nil when idle
	runGen    uint64             // incremented each run; used to avoid clearing a newer run's cancel
}

// GetGate returns the current permission gate (may be nil for yolo mode).
func (sctx *SessionContext) GetGate() *permission.Gate {
	return sctx.gate.Load()
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
	return ctx, sctx.runGen
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

// bridgeEvent translates a single core.AgentEvent into typed bus event(s).
func bridgeEvent(sctx *SessionContext, e core.AgentEvent) {
	sid := sctx.SessionID
	b := sctx.Bus

	switch e.Type {
	case core.AgentEventStart:
		b.Publish(AgentStarted{SessionID: sid})

	case core.AgentEventEnd:
		b.Publish(AgentEnded{SessionID: sid, Messages: e.Messages})

	case core.AgentEventError:
		b.Publish(AgentError{SessionID: sid, Err: e.Error})

	case core.AgentEventTurnStart:
		b.Publish(TurnStarted{SessionID: sid})

	case core.AgentEventTurnEnd:
		b.Publish(TurnEnded{SessionID: sid})

	case core.AgentEventMessageStart:
		b.Publish(MessageStarted{SessionID: sid, Message: e.Message})

	case core.AgentEventMessageUpdate:
		if e.AssistantEvent == nil {
			return
		}
		switch e.AssistantEvent.Type {
		case core.ProviderEventTextDelta:
			b.Publish(TextDelta{SessionID: sid, Delta: e.AssistantEvent.Delta})
		case core.ProviderEventThinkingDelta:
			b.Publish(ThinkingDelta{SessionID: sid, Delta: e.AssistantEvent.Delta})
		}

	case core.AgentEventMessageEnd:
		var fullText string
		for _, c := range e.Message.Content {
			if c.Type == "text" {
				fullText += c.Text
			}
		}
		b.Publish(MessageEnded{SessionID: sid, Message: e.Message, FullText: fullText})

	case core.AgentEventToolExecStart:
		b.Publish(ToolExecStarted{
			SessionID:  sid,
			ToolCallID: e.ToolCallID,
			ToolName:   e.ToolName,
			Args:       e.Args,
		})

	case core.AgentEventToolExecUpdate:
		var delta string
		if e.Result != nil {
			for _, c := range e.Result.Content {
				if c.Type == "text" {
					delta += c.Text
				}
			}
		}
		if delta != "" {
			b.Publish(ToolExecUpdate{
				SessionID:  sid,
				ToolCallID: e.ToolCallID,
				Delta:      delta,
			})
		}

	case core.AgentEventToolExecEnd:
		var resultText string
		if e.Result != nil {
			for _, c := range e.Result.Content {
				if c.Type == "text" {
					resultText += c.Text
				}
			}
		}
		b.Publish(ToolExecEnded{
			SessionID:  sid,
			ToolCallID: e.ToolCallID,
			ToolName:   e.ToolName,
			Result:     resultText,
			IsError:    e.IsError,
			Rejected:   e.Rejected,
		})
		// Emit task update on tool_end only (matches serve and TUI behavior).
		if e.ToolName == "tasks" && sctx.TaskStore != nil {
			b.Publish(TasksUpdated{
				SessionID: sid,
				Tasks:     sctx.TaskStore.Tasks(),
			})
		}

	case core.AgentEventSteer:
		if sctx.SteerFilter != nil && !sctx.SteerFilter(e.Text) {
			return
		}
		b.Publish(Steered{SessionID: sid, Text: e.Text})

	case core.AgentEventCompactionStart:
		b.Publish(CompactionStarted{SessionID: sid})

	case core.AgentEventCompactionEnd:
		b.Publish(CompactionEnded{
			SessionID: sid,
			Payload:   e.Compaction,
			Err:       e.Error,
		})
	}
}
