package xai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

type rewriteTransport struct{ target *url.URL }

func (t rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.URL.Scheme, r.URL.Host = t.target.Scheme, t.target.Host
	return http.DefaultTransport.RoundTrip(r)
}

func TestNewOAuth_UsesConsumerProxyAndRefreshesOnce(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/responses" || r.Header.Get("Accept") != "text/event-stream" {
			t.Fatalf("consumer request = %s accept=%q", r.URL.Path, r.Header.Get("Accept"))
		}
		if r.Header.Get("X-XAI-Token-Auth") != "xai-grok-cli" || r.Header.Get("x-authenticateresponse") != "authenticate-response" || r.Header.Get("x-grok-client-version") != grokBuildCompatVersion || r.Header.Get("x-grok-client-identifier") != "grok-shell" || r.Header.Get("x-grok-client-mode") != "interactive" || !strings.HasPrefix(r.Header.Get("User-Agent"), "Moa grok-shell/") || r.Header.Get("X-XAI-Referrer") != "" {
			t.Fatalf("consumer headers missing: %#v", r.Header)
		}
		if requests == 1 {
			if r.Header.Get("Authorization") != "Bearer old" {
				t.Fatal("first token not used")
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer new" {
			t.Fatal("refreshed token not used")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	p := NewOAuth("old", func(rejected string) (string, error) {
		if rejected != "old" {
			t.Fatalf("rejected token = %q", rejected)
		}
		return "new", nil
	})
	if p.baseURL != consumerBaseURL || p.baseURL == apiBaseURL {
		t.Fatalf("OAuth baseURL = %q", p.baseURL)
	}
	p.client.Transport = rewriteTransport{target}
	ch, err := p.Stream(context.Background(), core.Request{Model: core.Model{ID: "grok-4.5"}, Messages: []core.Message{core.NewUserMessage("hi")}, Options: core.StreamOptions{ThinkingLevel: "low"}})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestNewOAuth_Second401IsTerminal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	p := NewOAuth("old", func(string) (string, error) { return "new", nil })
	p.client.Transport = rewriteTransport{target}
	_, err := p.Stream(context.Background(), core.Request{Model: core.Model{ID: "grok-4.5"}, Options: core.StreamOptions{ThinkingLevel: "low"}})
	if err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("err = %v", err)
	}
}

func TestStream_PublicAPIContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer xai-key" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["store"] != false {
			t.Errorf("store = %#v, want false", body["store"])
		}
		if body["parallel_tool_calls"] != true {
			t.Errorf("parallel_tool_calls = %#v", body["parallel_tool_calls"])
		}
		if _, ok := body["max_output_tokens"]; ok {
			t.Error("xAI must omit unverified max_output_tokens")
		}
		reasoning := body["reasoning"].(map[string]any)
		if reasoning["effort"] != "high" {
			t.Errorf("reasoning effort = %#v", reasoning)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w,
			"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"id\":\"rs_1\"}}\n\n"+
				"data: {\"type\":\"response.reasoning_summary_text.delta\",\"output_index\":0,\"delta\":\"plan\"}\n\n"+
				"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"encrypted_content\":\"secret\",\"summary\":[{\"text\":\"plan\"}]}}\n\n"+
				"data: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":{\"type\":\"function_call\",\"id\":\"fc_a\",\"call_id\":\"call_a\",\"name\":\"one\"}}\n\n"+
				"data: {\"type\":\"response.output_item.added\",\"output_index\":2,\"item\":{\"type\":\"function_call\",\"id\":\"fc_b\",\"call_id\":\"call_b\",\"name\":\"two\"}}\n\n"+
				"data: {\"type\":\"response.function_call_arguments.done\",\"output_index\":2,\"arguments\":\"{\\\"n\\\":2}\"}\n\n"+
				"data: {\"type\":\"response.function_call_arguments.done\",\"output_index\":1,\"arguments\":\"{\\\"n\\\":1}\"}\n\n"+
				"data: {\"type\":\"response.completed\",\"response\":{\"model\":\"grok-4.5\",\"status\":\"completed\",\"usage\":{\"input_tokens\":8,\"output_tokens\":3,\"total_tokens\":11}}}\n\n")
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	provider := New("xai-key")
	provider.client.Transport = rewriteTransport{target}
	ch, err := provider.Stream(context.Background(), core.Request{Model: core.Model{ID: "grok-4.5"}, Messages: []core.Message{core.NewUserMessage("go")}, Options: core.StreamOptions{ThinkingLevel: "high"}})
	if err != nil {
		t.Fatal(err)
	}
	var final *core.Message
	for ev := range ch {
		if ev.Type == core.ProviderEventError {
			t.Fatal(ev.Error)
		}
		if ev.Type == core.ProviderEventDone {
			final = ev.Message
		}
	}
	if final == nil || final.Provider != "xai" || final.Usage == nil || final.Usage.TotalTokens != 11 {
		t.Fatalf("final = %#v", final)
	}
	var calls, thinking int
	for _, c := range final.Content {
		if c.Type == "tool_call" {
			calls++
		}
		if c.Type == "thinking" && strings.Contains(c.ThinkingSignature, "encrypted_content") {
			thinking++
		}
	}
	if calls != 2 || thinking != 1 {
		t.Fatalf("calls=%d thinking=%d", calls, thinking)
	}
}

