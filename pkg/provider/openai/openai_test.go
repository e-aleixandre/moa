package openai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
)

func TestStream_TextResponse(t *testing.T) {
	// Mock server that returns a simple text streaming response.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("bad auth: %s", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprint(w, `data: {"id":"chatcmpl-1","model":"gpt-5.3-codex","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}

data: {"id":"chatcmpl-1","model":"gpt-5.3-codex","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-1","model":"gpt-5.3-codex","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}

data: {"id":"chatcmpl-1","model":"gpt-5.3-codex","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}

data: [DONE]

`)
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
	if finalMsg.StopReason != "stop" {
		t.Fatalf("stop reason: %s", finalMsg.StopReason)
	}
}

func TestStream_ToolCallResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprint(w, `data: {"id":"chatcmpl-2","model":"gpt-5.3-codex","choices":[{"index":0,"delta":{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"bash","arguments":""}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-2","model":"gpt-5.3-codex","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"comm"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-2","model":"gpt-5.3-codex","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"and\":\"ls\"}"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-2","model":"gpt-5.3-codex","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]

`)
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

	var gotToolStart, gotDone bool
	var finalMsg *core.Message
	for event := range ch {
		switch event.Type {
		case core.ProviderEventToolCallStart:
			gotToolStart = true
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
	if !gotDone {
		t.Fatal("expected done")
	}

	// Verify tool call in final message.
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
