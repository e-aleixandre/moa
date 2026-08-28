package agent

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

func TestStreamErrorPersistsPartialTextAndThinking(t *testing.T) {
	provider := NewMockProvider(func(core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 3)
		ch <- core.AssistantEvent{Type: core.ProviderEventThinkingDelta, Delta: "valuable reasoning"}
		ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: "valuable partial"}
		ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: errors.New("connection lost")}
		close(ch)
		return ch, nil
	})
	ag := newTestAgent(provider)

	msgs, err := ag.Run(context.Background(), "start")
	if err == nil || !strings.Contains(err.Error(), "connection lost") {
		t.Fatalf("Run error = %v, want connection lost", err)
	}
	if len(msgs) != 2 || msgs[1].Role != "assistant" {
		t.Fatalf("partial stream message was not persisted: %+v", msgs)
	}
	got := msgs[1].Content
	if len(got) != 3 || got[0].Thinking != "valuable reasoning" || got[1].Text != "valuable partial" || got[2].Text != "(stopped: stream: connection lost)" {
		t.Fatalf("persisted partial stream content = %+v, want thinking, text, and stream error marker", got)
	}
}

func TestConsumeStreamReturnsPartialWhenChannelClosesWithoutDone(t *testing.T) {
	ch := make(chan core.AssistantEvent, 2)
	ch <- core.AssistantEvent{Type: core.ProviderEventThinkingDelta, Delta: "unfinished reasoning"}
	ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: "unfinished answer"}
	close(ch)

	msg, err := consumeStream(context.Background(), ch, NewEmitter(nil))
	if err == nil || !strings.Contains(err.Error(), "without final message") {
		t.Fatalf("consumeStream error = %v, want missing final message", err)
	}
	if msg == nil || len(msg.Content) != 2 || msg.Content[0].Thinking != "unfinished reasoning" || msg.Content[1].Text != "unfinished answer" {
		t.Fatalf("partial message = %+v, want accumulated thinking and text", msg)
	}
}

func TestPartialStreamErrorClosesOpenToolCalls(t *testing.T) {
	provider := NewMockProvider(func(core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 3)
		ch <- core.AssistantEvent{Type: core.ProviderEventToolCallStart, ToolCallID: "call_partial", ToolName: "write"}
		ch <- core.AssistantEvent{Type: core.ProviderEventToolCallDelta, ToolCallID: "call_partial", ToolName: "write", PartialArgs: map[string]any{"path": "draft.txt"}}
		ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: errors.New("connection lost")}
		close(ch)
		return ch, nil
	})
	ag := newTestAgent(provider)

	msgs, err := ag.Run(context.Background(), "start")
	if err == nil {
		t.Fatal("Run returned nil error")
	}
	if len(msgs) != 3 || msgs[1].Role != "assistant" || msgs[2].Role != "tool_result" {
		t.Fatalf("partial tool call was not closed: %+v", msgs)
	}
	if calls := extractToolCalls(&msgs[1].Message); len(calls) != 1 || calls[0].ToolCallID != "call_partial" {
		t.Fatalf("persisted tool calls = %+v, want call_partial", calls)
	}
	if msgs[2].ToolCallID != "call_partial" || !msgs[2].IsError {
		t.Fatalf("synthetic tool result = %+v, want error for call_partial", msgs[2])
	}

	tree := session.NewTree()
	for _, msg := range msgs {
		tree.Append(session.Entry{Type: session.EntryMessage, Message: msg})
	}
	tree.Append(session.Entry{
		Type: session.EntryMessage,
		Message: core.WrapMessage(core.Message{
			Role:    "assistant",
			Content: []core.Content{core.TextContent("balance probe")},
		}),
	})
	if err := tree.ValidBranchTarget(tree.LeafID()); err != nil {
		t.Fatalf("persisted context has a dangling tool call: %v", err)
	}
}

func TestPartialEmptyResponseIsNotRetriedOrDuplicated(t *testing.T) {
	provider := NewMockProvider(
		func(core.Request) (<-chan core.AssistantEvent, error) {
			ch := make(chan core.AssistantEvent, 2)
			ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: "valuable partial"}
			ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: &core.EmptyResponseError{Provider: "mock"}}
			close(ch)
			return ch, nil
		},
		simpleTextResponse("valuable partial"),
	)
	ag := newTestAgent(provider)

	msgs, err := ag.Run(context.Background(), "start")
	if err == nil {
		t.Fatal("partial stream failure was retried as a successful empty response")
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want 1; a response with partial output must not be retried", provider.calls)
	}
	occurrences := 0
	for _, msg := range msgs {
		for _, content := range msg.Content {
			if strings.Contains(content.Text, "valuable partial") {
				occurrences++
			}
		}
	}
	if occurrences != 1 {
		t.Fatalf("partial text occurrences = %d, want exactly 1; messages: %+v", occurrences, msgs)
	}
}

// TestConsumeStream_CancelledTurnWithLateDoneIsNotSuccess pins M18: when the
// turn is cancelled but the provider still delivers a complete final message
// within the drain window, consumeStream must return that message WITH a
// cancellation error — never a nil error. A nil error would make the run loop
// treat the cancelled turn as a clean success and execute its tool calls.
func TestConsumeStream_CancelledTurnWithLateDoneIsNotSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan core.AssistantEvent, 4)

	// Cancel first, then queue a complete Done message so the drain path sees it.
	cancel()
	final := &core.Message{Role: "assistant", Content: []core.Content{core.TextContent("done")}}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: final}

	msg, err := consumeStream(ctx, ch, NewEmitter(nil))

	if err == nil {
		t.Fatal("cancelled turn returned nil error — caller would execute its tool calls")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if msg != final {
		t.Fatalf("msg = %v, want the complete final message preserved for history", msg)
	}
}

