package agent

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/ealeixandre/moa/pkg/compaction"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/extension"
)

// BudgetExceededError is returned when a run's accumulated cost exceeds MaxBudget.
type BudgetExceededError struct {
	Spent float64
	Limit float64
}

func (e *BudgetExceededError) Error() string {
	return fmt.Sprintf("budget exceeded: $%.4f spent, limit $%.4f", e.Spent, e.Limit)
}

// Is reports whether target is a *BudgetExceededError, enabling errors.Is checks
// against the ErrBudgetExceeded sentinel.
func (e *BudgetExceededError) Is(target error) bool {
	_, ok := target.(*BudgetExceededError)
	return ok
}

// ErrBudgetExceeded is a sentinel for errors.Is checks.
var ErrBudgetExceeded = &BudgetExceededError{}

const steerBufferSize = 32 // capacity of the inter-step steering channel

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

	steerCh    chan string // buffered, drained by agentLoop between steps
	followUpMu sync.Mutex
	followUps  []string // consumed after agentLoop returns in execute()
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
	MaxBudget           float64       // Max USD per run. 0 = unlimited. Requires Model.Pricing when > 0.

	// Permission check called before each tool execution. May block waiting
	// for user approval. Return nil to approve, blocking decision to reject.
	// nil = no permission checks (all tools auto-approved).
	PermissionCheck func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision

	// Custom message conversion (nil = default: filter non-LLM messages)
	ConvertToLLM func([]core.AgentMessage) []core.Message

	// Compaction settings. nil = use DefaultCompactionSettings.
	// Set Enabled:false to disable.
	Compaction *core.CompactionSettings

	// DrainTimeout is the maximum time Send/Run will wait for subscribers to
	// finish processing events before returning. Default: 2s.
	// Set to 0 to disable auto-drain.
	DrainTimeout time.Duration

	Logger *slog.Logger
}

// New creates an Agent from config. Returns error if configuration is invalid.
// Call Run() to execute a prompt.
func New(cfg AgentConfig) (*Agent, error) {
	if cfg.Provider == nil {
		return nil, fmt.Errorf("agent: Provider is required")
	}
	if cfg.MaxBudget < 0 || math.IsNaN(cfg.MaxBudget) || math.IsInf(cfg.MaxBudget, 0) {
		return nil, fmt.Errorf("agent: MaxBudget must be >= 0 and finite, got %f", cfg.MaxBudget)
	}
	if cfg.MaxBudget > 0 && cfg.Model.Pricing == nil {
		return nil, fmt.Errorf("agent: MaxBudget requires Model.Pricing to be set")
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
	if cfg.DrainTimeout == 0 {
		cfg.DrainTimeout = 2 * time.Second
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
		steerCh: make(chan string, steerBufferSize),
	}, nil
}

// Run initializes state with a new prompt and runs the agent loop.
// Any previous conversation state is replaced. For multi-turn, use Send.
// Returns all messages produced during the run.
// Before returning, Run waits for all accepted in-flight events to be processed
// by subscribers (up to DrainTimeout). Dropped events are not waited on.
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

// IsRunning returns true if the agent is currently executing a Send/Run.
func (a *Agent) IsRunning() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cancel != nil
}

// Send appends a user message and runs the agent loop, continuing the conversation.
// If no previous state exists (e.g., first call without Run), state is auto-initialized.
// State mutation is atomic with the "not running" check — concurrent Send calls
// cannot corrupt state.
// Before returning, Send waits for all accepted in-flight events to be processed
// by subscribers (up to DrainTimeout). Dropped events are not waited on.
func (a *Agent) Send(ctx context.Context, prompt string) ([]core.AgentMessage, error) {
	return a.execute(ctx, func() {
		if a.state.Model.ID == "" {
			a.state.Model = a.config.Model
		}
		a.state.Messages = append(a.state.Messages,
			core.WrapMessage(core.NewUserMessage(prompt)))
	})
}

// SendWithCustom appends a user message with custom metadata and runs the agent loop.
// The custom map is attached to the AgentMessage (persisted in session, available
// to frontends for rendering decisions) but does not affect LLM behavior.
func (a *Agent) SendWithCustom(ctx context.Context, prompt string, custom map[string]any) ([]core.AgentMessage, error) {
	return a.execute(ctx, func() {
		if a.state.Model.ID == "" {
			a.state.Model = a.config.Model
		}
		msg := core.WrapMessage(core.NewUserMessage(prompt))
		msg.Custom = custom
		a.state.Messages = append(a.state.Messages, msg)
	})
}

