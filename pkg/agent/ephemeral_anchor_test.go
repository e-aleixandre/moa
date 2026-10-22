package agent

import (
	"context"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// ephemeralPositions runs a two-request tool loop whose inventory text comes
// from next() and returns the index of the ephemeral block in each request.
func ephemeralPositions(t *testing.T, implicit bool, next func() string) []int {
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
	prov.implicitCache = implicit
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
	pos := ephemeralPositions(t, true, func() string { return "inventory" })
	if len(pos) != 2 || pos[0] != 1 || pos[1] != 1 {
		t.Fatalf("positions = %v, want [1 1]", pos)
	}
}

func TestEphemeralReanchorsWhenInventoryChanges(t *testing.T) {
	n := 0
	pos := ephemeralPositions(t, true, func() string { n++; return "inventory " + string(rune('a'+n)) })
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

// A provider with its own breakpoints (Anthropic) caches the prefix up to the
// last durable block, so the block must stay last there.
func TestEphemeralNotAnchoredWithoutImplicitPrefixCache(t *testing.T) {
	pos := ephemeralPositions(t, false, func() string { return "inventory" })
	if len(pos) != 2 || pos[0] != 1 || pos[1] != 3 {
		t.Fatalf("positions = %v, want [1 3]", pos)
	}
}

func TestRequestShapeReportsPrefixBreak(t *testing.T) {
	var s requestShape
	a, b, c, x := core.NewUserMessage("a"), core.NewUserMessage("b"), core.NewUserMessage("c"), core.NewUserMessage("X")
	s.log(core.Request{Messages: []core.Message{a, b}})
	if s.items != 2 {
		t.Fatalf("items = %d", s.items)
	}
	if atPrev, _ := messagesChain([]core.Message{a, b, c}, s.items); atPrev != s.chain {
		t.Fatal("an appended request must have the previous one as prefix")
	}
	if atPrev, _ := messagesChain([]core.Message{a, x, c}, s.items); atPrev == s.chain {
		t.Fatal("a rewrite must break the prefix")
	}
}

func TestRecalledAnchorNeedsIdenticalPrefix(t *testing.T) {
	a, b, x := core.NewUserMessage("a"), core.NewUserMessage("b"), core.NewUserMessage("X")
	blk := core.NewUserMessage("inv")
	_, h := messagesChain([]core.Message{a, b}, 0)
	ag := &Agent{ephemeralAnchor: frozenEphemeral{msg: &blk, at: 2, prefix: h}}
	cfg := &loopConfig{agent: ag}
	if got := cfg.recalledAnchor([]core.Message{a, b, x}); got.msg == nil {
		t.Fatal("identical prefix must recall the anchor")
	}
	if got := cfg.recalledAnchor([]core.Message{a, x, x}); got.msg != nil {
		t.Fatal("rewritten prefix must drop the anchor")
	}
	if got := cfg.recalledAnchor([]core.Message{a}); got.msg != nil {
		t.Fatal("shortened history must drop the anchor")
	}
}
