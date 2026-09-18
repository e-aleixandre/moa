package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// The minimum gain has a 20k floor (minWarnBandTokens), so a toy window would
// make every trim unacceptable by construction. These tests therefore run at a
// realistic scale: a 100k window, which is where the ratio and the floor meet.
const trimTestWindow = 100_000

func trimSettings() core.CompactionSettings {
	return core.CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecent: 2000}
}

// seedTrimmableHistory fills an agent with a conversation whose old tool
// results are large enough to be worth eliding.
func seedTrimmableHistory(ag *Agent, results int, sizeChars int) {
	ag.state.Messages = append(ag.state.Messages, core.WrapMessage(core.NewUserMessage("start")))
	for i := range results {
		callID := "call-" + string(rune('a'+i))
		assistant := core.WrapMessage(core.Message{
			Role:    "assistant",
			Content: []core.Content{core.ToolCallContent(callID, "read", map[string]any{"path": "/x"})},
		})
		result := core.WrapMessage(core.NewToolResultMessage(callID, "read",
			[]core.Content{core.TextContent(strings.Repeat("x", sizeChars) + "\nExit code: 0")}, false))
		ag.state.Messages = append(ag.state.Messages, assistant, result)
	}
	ensureMsgIDs(ag.state.Messages)
}

func newTrimAgent(t *testing.T, prov core.Provider, settings *core.CompactionSettings) *Agent {
	t.Helper()
	ag, err := New(AgentConfig{
		Provider:            prov,
		Model:               core.Model{ID: "test", MaxInput: trimTestWindow},
		Compaction:          settings,
		Tools:               core.NewRegistry(),
		MaxTurns:            10,
		MaxToolCallsPerTurn: 5,
		MaxRunDuration:      30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ag
}

func collectTrimEvents(ag *Agent) (*[]core.AgentEvent, *sync.Mutex) {
	var mu sync.Mutex
	var events []core.AgentEvent
	ag.Subscribe(func(e core.AgentEvent) {
		switch e.Type {
		case core.AgentEventContextTrimmed, core.AgentEventCompactionStart, core.AgentEventCompactionEnd:
			mu.Lock()
			events = append(events, e)
			mu.Unlock()
		}
	})
	return &events, &mu
}

func waitForEvents(t *testing.T, events *[]core.AgentEvent, mu *sync.Mutex, n int) []core.AgentEvent {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := len(*events)
		mu.Unlock()
		if got >= n {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	return append([]core.AgentEvent(nil), *events...)
}

// The headline behaviour: enough elidable output means no summarizer call at
// all. The mock provider has exactly one handler, so a compaction would fail
// the run outright.
func TestTrim_ReplacesCompactionWhenTheGainIsEnough(t *testing.T) {
	prov := NewMockProvider(simpleTextResponse("carrying on"))
	settings := trimSettings()
	ag := newTrimAgent(t, prov, &settings)
	seedTrimmableHistory(ag, 30, 16_000)

	events, mu := collectTrimEvents(ag)

	if _, err := ag.Send(context.Background(), "continue"); err != nil {
		t.Fatalf("run failed (a summarizer call would exhaust the mock): %v", err)
	}

	got := waitForEvents(t, events, mu, 1)
	if len(got) == 0 || got[0].Type != core.AgentEventContextTrimmed {
		t.Fatalf("events = %+v, want a context_trimmed", got)
	}
	for _, e := range got {
		if e.Type == core.AgentEventCompactionStart {
			t.Fatal("the summarizer was called even though the trim was accepted")
		}
	}

	payload := got[0].Trim
	if payload == nil || payload.WatermarkMsgID == "" {
		t.Fatalf("trim payload = %+v, want a watermark", payload)
	}
	if payload.Results == 0 || payload.TokensAfter >= payload.TokensBefore {
		t.Fatalf("trim payload = %+v, want a real saving", payload)
	}
	if ag.CompactionEpoch() != 1 {
		t.Fatalf("epoch = %d, want 1: the anchored usage must stop being used", ag.CompactionEpoch())
	}

	// The provider-visible conversation carries placeholders, and every
	// tool_call still has its answer.
	var placeholders int
	for _, m := range ag.Messages() {
		if m.Role != "tool_result" {
			continue
		}
		if m.ToolCallID == "" {
			t.Fatal("a trimmed result lost its ToolCallID: the provider would reject the history")
		}
		for _, c := range m.Content {
			if strings.HasPrefix(c.Text, "[Output elided") {
				placeholders++
			}
		}
	}
	if placeholders != payload.Results {
		t.Fatalf("%d placeholders in history, payload says %d", placeholders, payload.Results)
	}
}

// The event has to carry the untrimmed view, or the tree cannot persist the
// originals of the run in flight and the transcript keeps placeholders.
func TestTrim_EventCarriesPreTrimOriginals(t *testing.T) {
	prov := NewMockProvider(simpleTextResponse("carrying on"))
	settings := trimSettings()
	ag := newTrimAgent(t, prov, &settings)
	seedTrimmableHistory(ag, 30, 16_000)

	events, mu := collectTrimEvents(ag)
	if _, err := ag.Send(context.Background(), "continue"); err != nil {
		t.Fatal(err)
	}
	got := waitForEvents(t, events, mu, 1)
	if len(got) == 0 {
		t.Fatal("no trim event")
	}

	var originals int
	for _, m := range got[0].TrimOriginals {
		for _, c := range m.Content {
			if strings.Count(c.Text, "x") > 1000 {
				originals++
			}
		}
	}
	if originals == 0 {
		t.Fatal("the trim event carried no untrimmed originals")
	}
}

// Not enough to elide: the feature has to get out of the way and let the
// summarizer run exactly as it did before.
func TestTrim_FallsBackToCompactionWhenTheGainIsTooSmall(t *testing.T) {
	prov := NewMockProvider(
		simpleTextResponse("## Goal\nsummary"), // the summarizer call
		simpleTextResponse("carrying on"),
	)
	settings := trimSettings()
	ag := newTrimAgent(t, prov, &settings)
	// One modest result: over the eligibility floor, far under the minimum gain.
	ag.state.Messages = append(ag.state.Messages,
		core.WrapMessage(core.NewUserMessage(strings.Repeat("prose ", 80_000))),
		core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{core.ToolCallContent("call-a", "read", nil)}}),
		core.WrapMessage(core.NewToolResultMessage("call-a", "read", []core.Content{core.TextContent(strings.Repeat("y", 20_000))}, false)),
	)
	ensureMsgIDs(ag.state.Messages)

	events, mu := collectTrimEvents(ag)
	if _, err := ag.Send(context.Background(), "continue"); err != nil {
		t.Fatal(err)
	}

	got := waitForEvents(t, events, mu, 2)
	sawCompaction := false
	for _, e := range got {
		if e.Type == core.AgentEventContextTrimmed {
			t.Fatalf("trimmed with a gain below the minimum: %+v", e.Trim)
		}
		if e.Type == core.AgentEventCompactionStart {
			sawCompaction = true
		}
	}
	if !sawCompaction {
		t.Fatalf("events = %+v, want the compaction fallback", got)
	}
}

// A child has no tree to record a watermark in, so it must not trim at all.
func TestTrim_DisabledSettingIsHonoured(t *testing.T) {
	prov := NewMockProvider(
		simpleTextResponse("## Goal\nsummary"),
		simpleTextResponse("carrying on"),
	)
	settings := trimSettings()
	settings.TrimDisabled = true
	ag := newTrimAgent(t, prov, &settings)
	seedTrimmableHistory(ag, 30, 16_000)

	events, mu := collectTrimEvents(ag)
	if _, err := ag.Send(context.Background(), "continue"); err != nil {
		t.Fatal(err)
	}
	for _, e := range waitForEvents(t, events, mu, 1) {
		if e.Type == core.AgentEventContextTrimmed {
			t.Fatal("a trim ran with TrimDisabled set")
		}
	}
}

// Second pass over an already-trimmed conversation: with no new region, there
// is nothing to win, so it must compact rather than churn the prefix again.
func TestTrim_DoesNotRepeatWithoutNewMaterial(t *testing.T) {
	prov := NewMockProvider(
		simpleTextResponse("first"),
		simpleTextResponse("## Goal\nsummary"),
		simpleTextResponse("second"),
	)
	settings := trimSettings()
	ag := newTrimAgent(t, prov, &settings)
	seedTrimmableHistory(ag, 30, 16_000)

	events, mu := collectTrimEvents(ag)
	if _, err := ag.Send(context.Background(), "continue"); err != nil {
		t.Fatal(err)
	}
	if _, err := ag.Send(context.Background(), "and again"); err != nil {
		t.Fatal(err)
	}

	got := waitForEvents(t, events, mu, 2)
	trims := 0
	for _, e := range got {
		if e.Type == core.AgentEventContextTrimmed {
			trims++
		}
	}
	if trims != 1 {
		t.Fatalf("%d trims across two turns; the watermark should have blocked the second", trims)
	}
}

// The notice survives a trim, so matching on its presence alone would let the
// eventual compaction arrive unannounced. It is per context epoch.
func TestAlreadyWarned_IsPerEpoch(t *testing.T) {
	notice := compactionNotice(20_000, 0)
	msgs := []core.AgentMessage{notice}

	if !alreadyWarned(msgs, 0) {
		t.Fatal("the epoch-0 notice was not recognised in epoch 0")
	}
	if alreadyWarned(msgs, 1) {
		t.Fatal("an epoch-0 notice suppressed the warning after a trim bumped the epoch")
	}

	// A notice persisted before epochs were stamped reads as epoch 0, which is
	// where it was emitted.
	legacy := compactionNotice(20_000, 0)
	delete(legacy.Custom, "epoch")
	if !alreadyWarned([]core.AgentMessage{legacy}, 0) {
		t.Fatal("a legacy notice stopped counting in epoch 0")
	}
	// Custom round-trips through JSON when a session is persisted.
	jsonish := compactionNotice(20_000, 2)
	jsonish.Custom["epoch"] = float64(2)
	if !alreadyWarned([]core.AgentMessage{jsonish}, 2) {
		t.Fatal("a JSON-decoded epoch did not match")
	}
}
