package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ealeixandre/moa/pkg/compaction"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/permission"
	"github.com/ealeixandre/moa/pkg/tool"
)

// doomLoopThreshold is the number of consecutive identical tool-call sets
// that triggers a forced stop. Prevents infinite loops burning tokens.
const doomLoopThreshold = 3

// doomLoopExemptTools are read-only status/wait tools that legitimately repeat
// while a model waits on background async work. Polling them (or blocking on a
// wait) must not trip the doom-loop detector, which would abort an otherwise
// healthy long-running task. Turns whose tool calls are *entirely* exempt are
// transparent to the detector: they neither increment nor reset the streak, so
// a genuine edit/status/edit loop is still caught across intervening polls.
var doomLoopExemptTools = map[string]bool{
	"bash_status":     true,
	"subagent_status": true,
	"bash_wait":       true,
	"subagent_wait":   true,
}

// maxPauseTurnResubmits caps consecutive pause_turn continuations. Anthropic
// pauses a long-running turn (stop_reason "pause_turn") and expects the client
// to resubmit the conversation as-is to let the model continue. We do that
// automatically (per the API's documented guidance), but cap it so a provider
// that keeps pausing without finishing can't spin forever burning tokens.
const maxPauseTurnResubmits = 5

// maxEmptyContinuations caps consecutive OpenAI end_turn:false continuations
// that make no progress. The backend can complete a response with
// end_turn:false ("not done, resend as-is to continue"); we resubmit like
// pause_turn, but cap consecutive no-progress continuations so a stuck backend
// can't loop forever.
const maxEmptyContinuations = 5

// maxEmptyRetries caps how many times a single stall point re-samples the same
// request after an empty (no text, no tool call) completion with no continue
// signal. One retry absorbs a transient empty turn (common while polling) without
// adding any message to the history; a second consecutive empty surfaces the
// error. Reset whenever a stream succeeds, so it caps *consecutive* empties.
const maxEmptyRetries = 1

// maxTruncationRetries caps resubmits after a response exhausts its output
// limit before yielding visible text or a complete tool call.
const maxTruncationRetries = 1

