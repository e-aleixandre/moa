package anthropic

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

// deferredSpecs is a registry-ordered tool list with one deferred MCP tool
// that sorts after every tool loaded up front.
func deferredSpecs() []core.ToolSpec {
	obj := json.RawMessage(`{"type":"object"}`)
	return []core.ToolSpec{
		{Name: "bash", Description: "Run", Parameters: obj},
		{Name: "mcp__playwright__browser_click", Description: "Click", Parameters: obj, Deferred: true},
		{Name: "read", Description: "Read", Parameters: obj},
		{Name: "tool_search", Description: "Search", Parameters: obj},
		{Name: "zz_mcp_like", Description: "Sorts last", Parameters: obj, Deferred: true},
	}
}

type wireBody struct {
	System   json.RawMessage   `json:"system"`
	Tools    []json.RawMessage `json:"tools"`
	Messages []struct {
		Role    string            `json:"role"`
		Content []json.RawMessage `json:"content"`
	} `json:"messages"`
}

func buildWire(t *testing.T, req core.Request, oauth bool) wireBody {
	t.Helper()
	raw, err := buildRequestBody(req, oauth)
	if err != nil {
		t.Fatal(err)
	}
	var b wireBody
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDeferredToolsPrecedeTheCachedLastTool(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		model, _ := core.ResolveModel("claude-opus-5-5")
		b := buildWire(t, core.Request{Model: model, Messages: []core.Message{core.NewUserMessage("hi")}, Tools: deferredSpecs()}, oauth)
		seenLoaded := false
		for i, raw := range b.Tools {
			var tl map[string]any
			_ = json.Unmarshal(raw, &tl)
			deferred := tl["defer_loading"] == true
			if deferred && seenLoaded {
				t.Fatalf("oauth=%v: deferred tool %v after a loaded one; the last tool must be loaded up front", oauth, tl["name"])
			}
			seenLoaded = seenLoaded || !deferred
			_, cached := tl["cache_control"]
			if deferred && cached {
				t.Fatalf("oauth=%v: cache_control on deferred tool %v is a 400", oauth, tl["name"])
			}
			if last := i == len(b.Tools)-1; last != cached {
				t.Fatalf("oauth=%v: cache_control on %v (index %d of %d), want only on the last tool", oauth, tl["name"], i, len(b.Tools))
			}
			if strings.HasPrefix(tl["name"].(string), "mcp__") != deferred && tl["name"] != "zz_mcp_like" {
				t.Fatalf("oauth=%v: %v deferred=%v", oauth, tl["name"], deferred)
			}
		}
		if !seenLoaded {
			t.Fatal("no tool loaded up front")
		}
	}
}

func searchHistory(target string) []core.Message {
	call := core.Message{Role: "assistant", Content: []core.Content{core.ToolCallContent("c1", "tool_search", map[string]any{"query": "click"})}}
	result := core.NewToolResultMessage("c1", "tool_search", []core.Content{
		core.TextContent("Loaded tools: " + target),
		core.ToolReferenceContent(target),
	}, false)
	return []core.Message{core.NewUserMessage("hi"), call, result}
}

// Loading a tool appends to the conversation and leaves the tool list and
// system prompt byte for byte as they were: the cached prefix survives.
func TestToolSearchLeavesThePrefixAlone(t *testing.T) {
	model, _ := core.ResolveModel("claude-opus-5-5")
	before := buildWire(t, core.Request{Model: model, System: "sys", Messages: []core.Message{core.NewUserMessage("hi")}, Tools: deferredSpecs()}, true)
	after := buildWire(t, core.Request{Model: model, System: "sys", Messages: searchHistory("mcp__playwright__browser_click"), Tools: deferredSpecs()}, true)
	if string(before.System) != string(after.System) {
		t.Fatal("system changed after a search")
	}
	if len(before.Tools) != len(after.Tools) {
		t.Fatal("tool list changed after a search")
	}
	for i := range before.Tools {
		if string(before.Tools[i]) != string(after.Tools[i]) {
			t.Fatalf("tool %d changed after a search:\n%s\n%s", i, before.Tools[i], after.Tools[i])
		}
	}
	var result struct {
		Type    string           `json:"type"`
		Content []map[string]any `json:"content"`
	}
	_ = json.Unmarshal(after.Messages[2].Content[0], &result)
	if result.Type != "tool_result" || len(result.Content) != 1 ||
		result.Content[0]["type"] != "tool_reference" || result.Content[0]["tool_name"] != "mcp__playwright__browser_click" {
		t.Fatalf("search result on the wire = %+v, want only the reference", result)
	}
}

