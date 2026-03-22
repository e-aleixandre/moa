package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
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

// Hooks is the interface the agent loop needs from the extension system.
// Defined here (consumer-side) so the loop doesn't depend on extension internals.
type Hooks interface {
	FireBeforeAgentStart(ctx context.Context) []core.AgentMessage
	FireToolCall(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision
	FireToolResult(ctx context.Context, name string, result core.Result, isError bool) core.Result
	FireContext(ctx context.Context, msgs []core.AgentMessage) []core.AgentMessage
	FireObserver(event core.AgentEvent)
}

// loopConfig holds all dependencies for the agent loop.
type loopConfig struct {
	provider     core.Provider
	tools        *core.Registry
	hooks        Hooks
	emitter      *Emitter
	state        *AgentState
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
	cfg.state.Messages = append(cfg.state.Messages, injected...)

	turnCount := 0
	inTurn := false // track open turn for cleanup

	// Doom loop detection: track consecutive identical tool-call sets.
	var lastToolSig string
	repeatCount := 0

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
		if cfg.compaction != nil && cfg.compaction.Enabled && cfg.model.MaxInput > 0 {
			estimate := core.EstimateContextTokens(
				cfg.state.Messages, cfg.systemPrompt, toolSpecs, cfg.state.CompactionEpoch,
			)
			if core.ShouldCompact(estimate.Tokens, cfg.model.MaxInput, *cfg.compaction) {
				emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventCompactionStart})

				result, compacted, err := compaction.Compact(
					ctx, cfg.provider, cfg.model, cfg.streamOpts,
					cfg.state.Messages, estimate.Tokens, cfg.model.MaxInput, *cfg.compaction,
				)
				if err != nil {
					// Non-fatal: log and continue with full context.
					emitLifecycle(cfg, core.AgentEvent{
						Type: core.AgentEventCompactionEnd, Error: err,
					})
				} else if result != nil {
					cfg.state.Messages = compacted
					cfg.state.CompactionEpoch++
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
						},
					})
				} else {
					// No cut point found — nothing to compact. Still close the lifecycle.
					emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventCompactionEnd})
				}
			}
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
			// On cancellation, save partial content so it persists in session.
			if assistantMsg != nil && ctx.Err() != nil {
				cfg.state.Messages = append(cfg.state.Messages, core.WrapMessage(*assistantMsg))
			}
			loopErr = fmt.Errorf("stream: %w", err)
			return loopErr
		}

		// Stamp assistant message with current compaction epoch for usage tracking.
		wrapped := core.WrapMessage(*assistantMsg)
		if cfg.state.CompactionEpoch > 0 {
			if wrapped.Custom == nil {
				wrapped.Custom = make(map[string]any)
			}
			wrapped.Custom["compaction_epoch"] = cfg.state.CompactionEpoch
		}
		cfg.state.Messages = append(cfg.state.Messages, wrapped)

		// Extract tool calls from assistant message
		toolCalls := extractToolCalls(assistantMsg)

		// Doom loop detection: if tool calls are identical to the previous
		// turn N times in a row, stop to prevent infinite token burn.
		if len(toolCalls) > 0 {
			sig := toolCallSignature(toolCalls)
			if sig == lastToolSig {
				repeatCount++
			} else {
				lastToolSig = sig
				repeatCount = 1
			}
			if repeatCount >= doomLoopThreshold {
				// Log the repeated tool calls for debugging
				var callNames []string
				for _, tc := range toolCalls {
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
				cfg.state.Messages = append(cfg.state.Messages,
					core.WrapMessage(core.NewUserMessage(msg)))
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
							return event.Message, nil
						}
					}
				case <-drainTimer.C:
					break drain
				}
			}
			// Build a partial message from accumulated deltas.
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
				if finalMsg != nil {
					emitter.Emit(core.AgentEvent{
						Type:    core.AgentEventMessageEnd,
						Message: core.WrapMessage(*finalMsg),
					})
				}
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
			allDone.Add(1)
			go func(idx int) {
				defer allDone.Done()
				slots[idx].result, slots[idx].isError = runTool(ctx, cfg, slots[idx].tc)
			}(i)

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

		default: // EffectShell, EffectUnknown
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

		cfg.emitter.Emit(core.AgentEvent{
			Type:       core.AgentEventToolExecEnd,
			ToolCallID: slots[i].tc.ToolCallID,
			ToolName:   slots[i].tc.ToolName,
			Result:     &result,
			IsError:    isError,
			Rejected:   false,
		})
		cfg.state.Messages = append(cfg.state.Messages, toolResultMessage(slots[i].tc, result, isError, false))
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
	cfg.state.Messages = append(cfg.state.Messages, toolResultMessage(slot.tc, result, true, rejected))
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
		cfg.state.Messages = append(cfg.state.Messages, msg)
	}
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
