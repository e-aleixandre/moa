package agent

import (
	"context"
	"errors"
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

// ErrMaxTurnsExceeded is a sentinel for a run that hit its MaxTurns cap.
// The concrete error wraps it (with the turn count) via fmt.Errorf("%w").
var ErrMaxTurnsExceeded = errors.New("max turns exceeded")

// ErrDoomLoop is a sentinel for a run stopped because it repeated identical
// tool calls (a doom loop). The concrete error wraps it via fmt.Errorf("%w").
var ErrDoomLoop = errors.New("doom loop detected")

const steerBufferSize = 32 // capacity of the steer queue

// steerQueue is an inspectable, order-preserving queue of steer items,
// replacing the old unbuffered-inspection chan string. Safe for concurrent
// use.
type steerQueue struct {
	mu    sync.Mutex
	items []core.SteerItem
}

// push appends an item, returning false (dropping it) if the queue is already
// at steerBufferSize (mirrors the old non-blocking channel send). The bool lets
// callers surface a "queue full" rejection instead of silently confirming.
func (q *steerQueue) push(it core.SteerItem) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) >= steerBufferSize {
		return false
	}
	q.items = append(q.items, it)
	return true
}

// pushFront re-inserts items at the head of the queue, preserving their order.
// Used to hand back items that were drained but then lost the race to start a
// run (deliverQueuedSteers): the concurrent run that won the slot drains them on
// its next step, so this is a transient, self-healing state. It does NOT drop to
// steerBufferSize: these items were already accepted (a caller was told the
// steer is queued), so truncating would silently lose an accepted user message.
// The overflow is bounded — at most the drained batch plus what one concurrent
// run accepted (<=steerBufferSize) — and clears on the next drain. push() still
// enforces the hard bound for fresh steers, which is where flooding is possible.
func (q *steerQueue) pushFront(items []core.SteerItem) {
	if len(items) == 0 {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = append(append([]core.SteerItem{}, items...), q.items...)
}

// drain removes and returns all queued items, in order.
func (q *steerQueue) drain() []core.SteerItem {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return nil
	}
	items := q.items
	q.items = nil
	return items
}

// drainUntilBarrier removes and returns the queued items up to (but NOT
// including) the first barrier (a queued command). If the head is a barrier it
// returns nil and leaves the queue untouched, so the run ends naturally and the
// bus can execute the command at the idle point. This is what preserves strict
// send order: steers queued before a command are injected into the current run;
// the command and anything after it wait for the run to finish.
func (q *steerQueue) drainUntilBarrier() []core.SteerItem {
	q.mu.Lock()
	defer q.mu.Unlock()
	cut := 0
	for cut < len(q.items) && !q.items[cut].IsBarrier() {
		cut++
	}
	if cut == 0 {
		return nil
	}
	items := q.items[:cut]
	q.items = append([]core.SteerItem{}, q.items[cut:]...)
	return items
}

// peekHead returns a copy of the item at the head of the queue without removing
// it. The bool is false when the queue is empty.
func (q *steerQueue) peekHead() (core.SteerItem, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return core.SteerItem{}, false
	}
	return q.items[0], true
}

// popBarrier removes the head item only if it is still the barrier with the
// given ID. It returns false when the head changed (a race with a concurrent
// drain or a fresh enqueue), so the caller re-checks instead of executing a
// command that is no longer at the front.
func (q *steerQueue) popBarrier(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 || !q.items[0].IsBarrier() || q.items[0].ID != id {
		return false
	}
	q.items = append([]core.SteerItem{}, q.items[1:]...)
	return true
}

// snapshot returns a copy of the queued items without removing them.
func (q *steerQueue) snapshot() []core.SteerItem {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return nil
	}
	items := make([]core.SteerItem, len(q.items))
	copy(items, q.items)
	return items
}

// clear drops all queued items without returning them.
func (q *steerQueue) clear() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = nil
}

