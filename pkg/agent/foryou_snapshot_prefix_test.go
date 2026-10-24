package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider/responses"
)

// forYouStages is the For you state the model sees across a session: a
// question opens, news is published, the user reads it, the question is
// answered, more news arrives. Every stage differs from the previous one.
var forYouStages = []string{
	"Q1 open",
	"Q1 open; N1 unread",
	"Q1 open; N1 read",
	"none open; N1 read",
	"none open; N1 read; N2 unread",
	"none open; N1 read; N2 read",
}

// forYouScene advances one stage per tool call, so the state changes while a
// tool runs, between the requests of one run.
type forYouScene struct {
	mu    sync.Mutex
	stage int
}

func (s *forYouScene) advance() {
	s.mu.Lock()
	if s.stage < len(forYouStages)-1 {
		s.stage++
	}
	s.mu.Unlock()
}

func (s *forYouScene) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return "FORYOU[" + forYouStages[s.stage] + "]"
}

func forYouRegistry(t *testing.T, scene *forYouScene) *core.Registry {
	t.Helper()
	reg := core.NewRegistry()
	if err := reg.Register(core.Tool{Name: "noop", Description: "noop", Parameters: []byte(`{"type":"object"}`), Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
		scene.advance()
		return core.TextResult("ok"), nil
	}}); err != nil {
		t.Fatal(err)
	}
	return reg
}

// newForYouAgent builds an agent whose For you state is scene, with a save
// that always succeeds (snapshots ask for a durable admission).
func newForYouAgent(t *testing.T, cfg AgentConfig, scene *forYouScene) *Agent {
	t.Helper()
	cfg.ForYouSnapshot = func() (string, bool) { return scene.text(), false }
	ag, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ag.SetProviderWaitSave(func(context.Context) error { return nil })
	return ag
}

func assertExtends(t *testing.T, label string, all [][][]byte) {
	t.Helper()
	for i := 1; i < len(all); i++ {
		if at := firstRewrite(all[i-1], all[i]); at >= 0 {
			after := []byte("<missing>")
			if at < len(all[i]) {
				after = all[i][at]
			}
			t.Errorf("%s: request %d rewrote item %d of request %d:\nbefore %s\nafter  %s", label, i+1, at, i, all[i-1][at], after)
		}
	}
}

// Every change of For you state (question opened, news published, news read,
// question answered) leaves the previous request byte-identical on Anthropic,
// inside a tool loop and across runs: the new state is appended, never moved.
func TestForYouChangesKeepAnthropicPrefix(t *testing.T) {
	scene := &forYouScene{}
	prov, bodies := anthropicAnchorServer(t, "tool", "tool", "text", "tool", "tool", "text")
	ag := newForYouAgent(t, AgentConfig{Provider: prov, Model: anthropicTailModel(t), ThinkingLevel: "high", SystemPrompt: "s",
		Tools: forYouRegistry(t, scene), MaxTurns: 10, MaxToolCallsPerTurn: 5, MaxRunDuration: 10 * time.Second}, scene)
	sendAnchor(t, ag, "first task")
	scene.advance() // a change between runs
	sendAnchor(t, ag, "second task")
	got := bodies()
	if len(got) != 6 {
		t.Fatalf("got %d requests, want 6", len(got))
	}
	all := make([][][]byte, len(got))
	for i, b := range got {
		all[i] = wireMessages(t, b)
		if !bytes.Contains(b, []byte(scene.textAt(stageOf(i)))) {
			t.Errorf("request %d does not carry the current state %q", i+1, scene.textAt(stageOf(i)))
		}
	}
	assertExtends(t, "anthropic", all)
}

// Same on OpenAI Responses, item by item.
func TestForYouChangesKeepOpenAIPrefix(t *testing.T) {
	scene := &forYouScene{}
	var mu sync.Mutex
	var bodies [][]byte
	record := func(req core.Request) {
		b, err := responses.BuildRequestBody(req, responses.Dialect{Provider: "openai", Model: req.Model.ID, SupportsPromptCacheKey: true, SupportsAccessPrograms: true})
		if err != nil {
			t.Error(err)
		}
		mu.Lock()
		bodies = append(bodies, b)
		mu.Unlock()
	}
	tool := func(id string) func(core.Request) (<-chan core.AssistantEvent, error) {
		return func(req core.Request) (<-chan core.AssistantEvent, error) {
			record(req)
			return stopReasonToolCallResponse(id, "noop", "tool_use")(req)
		}
	}
	text := func(req core.Request) (<-chan core.AssistantEvent, error) {
		record(req)
		return simpleTextResponse("done")(req)
	}
	prov := NewMockProvider(tool("c1"), tool("c2"), text, tool("c3"), tool("c4"), text)
	ag := newForYouAgent(t, AgentConfig{Provider: prov, Model: core.Model{ID: "gpt-6.1-sol", Provider: "openai"}, SystemPrompt: "fixed",
		Tools: forYouRegistry(t, scene), MaxTurns: 10, MaxToolCallsPerTurn: 5, MaxRunDuration: 10 * time.Second}, scene)
	sendAnchor(t, ag, "first task")
	scene.advance()
	sendAnchor(t, ag, "second task")
	if len(bodies) != 6 {
		t.Fatalf("got %d requests, want 6", len(bodies))
	}
	all := make([][][]byte, len(bodies))
	for i, b := range bodies {
		var req struct {
			Input []json.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(b, &req); err != nil {
			t.Fatal(err)
		}
		for _, item := range req.Input {
			all[i] = append(all[i], item)
		}
		if !bytes.Contains(b, []byte(scene.textAt(stageOf(i)))) {
			t.Errorf("request %d does not carry the current state %q", i+1, scene.textAt(stageOf(i)))
		}
	}
	assertExtends(t, "openai", all)
}

// stageOf is the stage request i (0-based) of the scripts above sees: one
// advance per tool call, plus one between the runs (before request 4).
func stageOf(i int) int {
	return [...]int{0, 1, 2, 3, 4, 5}[i]
}

func (s *forYouScene) textAt(stage int) string {
	return fmt.Sprintf("FORYOU[%s]", forYouStages[stage])
}