// Hooks is the interface the agent loop needs from the extension system.
// Defined here (consumer-side) so the loop doesn't depend on extension internals.
type Hooks interface {
	FireBeforeAgentStart(ctx context.Context) []core.AgentMessage
	FireToolCall(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision
	FireToolResult(ctx context.Context, name string, result core.Result, isError bool) core.Result
	FireContext(ctx context.Context, msgs []core.AgentMessage) []core.AgentMessage
	FireObserver(event core.AgentEvent)
}

// appendState safely appends messages to the shared *state. The loop is the
// only writer of *state during a run, but external readers (Agent.Messages,
// Agent.CompactionEpoch) hold stateMu, so every write must take it too. The
// critical section stays tiny and never calls back into the agent — holding
// stateMu across a callback would risk deadlock.
func (cfg *loopConfig) appendState(msgs ...core.AgentMessage) {
	cfg.stateMu.Lock()
	for i := range msgs {
		msgs[i].EnsureMsgID()
	}
	cfg.state.Messages = append(cfg.state.Messages, msgs...)
	cfg.stateMu.Unlock()
}

// loopConfig holds all dependencies for the agent loop.
type loopConfig struct {
	provider     core.Provider
	tools        *core.Registry
	hooks        Hooks
	emitter      *Emitter
	state        *AgentState
	stateMu      *sync.Mutex // guards writes to *state (shared with Agent.mu)
	model        core.Model
	systemPrompt string
	streamOpts   core.StreamOptions

	// Guardrails
	maxTurns            int
	maxToolCallsPerTurn int
	maxBudget           float64
	runCost             float64 // accumulated USD cost this run

	// Custom conversion (nil = default)
	convertToLLM func([]core.AgentMessage) []core.Message

	// Permission check (nil = all approved)
	permissionCheck func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision

	// Compaction
	compaction *core.CompactionSettings

	// Steering messages injected between steps
	steerCh <-chan string
}

// emitLifecycle emits a lifecycle event to both the emitter (subscribers)
// and extension observers. Uses the same event value for both.
func emitLifecycle(cfg *loopConfig, evt core.AgentEvent) {
	cfg.emitter.Emit(evt)
	cfg.hooks.FireObserver(evt)
}

// drainSteer non-blocking drains all pending steer messages.
func drainSteer(ch <-chan string) []string {
	if ch == nil {
		return nil
	}
	var msgs []string
	for {
		select {
		case msg := <-ch:
			msgs = append(msgs, msg)
		default:
			return msgs
		}
	}
}

// agentLoop is the core loop.
//
//  1. Fire before_agent_start hooks
//  2. For each turn:
//     a. Fire context hooks
//     b. Convert messages to LLM format
//     c. Stream from provider
//     d. Consume events, build assistant message
//     e. Extract and execute tool calls
//     f. If no tool calls → done
//
// Lifecycle guarantee: agent_start is always followed by agent_end.
// If an error occurs, agent_error is emitted before agent_end.
// Turns are always bracketed: turn_start is always followed by turn_end.
func agentLoop(ctx context.Context, cfg *loopConfig) error {
	emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventStart})

	// Fire before_agent_start hooks (can inject messages)
	injected := cfg.hooks.FireBeforeAgentStart(ctx)
	cfg.appendState(injected...)

	turnCount := 0
	inTurn := false // track open turn for cleanup

	// Doom loop detection: track consecutive identical tool-call sets.
	var lastToolSig string
	repeatCount := 0

	// pause_turn tracking: count consecutive pause_turn continuations, and note
	// when the previous iteration was a pause so we can skip compaction on the
	// resubmit (compacting between a pause and its resubmit would replace the
	// paused message and lose the continuation).
	pauseResubmits := 0
	justPaused := false

	// OpenAI end_turn:false continuations (StopReason "continue"): count
	// consecutive no-progress continuations, and re-sample once on a typed empty
	// response with no continue signal.
	emptyContinuations := 0
	emptyRetries := 0
	truncationRetries := 0

	var loopErr error
	defer func() {
		// Close open turn if needed
		if inTurn {
			emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventTurnEnd})
		}
		// Emit error if any
		if loopErr != nil {
			emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventError, Error: loopErr})
		}
		// Always emit agent_end
		emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventEnd, Messages: cfg.state.Messages})
	}()

	for {
		// Check context
		if ctx.Err() != nil {
			loopErr = ctx.Err()
			return loopErr
		}

		// Budget pre-check: catches overage added by compaction in the previous iteration
		// before we make another provider call.
		if cfg.maxBudget > 0 && cfg.runCost > cfg.maxBudget {
			loopErr = &BudgetExceededError{Spent: cfg.runCost, Limit: cfg.maxBudget}
			return loopErr
		}

		// Cache tool specs once per iteration (avoids repeated sort+allocate).
		toolSpecs := cfg.tools.Specs()

		// === COMPACTION CHECK ===
		// Invariant: runs once per iteration, before provider call, after
		// prior turn is fully committed to cfg.state.Messages.
		// Skipped on a pause_turn resubmit: the continuation must resend the
		// paused conversation as-is, and compacting it away here would drop the
		// message the model is waiting to continue.
		if !justPaused && cfg.compaction != nil && cfg.compaction.Enabled && cfg.model.MaxInput > 0 {
			estimate := core.EstimateContextTokens(
				cfg.state.Messages, cfg.systemPrompt, toolSpecs, cfg.state.CompactionEpoch,
			)
			window := cfg.compaction.EffectiveWindow(cfg.model.MaxInput)
			if core.ShouldCompact(estimate.Tokens, window, *cfg.compaction) {
				emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventCompactionStart})

				result, compacted, err := compaction.Compact(
					ctx, cfg.provider, cfg.model, cfg.streamOpts,
					cfg.state.Messages, estimate.Tokens, window, *cfg.compaction,
				)
				if err != nil {
					// Non-fatal: log and continue with full context.
					emitLifecycle(cfg, core.AgentEvent{
						Type: core.AgentEventCompactionEnd, Error: err,
					})
				} else if result != nil {
					for i := range compacted {
						compacted[i].EnsureMsgID()
					}
					cfg.stateMu.Lock()
					cfg.state.Messages = compacted
					cfg.state.CompactionEpoch++
					cfg.stateMu.Unlock()
					// Account for compaction LLM call cost.
					if result.Usage != nil && cfg.maxBudget > 0 {
						cfg.runCost += cfg.model.Pricing.Cost(*result.Usage)
					}
					emitLifecycle(cfg, core.AgentEvent{
						Type: core.AgentEventCompactionEnd,
						Compaction: &core.CompactionPayload{
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
						},
					})
				} else {
					// No cut point found — nothing to compact. Still close the lifecycle.
					emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventCompactionEnd})
				}
			}
		}
		// Compaction itself is a provider call and may have consumed the
		// remaining budget. Check again before issuing the normal turn request.
		if cfg.maxBudget > 0 && cfg.runCost > cfg.maxBudget {
			loopErr = &BudgetExceededError{Spent: cfg.runCost, Limit: cfg.maxBudget}
			return loopErr
		}

		// Guardrail: max turns
		turnCount++
		if cfg.maxTurns > 0 && turnCount > cfg.maxTurns {
			loopErr = fmt.Errorf("max turns exceeded (%d)", cfg.maxTurns)
			return loopErr
		}

		// Fire context hooks (can modify message list for this turn)
		messages := cfg.hooks.FireContext(ctx, cfg.state.Messages)

		// Convert to LLM messages (filter custom messages)
		var llmMessages []core.Message
		if cfg.convertToLLM != nil {
			llmMessages = cfg.convertToLLM(messages)
		} else {
			llmMessages = defaultConvertToLLM(messages)
		}

		inTurn = true
		emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventTurnStart})

		// Build request
		req := core.Request{
			Model:    cfg.model,
			System:   cfg.systemPrompt,
			Messages: llmMessages,
			Tools:    toolSpecs,
			Options:  cfg.streamOpts,
		}

		// Stream from provider
		ch, err := cfg.provider.Stream(ctx, req)
		if err != nil {
			loopErr = fmt.Errorf("provider: %w", err)
			return loopErr
		}

		// Consume stream, build assistant message, emit events
		assistantMsg, err := consumeStream(ctx, ch, cfg.emitter)
		if err != nil {
			// A typed empty-response error (completed with no text/tool call and
			// no continue signal) is often transient during polling. Re-sample
			// the SAME request once — nothing is appended to the history, so the
			// next iteration rebuilds the identical request — before surfacing
			// it. Not a fabricated "continue": no message is injected.
			var emptyErr *core.EmptyResponseError
			if errors.As(err, &emptyErr) {
				// An empty response can still bill input tokens; account for it
				// so retries (and the eventual error) can't slip past the budget.
				if cfg.maxBudget > 0 && emptyErr.Usage != nil {
					cfg.runCost += cfg.model.Pricing.Cost(*emptyErr.Usage)
				}
				if ctx.Err() == nil && emptyRetries < maxEmptyRetries {
					emptyRetries++
					inTurn = false
					emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventTurnEnd})
					continue
				}
			}
			// On cancellation, save partial content so it persists in session.
			if assistantMsg != nil && ctx.Err() != nil {
				cfg.appendState(core.WrapMessage(*assistantMsg))
			}
			loopErr = fmt.Errorf("stream: %w", err)
			return loopErr
		}
		// A stream succeeded: reset the consecutive-empty counter so the retry
		// budget applies per stall point, not per run.
		emptyRetries = 0

		// Stamp assistant message with current compaction epoch for usage tracking.
		wrapped := core.WrapMessage(*assistantMsg)
		if cfg.state.CompactionEpoch > 0 {
			if wrapped.Custom == nil {
				wrapped.Custom = make(map[string]any)
			}
			wrapped.Custom["compaction_epoch"] = cfg.state.CompactionEpoch
		}
		cfg.appendState(wrapped)
		// MessageEnd is a state-observable boundary: reconnect snapshots that
		// include this lifecycle event must also include its stable MsgID.
		emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventMessageEnd, Message: wrapped})

		// === STOP-REASON HANDLING (Anthropic pause_turn / refusal; OpenAI continue) ===
		// Runs after the message is committed and MessageEnd emitted, so any
		// partial text/thinking is already persisted and visible in both
		// frontends before we act on the stop reason.
		justPaused = false
		switch assistantMsg.StopReason {
		case "max_tokens":
			// A capped response can contain partial tool arguments, so never
			// execute tools from it. When no user-visible output was produced,
			// resubmit the persisted response once; this lets providers continue
			// from their signed reasoning state without injecting fake user text.
			toolCalls := extractToolCalls(assistantMsg)
			if cfg.maxBudget > 0 && assistantMsg.Usage != nil {
				cfg.runCost += cfg.model.Pricing.Cost(*assistantMsg.Usage)
				if cfg.runCost > cfg.maxBudget {
					loopErr = &BudgetExceededError{Spent: cfg.runCost, Limit: cfg.maxBudget}
					inTurn = false
					emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventTurnEnd})
					return loopErr
				}
			}
			if len(toolCalls) == 0 && !hasSubstantiveText(assistantMsg) && truncationRetries < maxTruncationRetries {
				truncationRetries++
				justPaused = true
				inTurn = false
				emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventTurnEnd})
				continue
			}
			loopErr = fmt.Errorf("model output truncated after reaching max tokens")
			inTurn = false
			emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventTurnEnd})
			return loopErr

		case "pause_turn":
			// The provider paused a long-running turn and expects us to resend
			// the conversation as-is to continue. The assistant message is
			// already in cfg.state.Messages, so the next iteration replays it
			// verbatim — a natural resubmit. We do NOT drain steering here: the
			// continuation must go back unchanged; queued steers wait for the
			// next tool cycle or become follow-ups.
			pauseResubmits++
			if pauseResubmits >= maxPauseTurnResubmits {
				loopErr = fmt.Errorf("model paused %d consecutive times (pause_turn) without finishing the turn", pauseResubmits)
				inTurn = false
				emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventTurnEnd})
				return loopErr
			}
			// Account for the paused response's cost; the pre-check at the top
			// of the next iteration enforces the budget before resubmitting.
			if cfg.maxBudget > 0 && assistantMsg.Usage != nil {
				cfg.runCost += cfg.model.Pricing.Cost(*assistantMsg.Usage)
			}
			justPaused = true
			inTurn = false
			emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventTurnEnd})
			continue // resubmit the paused conversation as-is

		case "continue":
			// OpenAI Responses completed with end_turn:false — the backend says
			// the turn is not over and wants the conversation resent as-is to
			// let the model keep going (codex turn.rs:2298→418). The assistant
			// message (its reasoning/text, if any) is already persisted, so the
			// next iteration replays it verbatim. Reset the streak on real
			// progress (substantive text this turn); otherwise count it so a
			// backend that keeps saying "not done" without output can't loop
			// forever. Like pause_turn: no steering drain, skip compaction on the
			// resubmit so the message the model wants to continue isn't dropped.
			if hasSubstantiveText(assistantMsg) {
				emptyContinuations = 0
			} else {
				emptyContinuations++
			}
			if emptyContinuations >= maxEmptyContinuations {
				loopErr = fmt.Errorf("model requested continuation %d consecutive times without progress", emptyContinuations)
				inTurn = false
				emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventTurnEnd})
				return loopErr
			}
			if cfg.maxBudget > 0 && assistantMsg.Usage != nil {
				cfg.runCost += cfg.model.Pricing.Cost(*assistantMsg.Usage)
			}
			justPaused = true
			inTurn = false
			emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventTurnEnd})
			continue // resubmit the conversation as-is to continue the turn

		case "refusal", "sensitive":
			// The model declined (policy refusal) or was cut by safety filters.
			// Surface a visible error with the provider's explanation instead of
			// ending the turn in silence. The refusal's partial text is already
			// persisted/shown via the MessageEnd above.
			reason := assistantMsg.ErrorMessage
			if reason == "" {
				reason = "the model refused to complete the request"
			}
			if assistantMsg.StopReason == "sensitive" {
				reason = "content flagged by safety filters: " + reason
			}
			loopErr = fmt.Errorf("model stopped (%s): %s", assistantMsg.StopReason, reason)
			inTurn = false
			emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventTurnEnd})
			return loopErr
		}
		// Any other stop reason: a real turn boundary — reset the streaks.
		pauseResubmits = 0
		emptyContinuations = 0
		truncationRetries = 0

		// Extract tool calls from assistant message
		toolCalls := extractToolCalls(assistantMsg)

		// Doom loop detection: if tool calls are identical to the previous
		// turn N times in a row, stop to prevent infinite token burn. Turns
		// consisting only of exempt status/wait tools (polling background async
		// work) are skipped entirely so they neither trip nor reset the streak.
		if nonExempt := filterDoomLoopCalls(toolCalls); len(nonExempt) > 0 {
			sig := toolCallSignature(nonExempt)
			if sig == lastToolSig {
				repeatCount++
			} else {
				lastToolSig = sig
				repeatCount = 1
			}
			if repeatCount >= doomLoopThreshold {
				// Log the repeated tool calls for debugging
				var callNames []string
				for _, tc := range nonExempt {
					callNames = append(callNames, fmt.Sprintf("%s(%v)", tc.ToolName, tc.Arguments))
				}
				loopErr = fmt.Errorf("doom loop detected: identical tool calls repeated %d times in a row: %v", repeatCount, callNames)

				// Inject tool_result messages for every pending tool_call so
				// the conversation stays valid (Anthropic requires a tool_result
				// immediately after every tool_use).
				injectErrorToolResults(cfg, toolCalls, loopErr.Error())
				return loopErr
			}
		}

		if len(toolCalls) == 0 {
			// Accumulate cost and check budget even on the final message so
			// callers know when a run blew through the limit.
			if cfg.maxBudget > 0 && assistantMsg.Usage != nil {
				cfg.runCost += cfg.model.Pricing.Cost(*assistantMsg.Usage)
				if cfg.runCost > cfg.maxBudget {
					loopErr = &BudgetExceededError{Spent: cfg.runCost, Limit: cfg.maxBudget}
					inTurn = false
					emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventTurnEnd})
					return loopErr
				}
			}
			inTurn = false
			emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventTurnEnd})
			break // No tools → done
		}

		// Execute tool calls concurrently.
		executeTools(ctx, cfg, toolCalls)

		// Inject steering messages between steps.
		if steered := drainSteer(cfg.steerCh); len(steered) > 0 {
			for _, msg := range steered {
				cfg.appendState(core.WrapMessage(core.NewUserMessage(msg)))
				emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventSteer, Text: msg})
			}
		}

		// Budget check — after tool execution so conversation state has matching
		// tool_result messages for every tool_call (no dangling calls).
		if cfg.maxBudget > 0 && assistantMsg.Usage != nil {
			cfg.runCost += cfg.model.Pricing.Cost(*assistantMsg.Usage)
			if cfg.runCost > cfg.maxBudget {
				loopErr = &BudgetExceededError{Spent: cfg.runCost, Limit: cfg.maxBudget}
				return loopErr
			}
		}

		inTurn = false
		emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventTurnEnd})
	}

	// agent_end emitted by defer
	return nil
}

