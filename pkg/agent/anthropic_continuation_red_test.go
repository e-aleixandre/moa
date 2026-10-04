package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider/anthropic"
)

type anthropicTailWireBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	Signature string `json:"signature"`
	Data      string `json:"data"`
	ID        string `json:"id"`
	ToolUseID string `json:"tool_use_id"`
}

type anthropicTailWireRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string                   `json:"role"`
		Content []anthropicTailWireBlock `json:"content"`
	} `json:"messages"`
}

// Enforce the demonstrated API shape constraints on the real serialized body,
// not on core.Messages (conversion can itself leave an assistant empty).
func anthropicTailWireError(req anthropicTailWireRequest) error {
	for i, msg := range req.Messages {
		if msg.Role != "assistant" {
			continue
		}
		if len(msg.Content) == 0 {
			return fmt.Errorf("messages.%d: assistant content must not be empty", i)
		}
		last := msg.Content[len(msg.Content)-1].Type
		if last == "thinking" || last == "redacted_thinking" {
			return fmt.Errorf("messages.%d: final assistant block cannot be %s", i, last)
		}
	}
	return nil
}

func anthropicTailTestServer(t *testing.T, key, firstSSE string) (*anthropic.Anthropic, <-chan anthropicTailWireRequest) {
	t.Helper()
	captured := make(chan anthropicTailWireRequest, 8)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req anthropicTailWireRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode local request: %v", err)
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		captured <- req
		if err := anthropicTailWireError(req); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"type":  "error",
				"error": map[string]any{"type": "invalid_request_error", "message": err.Error()},
			})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if calls.Add(1) == 1 && firstSSE != "" {
			_, _ = io.WriteString(w, firstSSE)
			return
		}
		_, _ = io.WriteString(w, anthropicTailDoneSSE)
	}))
	t.Cleanup(server.Close)
	return anthropic.NewWithBaseURL(key, server.URL), captured
}

func anthropicTailModel(t *testing.T) core.Model {
	t.Helper()
	model, ok := core.ResolveModel("claude-opus-5-5")
	if !ok {
		t.Fatal("claude-opus-5-5 is not in the model registry")
	}
	return model
}

func TestAnthropicNormalLoop_ThinkingOnlyMaxTokensContinuation(t *testing.T) {
	for _, redacted := range []bool{false, true} {
		for _, auth := range []struct{ name, key string }{
			{"api_key", "synthetic-not-a-credential"},
			{"oauth", "sk-ant-oat-synthetic-not-a-credential"},
		} {
			t.Run(fmt.Sprintf("redacted=%t/%s", redacted, auth.name), func(t *testing.T) {
				provider, captured := anthropicTailTestServer(t, auth.key, anthropicTailMaxTokensSSE(redacted))
				ag, err := New(AgentConfig{Provider: provider, Model: anthropicTailModel(t), ThinkingLevel: "high", MaxTurns: 3})
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, runErr := ag.Send(ctx, "synthetic task")
				if len(captured) != 2 {
					t.Fatalf("got %d HTTP requests, want turn plus automatic continuation; run error: %v", len(captured), runErr)
				}
				first, continuation := <-captured, <-captured
				if err := anthropicTailWireError(continuation); err != nil {
					t.Errorf("automatic continuation is API-invalid: %v", err)
				}
				if !reflect.DeepEqual(continuation.Messages, first.Messages) {
					t.Errorf("thinking-only truncation must resend the last valid wire prefix, without empty assistants or invented user text: %+v", continuation.Messages)
				}
				if runErr != nil {
					t.Errorf("automatic continuation must complete through the real Anthropic provider: %v", runErr)
				}
				history := ag.Messages()
				if len(history) < 2 || history[1].StopReason != "max_tokens" || len(history[1].Content) != 1 ||
					history[1].Content[0].Type != "thinking" || history[1].Content[0].ThinkingSignature != "synthetic-signature" ||
					history[1].Content[0].Redacted != redacted {
					t.Fatalf("request cleanup must preserve the original truncated response in history: %+v", history)
				}
				if runErr == nil && (len(history) != 3 || len(history[2].Content) != 1 || history[2].Content[0].Text != "done") {
					t.Errorf("want user, original truncated assistant, completed assistant; got %+v", history)
				}
			})
		}
	}
}

