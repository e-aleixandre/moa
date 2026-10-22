package agent

import (
	"context"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// ephemeralPositions runs a two-request tool loop whose inventory text comes
// from next() and returns the index of the ephemeral block in each request.
func ephemeralPositions(t *testing.T, next func() string) []int {
	t.Helper()
	reg := core.NewRegistry()
	if err := reg.Register(core.Tool{Name: "noop", Description: "noop", Parameters: []byte(`{"type":"object"}`), Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
		return core.TextResult("ok"), nil
	}}); err != nil {
		t.Fatal(err)
	}
	var pos []int
	record := func(req core.Request) {
		at := -1
		for i, m := range req.Messages {
			if m.Ephemeral {
				at = i
			}
		}
		pos = append(pos, at)
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
	ag, err := New(AgentConfig{Provider: prov, Model: core.Model{ID: "m", Provider: "openai"}, SystemPrompt: "s", Tools: reg, MaxTurns: 5, MaxToolCallsPerTurn: 5, MaxRunDuration: 10 * time.Second,
		EphemeralContext: func() *core.Message { m := core.NewUserMessage(next()); return &m }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ag.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	return pos
}

func TestEphemeralStaysAnchoredWhileUnchanged(t *testing.T) {
	pos := ephemeralPositions(t, func() string { return "inventory" })
	if len(pos) != 2 || pos[0] != 1 || pos[1] != 1 {
		t.Fatalf("positions = %v, want [1 1]", pos)
	}
}

func TestEphemeralReanchorsWhenInventoryChanges(t *testing.T) {
	n := 0
	pos := ephemeralPositions(t, func() string { n++; return "inventory " + string(rune('a'+n)) })
	// request 2 carries [user, assistant, tool_result, ephemeral].
	if len(pos) != 2 || pos[0] != 1 || pos[1] != 3 {
		t.Fatalf("positions = %v, want [1 3]", pos)
	}
}

func TestFrozenEphemeralReuse(t *testing.T) {
	m := core.Model{ID: "m", Provider: "p"}
	a, b := core.NewUserMessage("x"), core.NewUserMessage("x")
	f := frozenEphemeral{msg: &a, at: 2, epoch: 1, model: m}
	cases := []struct {
		name  string
		block *core.Message
		epoch int
		model core.Model
		hist  int
		want  bool
	}{
		{"same", &b, 1, m, 4, true},
		{"anchor gone", &b, 1, m, 1, false},
		{"compaction or trim", &b, 2, m, 4, false},
		{"model change", &b, 1, core.Model{ID: "n", Provider: "p"}, 4, false},
		{"block removed", nil, 1, m, 4, false},
	}
	for _, c := range cases {
		if got := f.reuse(c.block, c.epoch, c.model, c.hist); got != c.want {
			t.Errorf("%s: reuse = %v, want %v", c.name, got, c.want)
		}
	}
}