// consumeStream reads events from the provider channel, builds the assistant message,
// and emits AgentEvents for each streaming event.
//
// Semantics: message_end means the provider finished emitting this assistant
// message. It does not mean the turn has ended (tool execution may still follow).
func consumeStream(ctx context.Context, ch <-chan core.AssistantEvent, emitter *Emitter) (*core.Message, error) {
	var finalMsg *core.Message

	// Accumulate partial content so we can return it on cancellation.
	var partialText string
	var partialThinking string

	for {
		select {
		case <-ctx.Done():
			// Drain any remaining buffered events from the channel.
			// Use a short timeout — the provider goroutine may not close
			// the channel promptly after context cancellation.
			drainTimer := time.NewTimer(100 * time.Millisecond)
			defer drainTimer.Stop()
		drain:
			for {
				select {
				case event, ok := <-ch:
					if !ok {
						break drain
					}
					switch event.Type {
					case core.ProviderEventTextDelta:
						partialText += event.Delta
					case core.ProviderEventThinkingDelta:
						partialThinking += event.Delta
					case core.ProviderEventDone:
						if event.Message != nil {
							// Capture the complete message but keep draining; do not
							// early-return. finalMsg is preferred below over the
							// truncated partial.
							finalMsg = event.Message
						}
					}
				case <-drainTimer.C:
					break drain
				}
			}
			// A cancelled turn always returns ctx.Err() so the caller stops and
			// does NOT execute tool calls. Prefer the complete message if one
			// was received — either via the normal path before cancellation or
			// during the drain above; the top-level select is a race, so relying
			// on which branch consumed the final Done would be non-deterministic.
			if finalMsg != nil {
				return finalMsg, ctx.Err()
			}
			// Otherwise fall back to a partial message from accumulated deltas.
			if partialText != "" || partialThinking != "" {
				partial := &core.Message{Role: "assistant"}
				if partialThinking != "" {
					partial.Content = append(partial.Content, core.Content{
						Type:     "thinking",
						Thinking: partialThinking,
					})
				}
				if partialText != "" {
					partial.Content = append(partial.Content, core.TextContent(partialText))
				}
				return partial, ctx.Err()
			}
			return nil, ctx.Err()
		case event, ok := <-ch:
			if !ok {
				// Channel closed
				if finalMsg == nil {
					return nil, fmt.Errorf("stream ended without final message")
				}
				return finalMsg, nil
			}

			// Emit as message_update
			emitter.Emit(core.AgentEvent{
				Type:           core.AgentEventMessageUpdate,
				AssistantEvent: &event,
			})

			switch event.Type {
			case core.ProviderEventStart:
				if event.Partial != nil {
					emitter.Emit(core.AgentEvent{
						Type:    core.AgentEventMessageStart,
						Message: core.WrapMessage(*event.Partial),
					})
				}
			case core.ProviderEventTextDelta:
				partialText += event.Delta
			case core.ProviderEventThinkingDelta:
				partialThinking += event.Delta
			case core.ProviderEventDone:
				finalMsg = event.Message
			case core.ProviderEventError:
				if event.Error != nil {
					return nil, event.Error
				}
				return nil, fmt.Errorf("provider stream error")
			}
		}
	}
}

