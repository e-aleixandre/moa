package goal

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

func TestParseVerdict(t *testing.T) {
	tests := []struct {
		name          string
		in            string
		wantSatisfied bool
		wantFeedback  string
	}{
		{"clean json satisfied", `{"satisfied": true, "feedback": "all tests pass"}`, true, "all tests pass"},
		{"clean json not satisfied", `{"satisfied": false, "feedback": "3 tests still failing"}`, false, "3 tests still failing"},
		{"fenced json", "```json\n{\"satisfied\": true, \"feedback\": \"ok\"}\n```", true, "ok"},
		{"prose wrapped", "Here is my verdict:\n{\"satisfied\": false, \"feedback\": \"missing docs\"}\nThanks", false, "missing docs"},
		{"unparseable falls back to not-satisfied", "I think it looks good honestly", false, "I think it looks good honestly"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := parseVerdict(tt.in)
			if v.Satisfied != tt.wantSatisfied {
				t.Errorf("satisfied: got %v want %v", v.Satisfied, tt.wantSatisfied)
			}
			if v.Feedback != tt.wantFeedback {
				t.Errorf("feedback: got %q want %q", v.Feedback, tt.wantFeedback)
			}
		})
	}
}

// stubProvider returns a fixed assistant text for any request.
type stubProvider struct{ text string }

func (p stubProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	ch := make(chan core.AssistantEvent, 4)
	go func() {
		defer close(ch)
		msg := core.Message{
			Role:       "assistant",
			Content:    []core.Content{core.TextContent(p.text)},
			StopReason: "end_turn",
			Timestamp:  time.Now().Unix(),
		}
		ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: p.text}
		ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
	}()
	return ch, nil
}

func TestVerify_ParsesModelOutput(t *testing.T) {
	factory := func(core.Model) (core.Provider, error) {
		return stubProvider{text: `{"satisfied": true, "feedback": "objective met"}`}, nil
	}
	v, err := Verify(context.Background(), factory, "haiku", "make the build green", "go build ./... exit 0")
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if !v.Satisfied || v.Feedback != "objective met" {
		t.Fatalf("unexpected verdict: %+v", v)
	}
}

func TestVerify_NilFactory(t *testing.T) {
	if _, err := Verify(context.Background(), nil, "haiku", "obj", "ev"); err == nil {
		t.Fatal("Verify should error on nil factory")
	}
}

func TestVerify_UnknownModel(t *testing.T) {
	factory := func(core.Model) (core.Provider, error) { return stubProvider{}, nil }
	if _, err := Verify(context.Background(), factory, "no-such-model-xyz", "obj", "ev"); err == nil {
		t.Fatal("Verify should error when the verifier model can't be resolved")
	}
}

func TestVerify_ProviderError(t *testing.T) {
	factory := func(core.Model) (core.Provider, error) { return nil, fmt.Errorf("boom") }
	if _, err := Verify(context.Background(), factory, "haiku", "obj", "ev"); err == nil {
		t.Fatal("Verify should propagate provider-factory errors")
	}
}
