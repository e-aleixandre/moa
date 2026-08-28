package responses

import (
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestMessageTextAccumulationPreservesOrderWithoutItemDone(t *testing.T) {
	state := &streamState{
		message: core.Message{Role: "assistant", Provider: "openai"},
		slots:   make(map[int]*slot),
	}
	ch := make(chan core.AssistantEvent, 8)
	processEvent(state, &event{
		Type: eventOutputItemAdded, OutputIndex: 0,
		Item: &item{Type: "message"},
	}, ch)
	for _, delta := range []string{"alpha", " ", "beta", " ", "gamma"} {
		processEvent(state, &event{Type: eventOutputTextDelta, OutputIndex: 0, Delta: delta}, ch)
	}
	completed := event{Type: eventCompleted}
	completed.Response = newResponse("completed")
	processEvent(state, &completed, ch)

	if got := state.message.Content[0].Text; got != "alpha beta gamma" {
		t.Fatalf("final text = %q, want %q", got, "alpha beta gamma")
	}
}

func TestMessageTextAccumulationAllocations(t *testing.T) {
	delta := strings.Repeat("x", 32)
	allocs := testing.AllocsPerRun(5, func() {
		state := &streamState{
			message: core.Message{Role: "assistant", Provider: "openai"},
			slots:   make(map[int]*slot),
		}
		ch := make(chan core.AssistantEvent, 258)
		processEvent(state, &event{
			Type: eventOutputItemAdded, OutputIndex: 0,
			Item: &item{Type: "message"},
		}, ch)
		for range 256 {
			processEvent(state, &event{Type: eventOutputTextDelta, OutputIndex: 0, Delta: delta}, ch)
		}
		processEvent(state, &event{
			Type: eventOutputItemDone, OutputIndex: 0,
			Item: &item{Type: "message"},
		}, ch)
	})
	if allocs > 30 {
		t.Fatalf("stream accumulation allocated %.0f objects, want at most 30", allocs)
	}
}

func newResponse(status string) *struct {
	ID     string `json:"id"`
	Model  string `json:"model"`
	Status string `json:"status"`
	Usage  *struct {
		InputTokens        int `json:"input_tokens"`
		OutputTokens       int `json:"output_tokens"`
		TotalTokens        int `json:"total_tokens"`
		InputTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"input_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	EndTurn *bool `json:"end_turn"`
} {
	return &struct {
		ID     string `json:"id"`
		Model  string `json:"model"`
		Status string `json:"status"`
		Usage  *struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			TotalTokens        int `json:"total_tokens"`
			InputTokensDetails *struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		EndTurn *bool `json:"end_turn"`
	}{Status: status}
}