// defaultConvertToLLM filters AgentMessages to LLM-compatible Messages.
// Converts compaction_summary to a user message with a wrapper.
func defaultConvertToLLM(msgs []core.AgentMessage) []core.Message {
	var result []core.Message
	for _, m := range msgs {
		if m.Role == "compaction_summary" {
			text := ""
			for _, c := range m.Content {
				if c.Type == "text" {
					text += c.Text
				}
			}
			result = append(result, core.Message{
				Role: "user",
				Content: []core.Content{core.TextContent(
					"The conversation history before this point was compacted into the following summary:\n\n<summary>\n" + text + "\n</summary>",
				)},
				Timestamp: m.Timestamp,
			})
			continue
		}
		if m.IsLLMMessage() {
			result = append(result, m.Message)
		}
	}
	return result
}

// extractToolCalls extracts tool_call content blocks from an assistant message.
func extractToolCalls(msg *core.Message) []core.Content {
	var calls []core.Content
	for _, c := range msg.Content {
		if c.Type == "tool_call" {
			calls = append(calls, c)
		}
	}
	return calls
}

// hasSubstantiveText reports whether an assistant message carries visible text
// output (non-blank). Used to tell a "continue" turn that made real progress
// from an empty/reasoning-only continuation, so a no-progress streak can be
// capped. Tool calls never reach this path (they end the turn as tool_use).
func hasSubstantiveText(msg *core.Message) bool {
	for _, c := range msg.Content {
		if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
			return true
		}
	}
	return false
}