// len returns the number of queued items (steers and barriers).
func (q *steerQueue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// steerMessage builds the conversation message for a queued steer, using its
// full content blocks (text + images) when present and falling back to plain
// text otherwise. Callers wrap and assign a MsgID. The item's Content is
// expected to already be owned by the agent (copied at the enqueue/Send
// boundary), so this does not copy again.
func steerMessage(item core.SteerItem) core.Message {
	if len(item.Content) > 0 {
		return core.NewUserMessageWithContent(item.Content)
	}
	return core.NewUserMessage(item.Text)
}

// ownItem returns a copy of the item that shares no mutable backing state with
// the caller: its Content slice is deep-cloned (see core.CloneContent, which
// also clones each block's mutable Arguments map). Used at every boundary where
// an item enters the agent (Steer/SendItems), so a caller mutating its content
// after handing it over can't race the provider reading a.state.Messages.
func ownItem(it core.SteerItem) core.SteerItem {
	if len(it.Content) > 0 {
		it.Content = core.CloneContent(it.Content)
	}
	return it
}

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

	steers     steerQueue // inspectable queue, drained by agentLoop between steps
	followUpMu sync.Mutex
	followUps  []string // consumed after agentLoop returns in execute()

	lastRunCost float64 // USD cost of the most recent execute(), guarded by mu
	// lastRunTimedOut records whether the most recent execute() ended because
	// this run's own MaxRunDuration deadline tripped (ctx.Err() ==
	// context.DeadlineExceeded), as opposed to a user abort or a provider error
	// that merely wrapped a context error. Derived from the run context — the
	// authoritative intent signal, same as interruptedMarkerText — so callers
	// need not (unreliably) inspect the returned error's chain. Guarded by mu.
	lastRunTimedOut bool
}

