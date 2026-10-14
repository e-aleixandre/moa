package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// failingProvider fails every call, either as a Stream error or as a stream
// error event, and counts how often it was asked.
type failingProvider struct {
	mu    sync.Mutex
	err   error
	event bool
	calls int
}

func (p *failingProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	if !p.event {
		return nil, p.err
	}
	ch := make(chan core.AssistantEvent, 1)
	ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: p.err}
	close(ch)
	return ch, nil
}

func (p *failingProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func newFallbackAgent(t *testing.T, session core.Provider, sumProv core.Provider, sumModel core.Model) *Agent {
	t.Helper()
	settings := core.CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecent: 200}
	ag, err := New(AgentConfig{
		Provider:   session,
		Model:      core.Model{ID: "session-model", Name: "Opus 5.5", MaxInput: 1000, Pricing: &core.Pricing{Input: 1}},
		Compaction: &settings,
		Tools:      core.NewRegistry(),
		CompactSummarizer: func(core.Model) (core.Provider, core.Model, string) {
			return sumProv, sumModel, ""
		},
		MaxTurns:       5,
		MaxRunDuration: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := driveTurns(t, ag, 6); err != nil {
		t.Fatal(err)
	}
	return ag
}

func TestCompact_TemporaryProviderErrorFallsBackToSessionModel(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		event  bool
		reason string
	}{
		{"overloaded event", errors.New("openai: server_is_overloaded: try again"), true, "overloaded"},
		{"http 503", errors.New("summarization request: HTTP 503: unavailable"), false, "server error"},
		{"http 500", errors.New("openai: HTTP 500: boom"), false, "server error"},
		{"http 529", errors.New("anthropic: HTTP 529: overloaded_error"), false, "overloaded"},
		{"http 429", errors.New("openai: HTTP 429: slow down"), false, "rate limited"},
		{"network", errors.New("read tcp: connection reset by peer"), true, "network error"},
		{"timeout", fmt.Errorf("post: %w", context.DeadlineExceeded), false, "network error"},
		{"idle timeout", errors.New("read: stream idle timeout: no data received"), true, "network error"},
		{"conn refused", errors.New("dial tcp 127.0.0.1:443: connect: connection refused"), false, "network error"},
		{"dns", errors.New("dial tcp: lookup api.openai.com: no such host"), false, "network error"},
		{"anthropic api_error", errors.New("anthropic api_error: Internal server error"), true, "server error"},
		{"openai normalized", errors.New("openai: Internal server error"), true, "server error"},
		{"sanitized after retries", errors.New("xai: request failed after retries"), false, "provider error"},
		{"quota", fmt.Errorf("summarization: %w", &core.QuotaExceededError{Provider: "openai"}), true, "usage limit reached"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := &recordingProvider{text: "## Goal\nsession summary"}
			bad := &failingProvider{err: tc.err, event: tc.event}
			sumModel := core.Model{ID: "sol", Name: "Sol", MaxInput: 1000, Pricing: &core.Pricing{Input: 99}}
			ag := newFallbackAgent(t, session, bad, sumModel)
			// Auto-compaction may already have run during the turns above.
			badBefore, sessBefore := bad.count(), len(session.seen())

			payload, err := ag.Compact(context.Background(), "")
			if err != nil || payload == nil {
				t.Fatalf("compaction should have succeeded on the session model: payload=%v err=%v", payload, err)
			}
			if got := bad.count() - badBefore; got != 1 {
				t.Errorf("configured model tried %d times, want 1", got)
			}
			if got := session.seen()[sessBefore:]; len(got) != 1 || got[0] != "session-model" {
				t.Errorf("session model should serve exactly one retry, got %v", got)
			}
			want := "✂ Summarized with Opus 5.5 — Sol was unavailable (" + tc.reason + ")"
			if payload.SummarizerNotice != want {
				t.Errorf("notice = %q, want %q", payload.SummarizerNotice, want)
			}
			// Billed at the session model's rates, not the failed summarizer's.
			if payload.Pricing == nil || payload.Pricing.Input != 1 {
				t.Errorf("pricing = %+v, want the session model's", payload.Pricing)
			}
		})
	}
}

func TestCompact_NonTemporaryErrorDoesNotFallBack(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"401", errors.New("openai: HTTP 401: invalid api key")},
		{"400", errors.New("openai: HTTP 400: invalid request")},
		{"context too long", errors.New("openai: HTTP 400: context_length_exceeded, maximum context length")},
		{"refusal", errors.New("anthropic: refusal: stop_reason refusal")},
		{"403", errors.New("openai: access denied (HTTP 403)")},
		{"canceled", fmt.Errorf("summarization: %w", context.Canceled)},
		{"auth typed", &core.ProviderAuthError{Provider: "openai", Status: 401}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := &recordingProvider{text: "## Goal\nsession summary"}
			bad := &failingProvider{err: tc.err}
			sumModel := core.Model{ID: "sol", Name: "Sol", MaxInput: 1000}
			ag := newFallbackAgent(t, session, bad, sumModel)
			sessBefore := len(session.seen())

			payload, err := ag.Compact(context.Background(), "")
			if err == nil || payload != nil {
				t.Fatalf("a non-temporary error must surface as today, got payload=%v err=%v", payload, err)
			}
			if got := session.seen()[sessBefore:]; len(got) != 0 {
				t.Errorf("session model must not be tried, got %v", got)
			}
		})
	}
}

