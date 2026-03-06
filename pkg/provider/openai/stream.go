package openai

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

// chunk is the SSE JSON payload from OpenAI streaming.
type chunk struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int    `json:"index"`
		FinishReason string `json:"finish_reason"`
		Delta        struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// streamState tracks the evolving message across SSE chunks.
type streamState struct {
	message   core.Message
	started   bool
	toolCalls map[int]*toolCallState // index → accumulator
}

type toolCallState struct {
	id       string
	name     string
	argsJSON strings.Builder
}

// consumeStream parses SSE lines from OpenAI and emits normalized events.
// Guarantees exactly one terminal event before returning.
func consumeStream(ctx context.Context, body io.Reader, ch chan<- core.AssistantEvent) {
	state := &streamState{
		message: core.Message{
			Role:      "assistant",
			Provider:  "openai",
			Timestamp: time.Now().Unix(),
		},
		toolCalls: make(map[int]*toolCallState),
	}
	sentTerminal := false

	defer func() {
		if !sentTerminal {
			ch <- core.AssistantEvent{
				Type:  core.ProviderEventError,
				Error: fmt.Errorf("stream ended without terminal event"),
			}
		}
	}()

	reader := bufio.NewReaderSize(body, 1024*1024)

	for {
		if ctx.Err() != nil {
			ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: ctx.Err()}
			sentTerminal = true
			return
		}

		line, err := readLine(reader)
		if err != nil {
			if err == io.EOF {
				break
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: fmt.Errorf("read: %w", err)}
			sentTerminal = true
			return
		}

		// Skip empty lines and comments.
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := line[6:]

		if data == "[DONE]" {
			// Finalize: build complete tool calls into message content.
			finalizeTool(state)
			final := state.message
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &final}
			sentTerminal = true
			return
		}

		var c chunk
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			continue // skip malformed chunks
		}

		if c.Model != "" {
			state.message.Model = c.Model
		}

		// Usage (only in last chunk with stream_options.include_usage).
		if c.Usage != nil {
			state.message.Usage = &core.Usage{
				Input:       c.Usage.PromptTokens,
				Output:      c.Usage.CompletionTokens,
				TotalTokens: c.Usage.TotalTokens,
			}
		}

		if len(c.Choices) == 0 {
			continue
		}

		choice := c.Choices[0]
		delta := choice.Delta

		// First chunk: emit start.
		if !state.started {
			state.started = true
			partial := state.message
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &partial}
		}

		// Text delta.
		if delta.Content != "" {
			state.message.Content = appendOrUpdateText(state.message.Content, delta.Content)
			ch <- core.AssistantEvent{
				Type:  core.ProviderEventTextDelta,
				Delta: delta.Content,
			}
		}

		// Tool call deltas.
		for _, tc := range delta.ToolCalls {
			tcs, exists := state.toolCalls[tc.Index]
			if !exists {
				tcs = &toolCallState{}
				state.toolCalls[tc.Index] = tcs
			}
			if tc.ID != "" {
				tcs.id = tc.ID
			}
			if tc.Function.Name != "" {
				tcs.name = tc.Function.Name
				// Emit tool call start.
				ch <- core.AssistantEvent{
					Type:         core.ProviderEventToolCallStart,
					ContentIndex: tc.Index,
				}
			}
			if tc.Function.Arguments != "" {
				tcs.argsJSON.WriteString(tc.Function.Arguments)
				ch <- core.AssistantEvent{
					Type:         core.ProviderEventToolCallDelta,
					ContentIndex: tc.Index,
					Delta:        tc.Function.Arguments,
				}
			}
		}

		// Finish reason.
		if choice.FinishReason != "" {
			state.message.StopReason = choice.FinishReason
		}
	}
}

// finalizeTool converts accumulated tool call state into message content blocks.
func finalizeTool(state *streamState) {
	for i := 0; i < len(state.toolCalls); i++ {
		tc, ok := state.toolCalls[i]
		if !ok {
			continue
		}
		var args map[string]any
		if s := tc.argsJSON.String(); s != "" {
			json.Unmarshal([]byte(s), &args)
		}
		state.message.Content = append(state.message.Content,
			core.ToolCallContent(tc.id, tc.name, args),
		)
	}
}

// appendOrUpdateText appends text to the last text content block, or creates one.
func appendOrUpdateText(blocks []core.Content, text string) []core.Content {
	for i := len(blocks) - 1; i >= 0; i-- {
		if blocks[i].Type == "text" {
			blocks[i].Text += text
			return blocks
		}
	}
	return append(blocks, core.TextContent(text))
}

// readLine reads a single line handling long lines.
func readLine(r *bufio.Reader) (string, error) {
	var sb strings.Builder
	for {
		segment, isPrefix, err := r.ReadLine()
		if err != nil {
			return "", err
		}
		sb.Write(segment)
		if !isPrefix {
			return sb.String(), nil
		}
	}
}
