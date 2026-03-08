package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/ealeixandre/moa/pkg/compaction"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/tool"
)

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
// 1. Fire before_agent_start hooks
// 2. For each turn:
//   a. Fire context hooks
//   b. Convert messages to LLM format
//   c. Stream from provider
//   d. Consume events, build assistant message
//   e. Extract and execute tool calls
//   f. If no tool calls → done
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
		if len(toolCalls) == 0 {
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

		inTurn = false
		emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventTurnEnd})
	}

	// agent_end emitted by defer
	return nil
}

// consumeStream reads events from the provider channel, builds the assistant message,
// and emits AgentEvents for each streaming event.
func consumeStream(ctx context.Context, ch <-chan core.AssistantEvent, emitter *Emitter) (*core.Message, error) {
	var finalMsg *core.Message

	for {
		select {
		case <-ctx.Done():
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
	tc       core.Content
	approved bool
	reject   string      // rejection reason (empty if approved)
	result   core.Result // populated after execution
	isError  bool
}

// executeTools runs tool calls concurrently using a three-phase approach:
//
//  1. Pre-flight (sequential): guardrails, extension hooks, validation.
//     Rejected calls are handled immediately via rejectToolCall.
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
			slots[i].reject = "Tool call skipped: max tool calls per turn exceeded"
			continue
		}
		// Permission check (may block waiting for user approval)
		if cfg.permissionCheck != nil {
			if decision := cfg.permissionCheck(ctx, tc.ToolName, tc.Arguments); decision != nil && decision.Block {
				slots[i].reject = "Permission denied: " + decision.Reason
				continue
			}
		}
		if decision := cfg.hooks.FireToolCall(ctx, tc.ToolName, tc.Arguments); decision != nil && decision.Block {
			slots[i].reject = "Tool call blocked: " + decision.Reason
			continue
		}
		if err := tool.ValidateToolCall(cfg.tools, tc.ToolName, tc.Arguments); err != nil {
			slots[i].reject = "Parameter validation error: " + err.Error()
			continue
		}
		slots[i].approved = true
	}

	// Emit start events for approved calls.
	for i := range slots {
		if !slots[i].approved {
			continue
		}
		cfg.emitter.Emit(core.AgentEvent{
			Type:       core.AgentEventToolExecStart,
			ToolCallID: slots[i].tc.ToolCallID,
			ToolName:   slots[i].tc.ToolName,
			Args:       slots[i].tc.Arguments,
		})
	}

	// Phase 2: execute approved calls concurrently.
	var wg sync.WaitGroup
	for i := range slots {
		if !slots[i].approved {
			continue
		}
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			slots[idx].result, slots[idx].isError = runTool(ctx, cfg, slots[idx].tc)
		}(i)
	}
	wg.Wait()

	// Phase 3: collect results in original order.
	// Rejected calls emit start+end and append error results here (not in
	// pre-flight) to preserve the original tool call ordering in Messages.
	for i := range slots {
		if !slots[i].approved {
			rejectToolCall(cfg, slots[i].tc, slots[i].reject)
			continue
		}

		result := cfg.hooks.FireToolResult(ctx, slots[i].tc.ToolName, slots[i].result, slots[i].isError)
		isError := result.IsError

		cfg.emitter.Emit(core.AgentEvent{
			Type:       core.AgentEventToolExecEnd,
			ToolCallID: slots[i].tc.ToolCallID,
			ToolName:   slots[i].tc.ToolName,
			Result:     &result,
			IsError:    isError,
		})
		cfg.state.Messages = append(cfg.state.Messages, toolResultMessage(slots[i].tc, result, isError))
	}
}

// runTool calls a tool's Execute function and streams partial results.
// No lifecycle events — the caller controls event ordering.
func runTool(ctx context.Context, cfg *loopConfig, tc core.Content) (core.Result, bool) {
	t, ok := cfg.tools.Get(tc.ToolName)
	if !ok {
		return core.ErrorResult(fmt.Sprintf("unknown tool: %s", tc.ToolName)), true
	}
	if t.Execute == nil {
		return core.ErrorResult(fmt.Sprintf("tool %s has no execute function", tc.ToolName)), true
	}

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

// rejectToolCall emits the start/end events and appends an error result
// for a tool call that was rejected (skipped, blocked, or failed validation).
func rejectToolCall(cfg *loopConfig, tc core.Content, reason string) {
	cfg.emitter.Emit(core.AgentEvent{
		Type:       core.AgentEventToolExecStart,
		ToolCallID: tc.ToolCallID,
		ToolName:   tc.ToolName,
		Args:       tc.Arguments,
	})
	result := core.ErrorResult(reason)
	cfg.state.Messages = append(cfg.state.Messages, core.WrapMessage(
		core.NewToolResultMessage(tc.ToolCallID, tc.ToolName, result.Content, true),
	))
	cfg.emitter.Emit(core.AgentEvent{
		Type:       core.AgentEventToolExecEnd,
		ToolCallID: tc.ToolCallID,
		ToolName:   tc.ToolName,
		IsError:    true,
		Result:     &result,
	})
}

// toolResultMessage creates an AgentMessage wrapping a tool result.
func toolResultMessage(tc core.Content, result core.Result, isError bool) core.AgentMessage {
	return core.WrapMessage(core.NewToolResultMessage(
		tc.ToolCallID, tc.ToolName,
		result.Content, isError,
	))
}

