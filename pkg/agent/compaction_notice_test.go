package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// The notice has to say what to do, not just how much room is left: told only
// the number, the model carried on unchanged in every measured run against the
// real API.
func TestCompactionNotice_TellsTheAgentWhatToDo(t *testing.T) {
	text := ""
	for _, c := range compactionNotice(45000).Content {
		text += c.Text
	}

	if !strings.Contains(text, "45k") {
		t.Errorf("the notice should state the remaining room, got: %s", text)
	}
	if !strings.Contains(text, "persist them now") {
		t.Errorf("the notice must ask the agent to persist unsaved work, got: %s", text)
	}
	// Without this an agent with nothing pending announces the notice to the
	// user, which reads as noise.
	if !strings.Contains(text, "carry on without comment") {
		t.Errorf("the notice should keep quiet when there is nothing to save, got: %s", text)
	}
}

// The notice is moa speaking, not the user. Marking it is what lets the UI and
// the transcript tell them apart.
func TestCompactionNotice_IsMarkedInternal(t *testing.T) {
	msg := compactionNotice(1000)
	if msg.Custom["source"] != "compaction_notice" {
		t.Errorf("source = %v, want compaction_notice", msg.Custom["source"])
	}
	if msg.Custom["internal"] != true {
		t.Error("the notice should be marked internal")
	}
}

// Warning at the threshold would be useless: compaction happens that same turn.
func TestShouldWarnBeforeCompact_Window(t *testing.T) {
	settings := core.CompactionSettings{Enabled: true}
	const window = 100_000

	tests := []struct {
		name   string
		tokens int
		want   bool
	}{
		{"plenty of room", 50_000, false},
		{"outside the band", 70_000, false},
		{"inside the band", 86_000, true},
		{"already over the threshold: too late", 101_000, false},
	}
	for _, tt := range tests {
		got, remaining := core.ShouldWarnBeforeCompact(tt.tokens, window, settings)
		if got != tt.want {
			t.Errorf("%s: warn = %v, want %v", tt.name, got, tt.want)
		}
		if got && remaining <= 0 {
			t.Errorf("%s: warned with %d tokens remaining", tt.name, remaining)
		}
	}
}

// A subagent has neither memory nor the ephemeral checkpoint, so a warning
// could only prompt stray files in the workspace.
func TestStrategyAllowsNotice(t *testing.T) {
	tests := []struct {
		strategy string
		want     bool
	}{
		{core.CompactPlain, false},
		{core.CompactNotify, true},
		{core.CompactPrepare, true},
		{"", false},
	}
	for _, tt := range tests {
		cfg := &loopConfig{compactStrategy: func() string { return tt.strategy }}
		if got := strategyAllowsNotice(cfg); got != tt.want {
			t.Errorf("strategy %q: %v, want %v", tt.strategy, got, tt.want)
		}
	}
	if strategyAllowsNotice(&loopConfig{}) {
		t.Error("a run with no strategy configured must not warn")
	}
}

