package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// consumeUsage runs one SSE body through the stream and returns the usage of
// the final message.
func consumeUsage(t *testing.T, provider, model, sse string) *core.Usage {
	t.Helper()
	ch := make(chan core.AssistantEvent, 16)
	go func() {
		ConsumeStream(context.Background(), strings.NewReader(sse), ch, provider, model)
		close(ch)
	}()
	var usage *core.Usage
	for ev := range ch {
		if ev.Message != nil && ev.Message.Usage != nil {
			usage = ev.Message.Usage
		}
	}
	if usage == nil {
		t.Fatal("no usage reported")
	}
	return usage
}

// completedSSE builds a minimal but realistic completed response. The item and
// the text delta matter: an empty response is discarded as a stall before its
// usage is ever surfaced.
func completedSSE(model, usageJSON string) string {
	return `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1"}}

data: {"type":"response.output_text.delta","output_index":0,"delta":"ok"}

data: {"type":"response.completed","response":{"id":"resp_1","model":"` + model + `","status":"completed","usage":` + usageJSON + `}}

`
}

// TestConsumeStream_OpenAICacheWriteIsBilled covers the cache write OpenAI
// charges on GPT-5.6 and later: 1.25x the uncached input rate, reported as
// cache_write_tokens inside input_tokens_details.
//
// input_tokens is the total and includes both the cached and the written
// tokens, so all three buckets have to be split apart. Leaving the write inside
// the ordinary input bucket undercharged every written token by 25%.
func TestConsumeStream_OpenAICacheWriteIsBilled(t *testing.T) {
	usage := consumeUsage(t, "openai", "gpt-5.6-terra", completedSSE("gpt-5.6-terra",
		`{"input_tokens":15000,"output_tokens":300,"total_tokens":15300,"input_tokens_details":{"cached_tokens":9000,"cache_write_tokens":3000}}`))

	if usage.CacheRead != 9000 {
		t.Errorf("CacheRead = %d, want 9000", usage.CacheRead)
	}
	if usage.CacheWrite != 3000 {
		t.Errorf("CacheWrite = %d, want 3000", usage.CacheWrite)
	}
	// The ordinary bucket is what is left after both: 15000-9000-3000.
	if usage.Input != 3000 {
		t.Errorf("Input = %d, want 3000", usage.Input)
	}
	// OpenAI has no extended window; the 1h rate must never be applied.
	if usage.CacheWrite1h != 0 {
		t.Errorf("CacheWrite1h = %d, want 0", usage.CacheWrite1h)
	}

	m, ok := core.ResolveModel("gpt-5.6-terra")
	if !ok || m.Pricing == nil {
		t.Fatal("no pricing for gpt-5.6-terra")
	}
	p := m.Pricing
	want := 3000*p.Input/1e6 + 300*p.Output/1e6 + 9000*p.CacheRead/1e6 + 3000*p.CacheWrite/1e6
	if got := p.Cost(*usage); got != want {
		t.Errorf("cost = %v, want %v", got, want)
	}
}

// TestConsumeStream_NoCacheWriteReported covers responses without a
// cache_write_tokens field: xAI, whose caching is automatic and carries no
// write charge, and older OpenAI models that do not bill one either. The whole
// uncached remainder stays in the ordinary input bucket, as before.
func TestConsumeStream_NoCacheWriteReported(t *testing.T) {
	for _, tc := range []struct{ provider, model string }{
		{"xai", "grok-4.6"},
		{"openai", "gpt-5.5"},
	} {
		t.Run(tc.provider+"/"+tc.model, func(t *testing.T) {
			usage := consumeUsage(t, tc.provider, tc.model, completedSSE(tc.model,
				`{"input_tokens":12000,"output_tokens":300,"total_tokens":12300,"input_tokens_details":{"cached_tokens":9000}}`))

			if usage.CacheWrite != 0 || usage.CacheWrite1h != 0 {
				t.Errorf("cache write reported: CacheWrite=%d CacheWrite1h=%d, want 0/0",
					usage.CacheWrite, usage.CacheWrite1h)
			}
			if usage.CacheRead != 9000 || usage.Input != 3000 {
				t.Errorf("CacheRead=%d Input=%d, want 9000/3000", usage.CacheRead, usage.Input)
			}
		})
	}
}

// TestConsumeStream_CacheWriteClamped guards the arithmetic against an
// inconsistent report. A write larger than the uncached remainder must not
// drive the ordinary bucket negative, which would refund tokens that were
// actually billed.
func TestConsumeStream_CacheWriteClamped(t *testing.T) {
	usage := consumeUsage(t, "openai", "gpt-5.6-terra", completedSSE("gpt-5.6-terra",
		`{"input_tokens":10000,"output_tokens":10,"total_tokens":10010,"input_tokens_details":{"cached_tokens":9000,"cache_write_tokens":5000}}`))

	if usage.Input < 0 {
		t.Errorf("Input = %d, want >= 0", usage.Input)
	}
	if got := usage.Input + usage.CacheRead + usage.CacheWrite; got != 10000 {
		t.Errorf("buckets sum to %d, want 10000 (input_tokens)", got)
	}
}

func TestConsumeStream_ServiceTierControlsFastUsage(t *testing.T) {
	cases := []struct {
		name              string
		serviceTier       string
		requestedPriority bool
		wantFast          bool
	}{
		{"response priority", `,"service_tier":"priority"`, false, true},
		{"response default", `,"service_tier":"default"`, true, false},
		{"missing tier falls back to request", ``, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sse := `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1"}}

data: {"type":"response.output_text.delta","output_index":0,"delta":"ok"}

data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.6-terra","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}` + tc.serviceTier + `}}

`
			ch := make(chan core.AssistantEvent, 16)
			go func() {
				ConsumeStreamWithPriority(context.Background(), strings.NewReader(sse), ch, "openai", "gpt-5.6-terra", tc.requestedPriority)
				close(ch)
			}()
			var usage *core.Usage
			for event := range ch {
				if event.Message != nil {
					usage = event.Message.Usage
				}
			}
			if usage == nil || usage.Fast != tc.wantFast {
				t.Errorf("Usage.Fast = %v, want %v", usage != nil && usage.Fast, tc.wantFast)
			}
		})
	}
}