// A reference whose tool is no longer in the request (its server removed)
// would 400 every later request. It goes as text instead.
func TestOrphanToolReferenceBecomesText(t *testing.T) {
	model, _ := core.ResolveModel("claude-opus-5-5")
	b := buildWire(t, core.Request{Model: model, Messages: searchHistory("mcp__gone__thing"), Tools: deferredSpecs()}, true)
	raw := string(b.Messages[2].Content[0])
	if strings.Contains(raw, "tool_reference") {
		t.Fatalf("orphan reference sent: %s", raw)
	}
	if !strings.Contains(raw, "Loaded tools: mcp__gone__thing") || !strings.Contains(raw, "No longer available: mcp__gone__thing") {
		t.Fatalf("orphan result = %s", raw)
	}
}

// With nothing deferred (the full-list fallback, or a model without tool
// search) the references go as text: the tools are loaded anyway.
func TestToolReferenceWithoutDeferralIsText(t *testing.T) {
	model, _ := core.ResolveModel("claude-opus-5-5")
	specs := deferredSpecs()
	for i := range specs {
		specs[i].Deferred = false
	}
	b := buildWire(t, core.Request{Model: model, Messages: searchHistory("mcp__playwright__browser_click"), Tools: specs}, false)
	raw := string(b.Messages[2].Content[0])
	if strings.Contains(raw, "tool_reference") || strings.Contains(raw, "No longer available") || !strings.Contains(raw, "Loaded tools") {
		t.Fatalf("result without deferral = %s", raw)
	}
	for _, tl := range b.Tools {
		if strings.Contains(string(tl), "defer_loading") {
			t.Fatalf("defer_loading without deferral: %s", tl)
		}
	}
}

const okSSE = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"m1","model":"claude-opus-5-5","usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

func drain(t *testing.T, ch <-chan core.AssistantEvent) {
	t.Helper()
	for ev := range ch {
		if ev.Type == core.ProviderEventError && ev.Error != nil {
			t.Fatalf("stream error: %v", ev.Error)
		}
	}
}

// An endpoint that refuses deferred tools gets the same request with every
// tool loaded, the session goes on, and later requests skip the refusal.
func TestDeferralRefusedFallsBackToTheFullList(t *testing.T) {
	var deferredSent []bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		deferred := strings.Contains(string(raw), "defer_loading") || strings.Contains(string(raw), "tool_reference")
		deferredSent = append(deferredSent, deferred)
		if deferred {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"tools.1.defer_loading: Extra inputs are not permitted"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, okSSE)
	}))
	defer srv.Close()

	model, _ := core.ResolveModel("claude-opus-5-5")
	a := NewWithBaseURL("sk-ant-api03-test", srv.URL)
	if !a.SupportsDeferredTools(model.ID) {
		t.Fatal("a current model must start with deferral on")
	}
	ch, err := a.Stream(context.Background(), core.Request{Model: model, Messages: searchHistory("mcp__playwright__browser_click"), Tools: deferredSpecs()})
	if err != nil {
		t.Fatalf("the refusal ended the turn instead of falling back: %v", err)
	}
	drain(t, ch)
	if len(deferredSent) != 2 || !deferredSent[0] || deferredSent[1] {
		t.Fatalf("requests (deferred?) = %v, want [true false]", deferredSent)
	}
	if a.SupportsDeferredTools(model.ID) {
		t.Fatal("a refused combination must not be offered deferral again")
	}
	if NewWithBaseURL("sk-ant-api03-test", srv.URL).SupportsDeferredTools(model.ID) == true {
		t.Fatal("the refusal must outlive the per-request provider")
	}
}