// End to end: the notice has to reach the conversation the model sees, once,
// before compaction takes the unsaved work away.
func TestCompactionNotice_ReachesTheConversationOnce(t *testing.T) {
	prov := NewMockProvider(
		simpleTextResponse("one"),
		simpleTextResponse("two"),
		simpleTextResponse("three"),
	)
	reg := core.NewRegistry()
	if err := reg.Register(core.Tool{
		Name: "memory", Description: "save", Effect: core.EffectReadOnly,
		Parameters: []byte(`{"type":"object"}`),
		Execute: func(_ context.Context, _ map[string]any, _ func(core.Result)) (core.Result, error) {
			return core.TextResult("saved"), nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	// A window the conversation is already close to filling.
	settings := core.CompactionSettings{Enabled: true, ReserveTokens: 10, KeepRecent: 200}
	ag, err := New(AgentConfig{
		Provider:            prov,
		Model:               core.Model{ID: "test", MaxInput: 1000},
		Compaction:          &settings,
		CompactStrategy:     core.CompactNotify,
		Tools:               reg,
		MaxTurns:            10,
		MaxToolCallsPerTurn: 5,
		MaxRunDuration:      30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Push the context into the warning band without crossing the threshold.
	ag.state.Messages = append(ag.state.Messages, core.WrapMessage(core.NewUserMessage(strings.Repeat("filler ", 480))))

	if _, err := ag.Send(context.Background(), "go on"); err != nil {
		t.Fatal(err)
	}

	notices := 0
	for _, m := range ag.Messages() {
		if m.Custom != nil && m.Custom["source"] == "compaction_notice" {
			notices++
		}
	}
	if notices == 0 {
		t.Fatal("the agent was never warned that its context was about to be summarized")
	}
	if notices > 1 {
		t.Errorf("the agent was warned %d times in one run; once is enough", notices)
	}
}

// A child's tool set has no memory, which is how a subagent is told apart here:
// warning it could only produce stray files.
func TestCompactionNotice_SubagentIsNotWarned(t *testing.T) {
	reg := core.NewRegistry() // no memory tool: this is a child
	ag, err := New(AgentConfig{
		Provider:            NewMockProvider(simpleTextResponse("done")),
		Model:               core.Model{ID: "test", MaxInput: 1000},
		Compaction:          &core.CompactionSettings{Enabled: true, ReserveTokens: 10, KeepRecent: 200},
		CompactStrategy:     core.CompactNotify,
		Tools:               reg,
		MaxTurns:            10,
		MaxToolCallsPerTurn: 5,
		MaxRunDuration:      30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ag.state.Messages = append(ag.state.Messages, core.WrapMessage(core.NewUserMessage(strings.Repeat("filler ", 480))))
	if _, err := ag.Send(context.Background(), "go on"); err != nil {
		t.Fatal(err)
	}
	for _, m := range ag.Messages() {
		if m.Custom != nil && m.Custom["source"] == "compaction_notice" {
			t.Fatal("a subagent was warned, but it cannot persist anything")
		}
	}
}

// A narrow window leaves a band proportional to it, and that band can end up
// smaller than a single tool result: the context then jumps from under the band
// to over the threshold and the agent is never warned. Measured on a real
// server at compact_at=45k, where the band was 4.3k and one file read cost 5.4k.
func TestShouldWarnBeforeCompact_BandSurvivesASmallWindow(t *testing.T) {
	settings := core.CompactionSettings{Enabled: true, ReserveTokens: 16_384}
	const window = 45_000
	effective := window - settings.ReserveTokens

	// One large read away from the threshold: this is exactly the case that
	// went unwarned in production.
	tokens := effective - 5_400
	warn, remaining := core.ShouldWarnBeforeCompact(tokens, window, settings)
	if !warn {
		t.Fatalf("no warning at %d/%d tokens: one more file read compacts the conversation unannounced", tokens, effective)
	}
	if remaining <= 0 {
		t.Errorf("warned with %d tokens remaining", remaining)
	}
}

// Reproduces what happened in production: the agent was warned, the user
// replied, and the notice appeared again right after — twice in the transcript
// with the user's message in between.
//
// Every user message starts a new run, so a per-run flag re-warned on each turn
// for as long as the context stayed in the band.
func TestCompactionNotice_NotRepeatedOnTheNextUserTurn(t *testing.T) {
	prov := NewMockProvider(
		simpleTextResponse("one"),
		simpleTextResponse("two"),
		simpleTextResponse("three"),
	)
	reg := core.NewRegistry()
	if err := reg.Register(core.Tool{
		Name: "memory", Description: "save", Effect: core.EffectReadOnly,
		Parameters: []byte(`{"type":"object"}`),
		Execute: func(_ context.Context, _ map[string]any, _ func(core.Result)) (core.Result, error) {
			return core.TextResult("saved"), nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	settings := core.CompactionSettings{Enabled: true, ReserveTokens: 10, KeepRecent: 200}
	ag, err := New(AgentConfig{
		Provider:            prov,
		Model:               core.Model{ID: "test", MaxInput: 1000},
		Compaction:          &settings,
		CompactStrategy:     core.CompactNotify,
		Tools:               reg,
		MaxTurns:            10,
		MaxToolCallsPerTurn: 5,
		MaxRunDuration:      30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Inside the warning band, but not over the threshold.
	ag.state.Messages = append(ag.state.Messages, core.WrapMessage(core.NewUserMessage(strings.Repeat("filler ", 480))))

	if _, err := ag.Send(context.Background(), "go on"); err != nil {
		t.Fatal(err)
	}
	// The user answers: a second run, with the context still in the band.
	if _, err := ag.Send(context.Background(), "understood, carry on"); err != nil {
		t.Fatal(err)
	}

	notices := 0
	for _, m := range ag.Messages() {
		if m.Custom != nil && m.Custom["source"] == "compaction_notice" {
			notices++
		}
	}
	if notices != 1 {
		t.Errorf("warned %d times across two turns; the agent only needs telling once", notices)
	}
}