// toolExecSlot holds the state for one tool call during parallel execution.
type toolExecSlot struct {
	tc                 core.Content
	approved           bool
	startEmitted       bool
	rejectReason       string      // rejection reason (empty if approved)
	rejectKind         string      // "permission" or "other"
	permissionFeedback string      // optional approved feedback note from permission prompt
	result             core.Result // populated after execution
	isError            bool
}

const (
	rejectKindPermission = "permission"
	rejectKindOther      = "other"
)

// executeTools runs tool calls concurrently using a three-phase approach:
//
//  1. Pre-flight (sequential): guardrails, permission checks, extension hooks,
//     and validation. Tool start is emitted per-call right before permission check.
//  2. Execute (concurrent): approved calls run in parallel goroutines.
//     Each writes to its own slot — no shared mutable state.
//  3. Collect (sequential, in original order): run FireToolResult hooks,
//     emit tool_execution_end, append tool_result messages.
//
// Result messages are always appended in the same order as tool calls,
// regardless of execution completion order.
func executeTools(ctx context.Context, cfg *loopConfig, toolCalls []core.Content) {
	slots := make([]toolExecSlot, len(toolCalls))

	// Phase 1: pre-flight (sequential).
	maxCalls := cfg.maxToolCallsPerTurn
	for i, tc := range toolCalls {
		slots[i].tc = tc

		if maxCalls > 0 && i >= maxCalls {
			slots[i].rejectReason = "Tool call skipped: max tool calls per turn exceeded"
			slots[i].rejectKind = rejectKindOther
			continue
		}

		// Emit start right before permission evaluation so the UI can show
		// what is being requested before the prompt appears.
		cfg.emitter.Emit(core.AgentEvent{
			Type:       core.AgentEventToolExecStart,
			ToolCallID: tc.ToolCallID,
			ToolName:   tc.ToolName,
			Args:       tc.Arguments,
		})
		slots[i].startEmitted = true
		// Best effort: flush start to subscribers before we might block on
		// permission checks, so the UI sees the tool call first.
		if cfg.permissionCheck != nil {
			cfg.emitter.Drain(250 * time.Millisecond)
		}

		// Permission check (may block waiting for user approval).
		if cfg.permissionCheck != nil {
			if decision := cfg.permissionCheck(ctx, tc.ToolName, tc.Arguments); decision != nil && decision.Block {
				kind := decision.Kind
				if kind == "" {
					kind = core.ToolCallDecisionKindPermission
				}
				if kind == core.ToolCallDecisionKindPermission {
					slots[i].rejectReason = "Permission denied: " + decision.Reason
					slots[i].rejectKind = rejectKindPermission
				} else {
					slots[i].rejectReason = "Tool call blocked: " + decision.Reason
					slots[i].rejectKind = rejectKindOther
				}
				continue
			}
		}
		slots[i].permissionFeedback = permission.PopApprovedFeedback(tc.Arguments)
		if decision := cfg.hooks.FireToolCall(ctx, tc.ToolName, tc.Arguments); decision != nil && decision.Block {
			slots[i].rejectReason = "Tool call blocked: " + decision.Reason
			slots[i].rejectKind = rejectKindOther
			continue
		}
		if err := tool.ValidateToolCall(cfg.tools, tc.ToolName, tc.Arguments); err != nil {
			slots[i].rejectReason = "Parameter validation error: " + err.Error()
			slots[i].rejectKind = rejectKindOther
			continue
		}
		slots[i].approved = true
	}

	// Phase 2: execute with conflict-aware scheduling.
	//
	// ReadOnly tools run in parallel with everything. WritePath tools
	// sharing the same lock key run sequentially (preserving original order),
	// but different keys run in parallel. Shell/Unknown tools act as barriers:
	// they wait for all prior non-read calls before executing.
	var allDone sync.WaitGroup

	pathDone := map[string]<-chan struct{}{} // per-path: signals when prior writer finishes
	var lastShell <-chan struct{}            // last shell completion (nil initially)

	for i := range slots {
		if !slots[i].approved {
			continue
		}
		t, _ := cfg.tools.Get(slots[i].tc.ToolName)
		effect := t.Effect

		// WritePath with failed LockKey → treat as shell.
		var lockKey string
		if effect == core.EffectWritePath {
			if t.LockKey != nil {
				lockKey = t.LockKey(slots[i].tc.Arguments)
			}
			if lockKey == "" {
				effect = core.EffectShell
			}
		}

		switch effect {
		case core.EffectReadOnly:
			// A read targeting a specific path (LockKey set) must not run
			// concurrently with a writer or shell touching that same path, or it
			// could observe a half-written file. It chains on the path like a
			// writer; path-less reads still run fully in parallel.
			var rKey string
			if t.LockKey != nil {
				rKey = t.LockKey(slots[i].tc.Arguments)
			}
			if rKey == "" {
				allDone.Add(1)
				go func(idx int) {
					defer allDone.Done()
					slots[idx].result, slots[idx].isError = runTool(ctx, cfg, slots[idx].tc)
				}(i)
				break
			}
			done := make(chan struct{})
			waitForPath := pathDone[rKey]
			waitForShell := lastShell
			pathDone[rKey] = done
			allDone.Add(1)
			go func(idx int, wPath, wShell <-chan struct{}) {
				defer allDone.Done()
				defer close(done)
				if wPath != nil {
					<-wPath
				}
				if wShell != nil {
					<-wShell
				}
				slots[idx].result, slots[idx].isError = runTool(ctx, cfg, slots[idx].tc)
			}(i, waitForPath, waitForShell)

		case core.EffectWritePath:
			done := make(chan struct{})
			waitForPath := pathDone[lockKey] // nil if first writer to this path
			waitForShell := lastShell        // wait for most recent shell barrier
			pathDone[lockKey] = done

			allDone.Add(1)
			go func(idx int, wPath, wShell <-chan struct{}) {
				defer allDone.Done()
				defer close(done)
				if wPath != nil {
					<-wPath
				}
				if wShell != nil {
					<-wShell
				}
				slots[idx].result, slots[idx].isError = runTool(ctx, cfg, slots[idx].tc)
			}(i, waitForPath, waitForShell)

		default: // EffectShell, EffectUnknown, EffectInteractive
			done := make(chan struct{})
			allDone.Add(1)
			// Wait for all pending path writers + previous shell.
			waits := make([]<-chan struct{}, 0, len(pathDone)+1)
			for _, ch := range pathDone {
				waits = append(waits, ch)
			}
			if lastShell != nil {
				waits = append(waits, lastShell)
			}
			go func(idx int, waits []<-chan struct{}) {
				defer allDone.Done()
				defer close(done)
				for _, w := range waits {
					<-w
				}
				slots[idx].result, slots[idx].isError = runTool(ctx, cfg, slots[idx].tc)
			}(i, waits)
			// Shell becomes the new barrier; reset path tracking.
			lastShell = done
			pathDone = map[string]<-chan struct{}{}
		}
	}

	allDone.Wait()

	// Phase 3: collect results in original order.
	for i := range slots {
		if !slots[i].approved {
			rejectToolCall(cfg, slots[i])
			continue
		}

		resultWithFeedback := appendPermissionFeedback(slots[i].result, slots[i].permissionFeedback)
		result := cfg.hooks.FireToolResult(ctx, slots[i].tc.ToolName, resultWithFeedback, slots[i].isError)
		isError := result.IsError

		cfg.appendState(toolResultMessage(slots[i].tc, result, isError, false))
		cfg.emitter.Emit(core.AgentEvent{
			Type:       core.AgentEventToolExecEnd,
			ToolCallID: slots[i].tc.ToolCallID,
			ToolName:   slots[i].tc.ToolName,
			Result:     &result,
			IsError:    isError,
			Rejected:   false,
		})
	}
}