func TestAnthropicNormalLoop_ModelChangeDropsEmptyAssistant(t *testing.T) {
	for _, redacted := range []bool{false, true} {
		t.Run(fmt.Sprintf("redacted=%t", redacted), func(t *testing.T) {
			model := anthropicTailModel(t)
			provider, captured := anthropicTailTestServer(t, "sk-ant-oat-synthetic-not-a-credential", "")
			ag, err := New(AgentConfig{Provider: provider, Model: model, ThinkingLevel: "high", MaxTurns: 2})
			if err != nil {
				t.Fatal(err)
			}
			seed := []core.AgentMessage{
				core.WrapMessage(core.Message{MsgID: "u1", Role: "user", Content: []core.Content{core.TextContent("synthetic old task")}}),
				core.WrapMessage(core.Message{MsgID: "a1", Role: "assistant", Provider: "anthropic", RequestedModel: "claude-opus-5", Content: []core.Content{
					core.ToolCallContent("toolu_1", "noop", map[string]any{}),
				}}),
				core.WrapMessage(core.Message{MsgID: "r1", Role: "tool_result", ToolCallID: "toolu_1", Content: []core.Content{core.TextContent("ok")}}),
				core.WrapMessage(core.Message{MsgID: "a2", Role: "assistant", Provider: "anthropic", Model: "claude-opus-5", RequestedModel: "claude-opus-5",
					StopReason: "max_tokens", Content: []core.Content{{Type: "thinking", ThinkingSignature: "synthetic-old-model-signature", Redacted: redacted}}}),
			}
			before, err := json.Marshal(seed)
			if err != nil {
				t.Fatal(err)
			}
			// Restored provenance belongs to the previous model; the next ordinary
			// request filters it without changing the append-only transcript.
			if err := ag.RestoreConversation(seed, 0); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, runErr := ag.Send(ctx, "continue on the new model")
			if len(captured) != 1 {
				t.Fatalf("got %d requests, want one ordinary request: %v", len(captured), runErr)
			}
			req := <-captured
			if req.Model != model.ID {
				t.Errorf("wire model = %q, want %q", req.Model, model.ID)
			}
			if err := anthropicTailWireError(req); err != nil {
				t.Errorf("filtered old-model thinking left an API-invalid assistant: %v", err)
			}
			if len(req.Messages) != 3 || len(req.Messages[1].Content) != 1 || req.Messages[1].Content[0].Type != "tool_use" ||
				req.Messages[1].Content[0].ID != "toolu_1" || len(req.Messages[2].Content) != 2 ||
				req.Messages[2].Content[0].ToolUseID != "toolu_1" || req.Messages[2].Content[1].Text != "continue on the new model" {
				t.Errorf("drop only the empty assistant, preserving tool pairing and normal user-role merging: %+v", req.Messages)
			}
			if runErr != nil {
				t.Errorf("new-model request must complete: %v", runErr)
			}
			history := ag.Messages()
			if len(history) < len(seed) {
				t.Fatalf("lost restored history: %+v", history)
			}
			after, err := json.Marshal(history[:len(seed)])
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Error("request-only filtering changed the restored history")
			}
			after, err = json.Marshal(seed)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Error("request-only filtering mutated the input content slices")
			}
		})
	}
}

func TestAnthropicWire_TrimsOnlyTrailingThinkingWithoutMutation(t *testing.T) {
	signed := core.Content{Type: "thinking", Thinking: "synthetic reasoning", ThinkingSignature: "synthetic-signature"}
	redacted := core.Content{Type: "thinking", ThinkingSignature: "synthetic-redacted", Redacted: true}
	for _, tc := range []struct {
		name    string
		content []core.Content
		want    []string
	}{
		{"text_then_signed", []core.Content{core.TextContent("keep this text"), signed}, []string{"text"}},
		{"text_then_redacted", []core.Content{core.TextContent("keep this text"), redacted}, []string{"text"}},
		{"multiple_trailing_blocks", []core.Content{signed, core.TextContent("keep this text"), signed, redacted}, []string{"thinking", "text"}},
		{"valid_native_prefix", []core.Content{signed, redacted, core.TextContent("keep this text")}, []string{"thinking", "redacted_thinking", "text"}},
		{"unsigned_thinking_is_plain_text", []core.Content{core.TextContent("keep this text"), {Type: "thinking", Thinking: "unsigned partial"}}, []string{"text", "text"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, captured := anthropicTailTestServer(t, "sk-ant-oat-synthetic-not-a-credential", "")
			req := core.Request{
				Model: anthropicTailModel(t),
				Messages: []core.Message{
					core.NewUserMessage("synthetic task"),
					{Role: "assistant", Provider: "anthropic", Content: tc.content},
					core.NewUserMessage("continue"),
				},
				Options: core.StreamOptions{ThinkingLevel: "high"},
			}
			before, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ch, streamErr := provider.Stream(ctx, req)
			if streamErr == nil {
				for event := range ch {
					if event.Type == core.ProviderEventError {
						t.Errorf("local SSE error: %v", event.Error)
					}
				}
			}
			if len(captured) != 1 {
				t.Fatalf("got %d requests, want one: %v", len(captured), streamErr)
			}
			wire := <-captured
			if err := anthropicTailWireError(wire); err != nil {
				t.Errorf("serialized tail is API-invalid: %v", err)
			}
			if len(wire.Messages) != 3 || wire.Messages[1].Role != "assistant" {
				t.Fatalf("text-bearing assistant must survive: %+v", wire.Messages)
			}
			var types []string
			for _, block := range wire.Messages[1].Content {
				types = append(types, block.Type)
			}
			if !reflect.DeepEqual(types, tc.want) {
				t.Errorf("assistant block types = %v, want %v", types, tc.want)
			}
			keptText := false
			for _, block := range wire.Messages[1].Content {
				if block.Type == "text" && block.Text == "keep this text" {
					keptText = true
				}
			}
			if !keptText {
				t.Error("trimming a thinking tail lost or changed the retained text")
			}
			if tc.want[0] == "thinking" && (len(wire.Messages[1].Content) == 0 || wire.Messages[1].Content[0].Signature != signed.ThinkingSignature) {
				t.Error("valid native thinking prefix was changed")
			}
			if streamErr != nil {
				t.Errorf("trimmed request must be accepted by the local API validator: %v", streamErr)
			}
			after, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Error("serialization mutated the caller's request or content slices")
			}
		})
	}
}

func anthropicTailMaxTokensSSE(redacted bool) string {
	block := `{"type":"thinking","thinking":""}`
	deltas := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"synthetic-signature\"}}\n\n"
	if redacted {
		block = `{"type":"redacted_thinking","data":"synthetic-signature"}`
		deltas = ""
	}
	return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_truncated\",\"role\":\"assistant\",\"model\":\"claude-opus-5-5\",\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":" + block + "}\n\n" + deltas +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"},\"usage\":{\"output_tokens\":8}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
}

const anthropicTailDoneSSE = `event: message_start
data: {"type":"message_start","message":{"id":"msg_done","role":"assistant","model":"claude-opus-5-5","usage":{"input_tokens":5,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}

`
