package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/extension"
)

// Agent runs the core loop: prompt → LLM → tool calls → execute → repeat.
// It's a library — no I/O, no TUI, no filesystem opinions.
type Agent struct {
	config  AgentConfig
	state   AgentState
	tools   *core.Registry
	hooks   Hooks
	emitter *Emitter
	cancel  context.CancelFunc
	mu      sync.Mutex
}

// AgentConfig configures an Agent.
type AgentConfig struct {
	Provider      core.Provider
	Model         core.Model
	SystemPrompt  string
	ThinkingLevel string
	Tools         *core.Registry
	Extensions    []extension.Extension
	WorkspaceRoot string

	// Guardrails
	MaxTurns            int           // Default: 50. 0 = unlimited.
	MaxToolCallsPerTurn int           // Default: 20. 0 = unlimited.
	MaxRunDuration      time.Duration // Default: 30m. 0 = unlimited.

	// Custom message conversion (nil = default: filter non-LLM messages)
	ConvertToLLM func([]core.AgentMessage) []core.Message

	// Compaction settings. nil = use DefaultCompactionSettings.
	// Set Enabled:false to disable.
	Compaction *core.CompactionSettings

	Logger *slog.Logger
}

// New creates an Agent from config. Returns error if configuration is invalid.
// Call Run() to execute a prompt.
func New(cfg AgentConfig) (*Agent, error) {
	if cfg.Provider == nil {
		return nil, fmt.Errorf("agent: Provider is required")
	}

	// Apply defaults
	if cfg.MaxTurns == 0 {
		cfg.MaxTurns = 50
	}
	if cfg.MaxToolCallsPerTurn == 0 {
		cfg.MaxToolCallsPerTurn = 20
	}
	if cfg.MaxRunDuration == 0 {
		cfg.MaxRunDuration = 30 * time.Minute
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Tools == nil {
		cfg.Tools = core.NewRegistry()
	}
	if cfg.Compaction == nil {
		defaults := core.DefaultCompactionSettings
		cfg.Compaction = &defaults
	}

	// Set up extension host
	ext := extension.NewHost(cfg.Tools, cfg.Logger)
	for _, e := range cfg.Extensions {
		if err := ext.Load(e); err != nil {
			cfg.Logger.Error("failed to load extension", "error", err)
		}
	}

	return &Agent{
		config:  cfg,
		tools:   cfg.Tools,
		hooks:   ext,
		emitter: NewEmitter(cfg.Logger),
	}, nil
}

// Run initializes state with a new prompt and runs the agent loop.
// Any previous conversation state is replaced. For multi-turn, use Send.
// Returns all messages produced during the run.
// Events are delivered asynchronously to subscribers; there is no guarantee
// all events are delivered by the time Run returns.
func (a *Agent) Run(ctx context.Context, prompt string) ([]core.AgentMessage, error) {
	return a.execute(ctx, func() {
		a.state = AgentState{
			Messages: []core.AgentMessage{
				core.WrapMessage(core.NewUserMessage(prompt)),
			},
			Model: a.config.Model,
		}
	})
}

// Send appends a user message and runs the agent loop, continuing the conversation.
// If no previous state exists (e.g., first call without Run), state is auto-initialized.
// State mutation is atomic with the "not running" check — concurrent Send calls
// cannot corrupt state.
func (a *Agent) Send(ctx context.Context, prompt string) ([]core.AgentMessage, error) {
	return a.execute(ctx, func() {
		if a.state.Model.ID == "" {
			a.state.Model = a.config.Model
		}
		a.state.Messages = append(a.state.Messages,
			core.WrapMessage(core.NewUserMessage(prompt)))
	})
}

// Reset clears conversation state. Returns error if the agent is currently running.
func (a *Agent) Reset() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		return fmt.Errorf("cannot reset while agent is running")
	}
	a.state = AgentState{}
	return nil
}

// LoadMessages replaces the conversation history with the given messages.
// Used to restore a previous session. Returns error if the agent is running.
func (a *Agent) LoadMessages(msgs []core.AgentMessage) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		return fmt.Errorf("cannot load messages while agent is running")
	}
	a.state = AgentState{
		Messages: msgs,
		Model:    a.config.Model,
	}
	return nil
}

// LoadState replaces the full conversation state including compaction epoch.
// Used to restore a previous session with compaction history.
func (a *Agent) LoadState(msgs []core.AgentMessage, compactionEpoch int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		return fmt.Errorf("cannot load state while agent is running")
	}
	a.state = AgentState{
		Messages:        msgs,
		Model:           a.config.Model,
		CompactionEpoch: compactionEpoch,
	}
	return nil
}

// CompactionEpoch returns the current compaction epoch.
func (a *Agent) CompactionEpoch() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state.CompactionEpoch
}

// Messages returns a shallow copy of the current conversation messages.
// The returned slice is independent (append-safe), but individual messages
// share content slices with the internal state. Safe for reading (e.g., JSON
// marshaling for session persistence) but callers should not mutate content.
func (a *Agent) Messages() []core.AgentMessage {
	a.mu.Lock()
	defer a.mu.Unlock()
	msgs := make([]core.AgentMessage, len(a.state.Messages))
	copy(msgs, a.state.Messages)
	return msgs
}

// Subscribe registers a listener for agent events.
// Returns an unsubscribe function. Listeners are async — slow listeners don't block the loop.
func (a *Agent) Subscribe(fn func(core.AgentEvent)) func() {
	return a.emitter.Subscribe(fn)
}

// Abort cancels the current run.
func (a *Agent) Abort() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
	}
}

// execute runs the agent loop. prepare is called under a.mu to mutate state
// atomically with the "not running" check. This prevents races where concurrent
// callers could mutate state before getting the "already running" error.
func (a *Agent) execute(ctx context.Context, prepare func()) ([]core.AgentMessage, error) {
	a.mu.Lock()
	if a.cancel != nil {
		a.mu.Unlock()
		return nil, fmt.Errorf("agent is already running")
	}

	// Mutate state atomically with the running check
	prepare()

	// Apply run duration limit
	if a.config.MaxRunDuration > 0 {
		ctx, a.cancel = context.WithTimeout(ctx, a.config.MaxRunDuration)
	} else {
		ctx, a.cancel = context.WithCancel(ctx)
	}
	cancel := a.cancel
	a.mu.Unlock()

	defer func() {
		cancel()
		a.mu.Lock()
		a.cancel = nil
		a.mu.Unlock()
	}()

	// Build stream options
	streamOpts := core.StreamOptions{
		ThinkingLevel: a.config.ThinkingLevel,
	}

	cfg := &loopConfig{
		provider:            a.config.Provider,
		tools:               a.tools,
		hooks:               a.hooks,
		emitter:             a.emitter,
		state:               &a.state,
		model:               a.config.Model,
		systemPrompt:        a.config.SystemPrompt,
		streamOpts:          streamOpts,
		maxTurns:            a.config.MaxTurns,
		maxToolCallsPerTurn: a.config.MaxToolCallsPerTurn,
		convertToLLM:        a.config.ConvertToLLM,
		compaction:          a.config.Compaction,
	}

	err := agentLoop(ctx, cfg)
	// Return a copy — the internal slice is reused across turns (Send appends).
	// Without this, callers could mutate returned messages and corrupt state.
	msgs := make([]core.AgentMessage, len(a.state.Messages))
	copy(msgs, a.state.Messages)
	return msgs, err
}