func TestStream_RejectsUnsupportedThinking(t *testing.T) {
	_, err := New("key").Stream(context.Background(), core.Request{Model: core.Model{ID: "grok-4.5"}, Options: core.StreamOptions{ThinkingLevel: "xhigh"}})
	if err == nil {
		t.Fatal("expected invalid thinking error")
	}
}

func TestStream_ImagesAreSentAndDocumentsAreDegraded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []struct {
				Content []struct {
					Type     string `json:"type"`
					ImageURL string `json:"image_url"`
					Text     string `json:"text"`
				} `json:"content"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Input) != 1 || len(body.Input[0].Content) != 2 {
			t.Fatalf("input = %#v", body.Input)
		}
		image, document := body.Input[0].Content[0], body.Input[0].Content[1]
		if image.Type != "input_image" || image.ImageURL != "data:image/png;base64,iVBORw0KGgo=" {
			t.Fatalf("image = %#v", image)
		}
		if document.Type != "input_text" || !strings.Contains(document.Text, "report.pdf") || strings.Contains(document.Text, "cGRmLWJ5dGVz") {
			t.Fatalf("document must be a metadata-only advisory, got %#v", document)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	p := New("key")
	p.client.Transport = rewriteTransport{target}
	ch, err := p.Stream(context.Background(), core.Request{
		Model: core.Model{ID: "grok-4.5", Provider: "xai"},
		Messages: []core.Message{{Role: "user", Content: []core.Content{
			{Type: "image", MimeType: "image/png", Data: "iVBORw0KGgo="},
			{Type: "document", Filename: "report.pdf", MimeType: "application/pdf", Data: "cGRmLWJ5dGVz"},
		}}},
		Options: core.StreamOptions{ThinkingLevel: "low"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
}

func TestClassify429_StructuredQuotaAndTransientRate(t *testing.T) {
	quota := classifyHTTPResponse(&http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"60"}}}, []byte(`{"error":{"code":"usage_limit_exceeded","message":"private"}}`))
	q, ok := core.AsQuotaExceeded(quota)
	if !ok || q.ResetsIn != time.Minute {
		t.Fatalf("quota = %#v", quota)
	}
	if _, ok := core.AsQuotaExceeded(classifyHTTPError(http.StatusTooManyRequests, []byte(`{"error":{"type":"rate_limit"}}`))); ok {
		t.Fatal("transient rate classified as quota")
	}
}

func TestStream_TwoTurnReasoningAndParallelToolsReplay(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "text/event-stream")
		if len(bodies) == 1 {
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"id\":\"rs_1\"}}\n\ndata: {\"type\":\"response.reasoning_summary_text.delta\",\"output_index\":0,\"delta\":\"plan\"}\n\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"id\":\"rs_1\",\"encrypted_content\":\"encrypted\",\"summary\":[{\"text\":\"plan\"}]}}\n\ndata: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":{\"type\":\"function_call\",\"id\":\"fc_a\",\"call_id\":\"call_a\",\"name\":\"one\"}}\n\ndata: {\"type\":\"response.output_item.added\",\"output_index\":2,\"item\":{\"type\":\"function_call\",\"id\":\"fc_b\",\"call_id\":\"call_b\",\"name\":\"two\"}}\n\ndata: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":2,\"delta\":\"{\\\"n\\\":\"}\n\ndata: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":1,\"delta\":\"{\\\"n\\\":\"}\n\ndata: {\"type\":\"response.function_call_arguments.done\",\"output_index\":2,\"arguments\":\"{\\\"n\\\":2}\"}\n\ndata: {\"type\":\"response.function_call_arguments.done\",\"output_index\":1,\"arguments\":\"{\\\"n\\\":1}\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
			return
		}
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg_2\"}}\n\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"finished\"}\n\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg_2\",\"content\":[{\"type\":\"output_text\",\"text\":\"finished\"}]}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	provider := New("key")
	provider.client.Transport = rewriteTransport{target}
	request := core.Request{Model: core.Model{ID: "grok-4.5"}, Messages: []core.Message{core.NewUserMessage("go")}, Options: core.StreamOptions{ThinkingLevel: "low"}}
	first := terminalMessage(t, provider, request)
	if len(first.Content) != 3 || first.Content[0].ThinkingSignature == "" || first.Content[1].ToolCallID != "call_a" || first.Content[2].ToolCallID != "call_b" {
		t.Fatalf("first message = %#v", first.Content)
	}
	request.Messages = append(request.Messages, *first,
		core.Message{Role: "tool_result", ToolCallID: "call_a", Content: []core.Content{core.TextContent("one result")}},
		core.Message{Role: "tool_result", ToolCallID: "call_b", Content: []core.Content{core.TextContent("two result")}},
	)
	second := terminalMessage(t, provider, request)
	if len(second.Content) != 1 || second.Content[0].Text != "finished" {
		t.Fatalf("second message = %#v", second)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d", len(bodies))
	}
	input := bodies[1]["input"].([]any)
	var reasoning, callA, callB, outputA, outputB bool
	for _, raw := range input {
		item := raw.(map[string]any)
		switch item["type"] {
		case "reasoning":
			reasoning = item["encrypted_content"] == "encrypted"
		case "function_call":
			if item["id"] == "fc_a" && item["call_id"] == "call_a" && item["name"] == "one" {
				callA = true
			}
			if item["id"] == "fc_b" && item["call_id"] == "call_b" && item["name"] == "two" {
				callB = true
			}
		case "function_call_output":
			if item["call_id"] == "call_a" {
				outputA = true
			}
			if item["call_id"] == "call_b" {
				outputB = true
			}
		}
	}
	if !reasoning || !callA || !callB || !outputA || !outputB {
		t.Fatalf("replayed input = %#v", input)
	}
}

func terminalMessage(t *testing.T, provider *XAI, req core.Request) *core.Message {
	t.Helper()
	ch, err := provider.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	var final *core.Message
	terminals := 0
	for ev := range ch {
		if ev.IsTerminal() {
			terminals++
		}
		if ev.Type == core.ProviderEventError {
			t.Fatal(ev.Error)
		}
		if ev.Type == core.ProviderEventDone {
			final = ev.Message
		}
	}
	if terminals != 1 || final == nil {
		t.Fatalf("terminals=%d final=%#v", terminals, final)
	}
	return final
}

func TestStream_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"error":{"message":"invalid API key"}}`)
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	provider := New("bad-key")
	provider.client.Transport = rewriteTransport{target}
	_, err := provider.Stream(context.Background(), core.Request{Model: core.Model{ID: "grok-4.5"}, Options: core.StreamOptions{ThinkingLevel: "low"}})
	if err == nil || !strings.Contains(err.Error(), "authentication failed") || strings.Contains(err.Error(), "invalid API key") {
		t.Fatalf("error = %v, want xAI HTTP error", err)
	}
}

