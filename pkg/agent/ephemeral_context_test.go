package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// The ephemeral block is per request: an ordinary request evaluates it afresh,
// but a continuation of a request already sent (a transport repair, a
// pause_turn resubmit) resends the block that request carried, in the same
// place, so what the model was shown is not rewritten under it.

type ephemeralScene struct {
	mu     sync.Mutex
	text   string // what the block says now
	reqs   [][]string
	evalAt int // how many times the block was evaluated
}

func (s *ephemeralScene) set(text string) {
	s.mu.Lock()
	s.text = text
	s.mu.Unlock()
}

func (s *ephemeralScene) block() *core.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evalAt++
	if s.text == "" {
		return nil
	}
	m := core.NewUserMessage("<open_questions>" + s.text + "</open_questions>")
	return &m
}

// shape renders a request as one label per message.
func shape(req core.Request) []string {
	var out []string
	for _, m := range req.Messages {
		text := joinText(m.Content)
		switch {
		case strings.Contains(text, "transport error"):
			text = "HINT"
		case strings.HasPrefix(text, "<open_questions>"):
			text = "EPH(" + strings.TrimSuffix(strings.TrimPrefix(text, "<open_questions>"), "</open_questions>") + ")"
		}
		out = append(out, m.Role+":"+text)
	}
	return out
}

func (s *ephemeralScene) record(req core.Request) {
	s.mu.Lock()
	s.reqs = append(s.reqs, shape(req))
	s.mu.Unlock()
}

func (s *ephemeralScene) requests() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]string(nil), s.reqs...)
}

func newEphemeralAgent(t *testing.T, scene *ephemeralScene, compaction *core.CompactionSettings, maxInput int, handlers ...func(core.Request) (<-chan core.AssistantEvent, error)) (*Agent, *MockProvider) {
	t.Helper()
	wrapped := make([]func(core.Request) (<-chan core.AssistantEvent, error), len(handlers))
	for i, h := range handlers {
		wrapped[i] = func(req core.Request) (<-chan core.AssistantEvent, error) {
			scene.record(req)
			return h(req)
		}
	}
	prov := NewMockProvider(wrapped...)
	ag, err := New(AgentConfig{
		Provider:            prov,
		Model:               core.Model{ID: "test-model", Provider: "mock", MaxInput: maxInput},
		SystemPrompt:        "test",
		Tools:               core.NewRegistry(),
		MaxTurns:            10,
		MaxToolCallsPerTurn: 5,
		MaxRunDuration:      30 * time.Second,
		Compaction:          compaction,
		EphemeralContext:    scene.block,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ag, prov
}

func partialThenError(onPartial func()) func(core.Request) (<-chan core.AssistantEvent, error) {
	return func(core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 2)
		ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: "Hello"}
		if onPartial != nil {
			onPartial()
		}
		ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: errors.New("openai: unknown error")}
		close(ch)
		return ch, nil
	}
}

func TestEphemeralBlockIsFrozenAcrossATransportRepair(t *testing.T) {
	old := streamRepairBackoff
	streamRepairBackoff = []time.Duration{0, 0}
	t.Cleanup(func() { streamRepairBackoff = old })

	scene := &ephemeralScene{text: "Q1 open"}
	ag, _ := newEphemeralAgent(t, scene, nil, 0,
		// The question is answered while the partial streams.
		partialThenError(func() { scene.set("") }),
		simpleTextResponse(" world"),
		simpleTextResponse("next"),
	)
	if _, err := ag.Run(context.Background(), "start"); err != nil {
		t.Fatal(err)
	}
	reqs := scene.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want the request and its repair", len(reqs))
	}
	if want := []string{"user:start", "user:EPH(Q1 open)"}; !slices.Equal(reqs[0], want) {
		t.Fatalf("request = %v, want %v", reqs[0], want)
	}
	// Same block, same place: before the partial and the hint, not rewritten
	// (the question is already closed) and not dropped.
	if want := []string{"user:start", "user:EPH(Q1 open)", "assistant:Hello", "user:HINT"}; !slices.Equal(reqs[1], want) {
		t.Fatalf("repair = %v, want %v", reqs[1], want)
	}

	// The next ordinary request refreshes it: nothing is open any more.
	if _, err := ag.Send(context.Background(), "again"); err != nil {
		t.Fatal(err)
	}
	last := scene.requests()[2]
	for _, m := range last {
		if strings.Contains(m, "EPH(") {
			t.Fatalf("the next ordinary request still carries the frozen block: %v", last)
		}
	}
	for _, m := range ag.Messages() {
		if strings.Contains(joinText(m.Content), "open_questions") {
			t.Fatalf("the block entered the conversation: %+v", m)
		}
	}
}

func TestEphemeralBlockChangesOnlyAtTheNextOrdinaryBoundary(t *testing.T) {
	old := streamRepairBackoff
	streamRepairBackoff = []time.Duration{0, 0}
	t.Cleanup(func() { streamRepairBackoff = old })

	scene := &ephemeralScene{text: "Q1"}
	ag, _ := newEphemeralAgent(t, scene, nil, 0,
		// Q1 is answered and Q2 posted while the first partial streams; the
		// second partial fails again.
		partialThenError(func() { scene.set("Q2") }),
		partialThenError(nil),
		simpleTextResponse(" done"),
		simpleTextResponse("later"),
	)
	if _, err := ag.Run(context.Background(), "start"); err != nil {
		t.Fatal(err)
	}
	for i, req := range scene.requests() {
		if !slices.Contains(req, "user:EPH(Q1)") || slices.Contains(req, "user:EPH(Q2)") {
			t.Fatalf("request %d = %v: a continuation must keep the block its request was sent with", i, req)
		}
	}
	// The next ordinary request is the boundary where it refreshes.
	if _, err := ag.Send(context.Background(), "again"); err != nil {
		t.Fatal(err)
	}
	next := scene.requests()[3]
	if !slices.Contains(next, "user:EPH(Q2)") || slices.Contains(next, "user:EPH(Q1)") {
		t.Fatalf("the next ordinary request = %v, want the refreshed block", next)
	}
}