// SendWithContent appends a user message with mixed content blocks (text + images)
// and runs the agent loop, continuing the conversation.
// The content slice is shallow-copied to prevent caller aliasing. This is sufficient
// for text and image content blocks which only contain immutable string fields.
// Before returning, SendWithContent waits for all accepted in-flight events to be
// processed by subscribers (up to DrainTimeout). Dropped events are not waited on.
func (a *Agent) SendWithContent(ctx context.Context, content []core.Content) ([]core.AgentMessage, error) {
	cc := make([]core.Content, len(content))
	copy(cc, content)
	return a.execute(ctx, func() {
		if a.state.Model.ID == "" {
			a.state.Model = a.config.Model
		}
		a.state.Messages = append(a.state.Messages,
			core.WrapMessage(core.NewUserMessageWithContent(cc)))
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

// AppendMessage appends a non-LLM message to the current conversation state.
// Used by the TUI to persist timeline events before the next user turn.
func (a *Agent) AppendMessage(msg core.AgentMessage) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		return fmt.Errorf("cannot append message while agent is running")
	}
	a.state.Messages = append(a.state.Messages, msg)
	if a.state.Model.ID == "" {
		a.state.Model = a.config.Model
	}
	return nil
}

// CompactionEpoch returns the current compaction epoch.
func (a *Agent) CompactionEpoch() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state.CompactionEpoch
}

// Reconfigure swaps the provider, model, and/or thinking level mid-conversation.
// Preserves conversation history. Strips thinking blocks from historical assistant
// messages to avoid invalid signatures when the model changes.
// Returns error if the agent is currently running.
func (a *Agent) Reconfigure(provider core.Provider, model core.Model, thinkingLevel string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		return fmt.Errorf("cannot reconfigure while agent is running")
	}

	oldProvider := a.config.Model.Provider
	oldModel := a.config.Model.ID

	if provider != nil {
		a.config.Provider = provider
	}
	a.config.Model = model
	a.config.ThinkingLevel = thinkingLevel
	a.state.Model = model

	// Strip thinking blocks from history when the model changes.
	// Thinking signatures are model-specific and become invalid.
	if model.ID != oldModel || model.Provider != oldProvider {
		stripThinkingFromHistory(a.state.Messages)
	}

	return nil
}

// SetModel changes the model and optionally the provider.
// If provider is nil, keeps the current provider.
// Strips thinking blocks from history when the model changes.
// Returns error if the agent is currently running.
func (a *Agent) SetModel(provider core.Provider, model core.Model) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		return fmt.Errorf("cannot reconfigure while agent is running")
	}

	oldProvider := a.config.Model.Provider
	oldModel := a.config.Model.ID

	if provider != nil {
		a.config.Provider = provider
	}
	a.config.Model = model
	a.state.Model = model

	if model.ID != oldModel || model.Provider != oldProvider {
		stripThinkingFromHistory(a.state.Messages)
	}

	return nil
}

// SetThinkingLevel changes only the thinking level.
// Returns error if the agent is currently running.
func (a *Agent) SetThinkingLevel(level string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		return fmt.Errorf("cannot reconfigure while agent is running")
	}
	a.config.ThinkingLevel = level
	return nil
}

// SetPermissionCheck swaps the permission check function at runtime.
// nil disables permission checks. Returns error if the agent is running.
func (a *Agent) SetPermissionCheck(fn func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		return fmt.Errorf("cannot change permissions while agent is running")
	}
	a.config.PermissionCheck = fn
	return nil
}

// Model returns the current model.
func (a *Agent) Model() core.Model {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.config.Model
}

// ThinkingLevel returns the current thinking level.
func (a *Agent) ThinkingLevel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.config.ThinkingLevel
}

// SystemPrompt returns the current system prompt.
func (a *Agent) SystemPrompt() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.config.SystemPrompt
}

// SetSystemPrompt replaces the system prompt. Returns error if the agent is running.
func (a *Agent) SetSystemPrompt(prompt string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		return fmt.Errorf("cannot change system prompt while agent is running")
	}
	a.config.SystemPrompt = prompt
	return nil
}

// PermissionCheck returns the current permission callback.
func (a *Agent) PermissionCheck() func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.config.PermissionCheck
}

