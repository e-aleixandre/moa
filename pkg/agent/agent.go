package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ealeixandre/go-agent/pkg/core"
	"github.com/ealeixandre/go-agent/pkg/extension"
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

// Run executes a single prompt and blocks until the agent loop finishes.
// Returns all messages produced during the run.
// Events are delivered asynchronously to subscribers; there is no guarantee
// all events are delivered by the time Run returns.
func (a *Agent) Run(ctx context.Context, prompt string) ([]core.AgentMessage, error) {
	a.mu.Lock()
	if a.cancel != nil {
		a.mu.Unlock()
		return nil, fmt.Errorf("agent is already running")
	}

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

	// Initialize state
	a.state = AgentState{
		Messages: []core.AgentMessage{
			core.WrapMessage(core.NewUserMessage(prompt)),
		},
		Model: a.config.Model,
	}

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
	}

	err := agentLoop(ctx, cfg)

	return a.state.Messages, err
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