func TestEphemeralBlockIsFrozenAcrossPauseTurn(t *testing.T) {
	scene := &ephemeralScene{text: "Q1"}
	ag, _ := newEphemeralAgent(t, scene, nil, 0,
		func(req core.Request) (<-chan core.AssistantEvent, error) {
			scene.set("Q2") // changes while the paused response is produced
			return stopReasonResponse("Thinking...", "pause_turn", "", nil)(req)
		},
		simpleTextResponse("answer"),
	)
	if _, err := ag.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	reqs := scene.requests()
	if want := []string{"user:go", "user:EPH(Q1)"}; !slices.Equal(reqs[0], want) {
		t.Fatalf("request = %v, want %v", reqs[0], want)
	}
	// The resubmit finishes the paused request: the block stays where the model
	// saw it, before the paused reply, rather than moving to the end.
	if want := []string{"user:go", "user:EPH(Q1)", "assistant:Thinking..."}; !slices.Equal(reqs[1], want) {
		t.Fatalf("resubmit = %v, want %v", reqs[1], want)
	}
}

func TestEphemeralBlockOfAFreshRequestFollowsTheToolResults(t *testing.T) {
	scene := &ephemeralScene{text: "Q1"}
	reg := core.NewRegistry()
	_ = reg.Register(core.Tool{Name: "noop", Description: "noop", Parameters: []byte(`{"type":"object"}`),
		Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
			scene.set("Q2") // the batch changes the open questions
			return core.TextResult("ok"), nil
		}})
	prov := NewMockProvider(
		func(req core.Request) (<-chan core.AssistantEvent, error) {
			scene.record(req)
			return stopReasonToolCallResponse("c1", "noop", "tool_use")(req)
		},
		func(req core.Request) (<-chan core.AssistantEvent, error) {
			scene.record(req)
			return simpleTextResponse("done")(req)
		},
	)
	ag, err := New(AgentConfig{
		Provider: prov, Model: core.Model{ID: "test-model", Provider: "mock"}, SystemPrompt: "test", Tools: reg,
		MaxTurns: 10, MaxToolCallsPerTurn: 5, MaxRunDuration: 30 * time.Second, EphemeralContext: scene.block,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ag.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	reqs := scene.requests()
	last := reqs[1]
	if last[len(last)-1] != "user:EPH(Q2)" || slices.Contains(last, "user:EPH(Q1)") {
		t.Fatalf("the request after the batch = %v, want the refreshed block last", last)
	}
}

// The block counts in the size the compaction check judges: the same history
// that fits alone is compacted when the block tips it over.
func TestEphemeralBlockCountsInTheCompactionCheck(t *testing.T) {
	run := func(blockText string) int {
		scene := &ephemeralScene{text: blockText}
		settings := core.CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecent: 200}
		big := strings.Repeat("x", 2000)
		ag, prov := newEphemeralAgent(t, scene, &settings, 1000,
			largeTextResponse(big, &core.Usage{TotalTokens: 800}),
			simpleTextResponse("## Goal\nsummary"),
			simpleTextResponse("second"),
		)
		if _, err := ag.Run(context.Background(), "hello"); err != nil {
			t.Fatal(err)
		}
		// Reading the calls through a second Send keeps the first request out of it.
		scene.set(blockText)
		if _, err := ag.Send(context.Background(), "continue"); err != nil {
			t.Fatal(err)
		}
		return prov.calls
	}
	if calls := run(""); calls != 2 {
		t.Fatalf("without a block: %d provider calls, want 2 (no compaction)", calls)
	}
	if calls := run(strings.Repeat("an open question ", 60)); calls != 3 {
		t.Fatalf("with a block that tips the window: %d provider calls, want 3 (compaction ran)", calls)
	}
}

func TestContextExceedsAndRequestOverHardSeeTheBlock(t *testing.T) {
	cfg := &loopConfig{
		model:        core.Model{ID: "m", MaxInput: 1000},
		systemPrompt: "s",
		state:        &AgentState{},
		stateMu:      &sync.Mutex{},
	}
	cfg.state.Messages = []core.AgentMessage{core.WrapMessage(core.NewUserMessage(strings.Repeat("y", 3000)))} // ~750 tokens
	settings := &core.CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecent: 10}
	s := requestSettings{model: cfg.model, compaction: settings}

	if contextExceeds(cfg, s, nil, 0) {
		t.Fatal("the history alone is under the threshold")
	}
	if !contextExceeds(cfg, s, nil, 200) {
		t.Fatal("the block must count towards the threshold")
	}

	req := []core.Message{cfg.state.Messages[0].Message}
	if over, _, _ := requestOverHard(cfg, settings, req, nil); over {
		t.Fatal("the request without the block fits")
	}
	req = append(req, core.NewUserMessage(strings.Repeat("z", 1200)))
	if over, _, _ := requestOverHard(cfg, settings, req, nil); !over {
		t.Fatal("a request whose block pushes it past the hard limit must be refused")
	}
}