// AgentConfig configures an Agent.
type AgentConfig struct {
	Provider      core.Provider
	Model         core.Model
	SystemPrompt  string
	ThinkingLevel string
	CacheTTL      string // Prompt-cache TTL: "" (5m default) or "1h". Interactive agent only.
	MaxTokens     int    // Max output tokens per LLM call. 0 = shared model-aware default.
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

	// Apply defaults. MaxTurns, MaxToolCallsPerTurn, MaxRunDuration default
	// to 0 (unlimited), like MaxBudget. Set explicit values via config.json
	// or CLI flags if you want guardrails.
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

// SendWithMsgID is Send with a caller-supplied MsgID for the user message,
// letting the caller correlate a later event (e.g. a Steered announcement for a
// batch of queued steers folded into this one prompt) with the message that
// lands in state — so reconnect snapshots dedup it by that shared MsgID.
func (a *Agent) SendWithMsgID(ctx context.Context, prompt, msgID string) ([]core.AgentMessage, error) {
	return a.execute(ctx, func() {
		if a.state.Model.ID == "" {
			a.state.Model = a.config.Model
		}
		msg := core.WrapMessage(core.NewUserMessage(prompt))
		if msgID != "" {
			msg.MsgID = msgID
		}
		a.state.Messages = append(a.state.Messages, msg)
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
// The content is deep-cloned (core.CloneContent) to take ownership from the caller,
// so a later mutation of the caller's slice or of a block's Arguments map can't
// change the stored message or race a concurrent reader.
// Before returning, SendWithContent waits for all accepted in-flight events to be
// processed by subscribers (up to DrainTimeout). Dropped events are not waited on.
func (a *Agent) SendWithContent(ctx context.Context, content []core.Content) ([]core.AgentMessage, error) {
	cc := core.CloneContent(content)
	return a.execute(ctx, func() {
		if a.state.Model.ID == "" {
			a.state.Model = a.config.Model
		}
		a.state.Messages = append(a.state.Messages,
			core.WrapMessage(core.NewUserMessageWithContent(cc)))
	})
}

// SendItems appends one user message per queued item (each with its own MsgID,
// carrying image/content blocks when present) and runs the agent loop. It is
// used to start a fresh run for the steers that were queued after a barrier
// command, preserving per-item granularity (no folding into one message).
//
// msgIDs, when non-empty, supplies the stable MsgID for each item in order
// (len(msgIDs) must equal len(items)); an empty entry (or a nil/short slice) is
// auto-minted. Callers pre-mint so they can announce each delivered chip with a
// known MsgID without waiting for the run — clients dedup by MsgID on reconnect.
// The returned MsgIDs are the effective ones in item order. Barrier items are
// commands, never messages, and are skipped defensively.
func (a *Agent) SendItems(ctx context.Context, items []core.SteerItem, msgIDs []string) ([]core.AgentMessage, []string, error) {
	// Take ownership of each item's Content so a caller mutating its slices
	// after the call can't race the provider reading a.state.Messages.
	type owned struct {
		item  core.SteerItem
		msgID string
	}
	list := make([]owned, 0, len(items))
	for i, it := range items {
		if it.IsBarrier() {
			continue
		}
		mid := ""
		if i < len(msgIDs) {
			mid = msgIDs[i]
		}
		list = append(list, owned{item: ownItem(it), msgID: mid})
	}
	outIDs := make([]string, len(list))
	msgs, err := a.execute(ctx, func() {
		if a.state.Model.ID == "" {
			a.state.Model = a.config.Model
		}
		for i, o := range list {
			um := core.WrapMessage(steerMessage(o.item))
			if o.msgID != "" {
				um.MsgID = o.msgID
			} else {
				um.EnsureMsgID()
			}
			outIDs[i] = um.MsgID
			a.state.Messages = append(a.state.Messages, um)
		}
	})
	return msgs, outIDs, err
}

// PeekQueueHead returns a copy of the item at the head of the unified queue
// without removing it, and false when the queue is empty. Used by the bus queue
// pump to decide whether the next item is a barrier command or a steer.
func (a *Agent) PeekQueueHead() (core.SteerItem, bool) {
	return a.steers.peekHead()
}

// PopQueueBarrier removes the head item only if it is still the barrier command
// with the given ID, returning false when the head changed. Lets the pump
// execute a queued command exactly once, safely against concurrent enqueues.
func (a *Agent) PopQueueBarrier(id string) bool {
	return a.steers.popBarrier(id)
}

// DrainUntilBarrier removes and returns the queued steers up to (but not
// including) the first barrier command. Used by the pump to start a new run
// with the steers that follow an executed command.
func (a *Agent) DrainUntilBarrier() []core.SteerItem {
	return a.steers.drainUntilBarrier()
}

// Reset clears conversation state. Returns error if the agent is currently running.
//
// Reset deliberately does NOT drop the queued steers/barriers: when a queued
// /clear barrier is executed at idle, everything still in the queue is, by FIFO,
// behind the /clear and therefore belongs to the fresh conversation (reset
// in-place). Callers that want to discard the queue call CancelSteer explicitly.
func (a *Agent) Reset() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		return fmt.Errorf("cannot reset while agent is running")
	}
	a.state = AgentState{}
	return nil
}

// QueueLen returns the number of items currently in the unified queue rail
// (steers and barriers, including internal steers). Used by the producer-side
// strict-order gate: a user run must not start while the queue is non-empty.
func (a *Agent) QueueLen() int {
	return a.steers.len()
}

// LoadMessages replaces the conversation history with the given messages.
// Used to restore a previous session. Returns error if the agent is running.
func (a *Agent) LoadMessages(msgs []core.AgentMessage) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		return fmt.Errorf("cannot load messages while agent is running")
	}
	ensureMsgIDs(msgs)
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
	ensureMsgIDs(msgs)
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
	msg.EnsureMsgID()
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
	// Preserve New()'s invariant: a live MaxBudget requires pricing, else the
	// cost guardrail silently stops accumulating and never trips.
	if a.config.MaxBudget > 0 && model.Pricing == nil {
		return fmt.Errorf("cannot switch to a model without pricing while MaxBudget is set")
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
	if a.config.MaxBudget > 0 && model.Pricing == nil {
		return fmt.Errorf("cannot switch to a model without pricing while MaxBudget is set")
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

// SetCompactAt sets the soft compaction threshold in tokens. When >0, the agent
// compacts once context exceeds this many tokens (clamped to the model window),
// instead of waiting for the full window. 0 restores the default (window-based)
// behavior. Returns error if the agent is currently running.
func (a *Agent) SetCompactAt(tokens int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		return fmt.Errorf("cannot reconfigure while agent is running")
	}
	// Copy-on-write: the loop shares this pointer, and it may be shared across
	// agents, so replace it rather than mutating in place.
	settings := core.DefaultCompactionSettings
	if a.config.Compaction != nil {
		settings = *a.config.Compaction
	}
	settings.CompactAt = tokens
	a.config.Compaction = &settings
	return nil
}

// CompactAt returns the current soft compaction threshold in tokens (0 = the
// default window-based behavior).
func (a *Agent) CompactAt() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.config.Compaction == nil {
		return 0
	}
	return a.config.Compaction.CompactAt
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

// MaxBudget returns the current per-run budget ceiling in USD (0 = unlimited).
func (a *Agent) MaxBudget() float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.config.MaxBudget
}

// SetMaxBudget changes the per-run budget ceiling. Goal mode uses this to cap
// each iteration at the remaining total budget. Validated like New(): a positive
// budget requires model pricing. Returns an error if the agent is running.
func (a *Agent) SetMaxBudget(v float64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		return fmt.Errorf("cannot change budget while agent is running")
	}
	if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return fmt.Errorf("MaxBudget must be >= 0 and finite, got %f", v)
	}
	if v > 0 && a.config.Model.Pricing == nil {
		return fmt.Errorf("MaxBudget requires model pricing")
	}
	a.config.MaxBudget = v
	return nil
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

	// Claim the running slot for the whole operation. The compaction LLM call
	// below takes seconds and runs with the mutex released; without holding the
	// slot, a concurrent Send()/Run() would pass its own `a.cancel == nil` check,
	// start a run, and have its appended messages stomped when we write back the
	// stale snapshot. Setting a.cancel makes execute() return "already running"
	// and serializes against other Compact() calls.
	ctx, a.cancel = context.WithCancel(ctx)
	cancel := a.cancel
	a.mu.Unlock()

	defer func() {
		cancel()
		a.mu.Lock()
		a.cancel = nil
		a.mu.Unlock()
	}()

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
		msgs, estimate.Tokens, settings.EffectiveWindow(model.MaxInput), *settings,
	)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, nil
	}
	for i := range compacted {
		compacted[i].EnsureMsgID()
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
		SummaryMsgID:  compacted[0].MsgID,
		FirstKeptMsgID: func() string {
			if len(compacted) > 1 {
				return compacted[1].MsgID
			}
			return ""
		}(),
		Usage: result.Usage,
	}, nil
}

