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

// Responses API SSE event types we handle.
const (
	eventOutputItemAdded    = "response.output_item.added"
	eventOutputTextDelta    = "response.output_text.delta"
	eventFuncCallArgsDelta  = "response.function_call_arguments.delta"
	eventFuncCallArgsDone   = "response.function_call_arguments.done"
	eventOutputItemDone     = "response.output_item.done"
	eventCompleted          = "response.completed"
	eventFailed             = "response.failed"
	eventError              = "error"
	// Reasoning summary events (thinking).
	eventReasoningSummaryDelta = "response.reasoning_summary_text.delta"
)

// event is the raw SSE JSON payload from the Responses API.
type event struct {
	Type     string          `json:"type"`
	Item     *item           `json:"item,omitempty"`
	ItemRaw  json.RawMessage `json:"-"` // full JSON of item (set during parsing)
	Delta    string          `json:"delta,omitempty"`
	Response *struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Usage  *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	} `json:"response,omitempty"`
	// For function_call_arguments.done
	Arguments string `json:"arguments,omitempty"`
	// For error events
	Message string `json:"message,omitempty"`
	Code    string `json:"code,omitempty"`
}

type item struct {
	Type      string `json:"type"` // "message", "function_call", "reasoning"
	ID        string `json:"id,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Status    string `json:"status,omitempty"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content,omitempty"`
	Summary []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary,omitempty"`
}

// streamState tracks the evolving message across SSE events.
type streamState struct {
	message core.Message
	started bool

	// Current function call being streamed.
	currentCallID   string
	currentCallName string
	currentArgsJSON strings.Builder
	contentIndex    int
}

// consumeStream parses Responses API SSE and emits normalized AssistantEvents.
func consumeStream(ctx context.Context, body io.Reader, ch chan<- core.AssistantEvent) {
	state := &streamState{
		message: core.Message{
			Role:      "assistant",
			Provider:  "openai",
			Timestamp: time.Now().Unix(),
		},
		contentIndex: -1,
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

		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		// Some Responses API events use "event:" lines — we only need "data:" lines.
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := line[6:]
		if data == "[DONE]" {
			break
		}

		var ev event
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		// For output_item.done events, preserve the raw JSON of the item so we
		// can store it verbatim as ThinkingSignature (avoids losing unknown fields).
		if ev.Type == eventOutputItemDone {
			var raw struct {
				Item json.RawMessage `json:"item"`
			}
			if json.Unmarshal([]byte(data), &raw) == nil {
				ev.ItemRaw = raw.Item
			}
		}

		terminal := processEvent(state, &ev, ch)
		if terminal {
			sentTerminal = true
			return
		}
	}

	// If we reach here without a terminal event (unlikely for well-behaved servers),
	// emit done with what we have.
	if !sentTerminal {
		final := state.message
		ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &final}
		sentTerminal = true
	}
}