// A 400 that the full list does not cure was not about deferral: the error is
// the caller's, and deferral stays on.
func TestUnrelatedRefusalKeepsDeferral(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long"}}`)
	}))
	defer srv.Close()
	model, _ := core.ResolveModel("claude-opus-5-5")
	a := NewWithBaseURL("sk-ant-api03-test", srv.URL)
	_, err := a.Stream(context.Background(), core.Request{Model: model, Messages: []core.Message{core.NewUserMessage("hi")}, Tools: deferredSpecs()})
	if err == nil || !strings.Contains(err.Error(), "prompt is too long") {
		t.Fatalf("err = %v", err)
	}
	if calls != 2 || !a.SupportsDeferredTools(model.ID) {
		t.Fatalf("calls = %d, deferral = %v; want 2 and still on", calls, a.SupportsDeferredTools(model.ID))
	}
}

// Low credit is a terminal 400 of its own: no second request.
func TestLowCreditIsNotRetriedWithoutDeferral(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low"}}`)
	}))
	defer srv.Close()
	model, _ := core.ResolveModel("claude-opus-5-5")
	_, err := NewWithBaseURL("sk-ant-api03-test", srv.URL).Stream(context.Background(), core.Request{Model: model, Messages: []core.Message{core.NewUserMessage("hi")}, Tools: deferredSpecs()})
	if err == nil || calls != 1 {
		t.Fatalf("err = %v, calls = %d; want a low-credit error after one request", err, calls)
	}
}

func TestOldModelsDoNotDefer(t *testing.T) {
	for id, want := range map[string]bool{
		"claude-opus-5-5": true, "claude-sonnet-5-5": true, "claude-haiku-4-5-20251001": true, "claude-sonnet-4-5": true,
		"claude-3-5-haiku-latest": false, "claude-sonnet-4-20250514": false, "claude-opus-4-1": false,
	} {
		if got := modelDefersTools(id); got != want {
			t.Errorf("%s: %v, want %v", id, got, want)
		}
	}
}

// A search whose tools are partly gone still loads the ones that remain.
func TestPartlyOrphanSearchKeepsSurvivingReferences(t *testing.T) {
	model, _ := core.ResolveModel("claude-opus-5-5")
	msgs := searchHistory("mcp__playwright__browser_click")
	msgs[2].Content = append(msgs[2].Content, core.ToolReferenceContent("mcp__gone__thing"))
	b := buildWire(t, core.Request{Model: model, Messages: msgs, Tools: deferredSpecs()}, true)
	var result struct {
		Content []map[string]any `json:"content"`
	}
	_ = json.Unmarshal(b.Messages[2].Content[0], &result)
	if len(result.Content) != 1 || result.Content[0]["type"] != "tool_reference" || result.Content[0]["tool_name"] != "mcp__playwright__browser_click" {
		t.Fatalf("result = %+v, want only the surviving reference", result.Content)
	}
}

// The refusal reaches every credential kind on the endpoint: the capability
// is asked of the selected login, the request may go out on the API backup.
// Its log carries no upstream body, which can echo what was sent.
func TestDeferralRefusalIsPerEndpointAndLogsNoBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), "defer_loading") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"echo SYNTHETIC-SECRET"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, okSSE)
	}))
	defer srv.Close()
	var logs strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(prev)

	model, _ := core.ResolveModel("claude-opus-5-5")
	api := NewWithBaseURL("sk-ant-api03-test", srv.URL)
	ch, err := api.Stream(context.Background(), core.Request{Model: model, Messages: []core.Message{core.NewUserMessage("hi")}, Tools: deferredSpecs()})
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch)
	if NewWithBaseURL("sk-ant-oat01-test", srv.URL).SupportsDeferredTools(model.ID) {
		t.Fatal("an API refusal must stop deferral for the OAuth login on the same endpoint")
	}
	if strings.Contains(logs.String(), "SYNTHETIC-SECRET") || !strings.Contains(logs.String(), "deferred tools refused") {
		t.Fatalf("log = %s", logs.String())
	}
}
