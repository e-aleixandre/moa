package openai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
)

// responsesSSE helpers build Responses API SSE payloads.
func sseEvent(data string) string {
	return "data: " + data + "\n\n"
}

func TestStream_TextResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("bad auth: %s", r.Header.Get("Authorization"))
		}
		// Verify it hits /v1/responses.
		if r.URL.Path != "/v1/responses" {
			t.Errorf("wrong path: %s", r.URL.Path)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprint(w,
			sseEvent(`{"type":"response.output_item.added","item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":""}],"status":"in_progress"}}`)+
				sseEvent(`{"type":"response.output_text.delta","delta":"Hello"}`)+
				sseEvent(`{"type":"response.output_text.delta","delta":" world"}`)+
				sseEvent(`{"type":"response.output_item.done","item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"Hello world"}],"status":"completed"}}`)+
				sseEvent(`{"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}`),
		)
	}))
	defer server.Close()

	prov := NewWithBaseURL("test-key", server.URL)
	ch, err := prov.Stream(context.Background(), core.Request{
		Model:    core.Model{ID: "gpt-5.3-codex"},
		Messages: []core.Message{core.NewUserMessage("Hi")},
	})
	if err != nil {
		t.Fatal(err)
	}

	var text string
	var gotDone bool
	var finalMsg *core.Message
	for event := range ch {
		switch event.Type {
		case core.ProviderEventTextDelta:
			text += event.Delta
		case core.ProviderEventDone:
			gotDone = true
			finalMsg = event.Message
		case core.ProviderEventError:
			t.Fatalf("unexpected error: %v", event.Error)
		}
	}

	if text != "Hello world" {
		t.Fatalf("text: %q", text)
	}
	if !gotDone {
		t.Fatal("expected done event")
	}
	if finalMsg == nil {
		t.Fatal("expected final message")
	}
	if finalMsg.Role != "assistant" {
		t.Fatalf("role: %s", finalMsg.Role)
	}
	if finalMsg.Usage == nil || finalMsg.Usage.TotalTokens != 12 {
		t.Fatalf("usage: %+v", finalMsg.Usage)
	}
	if finalMsg.StopReason != "end_turn" {
		t.Fatalf("stop reason: %s", finalMsg.StopReason)
	}
}

func TestStream_ToolCallResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprint(w,
			sseEvent(`{"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"bash","arguments":""}}`)+
				sseEvent(`{"type":"response.function_call_arguments.delta","delta":"{\"comm"}`)+
				sseEvent(`{"type":"response.function_call_arguments.delta","delta":"and\":\"ls\"}"}`)+
				sseEvent(`{"type":"response.function_call_arguments.done","arguments":"{\"command\":\"ls\"}"}`)+
				sseEvent(`{"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"bash","arguments":"{\"command\":\"ls\"}","status":"completed"}}`)+
				sseEvent(`{"type":"response.completed","response":{"id":"resp_2","status":"completed"}}`),
		)
	}))
	defer server.Close()

	prov := NewWithBaseURL("key", server.URL)
	ch, err := prov.Stream(context.Background(), core.Request{
		Model:    core.Model{ID: "gpt-5.3-codex"},
		Messages: []core.Message{core.NewUserMessage("list files")},
	})
	if err != nil {
		t.Fatal(err)
	}

	var gotToolStart, gotToolEnd, gotDone bool
	var finalMsg *core.Message
	for event := range ch {
		switch event.Type {
		case core.ProviderEventToolCallStart:
			gotToolStart = true
		case core.ProviderEventToolCallEnd:
			gotToolEnd = true
		case core.ProviderEventDone:
			gotDone = true
			finalMsg = event.Message
		case core.ProviderEventError:
			t.Fatalf("error: %v", event.Error)
		}
	}

	if !gotToolStart {
		t.Fatal("expected tool call start")
	}
	if !gotToolEnd {
		t.Fatal("expected tool call end")
	}
	if !gotDone {
		t.Fatal("expected done")
	}

	var toolCall *core.Content
	for _, c := range finalMsg.Content {
		if c.Type == "tool_call" {
			toolCall = &c
			break
		}
	}
	if toolCall == nil {
		t.Fatal("expected tool_call in message content")
	}
	if toolCall.ToolCallID != "call_abc" {
		t.Fatalf("tool call id: %s", toolCall.ToolCallID)
	}
	if toolCall.ToolName != "bash" {
		t.Fatalf("tool name: %s", toolCall.ToolName)
	}
	cmd, _ := toolCall.Arguments["command"].(string)
	if cmd != "ls" {
		t.Fatalf("command arg: %v", toolCall.Arguments)
	}
}

