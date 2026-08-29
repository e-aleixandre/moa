package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// TestConsumeStream_NoExtendedCacheWrite pins the usage shape the Responses API
// produces for both providers that share this stream (OpenAI and xAI).
//
// Anthropic's cache write is split between a 5-minute and a 1-hour window,
// which are billed at different rates. These providers cache automatically:
// they report only cached read tokens, never a write, and honor no TTL. So
// CacheWrite and CacheWrite1h must both stay zero — a stray value there would
// bill phantom tokens at an extended rate the provider does not even offer.
func TestConsumeStream_NoExtendedCacheWrite(t *testing.T) {
	// A minimal but realistic completed response: an item, some text, and the
	// final usage. An empty response would be discarded as a stall before the
	// usage was ever surfaced.
	const sse = `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1"}}

data: {"type":"response.output_text.delta","output_index":0,"delta":"ok"}

data: {"type":"response.completed","response":{"id":"resp_1","model":"%s","status":"completed","usage":{"input_tokens":12000,"output_tokens":300,"total_tokens":12300,"input_tokens_details":{"cached_tokens":9000}}}}

`
	for _, tc := range []struct{ provider, model string }{
		{"openai", "gpt-5.6-terra"},
		{"xai", "grok-4.6"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			body := strings.NewReader(strings.Replace(sse, "%s", tc.model, 1))
			ch := make(chan core.AssistantEvent, 16)
			go func() {
				ConsumeStream(context.Background(), body, ch, tc.provider, tc.model)
				close(ch)
			}()

			var usage *core.Usage
			for ev := range ch {
				if ev.Message != nil && ev.Message.Usage != nil {
					usage = ev.Message.Usage
				}
				if ev.Partial != nil && ev.Partial.Usage != nil {
					usage = ev.Partial.Usage
				}
			}
			if usage == nil {
				t.Fatal("no usage reported")
			}
			if usage.CacheWrite != 0 || usage.CacheWrite1h != 0 {
				t.Errorf("cache write reported: CacheWrite=%d CacheWrite1h=%d, want 0/0",
					usage.CacheWrite, usage.CacheWrite1h)
			}
			// The cached portion is still split out of Input, which is what
			// keeps cache reads from being billed at the full input rate.
			if usage.CacheRead != 9000 || usage.Input != 3000 {
				t.Errorf("CacheRead=%d Input=%d, want 9000/3000", usage.CacheRead, usage.Input)
			}

			// And the resulting cost must equal a hand-computed one with no
			// write component at all.
			m, ok := core.ResolveModel(tc.model)
			if !ok || m.Pricing == nil {
				t.Fatalf("no pricing for %s", tc.model)
			}
			p := m.Pricing
			want := float64(usage.Input)*p.Input/1e6 +
				float64(usage.Output)*p.Output/1e6 +
				float64(usage.CacheRead)*p.CacheRead/1e6
			if got := p.Cost(*usage); got != want {
				t.Errorf("cost = %v, want %v", got, want)
			}
		})
	}
}
