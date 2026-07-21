package bus

import (
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

func queryRunTokens(t *testing.T, b EventBus) RunTokens {
	t.Helper()
	tokens, err := QueryTyped[GetRunTokens, RunTokens](b, GetRunTokens{})
	if err != nil {
		t.Fatalf("GetRunTokens: %v", err)
	}
	return tokens
}

func TestRunTokens_LogicalTrafficAndReset(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{messages: []core.AgentMessage{
		core.WrapMessage(core.NewUserMessage("previous run")),
	}}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	b.Publish(RunStarted{SessionID: "test-session", RunGen: 1})
	b.Drain(time.Second)

	user := core.NewUserMessage("inspect the file")
	assistantOne := core.Message{Role: "assistant", Content: []core.Content{
		core.TextContent("I will inspect it."),
		core.ThinkingContent("internal reasoning is not output"),
		core.ToolCallContent("call-1", "read", map[string]any{"path": "a.go"}),
	}}
	toolResult := core.NewToolResultMessage("call-1", "read", []core.Content{core.TextContent("package main")}, false)
	assistantTwo := core.Message{Role: "assistant", Content: []core.Content{
		core.TextContent("The file is valid."),
		core.ThinkingContent("also excluded"),
	}}
	fa.mu.Lock()
	fa.messages = append(fa.messages,
		core.WrapMessage(user),
		core.WrapMessage(assistantOne),
		core.WrapMessage(toolResult),
		core.WrapMessage(assistantTwo),
	)
	fa.mu.Unlock()

	b.Publish(MessageEnded{SessionID: "test-session", RunGen: 1})
	b.Drain(time.Second)

	wantUp := core.EstimateTokens(user) + core.EstimateTokens(toolResult)
	wantDown := core.EstimateOutputTokens(assistantOne) + core.EstimateOutputTokens(assistantTwo)
	if got := queryRunTokens(t, b); got != (RunTokens{Up: wantUp, Down: wantDown}) {
		t.Fatalf("run tokens = %+v, want up=%d down=%d", got, wantUp, wantDown)
	}

	b.Publish(RunStarted{SessionID: "test-session", RunGen: 2})
	b.Drain(time.Second)
	if got := queryRunTokens(t, b); got != (RunTokens{}) {
		t.Fatalf("run tokens after reset = %+v, want zero", got)
	}
}