// processEvent handles a single SSE event. Returns true if it emitted a terminal event.
func processEvent(state *streamState, ev *event, ch chan<- core.AssistantEvent) bool {
	switch ev.Type {
	case eventOutputItemAdded:
		if ev.Item == nil {
			return false
		}
		if !state.started {
			state.started = true
			partial := state.message
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &partial}
		}
		switch ev.Item.Type {
		case "function_call":
			state.contentIndex++
			state.currentCallID = ev.Item.CallID
			state.currentCallName = ev.Item.Name
			state.currentArgsJSON.Reset()
			if ev.Item.Arguments != "" {
				state.currentArgsJSON.WriteString(ev.Item.Arguments)
			}
			ch <- core.AssistantEvent{
				Type:         core.ProviderEventToolCallStart,
				ContentIndex: state.contentIndex,
			}
		case "message":
			state.contentIndex++
		case "reasoning":
			state.contentIndex++
		}

	case eventOutputTextDelta:
		if !state.started {
			state.started = true
			partial := state.message
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &partial}
		}
		if ev.Delta != "" {
			state.message.Content = appendOrUpdateText(state.message.Content, ev.Delta)
			ch <- core.AssistantEvent{
				Type:  core.ProviderEventTextDelta,
				Delta: ev.Delta,
			}
		}

	case eventReasoningSummaryDelta:
		if ev.Delta != "" {
			ch <- core.AssistantEvent{
				Type:  core.ProviderEventThinkingDelta,
				Delta: ev.Delta,
			}
		}

	case eventFuncCallArgsDelta:
		if ev.Delta != "" {
			state.currentArgsJSON.WriteString(ev.Delta)
			ch <- core.AssistantEvent{
				Type:         core.ProviderEventToolCallDelta,
				ContentIndex: state.contentIndex,
				Delta:        ev.Delta,
			}
		}

	case eventFuncCallArgsDone:
		argsStr := ev.Arguments
		if argsStr == "" {
			argsStr = state.currentArgsJSON.String()
		}
		var args map[string]any
		if argsStr != "" {
			json.Unmarshal([]byte(argsStr), &args)
		}
		state.message.Content = append(state.message.Content,
			core.ToolCallContent(state.currentCallID, state.currentCallName, args),
		)
		ch <- core.AssistantEvent{
			Type:         core.ProviderEventToolCallEnd,
			ContentIndex: state.contentIndex,
		}

	case eventOutputItemDone:
		if ev.Item == nil {
			return false
		}
		switch ev.Item.Type {
		case "message":
			// Reconcile final text from the done event.
			var text string
			for _, c := range ev.Item.Content {
				if c.Type == "output_text" {
					text += c.Text
				}
			}
			if text != "" {
				// Overwrite accumulated text with final authoritative version.
				replaceText(&state.message, text)
			}
		case "reasoning":
			// Store the raw item JSON as ThinkingSignature so it can be sent
			// back verbatim in future requests (preserves encrypted_content,
			// summary[].type, and any other fields the API requires).
			signature := string(ev.ItemRaw)
			if signature == "" {
				// Fallback: re-marshal the parsed item (lossy but better than nothing).
				if raw, err := json.Marshal(ev.Item); err == nil {
					signature = string(raw)
				}
			}
			var thinkingText string
			for _, s := range ev.Item.Summary {
				if thinkingText != "" {
					thinkingText += "\n\n"
				}
				thinkingText += s.Text
			}
			state.message.Content = append(state.message.Content,
				core.Content{
					Type:              "thinking",
					Text:              thinkingText,
					ThinkingSignature: signature,
				},
			)
		}

	case eventCompleted:
		if ev.Response != nil {
			state.message.StopReason = mapStatus(ev.Response.Status)
			if ev.Response.Usage != nil {
				state.message.Usage = &core.Usage{
					Input:       ev.Response.Usage.InputTokens,
					Output:      ev.Response.Usage.OutputTokens,
					TotalTokens: ev.Response.Usage.TotalTokens,
				}
			}
			state.message.Model = ev.Response.ID
		}
		// If any tool calls, stop reason should be tool_use.
		for _, c := range state.message.Content {
			if c.Type == "tool_call" {
				state.message.StopReason = "tool_use"
				break
			}
		}
		final := state.message
		ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &final}
		return true

	case eventFailed:
		errMsg := "response failed"
		if ev.Response != nil && ev.Response.Error != nil {
			errMsg = ev.Response.Error.Message
		}
		ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: fmt.Errorf("openai: %s", errMsg)}
		return true

	case eventError:
		errMsg := ev.Message
		if errMsg == "" {
			errMsg = ev.Code
		}
		if errMsg == "" {
			errMsg = "unknown error"
		}
		ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: fmt.Errorf("openai: %s", errMsg)}
		return true
	}

	return false
}

func mapStatus(status string) string {
	switch status {
	case "completed":
		return "end_turn"
	case "cancelled":
		return "cancelled"
	case "failed":
		return "error"
	case "incomplete":
		return "max_tokens"
	default:
		return status
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

// replaceText overwrites all text content with the final authoritative version.
func replaceText(msg *core.Message, text string) {
	for i := range msg.Content {
		if msg.Content[i].Type == "text" {
			msg.Content[i].Text = text
			return
		}
	}
	msg.Content = append(msg.Content, core.TextContent(text))
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