func TestClassifyHTTPError_SanitizesAndClassifiesQuota(t *testing.T) {
	secret := []byte(`{"message":"bad key secret-should-not-leak"}`)
	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusInternalServerError} {
		if err := classifyHTTPError(status, secret); strings.Contains(err.Error(), "secret-should-not-leak") {
			t.Fatalf("status %d leaked upstream body: %v", status, err)
		}
	}
	if _, ok := core.AsQuotaExceeded(classifyHTTPError(http.StatusTooManyRequests, []byte(`{"error":"quota exhausted"}`))); !ok {
		t.Fatal("quota 429 must be typed and terminal")
	}
}

func TestStream_Quota429IsNotRetried(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprint(w, `{"error":{"message":"credit quota exhausted"}}`)
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	provider := New("key")
	provider.client.Transport = rewriteTransport{target}
	_, err := provider.Stream(context.Background(), core.Request{Model: core.Model{ID: "grok-4.5"}, Options: core.StreamOptions{ThinkingLevel: "low"}})
	if _, ok := core.AsQuotaExceeded(err); !ok || requests != 1 {
		t.Fatalf("error=%v requests=%d; quota must be terminal", err, requests)
	}
}

func TestStream_SanitizesSSEError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: error\ndata: {\"type\":\"error\",\"message\":\"secret-should-not-leak\"}\n\n")
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	provider := New("key")
	provider.client.Transport = rewriteTransport{target}
	ch, err := provider.Stream(context.Background(), core.Request{Model: core.Model{ID: "grok-4.5"}, Options: core.StreamOptions{ThinkingLevel: "low"}})
	if err != nil {
		t.Fatal(err)
	}
	for ev := range ch {
		if ev.Type == core.ProviderEventError {
			if strings.Contains(ev.Error.Error(), "secret-should-not-leak") {
				t.Fatalf("SSE error leaked upstream detail: %v", ev.Error)
			}
			return
		}
	}
	t.Fatal("missing terminal error")
}
