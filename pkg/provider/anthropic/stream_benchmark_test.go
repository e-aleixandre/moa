package anthropic

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

var benchmarkAnthropicText string

func BenchmarkMessageTextAccumulation(b *testing.B) {
	for _, chunkSize := range []int{128, 10} {
		chunks := anthropicBenchmarkChunks(100*1024, chunkSize)
		payloads := make([]string, len(chunks))
		for i, chunk := range chunks {
			raw, _ := json.Marshal(map[string]any{
				"index": 0,
				"delta": map[string]any{"type": "text_delta", "text": chunk},
			})
			payloads[i] = string(raw)
		}
		b.Run(fmt.Sprintf("chunk_%dB", chunkSize), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				state := &streamState{}
				state.message.Content = append(state.message.Content, coreTextContent())
				state.contentIdx = 0
				state.blockType = "text"
				a := &Anthropic{}
				for _, payload := range payloads {
					a.handleContentBlockDelta(payload, state)
				}
				a.handleContentBlockStop(state)
				benchmarkAnthropicText = state.message.Content[0].Text
			}
		})
	}
}

func coreTextContent() core.Content { return core.TextContent("") }

func anthropicBenchmarkChunks(total, size int) []string {
	chunks := make([]string, 0, (total+size-1)/size)
	for remaining := total; remaining > 0; {
		n := min(size, remaining)
		chunks = append(chunks, string(make([]byte, n)))
		remaining -= n
	}
	return chunks
}
