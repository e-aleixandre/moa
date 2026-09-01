package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/e-aleixandre/moa/pkg/compaction"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/permission"
	"github.com/e-aleixandre/moa/pkg/sessioncheckpoint"
	"github.com/e-aleixandre/moa/pkg/tool"
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

var steerInterruptibleWaitTools = map[string]bool{
	"bash_wait":     true,
	"subagent_wait": true,
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
	provider            core.Provider
	tools               *core.Registry
	hooks               Hooks
	emitter             *Emitter
	state               *AgentState
	stateMu             *sync.Mutex // guards writes to *state (shared with Agent.mu)
	model               core.Model
	systemPrompt        string
	streamOpts          core.StreamOptions
	streamRepairBackoff []time.Duration
	// fast is read per provider request because a provider can disable the
	// session setting while this run is still processing tool calls.
	fast func() bool

	// Guardrails
	maxTurns            int
	maxToolCallsPerTurn int
	maxBudget           float64
	runCost             float64 // accumulated USD cost this run

	// Custom conversion (nil = default)
	convertToLLM func([]core.AgentMessage) []core.Message
	// Materialize attachment descriptors for a provider request (nil = no-op).
	materializeContent func(context.Context, []core.Message) ([]core.Message, error)

	// Permission check (nil = all approved)
	permissionCheck func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision

	// Compaction
	// compaction is read through a function, not captured once: the global
	// threshold can change mid-run (Settings applies to every conversation, open
	// or not), and a long run is exactly when that matters. Returns nil when
	// compaction is disabled.
	compaction func() *core.CompactionSettings
	// readCheckpoint returns the ephemeral session checkpoint to append to an
	// automatic compaction summary, and a callback to clear it once consumed.
	// Nil when no checkpoint slot is wired.
	readCheckpoint func() (string, func())
	// compactStrategy is what the agent gets before an automatic compaction:
	// core.CompactPlain, CompactNotify or CompactPrepare. Read per iteration so
	// a settings change reaches a run already in flight.
	compactStrategy func() string
	// checkpointSlot is the ephemeral checkpoint an automatic preparation turn
	// writes to. Nil when no slot is wired, which disables prepare.
	checkpointSlot *sessioncheckpoint.Slot

	// Steering messages injected between steps
	drainSteers func() []core.SteerItem
	// settleSteers settles a drained batch's inflight native-content bytes once
	// the batch's messages are appended to history (paired with drainSteers).
	settleSteers func([]core.SteerItem)
	// registerSteerWait makes one interruptible wait tool wake when a user steer
	// arrives. It returns the cleanup that removes the tool's cancellation hook.
	registerSteerWait func(context.CancelCauseFunc) func()
	// steerMu makes cancellation and the post-tool delivery boundary atomic.
	steerMu *sync.Mutex
}

func (cfg *loopConfig) requestOptions() core.StreamOptions {
	opts := cfg.streamOpts
	if cfg.fast != nil {
		opts.Fast = cfg.fast()
	}
	return opts
}

