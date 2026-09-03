package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// recordingProvider answers every call with the same short text and records the
// model each request was made against, so a test can tell WHICH provider served
// the summarization call rather than trusting that it happened.
type recordingProvider struct {
	mu     sync.Mutex
	text   string
	models []string
}

func (p *recordingProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	p.mu.Lock()
	p.models = append(p.models, req.Model.ID)
	p.mu.Unlock()
	return simpleTextResponse(p.text)(req)
}

// driveTurns runs enough conversation for FindCutPoint to have something to
// summarize: a single turn leaves nothing behind the cut.
func driveTurns(t *testing.T, ag *Agent, n int) error {
	t.Helper()
	if _, err := ag.Run(context.Background(), strings.Repeat("context ", 200)); err != nil {
		return err
	}
	for i := 1; i < n; i++ {
		if _, err := ag.Send(context.Background(), strings.Repeat("more context ", 200)); err != nil {
			return err
		}
	}
	return nil
}

func (p *recordingProvider) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.models...)
}

// A configured summarizer must actually serve the summarization call, on its own
// provider. Compaction is the one request in a session that shares no cached
// prefix with the conversation, so it is free to run on a cheaper model — but
// only if the routing really happens.
func TestCompact_ConfiguredSummarizerServesTheSummaryCall(t *testing.T) {
	session := &recordingProvider{text: "session answer"}
	summarizer := &recordingProvider{text: "## Goal\nsummary written elsewhere"}
	sumModel := core.Model{ID: "cheap-model", Name: "Cheap", MaxInput: 1000}

	settings := core.CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecent: 200}
	ag, err := New(AgentConfig{
		Provider:   session,
		Model:      core.Model{ID: "session-model", MaxInput: 1000},
		Compaction: &settings,
		Tools:      core.NewRegistry(),
		CompactSummarizer: func(core.Model) (core.Provider, core.Model, string) {
			return summarizer, sumModel, ""
		},
		MaxTurns:       5,
		MaxRunDuration: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Compaction only produces a payload when there is a cut point, so build a
	// conversation with enough turns to summarize.
	if err := driveTurns(t, ag, 6); err != nil {
		t.Fatal(err)
	}

	// Measure only the manual compaction: with a small window the automatic
	// path fires between turns too, and counting everything would prove
	// nothing about which call served the summary.
	sumBefore, sessionBefore := len(summarizer.seen()), len(session.seen())

	payload, err := ag.Compact(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if payload == nil {
		t.Fatal("compaction returned no payload")
	}

	sumCalls := summarizer.seen()[sumBefore:]
	if len(sumCalls) != 1 || sumCalls[0] != sumModel.ID {
		t.Errorf("the summary call should have gone to the summarizer's own model, got %v", sumCalls)
	}
	// The session's provider must not be billed for the summary at all.
	if extra := session.seen()[sessionBefore:]; len(extra) != 0 {
		t.Errorf("session provider must not serve the summary call, got %v", extra)
	}
	if payload.SummarizerNotice != "" {
		t.Errorf("an honoured summarizer must stay silent, got %q", payload.SummarizerNotice)
	}
}

// The fallback notice has to survive the run that produced it: the reader comes
// back hours later and needs to know the summary they are judging was not
// written by the model they configured.
func TestCompact_FallbackNoticeTravelsInThePayload(t *testing.T) {
	session := &recordingProvider{text: "## Goal\nsummary from the session model"}
	const notice = "✂ Summarized with Session — no usable credential for terra"

	settings := core.CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecent: 200}
	ag, err := New(AgentConfig{
		Provider:   session,
		Model:      core.Model{ID: "session-model", Name: "Session", MaxInput: 1000},
		Compaction: &settings,
		Tools:      core.NewRegistry(),
		// What the resolver does when the configured model cannot be honoured:
		// hand back the session's own model, plus the reason.
		CompactSummarizer: func(m core.Model) (core.Provider, core.Model, string) {
			return session, m, notice
		},
		MaxTurns:       5,
		MaxRunDuration: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Compaction only produces a payload when there is a cut point, so build a
	// conversation with enough turns to summarize.
	if err := driveTurns(t, ag, 6); err != nil {
		t.Fatal(err)
	}

	payload, err := ag.Compact(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if payload == nil {
		t.Fatal("compaction returned no payload")
	}
	if payload.SummarizerNotice != notice {
		t.Errorf("notice should reach the payload verbatim:\n got %q\nwant %q", payload.SummarizerNotice, notice)
	}
	// Falling back must still compact: a session that stops compacting grows
	// until it hits the window, which is worse than a costlier summary.
	if !strings.Contains(payload.Summary, "summary from the session model") {
		t.Errorf("fallback must still produce a summary, got %q", payload.Summary)
	}
}

// A summarizer that returns nothing usable must not take the compaction down
// with it: the session's own model summarizes instead.
func TestCompact_UnusableSummarizerFallsBackToTheSessionModel(t *testing.T) {
	session := &recordingProvider{text: "## Goal\nsummary from the session model"}

	settings := core.CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecent: 200}
	ag, err := New(AgentConfig{
		Provider:   session,
		Model:      core.Model{ID: "session-model", MaxInput: 1000},
		Compaction: &settings,
		Tools:      core.NewRegistry(),
		CompactSummarizer: func(core.Model) (core.Provider, core.Model, string) {
			return nil, core.Model{}, ""
		},
		MaxTurns:       5,
		MaxRunDuration: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Compaction only produces a payload when there is a cut point, so build a
	// conversation with enough turns to summarize.
	if err := driveTurns(t, ag, 6); err != nil {
		t.Fatal(err)
	}

	sessionBefore := len(session.seen())

	payload, err := ag.Compact(context.Background(), "")
	if err != nil {
		t.Fatalf("compaction must not fail when the summarizer is unusable: %v", err)
	}
	if payload == nil || payload.Summary == "" {
		t.Fatal("expected the session model to summarize instead")
	}
	// The session's own provider served the summary, since the summarizer gave
	// back nothing usable.
	if extra := session.seen()[sessionBefore:]; len(extra) != 1 {
		t.Errorf("session provider should have served exactly the summary call, got %v", extra)
	}
}