func TestStream_ThinkingResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprint(w,
			sseEvent(`{"type":"response.output_item.added","item":{"type":"reasoning","id":"rs_1"}}`)+
				sseEvent(`{"type":"response.reasoning_summary_text.delta","delta":"I think "}`)+
				sseEvent(`{"type":"response.reasoning_summary_text.delta","delta":"therefore"}`)+
				sseEvent(`{"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_1","summary":[{"text":"I think therefore"}]}}`)+
				sseEvent(`{"type":"response.output_item.added","item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":""}]}}`)+
				sseEvent(`{"type":"response.output_text.delta","delta":"Answer"}`)+
				sseEvent(`{"type":"response.output_item.done","item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"Answer"}],"status":"completed"}}`)+
				sseEvent(`{"type":"response.completed","response":{"id":"resp_3","status":"completed"}}`),
		)
	}))
	defer server.Close()

	prov := NewWithBaseURL("key", server.URL)
	ch, err := prov.Stream(context.Background(), core.Request{
		Model:    core.Model{ID: "gpt-5.3-codex"},
		Messages: []core.Message{core.NewUserMessage("think")},
	})
	if err != nil {
		t.Fatal(err)
	}

	var thinking, text string
	var gotDone bool
	var finalMsg *core.Message
	for event := range ch {
		switch event.Type {
		case core.ProviderEventThinkingDelta:
			thinking += event.Delta
		case core.ProviderEventTextDelta:
			text += event.Delta
		case core.ProviderEventDone:
			gotDone = true
			finalMsg = event.Message
		case core.ProviderEventError:
			t.Fatalf("error: %v", event.Error)
		}
	}

	if thinking != "I think therefore" {
		t.Fatalf("thinking: %q", thinking)
	}
	if text != "Answer" {
		t.Fatalf("text: %q", text)
	}
	if !gotDone {
		t.Fatal("expected done")
	}
	// Final message should have both thinking and text content.
	hasThinking := false
	hasText := false
	for _, c := range finalMsg.Content {
		if c.Type == "thinking" {
			hasThinking = true
			if c.ThinkingSignature == "" {
				t.Fatal("thinking should have signature")
			}
		}
		if c.Type == "text" {
			hasText = true
		}
	}
	if !hasThinking {
		t.Fatal("expected thinking content")
	}
	if !hasText {
		t.Fatal("expected text content")
	}
}

func TestStream_ErrorEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprint(w,
			sseEvent(`{"type":"error","code":"rate_limit","message":"too many requests"}`),
		)
	}))
	defer server.Close()

	prov := NewWithBaseURL("key", server.URL)
	ch, err := prov.Stream(context.Background(), core.Request{
		Model:    core.Model{ID: "gpt-5.3-codex"},
		Messages: []core.Message{core.NewUserMessage("test")},
	})
	if err != nil {
		t.Fatal(err)
	}

	gotError := false
	for event := range ch {
		if event.Type == core.ProviderEventError {
			gotError = true
		}
	}
	if !gotError {
		t.Fatal("expected error event")
	}
}

func TestStream_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":{"message":"invalid api key"}}`)
	}))
	defer server.Close()

	prov := NewWithBaseURL("bad-key", server.URL)
	_, err := prov.Stream(context.Background(), core.Request{
		Model:    core.Model{ID: "gpt-5.3-codex"},
		Messages: []core.Message{core.NewUserMessage("Hi")},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestStream_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprint(w,
			sseEvent(`{"type":"response.output_item.added","item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":""}]}}`)+
				sseEvent(`{"type":"response.output_text.delta","delta":"hi"}`),
		)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	prov := NewWithBaseURL("key", server.URL)
	ch, err := prov.Stream(ctx, core.Request{
		Model:    core.Model{ID: "gpt-5.3-codex"},
		Messages: []core.Message{core.NewUserMessage("test")},
	})
	if err != nil {
		t.Fatal(err)
	}

	<-ch // read first event
	cancel()

	gotError := false
	for event := range ch {
		if event.Type == core.ProviderEventError {
			gotError = true
		}
	}
	if !gotError {
		t.Fatal("expected error after cancellation")
	}
}

func TestNewOAuth_UsesCodexEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/codex/responses" {
			t.Errorf("expected /codex/responses, got %s", r.URL.Path)
		}
		if r.Header.Get("chatgpt-account-id") != "acct_123" {
			t.Errorf("missing chatgpt-account-id header")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprint(w,
			sseEvent(`{"type":"response.output_item.added","item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":""}]}}`)+
				sseEvent(`{"type":"response.output_text.delta","delta":"ok"}`)+
				sseEvent(`{"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`),
		)
	}))
	defer server.Close()

	prov := NewOAuth("token", "acct_123")
	prov.baseURL = server.URL

	ch, err := prov.Stream(context.Background(), core.Request{
		Model:    core.Model{ID: "gpt-5.3-codex"},
		Messages: []core.Message{core.NewUserMessage("test")},
	})
	if err != nil {
		t.Fatal(err)
	}

	var gotDone bool
	for event := range ch {
		if event.Type == core.ProviderEventDone {
			gotDone = true
		}
		if event.Type == core.ProviderEventError {
			t.Fatalf("error: %v", event.Error)
		}
	}
	if !gotDone {
		t.Fatal("expected done")
	}
}
