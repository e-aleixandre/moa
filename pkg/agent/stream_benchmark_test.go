package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

var benchmarkPartialMessage *core.Message

func TestPartialTextAccumulationAllocations(t *testing.T) {
	delta := strings.Repeat("x", 32)
	allocs := testing.AllocsPerRun(10, func() {
		ctx, cancel := context.WithCancel(context.Background())
		ch := make(chan core.AssistantEvent, 256)
		for range 256 {
			ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: delta}
		}
		close(ch)
		cancel()
		consumeStream(ctx, ch, NewEmitter(nil)) //nolint:errcheck
	})
	if allocs > 50 {
		t.Fatalf("partial stream accumulation allocated %.0f objects, want at most 50", allocs)
	}
}

func BenchmarkPartialTextAccumulation(b *testing.B) {
	for _, chunkSize := range []int{128, 10} {
		chunks := agentBenchmarkChunks(100*1024, chunkSize)
		b.Run(fmt.Sprintf("chunk_%dB", chunkSize), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				ctx, cancel := context.WithCancel(context.Background())
				ch := make(chan core.AssistantEvent, len(chunks))
				for _, chunk := range chunks {
					ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: chunk}
				}
				close(ch)
				cancel()
				benchmarkPartialMessage, _ = consumeStream(ctx, ch, NewEmitter(nil))
			}
		})
	}
}

func agentBenchmarkChunks(total, size int) []string {
	chunks := make([]string, 0, (total+size-1)/size)
	for remaining := total; remaining > 0; {
		n := min(size, remaining)
		chunks = append(chunks, string(make([]byte, n)))
		remaining -= n
	}
	return chunks
}
