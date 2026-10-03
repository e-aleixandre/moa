package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// pr28FailSummary fails every summary call: either the stream errors at once
// (provider down, 4xx) or it answers an empty text with a reported usage
// (spend without a usable summary).
type pr28FailSummary struct {
	calls     atomic.Int32
	withUsage bool
	delay     time.Duration
}

func (s *pr28FailSummary) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	s.calls.Add(1)
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if !s.withUsage {
		return nil, errors.New("summarizer unavailable")
	}
	m := core.Message{Role: "assistant", Content: []core.Content{core.TextContent("")}, StopReason: "end_turn",
		Usage: &core.Usage{Input: 1000, Output: 100}, Timestamp: time.Now().Unix()}
	ch := make(chan core.AssistantEvent, 2)
	ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &m}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &m}
	close(ch)
	return ch, nil
}

func pr28Agent(t *testing.T, sum core.Provider, prov *bgtProvider, chars int) *Agent {
	t.Helper()
	settings := core.CompactionSettings{Enabled: true, ReserveTokens: 1000, KeepRecent: 8000, CompactAt: 40000, TrimDisabled: true}
	ag, err := New(AgentConfig{
		Provider: prov, Model: core.Model{ID: "bgt", MaxInput: 100000}, Tools: core.NewRegistry(), Compaction: &settings,
		MaxTurns: 10, MaxRunDuration: 20 * time.Second,
		CompactSummarizer: func(m core.Model) (core.Provider, core.Model, string) {
			m.Pricing = bgtPricing
			return sum, m, ""
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ag.SetBackgroundCompaction(ctx, nil)
	if err := ag.LoadState([]core.AgentMessage{
		bgtMsg("user", "U0 ", chars), bgtMsg("assistant", "A0 ", chars), bgtMsg("user", "U1 ", chars), bgtMsg("assistant", "A1 ", chars),
	}, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ag.Abort()
		cancel()
		ag.WaitBackgroundCompaction()
	})
	return ag
}

// Contract (book, work/compactacion-atomica.md + step2-design.md): a summary
// error below hard closes the job non-fatally (the run carries on with its
// literal context); at hard the run STOPS with ErrContextCapacity rather than
// knowingly send an oversized request. One boundary = one summary attempt.
//
// Regression guard: the worker settles its own failed job (a.bgJob=nil) right after
// closing done. The boundary then reads "invalidated or replaced", answers
// again=true, finds no job and starts a fresh summary of the same P, in the
// same boundary, as long as the worker keeps winning that race.
func TestPR28Review_SummaryFailureIsOneAttemptPerBoundary(t *testing.T) {
	cases := []struct {
		name      string
		chars     int // per message; 4 messages
		withUsage bool
		delay     time.Duration
		overHard  bool
	}{
		// ~120k tokens > hard 99k: the request waits for the summary.
		{"hard/fast_error", 120000 * 4, false, 0, true},
		{"hard/empty_with_usage", 120000 * 4, true, 0, true},
		{"hard/slow_error", 120000 * 4, false, 20 * time.Millisecond, true},
		// ~40k tokens: soft (39k) < estimate < hard (99k).
		{"soft/fast_error", 40000 * 4, false, 0, false},
		{"soft/empty_with_usage", 40000 * 4, true, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sum := &pr28FailSummary{withUsage: c.withUsage, delay: c.delay}
			prov := &bgtProvider{}
			ag := pr28Agent(t, sum, prov, c.chars/4)
			var usage atomic.Int32
			ag.Subscribe(func(e core.AgentEvent) {
				if e.Type == core.AgentEventCompactionUsage {
					usage.Add(1)
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, err := ag.Send(ctx, "go")
			ag.WaitBackgroundCompaction()
			ag.Drain(bgtWait)
			n := sum.calls.Load()
			if c.overHard {
				if prov.calls.Load() != 0 {
					t.Fatalf("an over-hard ordinary request left: %d", prov.calls.Load())
				}
				if !errors.Is(err, ErrContextCapacity) {
					t.Errorf("run error=%v, want ErrContextCapacity", err)
				}
			} else if err != nil {
				t.Errorf("below hard a failed summary must not end the run: %v", err)
			}
			if n != 1 {
				t.Errorf("summary attempts in one boundary = %d, want 1 (usage receipts=%d)", n, usage.Load())
			}
		})
	}
}