func appendPermissionFeedback(result core.Result, feedback string) core.Result {
	if feedback == "" {
		return result
	}
	feedback = "Permission feedback: " + feedback
	for i := range result.Content {
		if result.Content[i].Type == "text" {
			text := result.Content[i].Text
			if text == "" {
				result.Content[i].Text = feedback
			} else {
				result.Content[i].Text = text + "\n\n" + feedback
			}
			return result
		}
	}
	result.Content = append(result.Content, core.TextContent(feedback))
	return result
}

// runTool calls a tool's Execute function and streams partial results.
// No lifecycle events — the caller controls event ordering.
// Panics in Execute are recovered and returned as error results.
func runTool(ctx context.Context, cfg *loopConfig, tc core.Content) (result core.Result, isError bool) {
	t, ok := cfg.tools.Get(tc.ToolName)
	if !ok {
		return core.ErrorResult(fmt.Sprintf("unknown tool: %s", tc.ToolName)), true
	}
	if t.Execute == nil {
		return core.ErrorResult(fmt.Sprintf("tool %s has no execute function", tc.ToolName)), true
	}

	defer func() {
		if r := recover(); r != nil {
			cfg.emitter.logger.Error("tool panic recovered", "tool", tc.ToolName, "error", r)
			result = core.ErrorResult(fmt.Sprintf("tool %s panicked: %v", tc.ToolName, r))
			isError = true
		}
	}()

	onUpdate := func(partial core.Result) {
		cfg.emitter.Emit(core.AgentEvent{
			Type:       core.AgentEventToolExecUpdate,
			ToolCallID: tc.ToolCallID,
			ToolName:   tc.ToolName,
			Result:     &partial,
		})
	}

	result, err := t.Execute(ctx, tc.Arguments, onUpdate)
	if err != nil {
		return core.ErrorResult(err.Error()), true
	}
	return result, result.IsError
}

