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
		_, _ = fmt.Fprint(w,
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

func TestStream_UsageSplitsCachedAndModel(t *testing.T) {
	// Responses API input_tokens (100) INCLUDES cached (80). We must record
	// Input=20 (non-cached) + CacheRead=80, and Model from response.model
	// ("gpt-5.5") — not response.id ("resp_9").
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = fmt.Fprint(w,
			sseEvent(`{"type":"response.output_item.added","item":{"type":"message","id":"msg_9","role":"assistant","content":[{"type":"output_text","text":""}],"status":"in_progress"}}`)+
				sseEvent(`{"type":"response.output_text.delta","delta":"ok"}`)+
				sseEvent(`{"type":"response.output_item.done","item":{"type":"message","id":"msg_9","role":"assistant","content":[{"type":"output_text","text":"ok"}],"status":"completed"}}`)+
				sseEvent(`{"type":"response.completed","response":{"id":"resp_9","model":"gpt-5.5","status":"completed","usage":{"input_tokens":100,"output_tokens":5,"total_tokens":105,"input_tokens_details":{"cached_tokens":80}}}}`),
		)
	}))
	defer server.Close()

	prov := NewWithBaseURL("key", server.URL)
	ch, err := prov.Stream(context.Background(), core.Request{
		Model:    core.Model{ID: "gpt-5.5"},
		Messages: []core.Message{core.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}

	var finalMsg *core.Message
	for event := range ch {
		if event.Type == core.ProviderEventDone {
			finalMsg = event.Message
		}
	}
	if finalMsg == nil || finalMsg.Usage == nil {
		t.Fatal("expected final message with usage")
	}
	if finalMsg.Usage.Input != 20 || finalMsg.Usage.CacheRead != 80 {
		t.Fatalf("expected Input=20 CacheRead=80, got Input=%d CacheRead=%d",
			finalMsg.Usage.Input, finalMsg.Usage.CacheRead)
	}
	if finalMsg.Model != "gpt-5.5" {
		t.Fatalf("expected Model=gpt-5.5, got %q", finalMsg.Model)
	}
}

func TestStream_ToolCallResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = fmt.Fprint(w,
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
		_, _ = fmt.Fprint(w,
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
		_, _ = fmt.Fprint(w,
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
		_, _ = fmt.Fprint(w, `{"error":{"message":"invalid api key"}}`)
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
		_, _ = fmt.Fprint(w,
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
		_, _ = fmt.Fprint(w,
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

// TestStream_RequiresResponseCompleted is the provider-stream contract for a
// Responses API success: transport EOF and the SSE [DONE] marker do not mean
// the response completed successfully. Only response.completed can produce a
// done event.
func TestStream_RequiresResponseCompleted(t *testing.T) {
	tests := []struct {
		name string
		end  string
	}{
		{name: "EOF", end: ""},
		{name: "done marker", end: sseEvent("[DONE]")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w,
					sseEvent(`{"type":"response.output_item.added","item":{"type":"message","id":"msg_1"}}`)+
						sseEvent(`{"type":"response.output_text.delta","delta":"partial"}`)+
						tt.end,
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

			var terminals []core.AssistantEvent
			for event := range ch {
				if event.IsTerminal() {
					terminals = append(terminals, event)
				}
			}

			if len(terminals) != 1 {
				t.Fatalf("terminal events = %d, want exactly 1", len(terminals))
			}
			if terminals[0].Type != core.ProviderEventError {
				t.Fatalf("terminal event = %q, want error", terminals[0].Type)
			}
			if terminals[0].Error == nil {
				t.Fatal("error terminal must include an error")
			}
		})
	}
}

// collectStream drains a provider stream, returning the final Done message (if
// any) and whether a terminal error was seen.
func collectStream(t *testing.T, ch <-chan core.AssistantEvent) (*core.Message, *core.AssistantEvent) {
	t.Helper()
	var final *core.Message
	var errEv *core.AssistantEvent
	for event := range ch {
		switch event.Type {
		case core.ProviderEventDone:
			m := event.Message
			final = m
		case core.ProviderEventError:
			e := event
			errEv = &e
		}
	}
	return final, errEv
}

func serveSSE(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = fmt.Fprint(w, body)
	}))
}

// TestStream_MessageCarriesTextSignature verifies the message item's id and
// phase survive as a TextSignature so they can be replayed next request. Losing
// phase makes OpenAI stop early on later turns.
func TestStream_MessageCarriesTextSignature(t *testing.T) {
	server := serveSSE(t,
		sseEvent(`{"type":"response.output_item.added","item":{"type":"message","id":"msg_1","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":""}]}}`)+
			sseEvent(`{"type":"response.output_text.delta","delta":"Hello"}`)+
			sseEvent(`{"type":"response.output_item.done","item":{"type":"message","id":"msg_1","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Hello"}],"status":"completed"}}`)+
			sseEvent(`{"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`),
	)
	defer server.Close()

	prov := NewWithBaseURL("key", server.URL)
	ch, err := prov.Stream(context.Background(), core.Request{
		Model:    core.Model{ID: "gpt-5.3-codex"},
		Messages: []core.Message{core.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	final, errEv := collectStream(t, ch)
	if errEv != nil {
		t.Fatalf("unexpected error: %v", errEv.Error)
	}
	if final == nil {
		t.Fatal("expected final message")
	}
	var text *core.Content
	for i := range final.Content {
		if final.Content[i].Type == "text" {
			text = &final.Content[i]
		}
	}
	if text == nil {
		t.Fatal("expected text content")
	}
	id, phase := parseTextSignature(text.TextSignature)
	if id != "msg_1" {
		t.Errorf("signature id = %q, want msg_1", id)
	}
	if phase != "final_answer" {
		t.Errorf("signature phase = %q, want final_answer", phase)
	}
}

// TestStream_ToolCallCarriesItemID verifies the function_call's fc_ output-item
// id is preserved (distinct from the call_id) for faithful replay.
func TestStream_ToolCallCarriesItemID(t *testing.T) {
	server := serveSSE(t,
		sseEvent(`{"type":"response.output_item.added","item":{"type":"function_call","id":"fc_9","call_id":"call_x","name":"bash","arguments":""}}`)+
			sseEvent(`{"type":"response.function_call_arguments.done","arguments":"{\"command\":\"ls\"}"}`)+
			sseEvent(`{"type":"response.output_item.done","item":{"type":"function_call","id":"fc_9","call_id":"call_x","name":"bash","arguments":"{\"command\":\"ls\"}","status":"completed"}}`)+
			sseEvent(`{"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`),
	)
	defer server.Close()

	prov := NewWithBaseURL("key", server.URL)
	ch, err := prov.Stream(context.Background(), core.Request{
		Model:    core.Model{ID: "gpt-5.3-codex"},
		Messages: []core.Message{core.NewUserMessage("ls")},
	})
	if err != nil {
		t.Fatal(err)
	}
	final, errEv := collectStream(t, ch)
	if errEv != nil {
		t.Fatalf("unexpected error: %v", errEv.Error)
	}
	var tc *core.Content
	toolEnds := 0
	for i := range final.Content {
		if final.Content[i].Type == "tool_call" {
			tc = &final.Content[i]
		}
	}
	_ = toolEnds
	if tc == nil {
		t.Fatal("expected tool_call")
	}
	if tc.ToolCallID != "call_x" {
		t.Errorf("call_id = %q, want call_x", tc.ToolCallID)
	}
	if tc.ToolCallItemID != "fc_9" {
		t.Errorf("item id = %q, want fc_9", tc.ToolCallItemID)
	}
}

// TestStream_ToolCallReconciledFromItemDone verifies that a function call whose
// arguments.done event never arrives is still recovered from the authoritative
// output_item.done (the pi/codex behavior). Without this the call is lost and
// the turn silently ends.
func TestStream_ToolCallReconciledFromItemDone(t *testing.T) {
	server := serveSSE(t,
		sseEvent(`{"type":"response.output_item.added","item":{"type":"function_call","id":"fc_2","call_id":"call_y","name":"bash","arguments":""}}`)+
			// NOTE: no function_call_arguments.done — only the item.done.
			sseEvent(`{"type":"response.output_item.done","item":{"type":"function_call","id":"fc_2","call_id":"call_y","name":"bash","arguments":"{\"command\":\"pwd\"}","status":"completed"}}`)+
			sseEvent(`{"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`),
	)
	defer server.Close()

	prov := NewWithBaseURL("key", server.URL)
	ch, err := prov.Stream(context.Background(), core.Request{
		Model:    core.Model{ID: "gpt-5.3-codex"},
		Messages: []core.Message{core.NewUserMessage("pwd")},
	})
	if err != nil {
		t.Fatal(err)
	}
	final, errEv := collectStream(t, ch)
	if errEv != nil {
		t.Fatalf("unexpected error: %v", errEv.Error)
	}
	var tc *core.Content
	for i := range final.Content {
		if final.Content[i].Type == "tool_call" {
			tc = &final.Content[i]
		}
	}
	if tc == nil {
		t.Fatal("tool_call not reconciled from output_item.done")
	}
	if tc.ToolName != "bash" || tc.ToolCallID != "call_y" {
		t.Errorf("reconciled call wrong: %+v", tc)
	}
	if cmd, _ := tc.Arguments["command"].(string); cmd != "pwd" {
		t.Errorf("reconciled args = %v", tc.Arguments)
	}
}

// TestStream_ToolCallNotDoubled verifies the dedupe: when both
// arguments.done AND output_item.done arrive, exactly one tool_call and one
// ToolCallEnd are produced.
func TestStream_ToolCallNotDoubled(t *testing.T) {
	server := serveSSE(t,
		sseEvent(`{"type":"response.output_item.added","item":{"type":"function_call","id":"fc_3","call_id":"call_z","name":"bash","arguments":""}}`)+
			sseEvent(`{"type":"response.function_call_arguments.done","arguments":"{\"command\":\"id\"}"}`)+
			sseEvent(`{"type":"response.output_item.done","item":{"type":"function_call","id":"fc_3","call_id":"call_z","name":"bash","arguments":"{\"command\":\"id\"}","status":"completed"}}`)+
			sseEvent(`{"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`),
	)
	defer server.Close()

	prov := NewWithBaseURL("key", server.URL)
	ch, err := prov.Stream(context.Background(), core.Request{
		Model:    core.Model{ID: "gpt-5.3-codex"},
		Messages: []core.Message{core.NewUserMessage("id")},
	})
	if err != nil {
		t.Fatal(err)
	}
	var toolEnds, toolCalls int
	for event := range ch {
		if event.Type == core.ProviderEventToolCallEnd {
			toolEnds++
		}
		if event.Type == core.ProviderEventDone {
			for _, c := range event.Message.Content {
				if c.Type == "tool_call" {
					toolCalls++
				}
			}
		}
	}
	if toolEnds != 1 {
		t.Errorf("ToolCallEnd events = %d, want 1 (dedupe failed)", toolEnds)
	}
	if toolCalls != 1 {
		t.Errorf("tool_call content blocks = %d, want 1 (dedupe failed)", toolCalls)
	}
}

// TestStream_IncompleteIsError verifies an incomplete (out-of-tokens) response
// surfaces as a visible error, not a silent success.
func TestStream_IncompleteIsError(t *testing.T) {
	server := serveSSE(t,
		sseEvent(`{"type":"response.output_item.added","item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":""}]}}`)+
			sseEvent(`{"type":"response.output_text.delta","delta":"partial"}`)+
			sseEvent(`{"type":"response.completed","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`),
	)
	defer server.Close()

	prov := NewWithBaseURL("key", server.URL)
	ch, err := prov.Stream(context.Background(), core.Request{
		Model:    core.Model{ID: "gpt-5.3-codex"},
		Messages: []core.Message{core.NewUserMessage("go")},
	})
	if err != nil {
		t.Fatal(err)
	}
	final, errEv := collectStream(t, ch)
	if errEv == nil {
		t.Fatal("expected incomplete to surface as error")
	}
	if final != nil {
		t.Fatal("incomplete must not produce a Done message")
	}
}

// TestStream_EmptyCompletedIsError verifies a completed response with no
// substantive content (no text, no tool call; reasoning-only counts as empty)
// surfaces as a visible error rather than a silent stall.
func TestStream_EmptyCompletedIsError(t *testing.T) {
	server := serveSSE(t,
		sseEvent(`{"type":"response.output_item.added","item":{"type":"reasoning","id":"rs_1"}}`)+
			sseEvent(`{"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_1","encrypted_content":"enc","summary":[]}}`)+
			sseEvent(`{"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`),
	)
	defer server.Close()

	prov := NewWithBaseURL("key", server.URL)
	ch, err := prov.Stream(context.Background(), core.Request{
		Model:    core.Model{ID: "gpt-5.3-codex"},
		Messages: []core.Message{core.NewUserMessage("go")},
	})
	if err != nil {
		t.Fatal(err)
	}
	final, errEv := collectStream(t, ch)
	if errEv == nil {
		t.Fatal("expected empty completed to surface as error")
	}
	if final != nil {
		t.Fatal("empty completed must not produce a Done message")
	}
}

// TestStream_ToolCallOnlyIsNotEmpty verifies a turn with a tool call but no text
// is a legitimate completion (substantive), not an empty-turn error.
func TestStream_ToolCallOnlyIsNotEmpty(t *testing.T) {
	server := serveSSE(t,
		sseEvent(`{"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","call_id":"call_a","name":"bash","arguments":""}}`)+
			sseEvent(`{"type":"response.function_call_arguments.done","arguments":"{\"command\":\"ls\"}"}`)+
			sseEvent(`{"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"call_a","name":"bash","arguments":"{\"command\":\"ls\"}","status":"completed"}}`)+
			sseEvent(`{"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`),
	)
	defer server.Close()

	prov := NewWithBaseURL("key", server.URL)
	ch, err := prov.Stream(context.Background(), core.Request{
		Model:    core.Model{ID: "gpt-5.3-codex"},
		Messages: []core.Message{core.NewUserMessage("ls")},
	})
	if err != nil {
		t.Fatal(err)
	}
	final, errEv := collectStream(t, ch)
	if errEv != nil {
		t.Fatalf("tool-call-only turn wrongly errored: %v", errEv.Error)
	}
	if final == nil || final.StopReason != "tool_use" {
		t.Fatalf("expected tool_use stop, got %+v", final)
	}
}

// TestStream_InterleavedToolCalls verifies two function calls whose events are
// interleaved (both added before either finishes) each produce exactly one
// tool_call with the right args/ids — the per-output_index slot behavior. A
// single global state would cross the wires or double-execute.
func TestStream_InterleavedToolCalls(t *testing.T) {
	server := serveSSE(t,
		sseEvent(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_a","call_id":"call_a","name":"bash","arguments":""}}`)+
			sseEvent(`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_b","call_id":"call_b","name":"read","arguments":""}}`)+
			sseEvent(`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"command\":"}`)+
			sseEvent(`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"path\":"}`)+
			sseEvent(`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"\"ls\"}"}`)+
			sseEvent(`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"\"/tmp\"}"}`)+
			sseEvent(`{"type":"response.function_call_arguments.done","output_index":1,"arguments":"{\"path\":\"/tmp\"}"}`)+
			sseEvent(`{"type":"response.function_call_arguments.done","output_index":0,"arguments":"{\"command\":\"ls\"}"}`)+
			sseEvent(`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_a","call_id":"call_a","name":"bash","arguments":"{\"command\":\"ls\"}","status":"completed"}}`)+
			sseEvent(`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_b","call_id":"call_b","name":"read","arguments":"{\"path\":\"/tmp\"}","status":"completed"}}`)+
			sseEvent(`{"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`),
	)
	defer server.Close()

	prov := NewWithBaseURL("key", server.URL)
	ch, err := prov.Stream(context.Background(), core.Request{
		Model:    core.Model{ID: "gpt-5.3-codex"},
		Messages: []core.Message{core.NewUserMessage("go")},
	})
	if err != nil {
		t.Fatal(err)
	}
	var toolEnds int
	final, errEv := func() (*core.Message, *core.AssistantEvent) {
		var f *core.Message
		var e *core.AssistantEvent
		for ev := range ch {
			switch ev.Type {
			case core.ProviderEventToolCallEnd:
				toolEnds++
			case core.ProviderEventDone:
				f = ev.Message
			case core.ProviderEventError:
				ee := ev
				e = &ee
			}
		}
		return f, e
	}()
	if errEv != nil {
		t.Fatalf("unexpected error: %v", errEv.Error)
	}
	if toolEnds != 2 {
		t.Fatalf("ToolCallEnd = %d, want 2", toolEnds)
	}
	var calls []core.Content
	for _, c := range final.Content {
		if c.Type == "tool_call" {
			calls = append(calls, c)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("tool_calls = %d, want 2", len(calls))
	}
	// call_a → bash ls ; call_b → read /tmp, matched by id (order preserved).
	byID := map[string]core.Content{}
	for _, c := range calls {
		byID[c.ToolCallID] = c
	}
	if a := byID["call_a"]; a.ToolName != "bash" || a.ToolCallItemID != "fc_a" || a.Arguments["command"] != "ls" {
		t.Errorf("call_a wrong: %+v", a)
	}
	if b := byID["call_b"]; b.ToolName != "read" || b.ToolCallItemID != "fc_b" || b.Arguments["path"] != "/tmp" {
		t.Errorf("call_b wrong: %+v", b)
	}
}

// TestStream_MultipleMessagesPreserved verifies two message items in one
// response keep their own text and signatures (no collapse into the first).
func TestStream_MultipleMessagesPreserved(t *testing.T) {
	server := serveSSE(t,
		sseEvent(`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_a","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":""}]}}`)+
			sseEvent(`{"type":"response.output_text.delta","output_index":0,"delta":"first"}`)+
			sseEvent(`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_a","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"first"}],"status":"completed"}}`)+
			sseEvent(`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"msg_b","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":""}]}}`)+
			sseEvent(`{"type":"response.output_text.delta","output_index":1,"delta":"second"}`)+
			sseEvent(`{"type":"response.output_item.done","output_index":1,"item":{"type":"message","id":"msg_b","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"second"}],"status":"completed"}}`)+
			sseEvent(`{"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`),
	)
	defer server.Close()

	prov := NewWithBaseURL("key", server.URL)
	ch, err := prov.Stream(context.Background(), core.Request{
		Model:    core.Model{ID: "gpt-5.3-codex"},
		Messages: []core.Message{core.NewUserMessage("go")},
	})
	if err != nil {
		t.Fatal(err)
	}
	final, errEv := collectStream(t, ch)
	if errEv != nil {
		t.Fatalf("unexpected error: %v", errEv.Error)
	}
	var texts []core.Content
	for _, c := range final.Content {
		if c.Type == "text" {
			texts = append(texts, c)
		}
	}
	if len(texts) != 2 {
		t.Fatalf("text blocks = %d, want 2", len(texts))
	}
	if texts[0].Text != "first" {
		t.Errorf("block0 text = %q", texts[0].Text)
	}
	if texts[1].Text != "second" {
		t.Errorf("block1 text = %q", texts[1].Text)
	}
	if id, phase := parseTextSignature(texts[0].TextSignature); id != "msg_a" || phase != "commentary" {
		t.Errorf("block0 sig = %q/%q", id, phase)
	}
	if id, phase := parseTextSignature(texts[1].TextSignature); id != "msg_b" || phase != "final_answer" {
		t.Errorf("block1 sig = %q/%q", id, phase)
	}
}

// TestStream_IncompleteEvent verifies the distinct response.incomplete terminal
// event surfaces as a visible error carrying its reason.
func TestStream_IncompleteEvent(t *testing.T) {
	server := serveSSE(t,
		sseEvent(`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":""}]}}`)+
			sseEvent(`{"type":"response.output_text.delta","output_index":0,"delta":"partial"}`)+
			sseEvent(`{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`),
	)
	defer server.Close()

	prov := NewWithBaseURL("key", server.URL)
	ch, err := prov.Stream(context.Background(), core.Request{
		Model:    core.Model{ID: "gpt-5.3-codex"},
		Messages: []core.Message{core.NewUserMessage("go")},
	})
	if err != nil {
		t.Fatal(err)
	}
	final, errEv := collectStream(t, ch)
	if errEv == nil {
		t.Fatal("expected response.incomplete to surface as error")
	}
	if final != nil {
		t.Fatal("incomplete must not produce a Done message")
	}
}

// TestStream_RefusalIsSubstantive verifies a message whose only content is a
// refusal is treated as real content (not an empty-turn error).
func TestStream_RefusalIsSubstantive(t *testing.T) {
	server := serveSSE(t,
		sseEvent(`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"refusal","refusal":""}]}}`)+
			sseEvent(`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"refusal","refusal":"I can't help with that."}],"status":"completed"}}`)+
			sseEvent(`{"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`),
	)
	defer server.Close()

	prov := NewWithBaseURL("key", server.URL)
	ch, err := prov.Stream(context.Background(), core.Request{
		Model:    core.Model{ID: "gpt-5.3-codex"},
		Messages: []core.Message{core.NewUserMessage("go")},
	})
	if err != nil {
		t.Fatal(err)
	}
	final, errEv := collectStream(t, ch)
	if errEv != nil {
		t.Fatalf("refusal wrongly errored: %v", errEv.Error)
	}
	if final == nil {
		t.Fatal("expected final message")
	}
	var got string
	for _, c := range final.Content {
		if c.Type == "text" {
			got = c.Text
		}
	}
	if got != "I can't help with that." {
		t.Errorf("refusal text = %q", got)
	}
}