// Steer queues a message for inter-step delivery. The agent sees it
// at the next gap between tool executions. Safe to call while running.
// Returns false if the queue is full (the message was dropped), so callers can
// surface a rejection instead of confirming a message that will never arrive.
func (a *Agent) Steer(it core.SteerItem) bool {
	return a.steers.push(ownItem(it))
}

// CancelSteer drops all steer messages still queued for inter-step delivery.
// Used when the user pulls queued steers back into the input to edit them, so
// the agent doesn't also deliver the originals (double-delivery). Safe to call
// while running; already-delivered steers cannot be recalled.
func (a *Agent) CancelSteer() {
	a.steers.clear()
}

// DrainSteers removes and returns all steer messages still queued for
// inter-step delivery. Used to hand queued messages to a new run when the
// operation that accepted them (e.g. a manual compaction) finishes without
// running the agent loop, which would otherwise leave them undelivered.
func (a *Agent) DrainSteers() []core.SteerItem {
	return a.steers.drain()
}

// PushSteersFront re-inserts items at the head of the steer queue, preserving
// their order. Used to hand drained items back when a delivery attempt loses a
// race for the run slot, so FIFO order (oldest first) survives.
func (a *Agent) PushSteersFront(items []core.SteerItem) {
	a.steers.pushFront(items)
}

