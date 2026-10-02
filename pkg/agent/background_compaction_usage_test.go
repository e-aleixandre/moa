package agent

import (
	"context"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// A provider-reported usage receipt is real spend even when its done message
// has no usable summary text. Do not fabricate usage; this fixture reports it.
func TestBackgroundCompactionReview_KnownUsageOnEmptySummary(t *testing.T) {
	for _, text := range []string{"", "valid summary"} {
		name := "empty_summary"
		if text != "" {
			name = "valid_summary_control"
		}
		t.Run(name, func(t *testing.T) {
			f := newBGT(t, nil)
			u := &core.Usage{Input: 1000, Output: 100}
			p := &scriptedProvider{handlers: []func(core.Request) (<-chan core.AssistantEvent, error){respondWith(core.Message{
				Role: "assistant", StopReason: "end_turn", Content: []core.Content{core.TextContent(text)}, Usage: u,
			})}}
			f.ag.mu.Lock()
			f.ag.config.CompactSummarizer = func(model core.Model) (core.Provider, core.Model, string) {
				model.Pricing = bgtPricing
				return p, model, ""
			}
			f.ag.mu.Unlock()
			if _, err := f.ag.Send(context.Background(), "go"); err != nil {
				t.Fatal(err)
			}
			f.ag.WaitBackgroundCompaction()
			f.ag.Drain(bgtWait)
			if calls := len(p.requests()); calls != 1 {
				t.Fatalf("fixture made %d summary requests, want 1", calls)
			}
			if got := f.count(isUsage); got != 1 {
				t.Fatalf("known usage receipts=%d for a provider-reported $%v summary outcome (text=%q), want exactly 1", got, bgtPricing.Cost(*u), text)
			}
			if text == "" && hasSummary(f.ag.Messages()) {
				t.Fatal("an unusable summary was adopted")
			}
		})
	}
}
