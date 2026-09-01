package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

func joinText(content []core.Content) string {
	var b strings.Builder
	for _, c := range content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

func TestIsRetryableStreamError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New("openai: unknown error"), true},
		{errors.New("xai: stream error"), true},
		{errors.New("read: context deadline exceeded"), true},
		{errors.New("stream ended without response.completed"), true},
		{errors.New("connection lost"), true},
		{errors.New("provider: token refresh failed: invalid_grant"), false},
		{errors.New("HTTP 400: boom"), false},
		{context.Canceled, false},
	}
	for _, tc := range cases {
		got := isRetryableStreamError(tc.err)
		if got != tc.want {
			t.Errorf("isRetryableStreamError(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestStreamRepairContinuesPartialWithoutUserSigue(t *testing.T) {
	old := streamRepairBackoff
	streamRepairBackoff = []time.Duration{0, 0}
	t.Cleanup(func() { streamRepairBackoff = old })

	var sawHint bool
	provider := NewMockProvider(
		func(core.Request) (<-chan core.AssistantEvent, error) {
			ch := make(chan core.AssistantEvent, 2)
			ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: "Hello"}
			ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: errors.New("openai: unknown error")}
			close(ch)
			return ch, nil
		},
		func(req core.Request) (<-chan core.AssistantEvent, error) {
			for _, m := range req.Messages {
				if m.Role == "user" && strings.Contains(joinText(m.Content), "transport error") {
					sawHint = true
				}
			}
			ch := make(chan core.AssistantEvent, 2)
			msg := core.Message{Role: "assistant", Content: []core.Content{core.TextContent(" world")}, StopReason: "end_turn", Timestamp: time.Now().Unix()}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
			close(ch)
			return ch, nil
		},
	)
	ag := newTestAgent(provider)
	msgs, err := ag.Run(context.Background(), "start")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !sawHint {
		t.Fatal("repair request did not include the internal continue hint")
	}
	var assistant core.AgentMessage
	for _, m := range msgs {
		if m.Role == "assistant" {
			assistant = m
		}
	}
	got := joinText(assistant.Content)
	if !strings.Contains(got, "Hello") || !strings.Contains(got, "world") {
		t.Fatalf("merged assistant = %+v, want Hello + world", assistant.Content)
	}
	if assistant.Timestamp == 0 {
		t.Fatal("repaired assistant timestamp is zero")
	}
	for _, m := range msgs {
		if m.Role == "user" && strings.Contains(joinText(m.Content), "transport error") {
			t.Fatal("internal continue hint was persisted in the transcript")
		}
	}
}

func TestStreamRepairGivesUpAfterCap(t *testing.T) {
	old := streamRepairBackoff
	streamRepairBackoff = []time.Duration{0, 0}
	t.Cleanup(func() { streamRepairBackoff = old })

	fail := func(core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 1)
		ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: errors.New("openai: unknown error")}
		close(ch)
		return ch, nil
	}
	provider := NewMockProvider(fail, fail, fail, fail)
	ag := newTestAgent(provider)
	_, err := ag.Run(context.Background(), "start")
	if err == nil || !strings.Contains(err.Error(), "unknown error") {
		t.Fatalf("Run error = %v, want unknown error after cap", err)
	}
	if provider.calls != 3 {
		t.Fatalf("Stream calls = %d, want 3 (1 + %d repairs)", provider.calls, maxStreamRepairs)
	}
}

func TestStreamRepairDoesNotRetryToolCalls(t *testing.T) {
	old := streamRepairBackoff
	streamRepairBackoff = []time.Duration{0, 0}
	t.Cleanup(func() { streamRepairBackoff = old })

	provider := NewMockProvider(func(core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 3)
		ch <- core.AssistantEvent{Type: core.ProviderEventToolCallStart, ToolCallID: "c1", ToolName: "write"}
		ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: errors.New("openai: unknown error")}
		close(ch)
		return ch, nil
	})
	ag := newTestAgent(provider)
	_, err := ag.Run(context.Background(), "start")
	if err == nil {
		t.Fatal("expected error")
	}
	if provider.calls != 1 {
		t.Fatalf("Stream calls = %d, want 1 (no repair after a tool call)", provider.calls)
	}
}