// rejectToolCall emits tool lifecycle end state and appends an error result
// for a tool call that was rejected (skipped, blocked, permission denied,
// or failed validation).
func rejectToolCall(cfg *loopConfig, slot toolExecSlot) {
	if !slot.startEmitted {
		cfg.emitter.Emit(core.AgentEvent{
			Type:       core.AgentEventToolExecStart,
			ToolCallID: slot.tc.ToolCallID,
			ToolName:   slot.tc.ToolName,
			Args:       slot.tc.Arguments,
		})
	}
	rejected := slot.rejectKind == rejectKindPermission
	reason := slot.rejectReason
	if reason == "" {
		reason = "Tool call rejected"
	}
	result := core.ErrorResult(reason)
	cfg.appendState(toolResultMessage(slot.tc, result, true, rejected))
	cfg.emitter.Emit(core.AgentEvent{
		Type:       core.AgentEventToolExecEnd,
		ToolCallID: slot.tc.ToolCallID,
		ToolName:   slot.tc.ToolName,
		IsError:    true,
		Rejected:   rejected,
		Result:     &result,
	})
}

// injectErrorToolResults appends a tool_result message for each pending tool_call
// so the conversation stays valid when the run is aborted (e.g. doom loop).
// Without this, providers like Anthropic reject the session on resume because
// every tool_use must have a matching tool_result.
func injectErrorToolResults(cfg *loopConfig, toolCalls []core.Content, errMsg string) {
	for _, tc := range toolCalls {
		content := []core.Content{core.TextContent(errMsg)}
		msg := core.WrapMessage(core.NewToolResultMessage(
			tc.ToolCallID, tc.ToolName, content, true,
		))
		cfg.appendState(msg)
	}
}

