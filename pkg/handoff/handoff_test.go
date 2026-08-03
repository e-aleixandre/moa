package handoff

import (
	"context"
	"errors"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

type providerFunc func(context.Context, core.Request) (<-chan core.AssistantEvent, error)

func (f providerFunc) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	return f(ctx, req)
}

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want Options
		ok   bool
	}{
		{name: "defaults", ok: true},
		{name: "separate flags", args: []string{"--model", "fable", "--thinking", "high"}, want: Options{ModelSpec: "fable", Thinking: "high"}, ok: true},
		{name: "equals flags", args: []string{"--model=fable", "--thinking=low"}, want: Options{ModelSpec: "fable", Thinking: "low"}, ok: true},
		{name: "unknown", args: []string{"continue"}},
		{name: "missing value", args: []string{"--model"}},
		{name: "invalid thinking", args: []string{"--thinking", "maximum"}},
		{name: "duplicate", args: []string{"--thinking", "low", "--thinking", "high"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.args)
			if tt.ok {
				if err != nil {
					t.Fatal(err)
				}
				if got != tt.want {
					t.Fatalf("Parse(%q) = %#v, want %#v", tt.args, got, tt.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("Parse(%q) succeeded", tt.args)
			}
		})
	}
}

func TestPrompt(t *testing.T) {
	got := Prompt("  ## Goal\nShip it\n")
	if got != "# Handoff from the previous conversation\n\n## Goal\nShip it\n\nContinue from this handoff. Verify the current repository state before changing files." {
		t.Fatalf("Prompt() = %q", got)
	}
}

func TestGenerateRejectsCancelledResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ch := make(chan core.AssistantEvent, 1)
	msg := core.Message{Role: "assistant", Content: []core.Content{core.TextContent("late handoff")}}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
	close(ch)
	_, _, err := Generate(ctx, providerFunc(func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
		return ch, nil
	}), core.Model{}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Generate() error = %v, want context.Canceled", err)
	}
}