// emitLifecycle emits a lifecycle event to both the emitter (subscribers)
// and extension observers. Uses the same event value for both.
func emitLifecycle(cfg *loopConfig, evt core.AgentEvent) {
	cfg.emitter.Emit(evt)
	cfg.hooks.FireObserver(evt)
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
	// Which compaction epoch has already had its preparation turn.
	preparedEpoch := -1

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
		// Read the settings fresh on every iteration: a global threshold change
		// must reach a run already in flight.
		var compactionSettings *core.CompactionSettings
		if cfg.compaction != nil {
			compactionSettings = cfg.compaction()
		}
		if !justPaused && compactionSettings != nil && compactionSettings.Enabled && cfg.model.MaxInput > 0 {
			estimate := core.EstimateContextTokens(
				cfg.state.Messages, cfg.systemPrompt, toolSpecs, cfg.state.CompactionEpoch,
			)
			window := compactionSettings.EffectiveWindow(cfg.model.MaxInput)

			// Warn before the threshold, not at it: an automatic compaction
			// arrives mid-task and replaces with a summary whatever the agent
			// worked out but never wrote down. Warning once, a few turns
			// earlier, is what gives it the chance to persist it.
			//
			// The notice states what to do, not just the number: told only how
			// many tokens remained, the model carried on as if nothing had
			// changed in every measured run.
			if strategyAllowsNotice(cfg) && !alreadyWarned(cfg.state.Messages) {
				if warn, remaining := core.ShouldWarnBeforeCompact(estimate.Tokens, window, *compactionSettings); warn {
					// appendState, not a bare append: it takes stateMu and
					// assigns the MsgID the tree needs to sync this message
					// under a stable identity.
					notice := compactionNotice(remaining)
					notice.EnsureMsgID()
					cfg.appendState(notice)
					// Announce it: without an event the notice only shows up on
					// the next reload, and a client watching live would see the
					// agent react to something that was not there.
					emitLifecycle(cfg, core.AgentEvent{
						Type:    core.AgentEventUserMessage,
						Message: notice,
						MsgID:   notice.MsgID,
					})
				}
			}

			if core.ShouldCompact(estimate.Tokens, window, *compactionSettings) {
				emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventCompactionStart})

				// Give the agent a turn to write down what only exists in this
				// conversation, before the summary replaces it. Once per
				// compaction epoch: a run that compacts twice prepares twice,
				// but a single compaction never prepares in a loop.
				if strategyIsPrepare(cfg) && preparedEpoch != cfg.state.CompactionEpoch+1 {
					preparedEpoch = cfg.state.CompactionEpoch + 1
					if err := runAutoPrepare(ctx, cfg, cfg.checkpointSlot); err != nil {
						// Non-fatal: losing the preparation is bad, losing the
						// compaction with it would be worse — the context is
						// already over the threshold.
						slog.Warn("auto prepare-compact failed; compacting without it", "error", err)
					}
				}

				// Same one-shot prefix as the manual path: the summarizer's own
				// system prompt over a flattened transcript, which nothing else
				// shares. Inheriting the session's cache TTL here would bill a
				// cache write — at 2x under the 1h TTL — for an entry no later
				// request can read. Routing (PromptCacheKey) is preserved.
				compactOpts := cfg.requestOptions()
				compactOpts.CacheRetention = core.CacheOff

				result, compacted, err := compaction.Compact(
					ctx, cfg.provider, cfg.model, compactOpts,
					cfg.state.Messages, estimate.Tokens, window, *compactionSettings, "",
				)
				if err != nil {
					// Non-fatal: log and continue with full context.
					emitLifecycle(cfg, core.AgentEvent{
						Type: core.AgentEventCompactionEnd, Error: err,
					})
				} else if result != nil {
					// Consume the ephemeral checkpoint here too. The manual
					// path already appended it; without this, an automatic
					// compaction dropped it on the floor.
					var consumeCheckpoint func()
					if cfg.readCheckpoint != nil {
						text, consume := cfg.readCheckpoint()
						compaction.AppendCheckpoint(result, compacted, text)
						consumeCheckpoint = consume
					}
					for i := range compacted {
						compacted[i].EnsureMsgID()
					}
					cfg.stateMu.Lock()
					cfg.state.Messages = compacted
					cfg.state.CompactionEpoch++
					cfg.stateMu.Unlock()
					if consumeCheckpoint != nil {
						consumeCheckpoint()
					}
					// Account for compaction LLM call cost.
					addRunCost(cfg, result.Usage)
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
			loopErr = fmt.Errorf("%w (%d)", ErrMaxTurnsExceeded, cfg.maxTurns)
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
		if cfg.materializeContent != nil {
			materialized, err := cfg.materializeContent(ctx, llmMessages)
			if err != nil {
				loopErr = fmt.Errorf("materialize content: %w", err)
				return loopErr
			}
			llmMessages = materialized
		}

		// Last barrier before the provider: an image/document block that still
		// carries only an AttachmentID has no bytes behind it. AttachmentID is
		// a moa-internal identifier no provider can resolve, so letting it
		// through means the model silently sees nothing at all. Fail loudly
		// instead. The very same block is legitimate in the in-memory history,
		// on disk, in UI snapshots and before materialization — it is only
		// illegal in a request bound for the provider, which is why the check
		// lives here and not in the state.
		if err := checkResolvedAttachments(llmMessages); err != nil {
			loopErr = err
			return loopErr
		}

		// Build request
		baseMessages := llmMessages
		var repairPartial *core.Message
		var assistantMsg *core.Message
		var streamErr error
		emptyRetry := false
		for attempt := 0; ; attempt++ {
			reqMessages := baseMessages
			if repairPartial != nil {
				reqMessages = append(append([]core.Message{}, baseMessages...), *repairPartial, streamContinueHint())
			}
			req := core.Request{
				Model:    cfg.model,
				System:   cfg.systemPrompt,
				Messages: reqMessages,
				Tools:    toolSpecs,
				Options:  cfg.requestOptions(),
			}

			ch, err := cfg.provider.Stream(ctx, req)
			if err != nil {
				loopErr = fmt.Errorf("provider: %w", err)
				return loopErr
			}

			var consumeErr error
			assistantMsg, consumeErr = consumeStream(ctx, ch, cfg.emitter)
			if consumeErr == nil {
				if repairPartial != nil {
					assistantMsg = mergeAssistant(repairPartial, assistantMsg)
				}
				streamErr = nil
				break
			}

			hasPartial := assistantMsg != nil && len(assistantMsg.Content) > 0
			var emptyErr *core.EmptyResponseError
			if errors.As(consumeErr, &emptyErr) {
				addRunCost(cfg, emptyErr.Usage)
				if !hasPartial && ctx.Err() == nil && emptyRetries < maxEmptyRetries {
					emptyRetries++
					emptyRetry = true
					break
				}
			}
			streamErr = fmt.Errorf("stream: %w", consumeErr)
			canRepair := ctx.Err() == nil &&
				!hasStreamedToolCalls(assistantMsg) &&
				isRetryableStreamError(streamErr) &&
				attempt < maxStreamRepairs
			if canRepair {
				if hasPartial {
					repairPartial = mergeAssistant(repairPartial, assistantMsg)
				}
				if waitErr := waitStreamRepair(ctx, attempt+1, cfg.streamRepairBackoff); waitErr != nil {
					streamErr = fmt.Errorf("stream: %w", waitErr)
					break
				}
				continue
			}
			break
		}
		if emptyRetry {
			inTurn = false
			emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventTurnEnd})
			continue
		}
		if streamErr != nil {
			hasPartial := assistantMsg != nil && len(assistantMsg.Content) > 0
			if repairPartial != nil {
				assistantMsg = mergeAssistant(repairPartial, assistantMsg)
				hasPartial = assistantMsg != nil && len(assistantMsg.Content) > 0
			}
			if hasPartial {
				assistantMsg.RequestedModel = cfg.model.ID
				toolResultErr := "Tool result unavailable: the run was cancelled before a result was recorded."
				if ctx.Err() == nil {
					assistantMsg.Content = append(assistantMsg.Content, core.TextContent(interruptedMarkerText(ctx, streamErr)))
					toolResultErr = "Tool result unavailable: the provider stream failed before a result was recorded."
				}
				msgs := []core.AgentMessage{core.WrapMessage(*assistantMsg)}
				msgs = append(msgs, errorToolResultMessages(
					extractToolCalls(assistantMsg),
					toolResultErr,
				)...)
				cfg.appendState(msgs...)
			}
			loopErr = streamErr
			return loopErr
		}
		if assistantMsg == nil {
			loopErr = fmt.Errorf("stream: empty assistant message")
			return loopErr
		}
		// Keep the requested identity on this response. A provider can return a
		// safety fallback without changing the session's selected model.
		assistantMsg.RequestedModel = cfg.model.ID
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
			addRunCost(cfg, assistantMsg.Usage)
			if cfg.maxBudget > 0 && assistantMsg.Usage != nil {
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
			if len(toolCalls) > 0 {
				injectErrorToolResults(cfg, toolCalls, "tool call not executed: model output was truncated")
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
			addRunCost(cfg, assistantMsg.Usage)
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
			addRunCost(cfg, assistantMsg.Usage)
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
				loopErr = fmt.Errorf("%w: identical tool calls repeated %d times in a row: %v", ErrDoomLoop, repeatCount, callNames)

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
			addRunCost(cfg, assistantMsg.Usage)
			if cfg.maxBudget > 0 && assistantMsg.Usage != nil {
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
		if cfg.steerMu != nil {
			cfg.steerMu.Lock()
		}
		// A stopped run must not deliver its queued steers in the narrow gap after
		// a cancelled tool returns. The abort cleanup owns discarding them, while
		// the frontend restores them to the composer for an explicit resend.
		if err := ctx.Err(); err != nil {
			if cfg.steerMu != nil {
				cfg.steerMu.Unlock()
			}
			return err
		}

		// Inject steering messages between steps.
		if cfg.drainSteers != nil {
			if steered := cfg.drainSteers(); len(steered) > 0 {
				for _, item := range steered {
					um := core.WrapMessage(steerMessage(item))
					um.Custom = item.Custom
					um.EnsureMsgID()
					cfg.appendState(um)
					// Carry the message's MsgID so serve can publish it on the
					// Steered event; clients dedup the user message by MsgID
					// (the reconnect snapshot may already contain it). Message
					// carries the injected blocks so a steer with attachments
					// renders live with its images, exactly like the prompt
					// announced by AgentEventUserMessage.
					//
					// The blocks are cloned for the event: um is already in the
					// agent's history, and subscribers receive events
					// asynchronously — one that mutates a block (or its
					// Arguments map) would otherwise corrupt the history the
					// next provider request replays.
					announced := um
					announced.Content = core.CloneContent(um.Content)
					emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventSteer, SteerID: item.ID, MsgID: um.MsgID, Text: item.Text, Message: announced})
				}
				// The drained steers are now in history; settle their inflight
				// native-content bytes (paired with drainSteers).
				if cfg.settleSteers != nil {
					cfg.settleSteers(steered)
				}
			}
		}
		if cfg.steerMu != nil {
			cfg.steerMu.Unlock()
		}

		// Budget check — after tool execution so conversation state has matching
		// tool_result messages for every tool_call (no dangling calls).
		addRunCost(cfg, assistantMsg.Usage)
		if cfg.maxBudget > 0 && assistantMsg.Usage != nil {
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

	// Accumulate partial content so failures cannot discard output already shown.
	var partialText strings.Builder
	var partialThinking strings.Builder
	var partialToolCalls []core.Content
	var toolCallIndexes map[string]int

	accumulate := func(event core.AssistantEvent) {
		switch event.Type {
		case core.ProviderEventTextDelta:
			partialText.WriteString(event.Delta)
		case core.ProviderEventThinkingDelta:
			partialThinking.WriteString(event.Delta)
		case core.ProviderEventToolCallStart, core.ProviderEventToolCallDelta:
			if event.ToolCallID == "" {
				return
			}
			if idx, ok := toolCallIndexes[event.ToolCallID]; ok {
				if event.ToolName != "" {
					partialToolCalls[idx].ToolName = event.ToolName
				}
				if event.PartialArgs != nil {
					partialToolCalls[idx].Arguments = event.PartialArgs
				}
				return
			}
			if toolCallIndexes == nil {
				toolCallIndexes = make(map[string]int)
			}
			toolCallIndexes[event.ToolCallID] = len(partialToolCalls)
			partialToolCalls = append(partialToolCalls, core.ToolCallContent(event.ToolCallID, event.ToolName, event.PartialArgs))
		}
	}

	partialMessage := func() *core.Message {
		if finalMsg != nil {
			return finalMsg
		}
		if partialText.Len() == 0 && partialThinking.Len() == 0 && len(partialToolCalls) == 0 {
			return nil
		}
		partial := &core.Message{Role: "assistant", Timestamp: time.Now().Unix()}
		partial.EnsureMsgID()
		if partialThinking.Len() > 0 {
			partial.Content = append(partial.Content, core.Content{
				Type:     "thinking",
				Thinking: partialThinking.String(),
			})
		}
		if partialText.Len() > 0 {
			partial.Content = append(partial.Content, core.TextContent(partialText.String()))
		}
		partial.Content = append(partial.Content, core.CloneContent(partialToolCalls)...)
		return partial
	}

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
					accumulate(event)
					switch event.Type {
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
			return partialMessage(), ctx.Err()
		case event, ok := <-ch:
			if !ok {
				// Channel closed
				if finalMsg == nil {
					return partialMessage(), fmt.Errorf("stream ended without final message")
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
			case core.ProviderEventDone:
				finalMsg = event.Message
			case core.ProviderEventError:
				if event.Error != nil {
					return partialMessage(), event.Error
				}
				return partialMessage(), fmt.Errorf("provider stream error")
			}
			accumulate(event)
		}
	}
}

// ErrUnresolvedAttachment reports a provider-bound request that still carries
// an attachment reference with no bytes. It is a bug in the producing or
// materializing side, never something the user can fix by retrying.
var ErrUnresolvedAttachment = errors.New("unresolved attachment reference in provider request")

// checkResolvedAttachments enforces the invariant at the only place shared by
// every agent: no image/document block may reach the provider with an
// AttachmentID and no Data. Failing here turns a silently invisible image into
// a visible error.
func checkResolvedAttachments(msgs []core.Message) error {
	for _, msg := range msgs {
		for _, content := range msg.Content {
			if content.Type != "image" && content.Type != "document" {
				continue
			}
			if content.AttachmentID != "" && content.Data == "" {
				return fmt.Errorf("%w: %s block %q was not materialized (the model would see nothing)",
					ErrUnresolvedAttachment, content.Type, content.AttachmentID)
			}
		}
	}
	return nil
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

	ctx = core.WithToolCallID(ctx, tc.ToolCallID)
	if steerInterruptibleWaitTools[tc.ToolName] && cfg.registerSteerWait != nil {
		var cancel context.CancelCauseFunc
		ctx, cancel = context.WithCancelCause(ctx)
		defer cfg.registerSteerWait(cancel)()
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
	cfg.appendState(errorToolResultMessages(toolCalls, errMsg)...)
}

func errorToolResultMessages(toolCalls []core.Content, errMsg string) []core.AgentMessage {
	msgs := make([]core.AgentMessage, 0, len(toolCalls))
	for _, tc := range toolCalls {
		content := []core.Content{core.TextContent(errMsg)}
		msg := core.WrapMessage(core.NewToolResultMessage(
			tc.ToolCallID, tc.ToolName, content, true,
		))
		msgs = append(msgs, msg)
	}
	return msgs
}

// addRunCost accumulates the USD cost of a usage record into the run total
// whenever the model has pricing, independent of whether a budget cap is
// active. This keeps Agent.RunCost() a faithful measure of real spend even for
// unlimited-budget runs; budget *enforcement* stays gated on maxBudget > 0 at
// each call site.
func addRunCost(cfg *loopConfig, usage *core.Usage) {
	if usage != nil && cfg.model.Pricing != nil {
		cfg.runCost += cfg.model.Pricing.Cost(*usage)
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
	for key, value := range result.Custom {
		if msg.Custom == nil {
			msg.Custom = make(map[string]any)
		}
		msg.Custom[key] = value
	}
	if rejected {
		if msg.Custom == nil {
			msg.Custom = make(map[string]any)
		}
		msg.Custom["rejected"] = true
	}
	return msg
}

// strategyAllowsNotice reports whether this run should warn the agent before an
// automatic compaction.
//
// Subagents never do: they cannot write memory or the ephemeral checkpoint
// (both are excluded from a child's tool set), so the only thing a warning
// could prompt is stray files in the workspace. A child's findings already have
// a durable home — the report it returns to its parent.
func strategyAllowsNotice(cfg *loopConfig) bool {
	if cfg.compactStrategy == nil {
		return false
	}
	switch cfg.compactStrategy() {
	case core.CompactNotify, core.CompactPrepare:
		return true
	}
	return false
}

// compactionNotice is the message the agent sees as its context fills up.
//
// It rides as a user-role message with an internal marker: providers only
// accept user and assistant roles mid-conversation, and the marker is what
// lets the UI and the transcript tell it apart from something the user typed.
func compactionNotice(remaining int) core.AgentMessage {
	text := fmt.Sprintf(
		"<system-reminder>\nContext is close to the compaction threshold: about %s tokens remain before this conversation is automatically summarized. Everything not written down will be replaced by that summary.\n\n"+
			"If you are holding findings, decisions or partial results that only exist in this conversation, persist them now — a file, memory, or the task list — before continuing. If there is nothing worth keeping, carry on without comment.\n</system-reminder>",
		humanizeTokens(remaining),
	)
	msg := core.WrapMessage(core.NewUserMessage(text))
	msg.Custom = map[string]any{"source": "compaction_notice", "internal": true}
	return msg
}

// humanizeTokens renders a token count the way a person would say it, so the
// model reads a magnitude rather than parsing digits.
func humanizeTokens(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%d", n)
}