// PendingSteers returns a snapshot of the user-visible steer messages still
// queued for inter-step delivery, without removing them. Used to report
// authoritative queue state (e.g. reconnect snapshots) without disturbing
// delivery order. Internal steers (system-generated, with suppressed delivery
// events) are excluded so they never surface as phantom "queued" chips.
func (a *Agent) PendingSteers() []core.SteerItem {
	all := a.steers.snapshot()
	out := all[:0:0]
	for _, it := range all {
		if !it.Internal {
			out = append(out, it)
		}
	}
	return out
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

// MarkerRunTimedOut is the synthetic assistant-message text inserted when a run
// ends on its MaxRunDuration deadline before the model replied. Exported so
// callers that surface partial output can recognise and exclude it (it is a
// status marker, not real assistant text).
const MarkerRunTimedOut = "(run timed out)"

// interruptedMarkerText returns the text for the synthetic assistant message
// inserted when a run ends before the model replied. It reflects the actual
// cause so a provider failure is never mislabeled as a user interruption.
//
// The run context is the authoritative signal for user intent: Agent.Abort
// cancels it (context.Canceled) and a MaxRunDuration timeout trips its deadline
// (context.DeadlineExceeded). A provider failure (e.g. a 429 usage limit) leaves
// the context live (ctx.Err() == nil). We therefore decide user-vs-provider from
// ctx.Err() ALONE — never from err's chain, which could wrap a context error
// while the context is still active — and in priority order:
//
//	deadline (timeout) → cancel (user abort) → quota → other error.
//
// A genuine abort wins even if a provider error also arrived in the same
// unwind: the user's explicit stop is the intent to record.
func interruptedMarkerText(ctx context.Context, err error) string {
	switch ctx.Err() {
	case context.DeadlineExceeded:
		return MarkerRunTimedOut
	case context.Canceled:
		return "(interrupted by user)"
	}
	if qe, ok := core.AsQuotaExceeded(err); ok {
		return "(stopped: " + qe.Error() + ")"
	}
	if err != nil {
		return "(stopped: " + err.Error() + ")"
	}
	return "(interrupted by user)"
}

// ensureMsgIDs assigns a stable MsgID to any message lacking one. It mutates the
// given slice's elements in place; callers passing externally-owned slices (e.g.
// LoadMessages/LoadState) intentionally leave the caller's copy consistent too.
// Callers must hold a.mu (or otherwise own the slice). Kept as a package helper
// so every state-mutating entry point can enforce the "no anonymous message in
// state" invariant that the tree syncer relies on for deduplication.
func ensureMsgIDs(msgs []core.AgentMessage) {
	for i := range msgs {
		msgs[i].EnsureMsgID()
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

	// Invariant: every message in state carries a stable MsgID before it can be
	// synced to the tree. The Run/Send* entry points build user messages without
	// one (core.NewUserMessage leaves MsgID empty), so they would first sync under
	// a positional "legacy:<index>" identity and later be re-appended when
	// compaction assigns them a real MsgID — duplicating them after the compaction
	// marker. Normalizing here, under a.mu, closes that gap for every ingress path.
	ensureMsgIDs(a.state.Messages)

	// Apply run duration limit. Keep the parent context to distinguish OUR own
	// deadline from an inherited one: with context.WithTimeout the effective
	// deadline is the earlier of parent and child, so ctx.Err() alone can't tell
	// whether it was this run's MaxRunDuration or a caller-imposed deadline that
	// fired. parentCtx lets TimedOut() attribute the timeout correctly.
	parentCtx := ctx
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
		ThinkingLevel:  a.config.ThinkingLevel,
		CacheRetention: a.config.CacheTTL,
	}
	if a.config.MaxTokens > 0 {
		mt := a.config.MaxTokens
		streamOpts.MaxTokens = &mt
	}

	cfg := &loopConfig{
		provider:            a.config.Provider,
		tools:               a.tools,
		hooks:               a.hooks,
		emitter:             a.emitter,
		state:               &a.state,
		stateMu:             &a.mu,
		model:               a.config.Model,
		systemPrompt:        a.config.SystemPrompt,
		streamOpts:          streamOpts,
		maxTurns:            a.config.MaxTurns,
		maxToolCallsPerTurn: a.config.MaxToolCallsPerTurn,
		maxBudget:           a.config.MaxBudget,
		convertToLLM:        a.config.ConvertToLLM,
		permissionCheck:     a.config.PermissionCheck,
		compaction:          a.config.Compaction,
		drainSteers:         a.steers.drainUntilBarrier,
	}

	var err error
	for {
		err = agentLoop(ctx, cfg)
		if err != nil {
			break
		}
		followUps := a.drainFollowUps()
		steered := a.steers.drainUntilBarrier()
		if len(followUps) == 0 && len(steered) == 0 {
			break
		}
		// Deterministic order: follow-ups first, then steered.
		// Lock each append: external readers (Messages/CompactionEpoch) may run
		// concurrently until the deferred cancel-cleanup clears a.cancel.
		for _, msg := range followUps {
			a.mu.Lock()
			um := core.WrapMessage(core.NewUserMessage(msg))
			um.EnsureMsgID()
			mid := um.MsgID
			a.state.Messages = append(a.state.Messages, um)
			a.mu.Unlock()
			cfg.emitter.Emit(core.AgentEvent{Type: core.AgentEventSteer, MsgID: mid, Text: msg})
		}
		for _, item := range steered {
			a.mu.Lock()
			um := core.WrapMessage(steerMessage(item))
			um.EnsureMsgID()
			mid := um.MsgID
			a.state.Messages = append(a.state.Messages, um)
			a.mu.Unlock()
			cfg.emitter.Emit(core.AgentEvent{Type: core.AgentEventSteer, SteerID: item.ID, MsgID: mid, Text: item.Text})
		}
	}

	// Classify the termination cause NOW, the instant the loop returned — before
	// any cleanup (marker insertion, Emitter.Drain) that could itself outlast our
	// deadline and taint ctx.Err(). Our own MaxRunDuration budget was exhausted
	// only if: the run ended with an error, we set a duration, our context tripped
	// its deadline, and the parent context did NOT itself end (a parent
	// deadline/cancel propagates into ctx too, but that's the caller's limit, not
	// ours). Same ctx.Err() signal interruptedMarkerText uses just below.
	timedOut := err != nil &&
		a.config.MaxRunDuration > 0 &&
		ctx.Err() == context.DeadlineExceeded &&
		parentCtx.Err() == nil

	// On a run error (including a user abort, which cancels the context),
	// discard any steers still queued for this now-dead run. Left in the queue
	// they would be injected as a stale user turn on the next Send — possibly
	// into a different conversation after Reset. On a user abort the frontend
	// preserves the user's intent separately, by moving its own locally-tracked
	// queued chips back into the input; the agent's buffer is cleared here
	// regardless.
	if err != nil {
		a.steers.clear()
	}

	// If the run ended before the model replied (last message is still the
	// user's), insert a synthetic assistant message to maintain role
	// alternation. Without it, the next Send appends another user message,
	// creating consecutive user turns that providers merge into one — the model
	// sees "2+2=" instead of separate turns.
	//
	// The marker text must reflect WHY the run ended: only a genuine user abort
	// gets "(interrupted by user)". A provider failure (e.g. a ChatGPT usage
	// limit / 429) must NOT be mislabeled as a user interruption — that both
	// confuses the user ("I didn't stop it") and lies to the model on replay.
	if err != nil && len(a.state.Messages) > 0 && a.state.Messages[len(a.state.Messages)-1].Role == "user" {
		a.mu.Lock()
		interrupted := core.WrapMessage(core.Message{
			Role:    "assistant",
			Content: []core.Content{core.TextContent(interruptedMarkerText(ctx, err))},
		})
		interrupted.EnsureMsgID()
		a.state.Messages = append(a.state.Messages, interrupted)
		a.mu.Unlock()
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

	// Record the run's true accumulated cost — including empty/failed-turn usage
	// the loop billed internally that never surfaces as an assistant message —
	// so callers can charge the real spend rather than re-deriving it from msgs.
	a.mu.Lock()
	a.lastRunCost = cfg.runCost
	// Record the cause captured right after the loop (see `timedOut` above),
	// not ctx.Err() here — by now cleanup/drain may have crossed our deadline.
	a.lastRunTimedOut = timedOut
	a.mu.Unlock()

	return msgs, err
}

// TimedOut reports whether the most recent Run/Send ended because the run's own
// MaxRunDuration deadline tripped. False for a user abort, a provider error, or
// a normal completion. Derived from the run context, not the returned error.
func (a *Agent) TimedOut() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastRunTimedOut
}

// RunCost returns the USD cost accumulated by the most recent Run/Send. It is a
// faithful measure of real spend whenever the model has pricing — including
// usage from empty or failed turns that never became an assistant message —
// regardless of whether a MaxBudget cap was active.
func (a *Agent) RunCost() float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastRunCost
}
