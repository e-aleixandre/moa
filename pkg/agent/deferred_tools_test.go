package agent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/toolsearch"
)

// deferringProvider is a MockProvider that loads deferred tools on demand,
// as Anthropic does.
type deferringProvider struct {
	*MockProvider
	defers bool
}

func (p *deferringProvider) SupportsDeferredTools(string) bool { return p.defers }

func deferredTestRegistry(t *testing.T, mcpCalls *atomic.Int32) *core.Registry {
	t.Helper()
	reg := core.NewRegistry()
	for _, tl := range []core.Tool{
		{Name: "read", Description: "Read a file", Parameters: []byte(`{"type":"object"}`), Effect: core.EffectReadOnly,
			Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) { return core.TextResult("ok"), nil }},
		{Name: "mcp__playwright__browser_click", Description: "Perform click on a web page", Deferrable: true,
			Parameters: []byte(`{"type":"object","properties":{"ref":{"type":"string","description":"Element reference"}}}`),
			Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
				mcpCalls.Add(1)
				return core.TextResult("clicked"), nil
			}},
		{Name: "mcp__lovable__list_projects", Description: "List Lovable projects", Deferrable: true, Parameters: []byte(`{"type":"object"}`),
			Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) { return core.TextResult("[]"), nil }},
	} {
		if err := reg.Register(tl); err != nil {
			t.Fatal(err)
		}
	}
	return reg
}

func TestRequestToolsDefersMCPOnlyWhereSupported(t *testing.T) {
	var n atomic.Int32
	reg := deferredTestRegistry(t, &n)

	full, deferred := requestTools(&deferringProvider{MockProvider: NewMockProvider()}, "m", reg.Specs(), reg)
	if deferred != nil {
		t.Fatalf("a provider without deferral got deferred tools: %v", deferred)
	}
	if want := reg.Specs(); len(full) != len(want) {
		t.Fatalf("tools changed without deferral: %d vs %d", len(full), len(want))
	}
	for _, s := range full {
		if s.Deferred || s.Name == toolsearch.Name {
			t.Fatalf("without deferral the list must be the registry's, got %+v", s)
		}
	}

	specs, deferred := requestTools(&deferringProvider{MockProvider: NewMockProvider(), defers: true}, "m", reg.Specs(), reg)
	if len(deferred) != 2 {
		t.Fatalf("deferred = %v, want the two MCP tools", deferred)
	}
	var names []string
	for _, s := range specs {
		names = append(names, s.Name)
		if s.Deferred != strings.HasPrefix(s.Name, "mcp__") {
			t.Fatalf("%s deferred=%v: only MCP tools are deferred", s.Name, s.Deferred)
		}
	}
	if got := strings.Join(names, ","); got != "mcp__lovable__list_projects,mcp__playwright__browser_click,read,tool_search" {
		t.Fatalf("names = %s", got)
	}
	idx := specs[len(specs)-1].Description
	for _, want := range []string{"- lovable (1 tools", "list_projects", "- playwright (1 tools", "browser_click"} {
		if !strings.Contains(idx, want) {
			t.Fatalf("tool_search index lacks %q:\n%s", want, idx)
		}
	}
}

