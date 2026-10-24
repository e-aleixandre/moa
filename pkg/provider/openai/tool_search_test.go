package openai

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func toolSearchReq(model string) core.Request {
	obj := json.RawMessage(`{"type":"object"}`)
	return core.Request{
		Model:    core.Model{ID: model, Provider: "openai"},
		Messages: []core.Message{core.NewUserMessage("hi")},
		Tools: []core.ToolSpec{
			{Name: "bash", Description: "Run", Parameters: obj},
			{Name: "mcp__playwright__browser_click", Description: "Click", Parameters: obj, Deferred: true},
			{Name: core.ToolSearchName, Description: "Search", Parameters: obj},
		},
	}
}

const toolSearchOKSSE = `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1"}}

data: {"type":"response.output_text.delta","output_index":0,"delta":"ok"}

data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-6.1-sol","status":"completed","usage":{"input_tokens":10,"output_tokens":1,"total_tokens":11}}}

`

func planProvider(url string) *OpenAI {
	p := NewOAuth("tok", "acct-1", nil)
	p.baseURL = url
	p.endpoint = codexEndpoint
	return p
}

func drainStream(t *testing.T, ch <-chan core.AssistantEvent) {
	t.Helper()
	for ev := range ch {
		if ev.Type == core.ProviderEventError {
			t.Fatalf("stream error: %v", ev.Error)
		}
	}
}

// Tool search is verified on the plan transport for every model it serves;
// the paid API has not been probed and keeps the full list.
func TestToolSearchOnlyOnThePlanForCurrentModels(t *testing.T) {
	plan := NewOAuth("tok", "acct", nil)
	for id, want := range map[string]bool{
		"gpt-6.1-sol": true, "gpt-6-sol": true, "gpt-6-luna": true, "gpt-6-astra": true, "gpt-5.6-sol": true,
		"gpt-5.6-terra": true, "gpt-5.4-mini": true, "gpt-daybreak-blue-latest": true,
		"gpt-5.3-codex": false, "gpt-5.2-codex": false, "gpt-4o": false, "o3": false,
	} {
		if got := plan.SupportsDeferredTools(id); got != want {
			t.Errorf("plan %s: %v, want %v", id, got, want)
		}
	}
	if New("sk-test").SupportsDeferredTools("gpt-6.1-sol") {
		t.Error("the API key transport must not defer: it was never probed")
	}
}

func TestToolSearchRefusedFallsBackToTheFullList(t *testing.T) {
	var native []bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		n := strings.Contains(string(raw), `"tool_search"`) && strings.Contains(string(raw), `"execution":"client"`)
		native = append(native, n)
		if n {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"Unsupported tool type: tool_search SYNTHETIC-SECRET"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, toolSearchOKSSE)
	}))
	defer srv.Close()
	var logs strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(prev)

	const model = "gpt-6-luna"
	p := planProvider(srv.URL)
	if !p.SupportsDeferredTools(model) {
		t.Fatal("a plan model must start with deferral on")
	}
	ch, err := p.Stream(context.Background(), toolSearchReq(model))
	if err != nil {
		t.Fatalf("the refusal ended the turn instead of falling back: %v", err)
	}
	drainStream(t, ch)
	if len(native) != 2 || !native[0] || native[1] {
		t.Fatalf("requests (native tool search?) = %v, want [true false]", native)
	}
	if planProvider(srv.URL).SupportsDeferredTools(model) {
		t.Fatal("a refused combination must not be offered deferral again")
	}
	if !planProvider(srv.URL).SupportsDeferredTools("gpt-6.1-sol") {
		t.Fatal("the refusal is per model")
	}
	if strings.Contains(logs.String(), "SYNTHETIC-SECRET") || !strings.Contains(logs.String(), "tool search refused") {
		t.Fatalf("log = %s", logs.String())
	}
}

// A 400 the full list does not cure was not about tool search: the error is
// the caller's, and deferral stays on.
func TestUnrelatedRefusalKeepsToolSearch(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"context_length_exceeded"}}`)
	}))
	defer srv.Close()
	const model = "gpt-6-astra"
	p := planProvider(srv.URL)
	_, err := p.Stream(context.Background(), toolSearchReq(model))
	if err == nil || !strings.Contains(err.Error(), "context_length_exceeded") {
		t.Fatalf("err = %v", err)
	}
	if calls != 2 || !p.SupportsDeferredTools(model) {
		t.Fatalf("calls = %d, deferral = %v; want 2 and still on", calls, p.SupportsDeferredTools(model))
	}
}

// Without deferred tools there is nothing to fall back from: one request.
func TestRefusalWithoutDeferredToolsIsNotRetried(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"bad"}}`)
	}))
	defer srv.Close()
	req := toolSearchReq("gpt-6-sol")
	req.Tools = req.Tools[:1]
	if _, err := planProvider(srv.URL).Stream(context.Background(), req); err == nil || calls != 1 {
		t.Fatalf("err = %v, calls = %d", err, calls)
	}
}

// The fallback resends the caller's request: Daybreak's cache routing is
// derived once, not again on top of the first attempt's.
func TestToolSearchFallbackKeepsTheCacheRouting(t *testing.T) {
	var keys, sessions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Key string `json:"prompt_cache_key"`
		}
		_ = json.Unmarshal(raw, &body)
		keys = append(keys, body.Key)
		sessions = append(sessions, r.Header.Get("session-id"))
		if strings.Contains(string(raw), `"execution":"client"`) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"unsupported"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, toolSearchOKSSE)
	}))
	defer srv.Close()
	req := toolSearchReq("gpt-6-sol")
	req.Options.PromptCacheKey = "moa:session:abc"
	req.Options.AccessProgram = "daybreak"
	ch, err := planProvider(srv.URL).Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	drainStream(t, ch)
	if len(keys) != 2 || keys[0] != keys[1] || sessions[0] != sessions[1] || keys[0] != "moa:session:abc:daybreak" {
		t.Fatalf("keys = %q, sessions = %q; want the same Daybreak routing twice", keys, sessions)
	}
}
