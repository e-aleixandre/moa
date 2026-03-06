package agent

import (
	"context"
	"fmt"

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

// noopHooks is the default when no extensions are loaded.
type noopHooks struct{}

func (noopHooks) FireBeforeAgentStart(context.Context) []core.AgentMessage      { return nil }
func (noopHooks) FireToolCall(context.Context, string, map[string]any) *core.ToolCallDecision {
	return nil
}
func (noopHooks) FireToolResult(_ context.Context, _ string, r core.Result, _ bool) core.Result {
	return r
}
func (noopHooks) FireContext(_ context.Context, msgs []core.AgentMessage) []core.AgentMessage {
	return msgs
}
func (noopHooks) FireObserver(core.AgentEvent) {}

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
}

// emitLifecycle emits a lifecycle event to both the emitter (subscribers)
// and extension observers. Uses the same event value for both.
func emitLifecycle(cfg *loopConfig, evt core.AgentEvent) {
	cfg.emitter.Emit(evt)
	cfg.hooks.FireObserver(evt)
}

// agentLoop is the core loop. NO steering/follow-up in V0.
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

		// Guardrail: max turns
		turnCount++
		if cfg.maxTurns > 0 && turnCount > cfg.maxTurns {
			loopErr = fmt.Errorf("max turns exceeded (%d)", cfg.maxTurns)
			return loopErr
		}

		// Fire context hooks (can modify message list for this turn)
		messages := cfg.hooks.FireContext(ctx, cfg.state.Messages)

		// Convert to LLM messages (filter custom messages)
		llmMessages := defaultConvertToLLM(messages)
		if cfg.convertToLLM != nil {
			llmMessages = cfg.convertToLLM(messages)
		}

		inTurn = true
		emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventTurnStart})

		// Build request
		req := core.Request{
			Model:    cfg.model,
			System:   cfg.systemPrompt,
			Messages: llmMessages,
			Tools:    cfg.tools.Specs(),
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

		cfg.state.Messages = append(cfg.state.Messages, core.WrapMessage(*assistantMsg))

		// Extract tool calls from assistant message
		toolCalls := extractToolCalls(assistantMsg)
		if len(toolCalls) == 0 {
			inTurn = false
			emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventTurnEnd})
			break // No tools → done
		}

		// Execute tool calls — respect MaxToolCallsPerTurn limit
		maxCalls := cfg.maxToolCallsPerTurn
		for i, tc := range toolCalls {
			if ctx.Err() != nil {
				loopErr = ctx.Err()
				return loopErr
			}

			// Guardrail: skip if over limit
			if maxCalls > 0 && i >= maxCalls {
				rejectToolCall(cfg, tc, "Tool call skipped: max tool calls per turn exceeded")
				continue
			}

			// Extension hook: can block
			if decision := cfg.hooks.FireToolCall(ctx, tc.ToolName, tc.Arguments); decision != nil && decision.Block {
				rejectToolCall(cfg, tc, "Tool call blocked: "+decision.Reason)
				continue
			}

			// Validate parameters
			if err := tool.ValidateToolCall(cfg.tools, tc.ToolName, tc.Arguments); err != nil {
				rejectToolCall(cfg, tc, "Parameter validation error: "+err.Error())
				continue
			}

			// Execute
			result, isError := executeTool(ctx, cfg.tools, tc, cfg.emitter)

			// Extension hook: can modify result (including error status)
			result = cfg.hooks.FireToolResult(ctx, tc.ToolName, result, isError)
			isError = result.IsError

			cfg.state.Messages = append(cfg.state.Messages, toolResultMessage(tc, result, isError))
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
func defaultConvertToLLM(msgs []core.AgentMessage) []core.Message {
	var result []core.Message
	for _, m := range msgs {
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

// executeTool runs a tool and returns the result.
func executeTool(ctx context.Context, registry *core.Registry, tc core.Content, emitter *Emitter) (core.Result, bool) {
	t, ok := registry.Get(tc.ToolName)
	if !ok {
		return core.ErrorResult(fmt.Sprintf("unknown tool: %s", tc.ToolName)), true
	}
	if t.Execute == nil {
		return core.ErrorResult(fmt.Sprintf("tool %s has no execute function", tc.ToolName)), true
	}

	emitter.Emit(core.AgentEvent{
		Type:       core.AgentEventToolExecStart,
		ToolCallID: tc.ToolCallID,
		ToolName:   tc.ToolName,
		Args:       tc.Arguments,
	})

	onUpdate := func(partial core.Result) {
		emitter.Emit(core.AgentEvent{
			Type:       core.AgentEventToolExecUpdate,
			ToolCallID: tc.ToolCallID,
			ToolName:   tc.ToolName,
			Result:     &partial,
		})
	}

	result, err := t.Execute(ctx, tc.Arguments, onUpdate)
	isError := err != nil || result.IsError
	if err != nil {
		result = core.ErrorResult(err.Error())
	}

	emitter.Emit(core.AgentEvent{
		Type:       core.AgentEventToolExecEnd,
		ToolCallID: tc.ToolCallID,
		ToolName:   tc.ToolName,
		Result:     &result,
		IsError:    isError,
	})

	return result, isError
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

