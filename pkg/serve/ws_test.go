package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

func TestWsEventFromBus_SubagentStarted(t *testing.T) {
	startedAt := time.UnixMilli(1_700_000_000_000)
	ev, ok := wsEventFromBus(bus.SubagentStarted{
		SessionID: "s1", JobID: "sa-1", OriginToolCallID: "toolu_1", Task: "do thing", Model: "haiku", Thinking: "high", Async: true,
		StartedAt: startedAt,
	})
	if !ok {
		t.Fatal("expected ok=true")
	}
	if ev.Type != "subagent_start" {
		t.Fatalf("Type = %q, want subagent_start", ev.Type)
	}
	data, ok := ev.Data.(SubagentStartData)
	if !ok {
		t.Fatalf("Data type = %T, want SubagentStartData", ev.Data)
	}
	want := SubagentStartData{JobID: "sa-1", OriginToolCallID: "toolu_1", Task: "do thing", Model: "haiku", Thinking: "high", Async: true, StartedAtMs: 1_700_000_000_000}
	if data != want {
		t.Fatalf("Data = %+v, want %+v", data, want)
	}

	t.Run("zero start time omits timestamp", func(t *testing.T) {
		ev, _ := wsEventFromBus(bus.SubagentStarted{JobID: "sa-2"})
		if data := ev.Data.(SubagentStartData); data.StartedAtMs != 0 {
			t.Fatalf("StartedAtMs = %d, want 0 for zero time", data.StartedAtMs)
		}
	})
}

func TestWsAttentionEventsCarryOccurrenceGeneration(t *testing.T) {
	permission, ok := wsEventFromBus(bus.PermissionRequested{SessionID: "s1", ID: "p1"}, 11)
	if !ok {
		t.Fatal("permission event not translated")
	}
	if got := permission.Data.(PermissionData).UnseenGen; got != 11 {
		t.Fatalf("permission unseen_gen = %d, want 11", got)
	}
	errorEvent, ok := wsEventFromBus(bus.StateChanged{SessionID: "s1", State: string(bus.StateError)}, 12)
	if !ok {
		t.Fatal("error event not translated")
	}
	if got := errorEvent.Data.(StateChangeData).UnseenGen; got != 12 {
		t.Fatalf("error unseen_gen = %d, want 12", got)
	}
	completion, ok := wsEventFromBus(bus.RunEnded{SessionID: "s1", RunGen: 1}, 13)
	if !ok {
		t.Fatal("completion event not translated")
	}
	if got := completion.Data.(RunEndData).UnseenGen; got != 13 {
		t.Fatalf("completion unseen_gen = %d, want 13", got)
	}
	cancelled, _ := wsEventFromBus(bus.RunEnded{Cancelled: true})
	if data := cancelled.Data.(RunEndData); !data.Cancelled || data.HasError {
		t.Fatalf("cancelled run end = %+v", data)
	}
	failed, _ := wsEventFromBus(bus.RunEnded{Err: errors.New("boom")})
	if data := failed.Data.(RunEndData); data.Cancelled || !data.HasError {
		t.Fatalf("failed run end = %+v", data)
	}
}

func TestWsPromptResolutionProjectsOnlyPromptIdentity(t *testing.T) {
	event := bus.PermissionResolved{ID: "p1"}
	projected, ok := wsEventFromBus(event, 0)
	if !ok {
		t.Fatal("resolution event not translated")
	}
	if got := projected.Data.(PromptResolutionData); got.ID != "p1" || got.Kind != "permission" {
		t.Fatalf("resolution = %+v", got)
	}
	ask, _ := wsEventFromBus(bus.AskUserResolved{ID: "a1"}, 0)
	if got := ask.Data.(PromptResolutionData); got.ID != "a1" || got.Kind != "ask" {
		t.Fatalf("ask resolution = %+v", got)
	}
}

func TestBuildInitData_SubagentThinking(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(delayedResponseHandler(time.Second, "done")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	subagentTool, ok := sess.infra.toolReg.Get("subagent")
	if !ok {
		t.Fatal("subagent tool not registered")
	}
	if _, err := subagentTool.Execute(core.WithToolCallID(context.Background(), "toolu_init"), map[string]any{
		"task": "inspect the contract", "async": true, "thinking": "medium",
	}, nil); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, time.Second, "subagent job creation", func() bool {
		return len(sess.subagents.Snapshot()) == 1
	})

	data := buildInitData(sess, bus.StreamingAggregate{}, nil)
	if len(data.Subagents) != 1 {
		t.Fatalf("Subagents = %+v, want one job", data.Subagents)
	}
	if got := data.Subagents[0].Thinking; got != "medium" {
		t.Fatalf("Thinking = %q, want medium", got)
	}
	if got := data.Subagents[0].OriginToolCallID; got != "toolu_init" {
		t.Fatalf("OriginToolCallID = %q, want toolu_init", got)
	}
}

func TestBuildInitDataCarriesAttentionNamespace(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	data := buildInitData(sess, bus.StreamingAggregate{}, nil)
	if data.AttentionNamespace != sess.attentionNamespace || data.AttentionNamespace == "" {
		t.Fatalf("attention namespace = %q, want %q", data.AttentionNamespace, sess.attentionNamespace)
	}
}

