package responses

import (
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

const benchmarkStreamBytes = 100 * 1024

var benchmarkResponseText string

func BenchmarkMessageTextAccumulation(b *testing.B) {
	for _, chunkSize := range []int{128, 10} {
		chunks := benchmarkChunks(benchmarkStreamBytes, chunkSize)
		b.Run(chunkName(chunkSize), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				state := &streamState{
					message: core.Message{Role: "assistant", Provider: "openai"},
					slots:   make(map[int]*slot),
				}
				ch := make(chan core.AssistantEvent, len(chunks)+2)
				processEvent(state, &event{
					Type: eventOutputItemAdded, OutputIndex: 0,
					Item: &item{Type: "message"},
				}, ch)
				for _, chunk := range chunks {
					processEvent(state, &event{Type: eventOutputTextDelta, OutputIndex: 0, Delta: chunk}, ch)
				}
				processEvent(state, &event{
					Type: eventOutputItemDone, OutputIndex: 0,
					Item: &item{Type: "message"},
				}, ch)
				benchmarkResponseText = state.message.Content[0].Text
			}
		})
	}
}

func benchmarkChunks(total, size int) []string {
	chunks := make([]string, 0, (total+size-1)/size)
	remaining := total
	for remaining > 0 {
		n := min(size, remaining)
		chunks = append(chunks, string(make([]byte, n)))
		remaining -= n
	}
	return chunks
}

func chunkName(size int) string {
	if size == 10 {
		return "extreme_10B"
	}
	return "realistic_128B"
}