type cancelledToolCallProvider struct {
	started chan struct{}
}

type cancelledPartialProvider struct {
	streamed chan struct{}
}

func (p *cancelledPartialProvider) Stream(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
	ch := make(chan core.AssistantEvent, 5)
	ch <- core.AssistantEvent{Type: core.ProviderEventThinkingDelta, Delta: "reason"}
	ch <- core.AssistantEvent{Type: core.ProviderEventThinkingDelta, Delta: "ing"}
	ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: "partial "}
	ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: "answer"}
	close(p.streamed)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

func TestAbortPersistsPartialStreamTextAndThinking(t *testing.T) {
	provider := &cancelledPartialProvider{streamed: make(chan struct{})}
	ag := newTestAgent(provider)
	done := make(chan error, 1)
	go func() {
		_, err := ag.Run(context.Background(), "start")
		done <- err
	}()

	<-provider.streamed
	ag.Abort()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("aborted run did not finish")
	}

	msgs := ag.Messages()
	if len(msgs) != 2 || msgs[1].Role != "assistant" {
		t.Fatalf("cancelled partial message was not persisted: %+v", msgs)
	}
	if got := msgs[1].Content; len(got) != 2 || got[0].Thinking != "reasoning" || got[1].Text != "partial answer" {
		t.Fatalf("persisted partial content = %+v", got)
	}
}

func (p *cancelledToolCallProvider) Stream(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
	ch := make(chan core.AssistantEvent, 1)
	close(p.started)
	go func() {
		defer close(ch)
		<-ctx.Done()
		msg := &core.Message{
			Role: "assistant",
			Content: []core.Content{{
				Type:       "tool_call",
				ToolCallID: "toolu_cancelled",
				ToolName:   "block",
			}},
		}
		ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: msg}
	}()
	return ch, nil
}

func TestAbortWithLateToolCallKeepsHistoryBalanced(t *testing.T) {
	provider := &cancelledToolCallProvider{started: make(chan struct{})}
	var executed atomic.Bool
	block := core.Tool{
		Name:       "block",
		Parameters: []byte(`{"type":"object"}`),
		Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
			executed.Store(true)
			return core.TextResult("unexpected"), nil
		},
	}
	ag := newTestAgent(provider, block)
	done := make(chan error, 1)
	go func() {
		_, err := ag.Run(context.Background(), "run the tool")
		done <- err
	}()

	<-provider.started
	ag.Abort()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("aborted run did not finish")
	}
	if executed.Load() {
		t.Fatal("tool was executed after cancellation")
	}

	msgs := ag.Messages()
	if len(msgs) != 3 || msgs[1].Role != "assistant" || msgs[2].Role != "tool_result" {
		t.Fatalf("cancelled tool call was not closed: %+v", msgs)
	}
	if msgs[2].ToolCallID != "toolu_cancelled" || !msgs[2].IsError {
		t.Fatalf("synthetic result = %+v, want cancelled error for toolu_cancelled", msgs[2])
	}

	tree := session.NewTree()
	for _, msg := range msgs {
		tree.Append(session.Entry{Type: session.EntryMessage, Message: msg})
	}
	tree.Append(session.Entry{
		Type: session.EntryMessage,
		Message: core.WrapMessage(core.Message{
			Role:    "assistant",
			Content: []core.Content{core.TextContent("balance probe")},
		}),
	})
	if err := tree.ValidBranchTarget(tree.LeafID()); err != nil {
		t.Fatalf("persisted context has a dangling tool call: %v", err)
	}
}

func TestAbortDuringToolExecutionPersistsErrorResult(t *testing.T) {
	started := make(chan struct{})
	block := core.Tool{
		Name:       "block",
		Parameters: []byte(`{"type":"object"}`),
		Execute: func(ctx context.Context, _ map[string]any, _ func(core.Result)) (core.Result, error) {
			close(started)
			<-ctx.Done()
			return core.Result{}, ctx.Err()
		},
	}
	ag := newTestAgent(NewMockProvider(toolCallResponse("toolu_running", "block", nil)), block)
	done := make(chan error, 1)
	go func() {
		_, err := ag.Run(context.Background(), "run the tool")
		done <- err
	}()

	<-started
	ag.Abort()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("aborted run did not finish")
	}

	msgs := ag.Messages()
	if len(msgs) != 3 || msgs[2].Role != "tool_result" {
		t.Fatalf("running tool result was discarded: %+v", msgs)
	}
	if msgs[2].ToolCallID != "toolu_running" || !msgs[2].IsError {
		t.Fatalf("tool result = %+v, want cancellation error for toolu_running", msgs[2])
	}
}

// A tool's Custom annotations reach the recorded tool_result: that map is how
// the subagent tool records which job a call spawned, and the UI reads it back
// from the transcript long after the job is gone.
func TestToolResultMessageCarriesResultCustom(t *testing.T) {
	call := core.Content{Type: "tool_call", ToolCallID: "toolu_1", ToolName: "subagent"}
	result := core.Result{Content: []core.Content{core.TextContent("done")}, Custom: map[string]any{"subagent_job_id": "sa-1"}}

	msg := toolResultMessage(call, result, false, false)
	if got := msg.Custom["subagent_job_id"]; got != "sa-1" {
		t.Fatalf("subagent_job_id = %v, want sa-1", got)
	}

	rejected := toolResultMessage(call, result, false, true)
	if rejected.Custom["subagent_job_id"] != "sa-1" || rejected.Custom["rejected"] != true {
		t.Fatalf("annotations and rejection must coexist, got %+v", rejected.Custom)
	}
}