// filterDoomLoopCalls drops exempt status/wait tool calls from a turn's tool
// set for doom-loop accounting. Polling background async work (or blocking on a
// wait) is legitimate repetition and must not trip the detector.
func filterDoomLoopCalls(calls []core.Content) []core.Content {
	var out []core.Content
	for _, c := range calls {
		if doomLoopExemptTools[c.ToolName] {
			continue
		}
		out = append(out, c)
	}
	return out
}

// toolCallSignature produces a stable hash of a set of tool calls (name + args)
// for doom loop detection. Order-sensitive — same calls in same order = same sig.
func toolCallSignature(calls []core.Content) string {
	h := sha256.New()
	for _, c := range calls {
		h.Write([]byte(c.ToolName))
		h.Write([]byte{0})
		// Marshal args deterministically enough for repeat detection.
		if b, err := json.Marshal(c.Arguments); err == nil {
			h.Write(b)
		}
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// toolResultMessage creates an AgentMessage wrapping a tool result.
func toolResultMessage(tc core.Content, result core.Result, isError bool, rejected bool) core.AgentMessage {
	msg := core.WrapMessage(core.NewToolResultMessage(
		tc.ToolCallID, tc.ToolName,
		result.Content, isError,
	))
	if rejected {
		if msg.Custom == nil {
			msg.Custom = make(map[string]any)
		}
		msg.Custom["rejected"] = true
	}
	return msg
}
