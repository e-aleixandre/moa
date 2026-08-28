package bus

import (
	"fmt"
	"strings"
	"testing"
)

var benchmarkStreamText string

func TestStreamingAggregateAccumulationAllocations(t *testing.T) {
	delta := strings.Repeat("x", 32)
	allocs := testing.AllocsPerRun(10, func() {
		sctx := &SessionContext{}
		for range 256 {
			sctx.streamMu.Lock()
			sctx.appendStreamTextLocked(delta)
			sctx.streamMu.Unlock()
		}
		benchmarkStreamText, _, _ = sctx.StreamingAggregate()
	})
	if allocs > 30 {
		t.Fatalf("streaming aggregate allocated %.0f objects, want at most 30", allocs)
	}
}

func BenchmarkStreamingAggregateAccumulation(b *testing.B) {
	for _, chunkSize := range []int{128, 10} {
		chunks := busBenchmarkChunks(100*1024, chunkSize)
		b.Run(fmt.Sprintf("chunk_%dB", chunkSize), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				sctx := &SessionContext{}
				for _, chunk := range chunks {
					sctx.streamMu.Lock()
					sctx.appendStreamTextLocked(chunk)
					sctx.streamMu.Unlock()
				}
				benchmarkStreamText, _, _ = sctx.StreamingAggregate()
			}
		})
	}
}

func busBenchmarkChunks(total, size int) []string {
	chunks := make([]string, 0, (total+size-1)/size)
	for remaining := total; remaining > 0; {
		n := min(size, remaining)
		chunks = append(chunks, string(make([]byte, n)))
		remaining -= n
	}
	return chunks
}