func TestBuildInitData_DeltaMessages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(delayedResponseHandler(time.Second, "done")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	tree := sess.runtime.Context().Tree
	tree.Append(session.Entry{Type: session.EntryMessage, Message: core.WrapMessage(core.Message{Role: "user", MsgID: "one", Content: []core.Content{core.TextContent("one")}})})
	tree.Append(session.Entry{Type: session.EntryMessage, Message: core.WrapMessage(core.Message{Role: "assistant", MsgID: "two", Content: []core.Content{core.TextContent("two")}})})

	data := buildInitDataAtAttentionGen(sess, bus.StreamingAggregate{}, nil, 0, "one", "", "")
	if data.DeltaBase != "one" || len(data.Messages) != 1 || data.Messages[0].MsgID != "two" {
		t.Fatalf("delta init = %+v", data)
	}

	data = buildInitDataAtAttentionGen(sess, bus.StreamingAggregate{}, nil, 0, "two", "", "")
	if data.DeltaBase != "two" || len(data.Messages) != 0 {
		t.Fatalf("empty delta init = %+v", data)
	}
}

func TestBuildInitData_InvalidDeltaFallsBackToFull(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(delayedResponseHandler(time.Second, "done")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	tree := sess.runtime.Context().Tree
	base := tree.Append(session.Entry{Type: session.EntryMessage, Message: core.WrapMessage(core.Message{Role: "user", MsgID: "base", Content: []core.Content{core.TextContent("base")}})})
	offPath := tree.Append(session.Entry{Type: session.EntryMessage, Message: core.WrapMessage(core.Message{Role: "assistant", MsgID: "old", Content: []core.Content{core.TextContent("old")}})})
	if err := tree.Branch(base); err != nil {
		t.Fatal(err)
	}
	tree.Append(session.Entry{Type: session.EntryMessage, Message: core.WrapMessage(core.Message{Role: "assistant", MsgID: "new", Content: []core.Content{core.TextContent("new")}})})

	data := buildInitDataAtAttentionGen(sess, bus.StreamingAggregate{}, nil, 0, offPath, "", "")
	if data.DeltaBase != "" || len(data.Messages) != 2 || data.Messages[1].MsgID != "new" {
		t.Fatalf("off-path fallback = %+v", data)
	}
	tree.Clear()
	data = buildInitDataAtAttentionGen(sess, bus.StreamingAggregate{}, nil, 0, base, "", "")
	if data.DeltaBase != "" || len(data.Messages) != 0 {
		t.Fatalf("clear fallback = %+v", data)
	}
}

func TestBuildInitData_DeltaIncludesCompactionMarker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(delayedResponseHandler(time.Second, "done")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	tree := sess.runtime.Context().Tree
	base := tree.Append(session.Entry{Type: session.EntryMessage, Message: core.WrapMessage(core.Message{Role: "user", MsgID: "base", Content: []core.Content{core.TextContent("base")}})})
	tree.Append(session.Entry{Type: session.EntryCompaction, Compaction: session.CompactionData{Summary: "summary", FirstKeptEntryID: base, TokensBefore: 4000}})
	data := buildInitDataAtAttentionGen(sess, bus.StreamingAggregate{}, nil, 0, base, "", "")
	if data.DeltaBase != base || len(data.Messages) != 1 || data.Messages[0].Role != "session_event" {
		t.Fatalf("compaction delta = %+v", data)
	}
}

func TestWsEventFromBus_SubagentUsage(t *testing.T) {
	t.Run("with usage", func(t *testing.T) {
		ev, ok := wsEventFromBus(bus.SubagentUsage{
			SessionID: "s1", JobID: "sa-1",
			Usage:   &core.Usage{Input: 100, Output: 42},
			CostUSD: 0.0123,
		})
		if !ok {
			t.Fatal("expected ok=true")
		}
		if ev.Type != "subagent_usage" {
			t.Fatalf("Type = %q, want subagent_usage", ev.Type)
		}
		data, ok := ev.Data.(SubagentUsageData)
		if !ok {
			t.Fatalf("Data type = %T, want SubagentUsageData", ev.Data)
		}
		want := SubagentUsageData{JobID: "sa-1", InputTokens: 100, OutputTokens: 42, CostUSD: 0.0123}
		if data != want {
			t.Fatalf("Data = %+v, want %+v", data, want)
		}
	})

	t.Run("nil usage", func(t *testing.T) {
		ev, ok := wsEventFromBus(bus.SubagentUsage{SessionID: "s1", JobID: "sa-2", Usage: nil, CostUSD: 0})
		if !ok {
			t.Fatal("expected ok=true")
		}
		data := ev.Data.(SubagentUsageData)
		if data.InputTokens != 0 || data.OutputTokens != 0 {
			t.Fatalf("expected zero tokens for nil usage, got %+v", data)
		}
	})

	t.Run("is lossy", func(t *testing.T) {
		ev, _ := wsEventFromBus(bus.SubagentUsage{JobID: "sa-1"})
		if !isLossyWsEvent(ev) {
			t.Fatal("subagent_usage should be lossy")
		}
	})

	t.Run("terminal outcome preserves result separately from failure", func(t *testing.T) {
		finished := time.UnixMilli(1_700_000_000_000)
		ev, ok := wsEventFromBus(bus.SubagentEnded{
			SessionID: "s1", JobID: "sa-3", Task: "inspect", Async: true,
			Status: "failed", Error: "connection refused", FinishedAt: finished,
		})
		if !ok {
			t.Fatal("expected ok=true")
		}
		data := ev.Data.(SubagentEndData)
		if data.Task != "inspect" || !data.Async || data.Status != "failed" || data.Result != "" || data.Error != "connection refused" || data.FinishedAtMs != finished.UnixMilli() {
			t.Fatalf("terminal data = %+v", data)
		}
	})
}

func TestWsEventFromBus_MessageEnded_InputIncludesCache(t *testing.T) {
	// The ↑ heartbeat must reflect the input the model actually processed:
	// fresh input PLUS the cached context replayed each step (CacheRead/Write).
	// Usage.Input alone omits cache, which under Anthropic prompt caching is
	// nearly the whole prompt — the count would read far too low.
	ev, ok := wsEventFromBus(bus.MessageEnded{
		Message: core.AgentMessage{Message: core.Message{
			MsgID: "m1",
			Usage: &core.Usage{Input: 500, CacheRead: 12000, CacheWrite: 1500, Output: 320},
		}},
	})
	if !ok || ev.Type != "message_end" {
		t.Fatalf("Type = %q ok=%v, want message_end", ev.Type, ok)
	}
	data, ok := ev.Data.(MessageEndData)
	if !ok {
		t.Fatalf("Data type = %T, want MessageEndData", ev.Data)
	}
	if data.InputTokens != 14000 { // 500 + 12000 + 1500
		t.Fatalf("InputTokens = %d, want 14000 (input+cache_read+cache_write)", data.InputTokens)
	}
	if data.OutputTokens != 320 {
		t.Fatalf("OutputTokens = %d, want 320", data.OutputTokens)
	}
}

func TestWsEventFromBus_MCPChanged(t *testing.T) {
	ev, ok := wsEventFromBus(bus.MCPChanged{
		SessionID: "s1", Total: 3, Ready: 1, Disabled: 1, Unhealthy: 1, Pending: 1,
	})
	if !ok || ev.Type != "mcp_change" {
		t.Fatalf("Type = %q ok=%v, want mcp_change", ev.Type, ok)
	}
	data, ok := ev.Data.(MCPChangeData)
	if !ok {
		t.Fatalf("Data type = %T, want MCPChangeData", ev.Data)
	}
	want := MCPChangeData{Total: 3, Ready: 1, Disabled: 1, Unhealthy: 1, Pending: 1}
	if data != want {
		t.Fatalf("Data = %+v, want %+v", data, want)
	}
}

func TestWsEventFromBus_RunTokensUpdated(t *testing.T) {
	ev, ok := wsEventFromBus(bus.RunTokensUpdated{SessionID: "s1", RunGen: 4, Up: 125, Down: 75})
	if !ok || ev.Type != "run_tokens" {
		t.Fatalf("Type = %q ok=%v, want run_tokens", ev.Type, ok)
	}
	data, ok := ev.Data.(RunTokensData)
	if !ok {
		t.Fatalf("Data type = %T, want RunTokensData", ev.Data)
	}
	if data != (RunTokensData{Up: 125, Down: 75}) {
		t.Fatalf("Data = %+v, want up=125 down=75", data)
	}
}

func TestBuildInitData_IncludesRunTokens(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(delayedResponseHandler(time.Second, "done")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	data := buildInitData(sess, bus.StreamingAggregate{}, nil)
	if data.RunTokensUp != 0 || data.RunTokensDown != 0 {
		t.Fatalf("initial run tokens = up=%d down=%d, want zero", data.RunTokensUp, data.RunTokensDown)
	}
	payload, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"run_tokens_up":0`) || !strings.Contains(string(payload), `"run_tokens_down":0`) {
		t.Fatalf("init JSON missing zero run tokens: %s", payload)
	}
}

func TestWsEventFromBus_CommandQueued(t *testing.T) {
	ev, ok := wsEventFromBus(bus.CommandQueued{SessionID: "s1", ID: "c1", Raw: "/compact"})
	if !ok || ev.Type != "command_queued" {
		t.Fatalf("Type = %q ok=%v, want command_queued", ev.Type, ok)
	}
	data, ok := ev.Data.(CommandQueuedData)
	if !ok {
		t.Fatalf("Data type = %T, want CommandQueuedData", ev.Data)
	}
	if data != (CommandQueuedData{ID: "c1", Raw: "/compact"}) {
		t.Fatalf("Data = %+v", data)
	}
}

func TestWsEventFromBus_CommandDequeued(t *testing.T) {
	ev, ok := wsEventFromBus(bus.CommandDequeued{SessionID: "s1", ID: "c1", Raw: "/compact", Executed: true})
	if !ok || ev.Type != "command_dequeued" {
		t.Fatalf("Type = %q ok=%v, want command_dequeued", ev.Type, ok)
	}
	data, ok := ev.Data.(CommandDequeuedData)
	if !ok {
		t.Fatalf("Data type = %T, want CommandDequeuedData", ev.Data)
	}
	if data != (CommandDequeuedData{ID: "c1", Raw: "/compact", Executed: true}) {
		t.Fatalf("Data = %+v", data)
	}
}

func TestWsEventFromBus_UserMessageAppended(t *testing.T) {
	ev, ok := wsEventFromBus(bus.UserMessageAppended{
		SessionID: "s1", MsgID: "m1", Text: "hola",
		Custom: map[string]any{"source": "secret_batch", "secret_aliases": []string{"db"}},
	})
	if !ok || ev.Type != "user_message" {
		t.Fatalf("Type = %q ok=%v, want user_message", ev.Type, ok)
	}
	data, ok := ev.Data.(UserMessageData)
	if !ok {
		t.Fatalf("Data type = %T, want UserMessageData", ev.Data)
	}
	if data.MsgID != "m1" || data.Text != "hola" || len(data.Content) != 0 {
		t.Fatalf("Data = %+v, want text-only message m1", data)
	}
	if data.Custom["source"] != "secret_batch" {
		t.Fatalf("Custom = %#v", data.Custom)
	}
}

func TestWsEventFromBus_UserMessageAppended_ProjectsCustom(t *testing.T) {
	ev, ok := wsEventFromBus(bus.UserMessageAppended{
		SessionID: "s1", MsgID: "m1", Text: "hola",
		Custom: map[string]any{"source": "secret_batch", "secret_aliases": []string{"db"}, "internal": "nope"},
	})
	if !ok {
		t.Fatal("event was not translated")
	}
	data := ev.Data.(UserMessageData)
	if data.Custom["source"] != "secret_batch" {
		t.Fatalf("source = %#v", data.Custom)
	}
	if _, ok := data.Custom["internal"]; ok {
		t.Fatalf("internal custom field exposed in WS payload: %#v", data.Custom)
	}
}

func TestWsEventFromBus_UserMessageAppended_DropsInlineAttachment(t *testing.T) {
	// A structured send travels as content blocks, but a whole inline image
	// must not be pushed to a phone: the history projection strips it.
	ev, ok := wsEventFromBus(bus.UserMessageAppended{
		SessionID: "s1", MsgID: "m2",
		Content: []core.Content{
			core.ImageContent(strings.Repeat("a", historyContentMaxBytes+1), "image/png"),
			core.TextContent("mira esto"),
		},
	})
	if !ok || ev.Type != "user_message" {
		t.Fatalf("Type = %q ok=%v, want user_message", ev.Type, ok)
	}
	data, ok := ev.Data.(UserMessageData)
	if !ok {
		t.Fatalf("Data type = %T, want UserMessageData", ev.Data)
	}
	if len(data.Content) != 2 {
		t.Fatalf("Content = %+v, want two blocks", data.Content)
	}
	if data.Content[0].Data != "" {
		t.Fatalf("oversized inline image data was not stripped")
	}
	if data.Content[1].Text != "mira esto" {
		t.Fatalf("text block = %q, want %q", data.Content[1].Text, "mira esto")
	}
}

func TestWsEventFromBus_Steered_DropsInlineAttachment(t *testing.T) {
	// A steer delivered with attachments carries its blocks so the message
	// renders live with its thumbnail, but the inline payload must be bounded
	// by the same history projection as a normal user message.
	ev, ok := wsEventFromBus(bus.Steered{
		SessionID: "s1", ID: "st1", MsgID: "m3", Text: "mira esto",
		Content: []core.Content{
			core.ImageContent(strings.Repeat("a", historyContentMaxBytes+1), "image/png"),
			core.TextContent("mira esto"),
		},
	})
	if !ok || ev.Type != "steer" {
		t.Fatalf("Type = %q ok=%v, want steer", ev.Type, ok)
	}
	data, ok := ev.Data.(SteerData)
	if !ok {
		t.Fatalf("Data type = %T, want SteerData", ev.Data)
	}
	if data.ID != "st1" || data.MsgID != "m3" || data.Text != "mira esto" {
		t.Fatalf("Data = %+v", data)
	}
	if len(data.Content) != 2 {
		t.Fatalf("Content = %+v, want two blocks", data.Content)
	}
	if data.Content[0].Data != "" {
		t.Fatalf("oversized inline image data was not stripped")
	}
	if data.Content[1].Text != "mira esto" {
		t.Fatalf("text block = %q, want %q", data.Content[1].Text, "mira esto")
	}
}

// A steer's own text is bounded like a user message's: a huge prompt sent with
// an attachment must not ride along uncapped just because Content is projected.
func TestWsEventFromBus_Steered_TruncatesOversizedText(t *testing.T) {
	huge := strings.Repeat("x", 4*historyContentMaxBytes)
	ev, ok := wsEventFromBus(bus.Steered{
		SessionID: "s1", ID: "st3", MsgID: "m5", Text: huge,
		Content: []core.Content{core.ImageContent("aW1n", "image/png")},
	})
	if !ok {
		t.Fatal("expected a steer event")
	}
	data := ev.Data.(SteerData)
	if len(data.Text) >= len(huge) {
		t.Fatalf("steer text = %d bytes, want it truncated below the original %d", len(data.Text), len(huge))
	}
	if !strings.Contains(data.Text, "truncated on this device") {
		t.Fatalf("truncated steer text lost its marker: %q", data.Text[max(0, len(data.Text)-60):])
	}
}

func TestWsEventFromBus_Steered_TextOnly(t *testing.T) {
	ev, ok := wsEventFromBus(bus.Steered{SessionID: "s1", ID: "st2", MsgID: "m4", Text: "sigue"})
	if !ok || ev.Type != "steer" {
		t.Fatalf("Type = %q ok=%v, want steer", ev.Type, ok)
	}
	data, ok := ev.Data.(SteerData)
	if !ok {
		t.Fatalf("Data type = %T, want SteerData", ev.Data)
	}
	if data.Text != "sigue" || len(data.Content) != 0 {
		t.Fatalf("Data = %+v, want text-only steer", data)
	}
}

func TestCountImageContent(t *testing.T) {
	got := countImageContent([]core.Content{
		core.TextContent("hi"),
		core.ImageContent("data", "image/png"),
		core.ImageContent("data2", "image/png"),
	})
	if got != 2 {
		t.Fatalf("countImageContent = %d, want 2", got)
	}
}

func TestLimitInitHistoryBoundsPayloadAndInlineAttachments(t *testing.T) {
	largeImage := strings.Repeat("a", historyContentMaxBytes+1)
	messages := make([]core.AgentMessage, initHistoryMaxMessages+10)
	for i := range messages {
		messages[i] = core.WrapMessage(core.Message{Role: "user", Content: []core.Content{core.TextContent(fmt.Sprintf("message %d", i))}})
	}
	messages[len(messages)-1] = core.WrapMessage(core.Message{Role: "user", Content: []core.Content{{Type: "image", Data: largeImage, MimeType: "image/png"}}})

	limited, truncated := limitInitHistory(messages)
	if !truncated || len(limited) != initHistoryMaxMessages {
		t.Fatalf("limited=%d truncated=%v, want %d and true", len(limited), truncated, initHistoryMaxMessages)
	}
	if got := limited[len(limited)-1].Content[0].Data; got != "" {
		t.Fatalf("inline image retained %d bytes", len(got))
	}
}

func TestLimitInitHistoryBoundsLargeText(t *testing.T) {
	message := core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{core.TextContent(strings.Repeat("x", historyContentMaxBytes+1))}})
	limited, truncated := limitInitHistory([]core.AgentMessage{message})
	if truncated {
		t.Fatal("single bounded message should not be marked as omitted history")
	}
	if got := limited[0].Content[0].Text; len(got) <= historyContentMaxBytes || !strings.Contains(got, "historic content truncated") {
		t.Fatalf("large text was not safely truncated: %d bytes", len(got))
	}
}

func TestLimitInitHistoryDropsOversizedToolArguments(t *testing.T) {
	message := core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{{
		Type: "tool_call", ToolCallID: "tool-1", ToolName: "bash",
		Arguments: map[string]any{"command": strings.Repeat("x", historyContentMaxBytes+1)},
	}}})
	limited, _ := limitInitHistory([]core.AgentMessage{message})
	args := limited[0].Content[0].Arguments
	if args["_truncated"] != true {
		t.Fatalf("oversized args = %#v, want truncation marker", args)
	}
}

func TestBuildInitData_ReconnectProjectsSecretCustom(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	const secretDir = "/tmp/moa-secrets-private/batch-private"
	appendConversationTestMessage(sess, "secret-note", "user", "secret batch staged", map[string]any{
		"source":         "secret_batch",
		"secret_aliases": []string{"token"},
		"secret_dir":     secretDir,
		"internal":       true,
	})

	data := buildInitData(sess, bus.StreamingAggregate{}, nil)
	if len(data.Messages) != 1 {
		t.Fatalf("init messages = %#v", data.Messages)
	}
	custom := data.Messages[0].Custom
	aliases, ok := custom["secret_aliases"].([]string)
	if custom["source"] != "secret_batch" || !ok || len(aliases) != 1 {
		t.Fatalf("projected custom = %#v", custom)
	}
	if _, ok := custom["secret_dir"]; ok {
		t.Fatalf("secret_dir leaked in reconnect history: %#v", custom)
	}
	if _, ok := custom["internal"]; ok {
		t.Fatalf("internal custom field leaked in reconnect history: %#v", custom)
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secretDir) {
		t.Fatalf("secret directory reached reconnect payload: %s", encoded)
	}
}

func TestWsEventFromBus_BashJobLifecycle(t *testing.T) {
	start, ok := wsEventFromBus(bus.BashJobStarted{SessionID: "s1", JobID: "bash-1", OwnerAgentID: "subagent-1", Command: "go test ./...", CWD: "/work"})
	if !ok || start.Type != "bash_job_start" {
		t.Fatalf("start = %+v, ok=%v", start, ok)
	}
	if got := start.Data.(BashJobStartData); got.JobID != "bash-1" || got.OwnerAgentID != "subagent-1" || got.Command != "go test ./..." {
		t.Fatalf("start data = %+v", got)
	}
	output, ok := wsEventFromBus(bus.BashJobOutput{SessionID: "s1", JobID: "bash-1", OwnerAgentID: "subagent-1", Delta: "ok\n"})
	if !ok || output.Type != "bash_job_output" || !isLossyWsEvent(output) {
		t.Fatalf("output = %+v, ok=%v", output, ok)
	}
	end, ok := wsEventFromBus(bus.BashJobEnded{SessionID: "s1", JobID: "bash-1", OwnerAgentID: "subagent-1", Status: "completed", Output: "ok\n"})
	if !ok || end.Type != "bash_job_end" || isLossyWsEvent(end) {
		t.Fatalf("end = %+v, ok=%v", end, ok)
	}
	complete, ok := wsEventFromBus(bus.BashCompleted{SessionID: "s1", JobID: "bash-1", OwnerAgentID: "subagent-1", Status: "completed", Text: "done"})
	if !ok || complete.Type != "bash_complete" || complete.Data.(BashCompleteData).OwnerAgentID != "subagent-1" {
		t.Fatalf("complete = %+v, ok=%v", complete, ok)
	}
}

func TestWsEventFromBus_SubagentEnded(t *testing.T) {
	t.Run("with usage", func(t *testing.T) {
		ev, ok := wsEventFromBus(bus.SubagentEnded{
			SessionID: "s1", JobID: "sa-1", Status: "completed",
			Usage:   &core.Usage{Input: 100, Output: 42},
			CostUSD: 0.0123,
		})
		if !ok {
			t.Fatal("expected ok=true")
		}
		if ev.Type != "subagent_end" {
			t.Fatalf("Type = %q, want subagent_end", ev.Type)
		}
		data, ok := ev.Data.(SubagentEndData)
		if !ok {
			t.Fatalf("Data type = %T, want SubagentEndData", ev.Data)
		}
		want := SubagentEndData{JobID: "sa-1", Status: "completed", InputTokens: 100, OutputTokens: 42, CostUSD: 0.0123}
		if data != want {
			t.Fatalf("Data = %+v, want %+v", data, want)
		}
	})

	t.Run("nil usage", func(t *testing.T) {
		ev, ok := wsEventFromBus(bus.SubagentEnded{
			SessionID: "s1", JobID: "sa-2", Status: "failed", Usage: nil, CostUSD: 0,
		})
		if !ok {
			t.Fatal("expected ok=true")
		}
		data := ev.Data.(SubagentEndData)
		if data.InputTokens != 0 || data.OutputTokens != 0 {
			t.Fatalf("expected zero tokens for nil usage, got %+v", data)
		}
	})
}

func TestPersistedSubagentOutcome_BackwardCompatibleResultFallback(t *testing.T) {
	legacy := session.SubagentTranscript{
		Status: "completed",
		Messages: []core.AgentMessage{{Message: core.Message{
			Role: "assistant", Content: []core.Content{core.TextContent("legacy final result")},
		}}},
	}
	result, resultErr := persistedSubagentOutcome(legacy)
	if result != "legacy final result" || resultErr != "" {
		t.Fatalf("legacy outcome = result %q, error %q", result, resultErr)
	}

	failed := session.SubagentTranscript{Status: "failed", Messages: legacy.Messages}
	result, resultErr = persistedSubagentOutcome(failed)
	if result != "" || resultErr != "" {
		t.Fatalf("failed legacy outcome must not mislabel partial output: result %q, error %q", result, resultErr)
	}
}

func TestBuildInitData_SortsPersistedSubagentOutcomesChronologically(t *testing.T) {
	// Sorting itself is intentionally independent of filesystem/list order;
	// completed cards must replay oldest-to-newest in the parent timeline.
	outcomes := []SubagentEndData{
		{JobID: "late", FinishedAtMs: 30},
		{JobID: "unknown"},
		{JobID: "early", FinishedAtMs: 10},
	}
	sortSubagentOutcomes(outcomes)
	if outcomes[0].JobID != "early" || outcomes[1].JobID != "late" || outcomes[2].JobID != "unknown" {
		t.Fatalf("outcome order = %+v", outcomes)
	}
}

func TestWsEventFromBus_SubagentEvent_Translatable(t *testing.T) {
	ev, ok := wsEventFromBus(bus.SubagentEvent{
		SessionID: "s1", JobID: "sa-1",
		Inner: bus.TextDelta{SessionID: "s1", RunGen: 1, Delta: "hello"},
	})
	if !ok {
		t.Fatal("expected ok=true")
	}
	if ev.Type != "subagent_event" {
		t.Fatalf("Type = %q, want subagent_event", ev.Type)
	}
	data, ok := ev.Data.(SubagentEventData)
	if !ok {
		t.Fatalf("Data type = %T, want SubagentEventData", ev.Data)
	}
	if data.JobID != "sa-1" {
		t.Fatalf("JobID = %q, want sa-1", data.JobID)
	}
	if data.Event == nil {
		t.Fatal("Event is nil")
	}
	if data.Event.Type != "text_delta" {
		t.Fatalf("inner Type = %q, want text_delta", data.Event.Type)
	}
	innerData, ok := data.Event.Data.(DeltaData)
	if !ok {
		t.Fatalf("inner Data type = %T, want DeltaData", data.Event.Data)
	}
	if innerData.Delta != "hello" {
		t.Fatalf("inner Delta = %q, want hello", innerData.Delta)
	}
}

func TestWsEventFromBus_SubagentEvent_NonTranslatable(t *testing.T) {
	// AgentStarted has no case in wsEventFromBus, so it must not be wrapped
	// as a subagent_event either.
	_, ok := wsEventFromBus(bus.SubagentEvent{
		SessionID: "s1", JobID: "sa-1",
		Inner: bus.AgentStarted{SessionID: "s1", RunGen: 1},
	})
	if ok {
		t.Fatal("expected ok=false for a non-translatable inner event")
	}
}

func TestIsLossyWsEvent(t *testing.T) {
	cases := []struct {
		name  string
		event Event
		want  bool
	}{
		{"text_delta", Event{Type: "text_delta"}, true},
		{"thinking_delta", Event{Type: "thinking_delta"}, true},
		{"tool_update", Event{Type: "tool_update"}, true},
		{"tool_call_delta", Event{Type: "tool_call_delta"}, true},
		{"message_end structural", Event{Type: "message_end"}, false},
		{"tool_end structural", Event{Type: "tool_end"}, false},
		{"run_end structural", Event{Type: "run_end"}, false},
		{"subagent_start structural", Event{Type: "subagent_start"}, false},
		{"subagent_end structural", Event{Type: "subagent_end"}, false},
		{
			"subagent_event wrapping text_delta is lossy",
			Event{Type: "subagent_event", Data: SubagentEventData{
				JobID: "sa-1", Event: &Event{Type: "text_delta"},
			}},
			true,
		},
		{
			"subagent_event wrapping message_end is structural",
			Event{Type: "subagent_event", Data: SubagentEventData{
				JobID: "sa-1", Event: &Event{Type: "message_end"},
			}},
			false,
		},
		{
			"subagent_event with nil inner is structural",
			Event{Type: "subagent_event", Data: SubagentEventData{JobID: "sa-1"}},
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isLossyWsEvent(tc.event); got != tc.want {
				t.Fatalf("isLossyWsEvent(%q) = %v, want %v", tc.event.Type, got, tc.want)
			}
		})
	}
}

// TestWSReactor_CleanupStopsWatcher verifies the context-watcher goroutine exits
// when the reactor is cleaned up early (e.g. a WS reconnect) even though the
// session context is still alive — otherwise each reconnect leaks a goroutine
// plus its 512-slot channel until the whole session ends.
func TestWSReactor_CleanupStopsWatcher(t *testing.T) {
	b := bus.NewLocalBus()
	defer b.Close()

	ctx := context.Background() // never cancelled: the watcher must exit via r.done
	runtime.GC()
	before := runtime.NumGoroutine()

	r := newWsReactor(b, ctx, "")
	r.cleanup()

	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			t.Fatalf("watcher goroutine leaked after cleanup: before=%d now=%d", before, runtime.NumGoroutine())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWSReactorUnresolvedLiveAttentionForcesReconnectInsteadOfGenerationZero(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("done")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	r := newWsReactor(sess.runtime.Bus, sess.infra.sessionCtx, sess.CWD, func(seq uint64) uint64 {
		return mgr.attentionGenerationForSequence(sess, seq)
	})
	defer r.cleanup()

	// The tracker cannot acquire its recorder lock before its bounded wait. The
	// old path sent unseen_gen:0; the reactor must instead close so the next init
	// uses the explicit attention_bound:false recovery protocol.
	mgr.attentionSeqMu.Lock()
	sess.runtime.Bus.Publish(bus.PermissionRequested{SessionID: sess.ID, ID: "p1"})
	select {
	case <-r.Done():
	case <-time.After(attentionSequenceWait + time.Second):
		mgr.attentionSeqMu.Unlock()
		t.Fatal("unresolved live attention did not force reconnect")
	}
	mgr.attentionSeqMu.Unlock()
	select {
	case event := <-r.Events():
		if event.Type == "permission_request" {
			data := event.Data.(PermissionData)
			if data.UnseenGen == 0 {
				t.Fatal("serialized permission carried silent generation zero")
			}
		}
	default:
	}
}

func TestEnrichEditToolStart(t *testing.T) {
	dir := t.TempDir()
	var sb strings.Builder
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	path := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	editStart := func(args map[string]any) Event {
		return Event{Type: "tool_start", Data: ToolStartData{
			ToolCallID: "tc1", ToolName: "edit", Args: args,
		}}
	}

	t.Run("edit at line 260 gets start_line 260", func(t *testing.T) {
		e := enrichEditToolStart(editStart(map[string]any{
			"path": path, "oldText": "line 260\nline 261", "newText": "x",
		}), dir)
		d := e.Data.(ToolStartData)
		if d.StartLine != 260 {
			t.Errorf("StartLine = %d, want 260", d.StartLine)
		}
	})

	t.Run("relative path resolves against cwd", func(t *testing.T) {
		e := enrichEditToolStart(editStart(map[string]any{
			"path": "big.txt", "oldText": "line 42", "newText": "x",
		}), dir)
		d := e.Data.(ToolStartData)
		if d.StartLine != 42 {
			t.Errorf("StartLine = %d, want 42", d.StartLine)
		}
	})

	t.Run("oldText not found degrades to 1", func(t *testing.T) {
		e := enrichEditToolStart(editStart(map[string]any{
			"path": path, "oldText": "no such content here", "newText": "x",
		}), dir)
		d := e.Data.(ToolStartData)
		if d.StartLine != 1 {
			t.Errorf("StartLine = %d, want 1", d.StartLine)
		}
	})

	t.Run("missing file degrades StartLine to 1", func(t *testing.T) {
		e := enrichEditToolStart(editStart(map[string]any{
			"path": filepath.Join(dir, "nope.txt"), "oldText": "x", "newText": "y",
		}), dir)
		d := e.Data.(ToolStartData)
		if d.StartLine != 1 {
			t.Errorf("StartLine = %d, want 1 (degraded)", d.StartLine)
		}
	})

	t.Run("non-edit tools untouched", func(t *testing.T) {
		e := enrichEditToolStart(Event{Type: "tool_start", Data: ToolStartData{
			ToolCallID: "tc1", ToolName: "bash", Args: map[string]any{"command": "ls"},
		}}, dir)
		d := e.Data.(ToolStartData)
		if d.StartLine != 0 {
			t.Errorf("StartLine = %d, want 0", d.StartLine)
		}
	})

	t.Run("non-tool_start events untouched", func(t *testing.T) {
		orig := Event{Type: "text_delta", Data: DeltaData{Delta: "hi"}}
		if got := enrichEditToolStart(orig, dir); got != orig {
			t.Errorf("event was modified: %+v", got)
		}
	})
}

// TestUserMessage_AnnouncedOnlyOnceInHistory is the regression guard for the
// reconnect race. A client that connects around a send takes an init snapshot
// paired with a sequence cut and then streams only events above that cut. If
// the user_message announcement were published BEFORE the message reached
// history — as it was when the bus handler published it right after spawning
// the run goroutine — a snapshot taken in that window would miss the message
// while its cut already covered the event: neither path delivers it and the
// message is lost until a reload.
//
// The announcement now comes from the append point, which makes the invariant
// directly checkable: at the instant the event is published, the very query
// buildInitData uses for the snapshot already returns the message.
func TestUserMessage_AnnouncedOnlyOnceInHistory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(delayedResponseHandler(300*time.Millisecond, "reply")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	const msgID = "c-race_1"
	type observation struct {
		count     int
		inHistory bool
	}
	obs := make(chan observation, 4)
	unsub := sess.runtime.Bus.Subscribe(func(e bus.UserMessageAppended) {
		if e.MsgID != msgID {
			return
		}
		// Read history exactly as a reconnecting client's snapshot does.
		found := 0
		for _, m := range buildInitData(sess, bus.StreamingAggregate{}, nil).Messages {
			if m.MsgID == msgID {
				found++
			}
		}
		obs <- observation{count: found, inHistory: found > 0}
	})
	defer unsub()

	if _, _, _, err := mgr.Send(sess.ID, "carrera", nil, "", msgID); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 5*time.Second, "run settles", func() bool { return sessState(sess) == StateIdle })
	sess.runtime.Bus.Drain(2 * time.Second)

	select {
	case got := <-obs:
		if !got.inHistory {
			t.Fatal("user_message was announced before the message was in history: a snapshot taken here would lose it")
		}
		if got.count != 1 {
			t.Fatalf("snapshot contains the message %d times, want exactly 1", got.count)
		}
	default:
		t.Fatal("the send was never announced on the bus")
	}
	if len(obs) != 0 {
		t.Fatalf("the send was announced %d extra times", len(obs))
	}
}

// The snapshot must name the tool calls that are still generating arguments or
// still executing: they are in no message history yet, so without them the
// client rebuilds a nameless "Calling" row after switching conversations.
func TestLiveToolInitDataProjectsPhaseAndAnchor(t *testing.T) {
	startedAt := time.UnixMilli(1_700_000_000_000)
	got := liveToolInitData([]bus.LiveToolCall{
		{ToolCallID: "tc1", ToolName: "edit", Args: map[string]any{"path": "pkg/serve/ws.go"}, Phase: bus.LiveToolPhaseGenerating, StartedAt: startedAt},
		{ToolCallID: "tc2", ToolName: "bash", Args: map[string]any{"command": "go test ./..."}, Phase: bus.LiveToolPhaseRunning, StartedAt: startedAt},
		// An entry without an ID can't be reconciled by the client; drop it.
		{ToolName: "read", Phase: bus.LiveToolPhaseRunning},
	})
	if len(got) != 2 {
		t.Fatalf("live tools = %+v, want two projected calls", got)
	}
	if got[0].ToolName != "edit" || got[0].Status != bus.LiveToolPhaseGenerating || got[0].Args["path"] != "pkg/serve/ws.go" {
		t.Fatalf("generating call = %+v", got[0])
	}
	if got[0].StartedAtMs != startedAt.UnixMilli() {
		t.Fatalf("StartedAtMs = %d, want %d", got[0].StartedAtMs, startedAt.UnixMilli())
	}
	if got[1].ToolName != "bash" || got[1].Status != bus.LiveToolPhaseRunning {
		t.Fatalf("running call = %+v", got[1])
	}
	if liveToolInitData(nil) != nil {
		t.Fatal("empty registry should project to nil, not an empty array")
	}
}

// A live `write` can carry a whole file in its arguments. The name is what the
// row needs; the payload must not be pushed to a phone on every reconnect.
func TestLiveToolInitDataBoundsHugeArguments(t *testing.T) {
	got := liveToolInitData([]bus.LiveToolCall{{
		ToolCallID: "tc1", ToolName: "write", Phase: bus.LiveToolPhaseRunning,
		Args: map[string]any{"content": strings.Repeat("x", historyContentMaxBytes+1)},
	}})
	if len(got) != 1 {
		t.Fatalf("live tools = %+v, want one", got)
	}
	if got[0].ToolName != "write" {
		t.Fatalf("tool name lost while bounding args: %+v", got[0])
	}
	if got[0].Args["_truncated"] != true {
		t.Fatalf("oversized args = %#v, want truncation marker", got[0].Args)
	}
	if _, ok := got[0].Args["content"]; ok {
		t.Fatal("oversized argument payload still travels in the snapshot")
	}
}

func TestBuildInitDataCarriesLiveTools(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(delayedResponseHandler(time.Second, "done")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	data := buildInitData(sess, bus.StreamingAggregate{}, []bus.LiveToolCall{
		{ToolCallID: "tc1", ToolName: "bash", Phase: bus.LiveToolPhaseRunning, StartedAt: time.Now()},
	})
	if len(data.LiveTools) != 1 || data.LiveTools[0].ToolName != "bash" {
		t.Fatalf("InitData.LiveTools = %+v, want the live bash call", data.LiveTools)
	}
}
