package agent

import (
	"context"
	"fmt"

	"github.com/ealeixandre/go-agent/pkg/core"
	"github.com/ealeixandre/go-agent/pkg/extension"
	"github.com/ealeixandre/go-agent/pkg/tool"
)

// loopConfig holds all dependencies for the agent loop.
type loopConfig struct {
	provider     core.Provider
	tools        *core.Registry
	extensions   *extension.Host
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
	if cfg.extensions != nil {
		cfg.extensions.FireObserver(evt)
	}
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
	if cfg.extensions != nil {
		injected := cfg.extensions.FireBeforeAgentStart(ctx)
		cfg.state.Messages = append(cfg.state.Messages, injected...)
	}

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
		messages := cfg.state.Messages
		if cfg.extensions != nil {
			messages = cfg.extensions.FireContext(ctx, messages)
		}

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
				cfg.emitter.Emit(core.AgentEvent{
					Type:       core.AgentEventToolExecStart,
					ToolCallID: tc.ToolCallID,
					ToolName:   tc.ToolName,
					Args:       tc.Arguments,
				})
				skippedResult := core.NewToolResultMessage(
					tc.ToolCallID, tc.ToolName,
					[]core.Content{core.TextContent("Tool call skipped: max tool calls per turn exceeded")},
					true,
				)
				cfg.state.Messages = append(cfg.state.Messages, core.WrapMessage(skippedResult))
				cfg.emitter.Emit(core.AgentEvent{
					Type:       core.AgentEventToolExecEnd,
					ToolCallID: tc.ToolCallID,
					ToolName:   tc.ToolName,
					IsError:    true,
					Result:     &core.Result{Content: skippedResult.Content, IsError: true},
				})
				continue
			}

			// Extension hook: can block
			if cfg.extensions != nil {
				if decision := cfg.extensions.FireToolCall(ctx, tc.ToolName, tc.Arguments); decision != nil && decision.Block {
					cfg.emitter.Emit(core.AgentEvent{
						Type:       core.AgentEventToolExecStart,
						ToolCallID: tc.ToolCallID,
						ToolName:   tc.ToolName,
						Args:       tc.Arguments,
					})
					cfg.state.Messages = append(cfg.state.Messages, blockedToolResult(tc, decision.Reason))
					cfg.emitter.Emit(core.AgentEvent{
						Type:       core.AgentEventToolExecEnd,
						ToolCallID: tc.ToolCallID,
						ToolName:   tc.ToolName,
						IsError:    true,
						Result:     &core.Result{Content: []core.Content{core.TextContent("Blocked: " + decision.Reason)}, IsError: true},
					})
					continue
				}
			}

			// Validate parameters
			if err := tool.ValidateToolCall(cfg.tools, tc.ToolName, tc.Arguments); err != nil {
				cfg.emitter.Emit(core.AgentEvent{
					Type:       core.AgentEventToolExecStart,
					ToolCallID: tc.ToolCallID,
					ToolName:   tc.ToolName,
					Args:       tc.Arguments,
				})
				errMsg := errorToolResult(tc, err.Error())
				cfg.state.Messages = append(cfg.state.Messages, errMsg)
				cfg.emitter.Emit(core.AgentEvent{
					Type:       core.AgentEventToolExecEnd,
					ToolCallID: tc.ToolCallID,
					ToolName:   tc.ToolName,
					IsError:    true,
					Result:     &core.Result{Content: errMsg.Content, IsError: true},
				})
				continue
			}

			// Execute
			result, isError := executeTool(ctx, cfg.tools, tc, cfg.emitter)

			// Extension hook: can modify result
			if cfg.extensions != nil {
				result = cfg.extensions.FireToolResult(ctx, tc.ToolName, result, isError)
			}

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

// blockedToolResult creates an AgentMessage for a blocked tool call.
func blockedToolResult(tc core.Content, reason string) core.AgentMessage {
	return core.WrapMessage(core.NewToolResultMessage(
		tc.ToolCallID, tc.ToolName,
		[]core.Content{core.TextContent("Tool call blocked: " + reason)},
		true,
	))
}

// errorToolResult creates an AgentMessage for a tool validation error.
func errorToolResult(tc core.Content, errMsg string) core.AgentMessage {
	return core.WrapMessage(core.NewToolResultMessage(
		tc.ToolCallID, tc.ToolName,
		[]core.Content{core.TextContent("Parameter validation error: " + errMsg)},
		true,
	))
}

// toolResultMessage creates an AgentMessage wrapping a tool result.
func toolResultMessage(tc core.Content, result core.Result, isError bool) core.AgentMessage {
	return core.WrapMessage(core.NewToolResultMessage(
		tc.ToolCallID, tc.ToolName,
		result.Content, isError,
	))
}