// A deferred tool called before any search is refused with its schema and
// is not run; once a tool_search result has loaded it, the same call runs.
func TestDeferredToolRunsOnlyOnceLoaded(t *testing.T) {
	var mcpCalls atomic.Int32
	reg := deferredTestRegistry(t, &mcpCalls)
	var results []core.Message
	capture := func(req core.Request) {
		if n := len(req.Messages); n > 0 && req.Messages[n-1].Role == "tool_result" {
			results = append(results, req.Messages[n-1])
		}
	}
	var sent []core.Request
	step := func(h func(core.Request) (<-chan core.AssistantEvent, error)) func(core.Request) (<-chan core.AssistantEvent, error) {
		return func(req core.Request) (<-chan core.AssistantEvent, error) {
			sent = append(sent, req)
			capture(req)
			return h(req)
		}
	}
	click := "mcp__playwright__browser_click"
	prov := &deferringProvider{defers: true, MockProvider: NewMockProvider(
		step(toolCallResponse("c1", click, map[string]any{"ref": "e1"})),
		step(toolCallResponse("c2", toolsearch.Name, map[string]any{"query": "click a web page"})),
		step(toolCallResponse("c3", click, map[string]any{"ref": "e1"})),
		step(simpleTextResponse("done")),
	)}
	ag, err := New(AgentConfig{Provider: prov, Model: core.Model{ID: "m", Provider: "anthropic"}, SystemPrompt: "s", Tools: reg,
		MaxTurns: 6, MaxToolCallsPerTurn: 5, MaxRunDuration: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ag.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("tool results = %d, want 3", len(results))
	}

	notLoaded := results[0]
	text := notLoaded.Content[0].Text
	if !notLoaded.IsError || !strings.Contains(text, "not loaded") || !strings.Contains(text, `"ref"`) || !strings.Contains(text, toolsearch.Name) {
		t.Fatalf("unloaded call: want an error with the schema and how to load it, got %+v", notLoaded)
	}
	if strings.Contains(text, "unknown tool") {
		t.Fatalf("unloaded call reported as unknown: %s", text)
	}

	search := results[1]
	var refs []string
	for _, c := range search.Content {
		if c.Type == "tool_reference" {
			refs = append(refs, c.ToolName)
		}
	}
	if search.IsError || len(refs) == 0 || refs[0] != click {
		t.Fatalf("search result = %+v, want a reference to %s first", search, click)
	}

	if results[2].IsError || mcpCalls.Load() != 1 {
		t.Fatalf("loaded call: result %+v, executions %d; want one successful run", results[2], mcpCalls.Load())
	}

	// The search adds to the conversation, never to the tool list.
	for i := 1; i < len(sent); i++ {
		if toolsHash(sent[i].Tools) != toolsHash(sent[0].Tools) {
			t.Fatalf("request %d changed the tool list", i)
		}
	}
}

// Without deferral (OpenAI and the rest) there is no tool_search, and an MCP
// tool runs on first call as before.
func TestNoDeferralKeepsDirectCalls(t *testing.T) {
	var mcpCalls atomic.Int32
	reg := deferredTestRegistry(t, &mcpCalls)
	var first core.Request
	prov := &deferringProvider{MockProvider: NewMockProvider(
		func(req core.Request) (<-chan core.AssistantEvent, error) {
			first = req
			return toolCallResponse("c1", "mcp__playwright__browser_click", map[string]any{"ref": "e1"})(req)
		},
		simpleTextResponse("done"),
	)}
	ag, err := New(AgentConfig{Provider: prov, Model: core.Model{ID: "m", Provider: "openai"}, SystemPrompt: "s", Tools: reg,
		MaxTurns: 4, MaxToolCallsPerTurn: 5, MaxRunDuration: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ag.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if mcpCalls.Load() != 1 {
		t.Fatalf("MCP executions = %d, want 1", mcpCalls.Load())
	}
	for _, s := range first.Tools {
		if s.Deferred || s.Name == toolsearch.Name {
			t.Fatalf("request without deferral carries %+v", s)
		}
	}
}

type kaDeferringProvider struct{ *kaProvider }

func (kaDeferringProvider) SupportsDeferredTools(string) bool { return true }

// Keep-alive compares the next request's tools with the last one's. With
// deferral those carry tool_search and the deferred flags: comparing against
// the registry's bare specs would never match and an idle owner with MCP
// would go cold.
func TestKeepAlivePingsWithDeferredTools(t *testing.T) {
	var n atomic.Int32
	h := newKAHarness(t, core.KeepAliveRoleOwner, 250_000, "1h")
	prov := kaDeferringProvider{h.prov}
	a, err := New(AgentConfig{Provider: prov, Model: kaModel, SystemPrompt: "system", ThinkingLevel: "high", CacheTTL: "1h", Tools: deferredTestRegistry(t, &n)})
	if err != nil {
		t.Fatal(err)
	}
	h.agent.SetKeepAlive(nil)
	h.agent = a
	a.SetKeepAlive(&h.cfg)
	t.Cleanup(func() { a.SetKeepAlive(nil) })
	h.send("hello")
	if tools := h.prov.streams[0].Tools; tools[len(tools)-1].Name != toolsearch.Name {
		t.Fatalf("request without tool_search: %+v", tools)
	}
	h.advance(55 * time.Minute)
	if got := h.prov.pingCount(); got != 1 {
		t.Fatalf("pings = %d, want 1", got)
	}
}