func TestCompact_NoRetryWhenConfiguredModelIsTheSessionModel(t *testing.T) {
	session := &failingProvider{err: errors.New("openai: HTTP 503: down"), event: true}
	settings := core.CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecent: 200}
	model := core.Model{ID: "same", MaxInput: 1000}
	_, _, _, _, err := compactWithSessionFallback(
		context.Background(), session, model, core.StreamOptions{}, "",
		session, model, core.StreamOptions{},
		longHistory(), 900, 1000, settings, "", nil)
	if err == nil {
		t.Fatal("expected the error to surface")
	}
	if session.count() != 1 {
		t.Errorf("provider called %d times, want 1", session.count())
	}
}

func TestCompact_FallbackFailureSurfacesTheRetryError(t *testing.T) {
	session := &failingProvider{err: errors.New("openai: HTTP 401: nope"), event: true}
	bad := &failingProvider{err: errors.New("openai: HTTP 503: down"), event: true}
	settings := core.CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecent: 200}
	_, _, _, _, err := compactWithSessionFallback(
		context.Background(), bad, core.Model{ID: "sol", MaxInput: 1000}, core.StreamOptions{}, "",
		session, core.Model{ID: "sess", MaxInput: 1000}, core.StreamOptions{},
		longHistory(), 900, 1000, settings, "", nil)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("want the session retry's error, got %v", err)
	}
	if bad.count() != 1 || session.count() != 1 {
		t.Errorf("each model must be tried once, got %d/%d", bad.count(), session.count())
	}
}

func longHistory() []core.AgentMessage {
	var msgs []core.AgentMessage
	for i := 0; i < 8; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs = append(msgs, core.AgentMessage{Message: core.Message{
			Role: role, Content: []core.Content{core.TextContent(strings.Repeat("context ", 200))},
		}})
	}
	return msgs
}

// The automatic path (notify) must fall back too, and bill the retry at the
// session model's rates.
func TestAutoCompact_TemporaryErrorFallsBackToSessionModel(t *testing.T) {
	session := &recordingProvider{text: "## Goal\nsession summary"}
	bad := &failingProvider{err: errors.New("openai: server_is_overloaded"), event: true}
	var mu sync.Mutex
	var notices []string
	settings := core.CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecent: 200}
	ag, err := New(AgentConfig{
		Provider:   session,
		Model:      core.Model{ID: "session-model", Name: "Opus 5.5", MaxInput: 1000},
		Compaction: &settings,
		Tools:      core.NewRegistry(),
		CompactSummarizer: func(core.Model) (core.Provider, core.Model, string) {
			return bad, core.Model{ID: "sol", Name: "Sol", MaxInput: 1000}, ""
		},
		MaxTurns:       5,
		MaxRunDuration: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ag.Subscribe(func(e core.AgentEvent) {
		if e.Type == core.AgentEventCompactionEnd && e.Compaction != nil {
			mu.Lock()
			notices = append(notices, e.Compaction.SummarizerNotice)
			mu.Unlock()
		}
	})
	if err := driveTurns(t, ag, 6); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(notices) == 0 || bad.count() == 0 {
		t.Fatalf("auto compaction did not run (notices=%v, bad calls=%d)", notices, bad.count())
	}
	for _, n := range notices {
		if n != "✂ Summarized with Opus 5.5 — Sol was unavailable (overloaded)" {
			t.Errorf("notice = %q", n)
		}
	}
}

// The background (atomic) path falls back too: the session provider writes the
// summary, the usage receipt is priced at the session model's rates, and the
// notice travels with the adopted compaction.
func TestBackgroundCompaction_TemporaryErrorFallsBackToSessionModel(t *testing.T) {
	f := newBGT(t, nil)
	bad := &failingProvider{err: errors.New("openai: HTTP 503: down"), event: true}
	f.ag.mu.Lock()
	f.ag.config.CompactSummarizer = func(m core.Model) (core.Provider, core.Model, string) {
		return bad, core.Model{ID: "sol", Name: "Sol", MaxInput: 100000, Pricing: &core.Pricing{Input: 99}}, ""
	}
	f.ag.mu.Unlock()
	if _, err := f.ag.Send(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	f.ag.WaitBackgroundCompaction()
	f.ag.Drain(bgtWait)
	if bad.count() != 1 {
		t.Fatalf("configured summarizer tried %d times, want 1", bad.count())
	}
	if f.sum.calls.Load() != 0 {
		t.Fatal("fixture summarizer must not be used")
	}
	if !hasSummary(f.ag.Messages()) {
		t.Fatalf("the session model did not write the summary: %v", f.ag.Messages())
	}
}

// An exhausted budget forbids the retry, as it would any request: the original
// error comes back and the session model is never called.
func TestCompact_ExhaustedBudgetPreventsRetry(t *testing.T) {
	bad := &failingProvider{err: errors.New("openai: HTTP 503: down"), event: true}
	session := &recordingProvider{text: "## Goal\nsession summary"}
	settings := core.CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecent: 200}
	_, _, _, _, err := compactWithSessionFallback(
		context.Background(), bad, core.Model{ID: "sol", MaxInput: 1000}, core.StreamOptions{}, "",
		session, core.Model{ID: "sess", MaxInput: 1000}, core.StreamOptions{},
		longHistory(), 900, 1000, settings, "", func() bool { return false })
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("want the original error, got %v", err)
	}
	if got := session.seen(); len(got) != 0 {
		t.Errorf("session model called despite exhausted budget: %v", got)
	}
}