// stripThinkingFromHistory removes thinking content blocks from assistant
// messages. Thinking signatures are model-specific — sending stale signatures
// to a different model causes errors.
// Allocates new content slices (doesn't mutate original slices that may be
// shared with async session saves).
func stripThinkingFromHistory(msgs []core.AgentMessage) {
	for i := range msgs {
		if msgs[i].Role != "assistant" {
			continue
		}
		hasThinking := false
		for _, c := range msgs[i].Content {
			if c.Type == "thinking" {
				hasThinking = true
				break
			}
		}
		if !hasThinking {
			continue
		}
		filtered := make([]core.Content, 0, len(msgs[i].Content))
		for _, c := range msgs[i].Content {
			if c.Type != "thinking" {
				filtered = append(filtered, c)
			}
		}
		msgs[i].Content = filtered
	}
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
// Send/Run auto-drain before returning, so all accepted events are processed.
func (a *Agent) Subscribe(fn func(core.AgentEvent)) func() {
	return a.emitter.Subscribe(fn)
}

// Drain waits until all in-flight events have been processed by subscribers,
// or timeout expires. Rarely needed — Send/Run auto-drain before returning.
// Kept for backward compatibility and special cases (e.g., mid-run flushes).
func (a *Agent) Drain(timeout time.Duration) {
	a.emitter.Drain(timeout)
}

// Compact forces context compaction regardless of the auto-compaction threshold.
// Returns the compaction payload on success, nil if there was nothing to compact,
// or an error if the agent is running or compaction fails.
func (a *Agent) Compact(ctx context.Context) (*core.CompactionPayload, error) {
	a.mu.Lock()
	if a.cancel != nil {
		a.mu.Unlock()
		return nil, fmt.Errorf("cannot compact while agent is running")
	}

	msgs := a.state.Messages
	model := a.config.Model
	provider := a.config.Provider
	settings := a.config.Compaction
	epoch := a.state.CompactionEpoch
	a.mu.Unlock()

	if settings == nil {
		defaults := core.DefaultCompactionSettings
		settings = &defaults
	}
	if model.MaxInput <= 0 {
		return nil, fmt.Errorf("model has no context window configured")
	}

	toolSpecs := a.tools.Specs()
	estimate := core.EstimateContextTokens(msgs, a.config.SystemPrompt, toolSpecs, epoch)

	streamOpts := core.StreamOptions{ThinkingLevel: a.config.ThinkingLevel}
	result, compacted, err := compaction.Compact(
		ctx, provider, model, streamOpts,
		msgs, estimate.Tokens, model.MaxInput, *settings,
	)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, nil
	}

	a.mu.Lock()
	a.state.Messages = compacted
	a.state.CompactionEpoch++
	a.mu.Unlock()

	return &core.CompactionPayload{
		Summary:       result.Summary,
		TokensBefore:  result.TokensBefore,
		TokensAfter:   result.TokensAfter,
		ReadFiles:     result.ReadFiles,
		ModifiedFiles: result.ModifiedFiles,
	}, nil
}

// Steer queues a message for inter-step delivery. The agent sees it
// at the next gap between tool executions. Safe to call while running.
// No-op if the buffer is full (non-blocking send).
func (a *Agent) Steer(msg string) {
	select {
	case a.steerCh <- msg:
	default:
	}
}

// Enqueue queues a message for post-turn delivery. It will be processed
// after the current agent turn completes, triggering a new turn.
// Safe to call at any time.
func (a *Agent) Enqueue(msg string) {
	a.followUpMu.Lock()
	defer a.followUpMu.Unlock()
	a.followUps = append(a.followUps, msg)
}

func (a *Agent) drainFollowUps() []string {
	a.followUpMu.Lock()
	defer a.followUpMu.Unlock()
	if len(a.followUps) == 0 {
		return nil
	}
	msgs := a.followUps
	a.followUps = nil
	return msgs
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
		maxBudget:           a.config.MaxBudget,
		convertToLLM:        a.config.ConvertToLLM,
		permissionCheck:     a.config.PermissionCheck,
		compaction:          a.config.Compaction,
		steerCh:             a.steerCh,
	}

	var err error
	for {
		err = agentLoop(ctx, cfg)
		if err != nil {
			break
		}
		followUps := a.drainFollowUps()
		steered := drainSteer(a.steerCh)
		if len(followUps) == 0 && len(steered) == 0 {
			break
		}
		// Deterministic order: follow-ups first, then steered.
		for _, msg := range append(followUps, steered...) {
			a.state.Messages = append(a.state.Messages,
				core.WrapMessage(core.NewUserMessage(msg)))
			cfg.emitter.Emit(core.AgentEvent{Type: core.AgentEventSteer, Text: msg})
		}
	}

	// On abort: if the last message is a user message with no assistant reply,
	// insert a synthetic assistant "(interrupted)" to maintain role alternation.
	// Without this, the next Send appends another user message, creating
	// consecutive user messages that providers merge into one — the model
	// sees "2+2=" instead of four separate turns.
	if err != nil && len(a.state.Messages) > 0 && a.state.Messages[len(a.state.Messages)-1].Role == "user" {
		a.state.Messages = append(a.state.Messages, core.WrapMessage(core.Message{
			Role:    "assistant",
			Content: []core.Content{core.TextContent("(interrupted by user)")},
		}))
	}

	// Ensure all async events from this run have been processed by subscribers
	// before returning. The timeout is a safety net for stuck handlers.
	if a.config.DrainTimeout > 0 {
		a.emitter.Drain(a.config.DrainTimeout)
	}

	// Return a copy — the internal slice is reused across turns (Send appends).
	// Without this, callers could mutate returned messages and corrupt state.
	msgs := make([]core.AgentMessage, len(a.state.Messages))
	copy(msgs, a.state.Messages)
	return msgs, err
}
