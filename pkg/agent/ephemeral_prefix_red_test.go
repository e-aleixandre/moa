package agent

// Diagnostic RED for an unchanged request-only block moving across an ordinary
// tool boundary. It demonstrates local wire-prefix churn, not a remote cache miss.
import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider/responses"
)

func TestREDUnchangedEphemeralKeepsOpenAIPrefix(t *testing.T) {
	reg := core.NewRegistry()
	if err := reg.Register(core.Tool{Name: "noop", Description: "noop", Parameters: []byte(`{"type":"object"}`), Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
		return core.TextResult("ok"), nil
	}}); err != nil {
		t.Fatal(err)
	}
	var bodies [][]byte
	record := func(req core.Request) {
		b, err := responses.BuildRequestBody(req, responses.Dialect{Provider: "openai", Model: req.Model.ID, SupportsPromptCacheKey: true, SupportsAccessPrograms: true})
		if err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, b)
	}
	prov := NewMockProvider(
		func(req core.Request) (<-chan core.AssistantEvent, error) {
			record(req)
			return stopReasonToolCallResponse("c1", "noop", "tool_use")(req)
		},
		func(req core.Request) (<-chan core.AssistantEvent, error) {
			record(req)
			return simpleTextResponse("done")(req)
		},
	)
	ag, err := New(AgentConfig{Provider: prov, Model: core.Model{ID: "gpt-6.1-sol", Provider: "openai"}, SystemPrompt: "fixed instructions", Tools: reg, MaxTurns: 5, MaxToolCallsPerTurn: 5, MaxRunDuration: 10 * time.Second, EphemeralContext: func() *core.Message { m := core.NewUserMessage("<recent_news>unchanged</recent_news>"); return &m }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ag.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("captured %d local mock requests, want 2", len(bodies))
	}
	var a, b struct {
		Input []json.RawMessage `json:"input"`
	}
	if err = json.Unmarshal(bodies[0], &a); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(bodies[1], &b); err != nil {
		t.Fatal(err)
	}
	for i, item := range a.Input {
		if i >= len(b.Input) || !bytes.Equal(item, b.Input[i]) {
			t.Fatalf("unchanged ephemeral destroyed previous wire prefix at input[%d]: previous_items=%d current_items=%d; no remote provider called", i, len(a.Input), len(b.Input))
		}
	}
}
