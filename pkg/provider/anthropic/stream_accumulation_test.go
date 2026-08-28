package anthropic

import (
	"fmt"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestContentAccumulationPreservesTextThinkingAndSignature(t *testing.T) {
	a := &Anthropic{}
	state := &streamState{}

	a.handleContentBlockStart(`{"index":0,"content_block":{"type":"thinking"}}`, state)
	for _, delta := range []string{"reason", " in", " order"} {
		a.handleContentBlockDelta(fmt.Sprintf(`{"index":0,"delta":{"type":"thinking_delta","thinking":%q}}`, delta), state)
	}
	for _, delta := range []string{"sig", "nature"} {
		a.handleContentBlockDelta(fmt.Sprintf(`{"index":0,"delta":{"type":"signature_delta","signature":%q}}`, delta), state)
	}
	a.handleContentBlockStop(state)

	a.handleContentBlockStart(`{"index":1,"content_block":{"type":"text"}}`, state)
	for _, delta := range []string{"final", " ", "answer"} {
		a.handleContentBlockDelta(fmt.Sprintf(`{"index":1,"delta":{"type":"text_delta","text":%q}}`, delta), state)
	}
	a.handleContentBlockStop(state)
	final := a.handleMessageStop(state).Message

	if got := final.Content[0]; got.Thinking != "reason in order" || got.ThinkingSignature != "signature" {
		t.Fatalf("thinking block = %+v", got)
	}
	if got := final.Content[1].Text; got != "final answer" {
		t.Fatalf("final text = %q, want %q", got, "final answer")
	}
}

func TestAnthropicTextAccumulationAllocations(t *testing.T) {
	delta := strings.Repeat("x", 32)
	payload := fmt.Sprintf(`{"index":0,"delta":{"type":"text_delta","text":%q}}`, delta)
	allocs := testing.AllocsPerRun(5, func() {
		a := &Anthropic{}
		state := &streamState{
			message:    core.Message{Content: []core.Content{core.TextContent("")}},
			contentIdx: 0,
			blockType:  "text",
		}
		for range 256 {
			a.handleContentBlockDelta(payload, state)
		}
		a.handleContentBlockStop(state)
	})
	if allocs > 3100 {
		t.Fatalf("stream accumulation allocated %.0f objects, want at most 3100", allocs)
	}
}
