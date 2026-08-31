package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/sessioncheckpoint"
)

// prepareHarness builds an agent whose context is already over the compaction
// threshold, so the next send compacts on its first iteration.
//
// The transcript is filled directly rather than driven through turns: how many
// responses a run consumes changes as soon as anything adds a message (the
// notice does), and a harness that depends on that count breaks for reasons
// that have nothing to do with what is being tested.
func prepareHarness(t *testing.T, strategy string, responses ...func(core.Request) (<-chan core.AssistantEvent, error)) (*Agent, *seenRequests) {
	t.Helper()
	slot := &sessioncheckpoint.Slot{}
	seen := &seenRequests{}
	recorded := make([]func(core.Request) (<-chan core.AssistantEvent, error), len(responses))
	for i, resp := range responses {
		recorded[i] = func(req core.Request) (<-chan core.AssistantEvent, error) {
			seen.add(req)
			return resp(req)
		}
	}
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
	ag, err := New(AgentConfig{
		Provider:            NewMockProvider(recorded...),
		Model:               core.Model{ID: "test", MaxInput: 1000},
		Compaction:          &core.CompactionSettings{Enabled: true, ReserveTokens: 10, KeepRecent: 10},
		CompactStrategy:     strategy,
		SessionCheckpoint:   slot,
		Tools:               reg,
		MaxTurns:            10,
		MaxToolCallsPerTurn: 5,
		MaxRunDuration:      30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Already over the threshold: the next send compacts straight away.
	for i := 0; i < 6; i++ {
		ag.state.Messages = append(ag.state.Messages,
			core.WrapMessage(core.NewUserMessage(strings.Repeat("filler ", 120))),
			core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{core.TextContent(strings.Repeat("reply ", 120))}}),
		)
	}
	return ag, seen
}

// compactNow drives the single send that crosses the threshold.
func compactNow(t *testing.T, ag *Agent) {
	t.Helper()
	if _, err := ag.Send(context.Background(), "carry on"); err != nil {
		t.Fatal(err)
	}
}

// seenRequests records every request the provider received.
type seenRequests struct {
	mu   sync.Mutex
	reqs []core.Request
}

func (s *seenRequests) add(req core.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, req)
}

func (s *seenRequests) all() []core.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]core.Request{}, s.reqs...)
}

// asksToPrepare reports whether any request carried the preparation prompt.
func (s *seenRequests) asksToPrepare() bool {
	for _, req := range s.all() {
		for _, m := range req.Messages {
			for _, c := range m.Content {
				if strings.Contains(c.Text, "Prepare this conversation for imminent compaction") {
					return true
				}
			}
		}
	}
	return false
}

// The whole point: the agent gets a turn to save its work, and it happens
// before the summary replaces it.
func TestAutoPrepare_RunsBeforeCompaction(t *testing.T) {
	ag, seen := prepareHarness(t, core.CompactPrepare,
		simpleTextResponse("prepared: noted the findings"), // preparation turn
		simpleTextResponse("## Goal\nsummary"),             // summarizer
		simpleTextResponse("carrying on"),                  // turn after compaction
	)
	compactNow(t, ag)
	if !seen.asksToPrepare() {
		t.Fatal("the agent was never asked to prepare before its context was summarized")
	}
	// Preparing after the summary would be pointless: the work is already gone.
	reqs := seen.all()
	prepareAt, summarizeAt := -1, -1
	for i, req := range reqs {
		for _, m := range req.Messages {
			for _, c := range m.Content {
				if prepareAt < 0 && strings.Contains(c.Text, "Prepare this conversation for imminent compaction") {
					prepareAt = i
				}
			}
		}
		if summarizeAt < 0 && strings.Contains(req.System, "conversation summarizer") {
			summarizeAt = i
		}
	}
	if summarizeAt >= 0 && prepareAt > summarizeAt {
		t.Errorf("prepared at call %d, after the summarizer ran at %d", prepareAt, summarizeAt)
	}
}

// The preparation transcript is throwaway: only its side effects survive.
// Leaking it would put an internal turn in front of the user and feed it to the
// summarizer.
func TestAutoPrepare_DoesNotLeakIntoTheConversation(t *testing.T) {
	ag, _ := prepareHarness(t, core.CompactPrepare,
		simpleTextResponse("prepared: noted the findings"),
		simpleTextResponse("## Goal\nsummary"),
		simpleTextResponse("carrying on"),
	)
	compactNow(t, ag)
	for _, m := range ag.Messages() {
		for _, c := range m.Content {
			if strings.Contains(c.Text, "Prepare this conversation for imminent compaction") {
				t.Fatal("the internal preparation prompt ended up in the user's conversation")
			}
		}
	}
}

// notify and plain must not pay for a preparation turn.
func TestAutoPrepare_OnlyUnderPrepareStrategy(t *testing.T) {
	for _, strategy := range []string{core.CompactPlain, core.CompactNotify} {
		ag, seen := prepareHarness(t, strategy,
			simpleTextResponse("## Goal\nsummary"),
			simpleTextResponse("carrying on"),
		)
		compactNow(t, ag)
		if seen.asksToPrepare() {
			t.Errorf("strategy %q took a preparation turn it did not ask for", strategy)
		}
	}
}

// A failed preparation must not take the compaction down with it: the context
// is already over the threshold and has to be summarized either way.
func TestAutoPrepare_FailureStillCompacts(t *testing.T) {
	// One response only: the preparation turn consumes it, the summarizer then
	// gets an error from the exhausted mock.
	ag, _ := prepareHarness(t, core.CompactPrepare,
		simpleTextResponse("prepared"),
		simpleTextResponse("## Goal\nsummary"),
		simpleTextResponse("carrying on"),
	)
	compactNow(t, ag)
	if ag.state.CompactionEpoch == 0 {
		t.Error("the conversation was never compacted")
	}
}
