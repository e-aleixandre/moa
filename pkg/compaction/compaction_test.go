package compaction

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ealeixandre/moa/pkg/core"
)

// --- FindCutPoint tests ---

func makeMsg(role, text string, tokens int) core.AgentMessage {
	// Pad text to hit approximate token count (chars/4).
	padded := text + strings.Repeat("x", tokens*4-len(text))
	return core.AgentMessage{
		Message: core.Message{
			Role:    role,
			Content: []core.Content{core.TextContent(padded)},
		},
	}
}

func TestFindCutPoint_BasicCut(t *testing.T) {
	msgs := []core.AgentMessage{
		makeMsg("user", "old1", 5000),
		makeMsg("assistant", "old2", 5000),
		makeMsg("user", "old3", 5000),
		makeMsg("assistant", "old4", 5000),
		makeMsg("user", "recent1", 10000),
		makeMsg("assistant", "recent2", 10000),
	}
	settings := core.CompactionSettings{Enabled: true, ReserveTokens: 16384, KeepRecent: 20000}
	cut := FindCutPoint(msgs, 40000, 50000, settings)
	if cut == 0 {
		t.Fatal("expected non-zero cut")
	}
	// Should keep recent messages worth ~20K tokens.
	if cut >= len(msgs) {
		t.Fatalf("cut %d out of range", cut)
	}
}

func TestFindCutPoint_EverythingFits(t *testing.T) {
	msgs := []core.AgentMessage{
		makeMsg("user", "a", 100),
		makeMsg("assistant", "b", 100),
	}
	settings := core.CompactionSettings{Enabled: true, ReserveTokens: 16384, KeepRecent: 20000}
	cut := FindCutPoint(msgs, 200, 200_000, settings)
	if cut != 0 {
		t.Fatalf("expected 0 (everything fits), got %d", cut)
	}
}

func TestFindCutPoint_SnapsToValidBoundary(t *testing.T) {
	msgs := []core.AgentMessage{
		makeMsg("user", "old", 50000),
		makeMsg("assistant", "old-resp", 50000),
		// tool_result should never be a cut point
		{Message: core.Message{Role: "tool_result", Content: []core.Content{core.TextContent(strings.Repeat("x", 10000*4))}, ToolName: "bash", ToolCallID: "c1"}},
		makeMsg("user", "recent", 10000),
		makeMsg("assistant", "recent-resp", 10000),
	}
	settings := core.CompactionSettings{Enabled: true, ReserveTokens: 16384, KeepRecent: 15000}
	cut := FindCutPoint(msgs, 130000, 50000, settings)
	if cut > 0 {
		role := msgs[cut].Role
		if role == "tool_result" {
			t.Fatalf("cut at tool_result (index %d), should snap to user/assistant", cut)
		}
	}
}

func TestFindCutPoint_TrailingToolResultsNeverOrphan(t *testing.T) {
	// Large tool_results at the very end (no user/assistant boundary ahead of
	// the cut) must snap backward to the assistant that owns them, never
	// leaving a tool_result orphaned from its tool_use.
	toolResult := func(id string, tokens int) core.AgentMessage {
		return core.AgentMessage{Message: core.Message{
			Role:       "tool_result",
			Content:    []core.Content{core.TextContent(strings.Repeat("x", tokens*4))},
			ToolName:   "bash",
			ToolCallID: id,
		}}
	}
	assistantWithCalls := core.AgentMessage{Message: core.Message{
		Role: "assistant",
		Content: []core.Content{
			core.ToolCallContent("c1", "bash", map[string]any{"command": "a"}),
			core.ToolCallContent("c2", "bash", map[string]any{"command": "b"}),
		},
	}}
	msgs := []core.AgentMessage{
		makeMsg("user", "old", 5000),
		assistantWithCalls,
		toolResult("c1", 30000),
		toolResult("c2", 30000),
	}
	settings := core.CompactionSettings{Enabled: true, ReserveTokens: 16384, KeepRecent: 15000}
	cut := FindCutPoint(msgs, 130000, 50000, settings)
	if cut > 0 && msgs[cut].Role == "tool_result" {
		t.Fatalf("cut at index %d is a tool_result — orphaned from its tool_use", cut)
	}
	// The kept slice must not start with a tool_result (Anthropic/OpenAI reject it).
	if cut < len(msgs) && msgs[cut].Role == "tool_result" {
		t.Fatalf("kept messages start with tool_result at %d", cut)
	}
}

func TestFindCutPoint_Empty(t *testing.T) {
	cut := FindCutPoint(nil, 0, 200_000, core.CompactionSettings{Enabled: true})
	if cut != 0 {
		t.Fatalf("expected 0, got %d", cut)
	}
}

func TestFindCutPoint_WithCompactionSummary(t *testing.T) {
	msgs := []core.AgentMessage{
		{Message: core.Message{Role: "compaction_summary", Content: []core.Content{core.TextContent(strings.Repeat("x", 5000*4))}}},
		makeMsg("user", "a", 50000),
		makeMsg("assistant", "b", 50000),
		makeMsg("user", "c", 10000),
		makeMsg("assistant", "d", 10000),
	}
	settings := core.CompactionSettings{Enabled: true, ReserveTokens: 16384, KeepRecent: 15000}
	cut := FindCutPoint(msgs, 125000, 50000, settings)
	// compaction_summary is a valid cut boundary.
	if cut > 0 && msgs[cut].Role == "tool_result" {
		t.Fatalf("should not cut at tool_result")
	}
}

// --- SerializeForSummary tests ---

func TestSerializeForSummary_Format(t *testing.T) {
	msgs := []core.AgentMessage{
		{Message: core.Message{Role: "user", Content: []core.Content{core.TextContent("hello")}}},
		{Message: core.Message{Role: "assistant", Content: []core.Content{core.TextContent("hi there")}}},
		{Message: core.Message{Role: "tool_result", ToolName: "bash", Content: []core.Content{core.TextContent("output")}}},
	}
	s := SerializeForSummary(msgs, 0)
	if !strings.Contains(s, "[User]: hello") {
		t.Fatal("missing user line")
	}
	if !strings.Contains(s, "[Assistant]: hi there") {
		t.Fatal("missing assistant line")
	}
	if !strings.Contains(s, "[Tool result: bash]: output") {
		t.Fatal("missing tool result line")
	}
}

func TestSerializeForSummary_Truncation(t *testing.T) {
	// Create a message that exceeds the default cap.
	huge := strings.Repeat("x", defaultMaxSerializationChars+1000)
	msgs := []core.AgentMessage{
		{Message: core.Message{Role: "user", Content: []core.Content{core.TextContent(huge)}}},
		{Message: core.Message{Role: "user", Content: []core.Content{core.TextContent("second")}}},
	}
	s := SerializeForSummary(msgs, 0)
	if !strings.Contains(s, "earlier messages omitted") {
		t.Fatal("expected omission marker")
	}
	// The newest message must survive: dropping it would hide the work
	// closest to the kept tail.
	if !strings.Contains(s, "second") {
		t.Fatal("most recent message must be kept when dropping for size")
	}
}

func TestSerializeForSummary_ModelDerivedLimit(t *testing.T) {
	// A 128k-token model should derive a 256k char limit.
	msgs := []core.AgentMessage{
		{Message: core.Message{Role: "user", Content: []core.Content{
			core.TextContent(strings.Repeat("x", 300_000)),
		}}},
		{Message: core.Message{Role: "user", Content: []core.Content{
			core.TextContent("tail"),
		}}},
	}
	s := SerializeForSummary(msgs, 128_000) // limit = 256k chars
	if !strings.Contains(s, "earlier messages omitted") {
		t.Fatal("expected omission for 128k model")
	}
	if !strings.Contains(s, "tail") {
		t.Fatal("most recent message must be kept when dropping for size")
	}

	// Same messages with a 400k model should NOT truncate.
	s2 := SerializeForSummary(msgs, 400_000) // limit = 800k chars
	if strings.Contains(s2, "earlier messages omitted") {
		t.Fatal("should not truncate for 400k model")
	}
	if !strings.Contains(s2, "tail") {
		t.Fatal("tail message should be present for large model")
	}
}

func TestSerializeForSummary_ToolCallInAssistant(t *testing.T) {
	msgs := []core.AgentMessage{
		{Message: core.Message{Role: "assistant", Content: []core.Content{
			core.TextContent("I'll read the file"),
			core.ToolCallContent("c1", "read", map[string]any{"path": "main.go"}),
		}}},
	}
	s := SerializeForSummary(msgs, 0)
	if !strings.Contains(s, "[Tool call: read]") {
		t.Fatal("missing tool call annotation")
	}
}

// --- ExtractFileOps tests ---

func TestExtractFileOps(t *testing.T) {
	msgs := []core.AgentMessage{
		{Message: core.Message{Role: "assistant", Content: []core.Content{
			core.ToolCallContent("c1", "read", map[string]any{"path": "main.go"}),
			core.ToolCallContent("c2", "read", map[string]any{"path": "go.mod"}),
			core.ToolCallContent("c3", "write", map[string]any{"path": "new.go"}),
			core.ToolCallContent("c4", "edit", map[string]any{"path": "main.go"}),
		}}},
	}
	ops := ExtractFileOps(msgs)

	readOnly := ops.ReadOnly()
	// main.go was also edited, so only go.mod is read-only.
	if len(readOnly) != 1 || readOnly[0] != "go.mod" {
		t.Fatalf("ReadOnly: got %v", readOnly)
	}

	modified := ops.Modified()
	if len(modified) != 2 {
		t.Fatalf("Modified: expected 2, got %v", modified)
	}
	// Sorted: main.go, new.go.
	if modified[0] != "main.go" || modified[1] != "new.go" {
		t.Fatalf("Modified: got %v", modified)
	}
}

func TestExtractFileOps_NoPath(t *testing.T) {
	msgs := []core.AgentMessage{
		{Message: core.Message{Role: "assistant", Content: []core.Content{
			core.ToolCallContent("c1", "bash", map[string]any{"command": "ls"}),
		}}},
	}
	ops := ExtractFileOps(msgs)
	if len(ops.ReadOnly()) != 0 || len(ops.Modified()) != 0 {
		t.Fatal("expected empty ops for bash")
	}
}

func TestExtractFileOps_MultiEditAndApplyPatch(t *testing.T) {
	patch := "*** Begin Patch\n" +
		"*** Add File: created.go\n" +
		"+package main\n" +
		"*** Update File: existing.go\n" +
		"@@\n" +
		"-old\n" +
		"+new\n" +
		"*** Delete File: gone.go\n" +
		"*** End Patch\n"
	msgs := []core.AgentMessage{
		{Message: core.Message{Role: "assistant", Content: []core.Content{
			core.ToolCallContent("c1", "multiedit", map[string]any{"path": "batch.go"}),
			core.ToolCallContent("c2", "apply_patch", map[string]any{"patch": patch}),
		}}},
	}
	ops := ExtractFileOps(msgs)

	// multiedit (batch.go) + apply_patch add/update/delete (created/existing/gone).
	modified := ops.Modified()
	want := []string{"batch.go", "created.go", "existing.go", "gone.go"} // sorted
	if len(modified) != len(want) {
		t.Fatalf("Modified: expected %v, got %v", want, modified)
	}
	for i, w := range want {
		if modified[i] != w {
			t.Fatalf("Modified[%d]: expected %q, got %v", i, w, modified)
		}
	}
}

// --- mockProvider for GenerateSummary / Compact tests ---

type mockProvider struct {
	response string
	err      error
}

func (m *mockProvider) Stream(_ context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
	if m.err != nil {
		return nil, m.err
	}
	ch := make(chan core.AssistantEvent, 3)
	ch <- core.AssistantEvent{Type: core.ProviderEventStart}
	ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: m.response}
	ch <- core.AssistantEvent{
		Type:    core.ProviderEventDone,
		Message: &core.Message{Role: "assistant", Content: []core.Content{core.TextContent(m.response)}},
	}
	close(ch)
	return ch, nil
}

func TestGenerateSummary_Normal(t *testing.T) {
	prov := &mockProvider{response: "## Goal\nTest goal"}
	msgs := []core.AgentMessage{
		{Message: core.Message{Role: "user", Content: []core.Content{core.TextContent("hello")}}},
	}
	summary, _, err := GenerateSummary(context.Background(), prov, core.Model{ID: "test"}, core.StreamOptions{}, msgs, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "Test goal") {
		t.Fatalf("unexpected summary: %s", summary)
	}
}

func TestGenerateSummary_FallbackToFinalMessage(t *testing.T) {
	// Provider that sends no text_delta but has content in the done message.
	ch := make(chan core.AssistantEvent, 2)
	ch <- core.AssistantEvent{Type: core.ProviderEventStart}
	ch <- core.AssistantEvent{
		Type:    core.ProviderEventDone,
		Message: &core.Message{Role: "assistant", Content: []core.Content{core.TextContent("fallback summary")}},
	}
	close(ch)

	prov := &channelProvider{ch: ch}
	msgs := []core.AgentMessage{
		{Message: core.Message{Role: "user", Content: []core.Content{core.TextContent("hello")}}},
	}
	summary, _, err := GenerateSummary(context.Background(), prov, core.Model{ID: "test"}, core.StreamOptions{}, msgs, "")
	if err != nil {
		t.Fatal(err)
	}
	if summary != "fallback summary" {
		t.Fatalf("expected fallback, got: %s", summary)
	}
}

type channelProvider struct {
	ch chan core.AssistantEvent
}

func (p *channelProvider) Stream(_ context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
	return p.ch, nil
}

func TestGenerateSummary_EmptyOutput(t *testing.T) {
	prov := &mockProvider{response: "   "} // whitespace-only
	msgs := []core.AgentMessage{
		{Message: core.Message{Role: "user", Content: []core.Content{core.TextContent("hello")}}},
	}
	_, _, err := GenerateSummary(context.Background(), prov, core.Model{ID: "test"}, core.StreamOptions{}, msgs, "")
	if err == nil {
		t.Fatal("expected error for empty output")
	}
}

func TestGenerateSummary_ProviderError(t *testing.T) {
	prov := &mockProvider{err: context.DeadlineExceeded}
	msgs := []core.AgentMessage{
		{Message: core.Message{Role: "user", Content: []core.Content{core.TextContent("hello")}}},
	}
	_, _, err := GenerateSummary(context.Background(), prov, core.Model{ID: "test"}, core.StreamOptions{}, msgs, "")
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- Compact integration tests ---

func TestCompact_NothingToCompact(t *testing.T) {
	msgs := []core.AgentMessage{
		makeMsg("user", "hello", 100),
		makeMsg("assistant", "hi", 100),
	}
	prov := &mockProvider{response: "summary"}
	result, out, err := Compact(context.Background(), prov, core.Model{ID: "test"}, core.StreamOptions{}, msgs, 200, 200_000, core.DefaultCompactionSettings)
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatal("expected nil result when nothing to compact")
	}
	if len(out) != len(msgs) {
		t.Fatal("messages should be unchanged")
	}
}

func TestCompact_ProducesValidOutput(t *testing.T) {
	// Build messages that exceed threshold.
	msgs := []core.AgentMessage{
		makeMsg("user", "old1", 50000),
		makeMsg("assistant", "old2", 50000),
		makeMsg("user", "old3", 50000),
		makeMsg("assistant", "old4", 50000),
		makeMsg("user", "recent", 5000),
		makeMsg("assistant", "recent-resp", 5000),
	}
	settings := core.CompactionSettings{Enabled: true, ReserveTokens: 16384, KeepRecent: 10000}
	prov := &mockProvider{response: "## Goal\nBuild a thing"}

	result, compacted, err := Compact(context.Background(), prov, core.Model{ID: "test"}, core.StreamOptions{}, msgs, 210000, 200_000, settings)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("expected compaction result")
	}
	if result.TokensBefore != 210000 {
		t.Fatalf("TokensBefore: expected 210000, got %d", result.TokensBefore)
	}
	if result.TokensAfter >= result.TokensBefore {
		t.Fatalf("TokensAfter (%d) should be less than TokensBefore (%d)", result.TokensAfter, result.TokensBefore)
	}
	if len(compacted) == 0 {
		t.Fatal("expected compacted messages")
	}
	if compacted[0].Role != "compaction_summary" {
		t.Fatalf("first message should be compaction_summary, got %s", compacted[0].Role)
	}
	if !strings.Contains(result.Summary, "Build a thing") {
		t.Fatalf("summary missing content: %s", result.Summary)
	}
}

func TestCompact_ExtractsPreviousSummary(t *testing.T) {
	msgs := []core.AgentMessage{
		{Message: core.Message{Role: "compaction_summary", Content: []core.Content{core.TextContent("old summary")}}},
		makeMsg("user", "new1", 50000),
		makeMsg("assistant", "new2", 50000),
		makeMsg("user", "recent", 5000),
		makeMsg("assistant", "recent-resp", 5000),
	}
	settings := core.CompactionSettings{Enabled: true, ReserveTokens: 16384, KeepRecent: 10000}

	var capturedReq core.Request
	prov := &capturingProvider{
		response: "merged summary",
		capture:  func(req core.Request) { capturedReq = req },
	}

	result, _, err := Compact(context.Background(), prov, core.Model{ID: "test"}, core.StreamOptions{}, msgs, 110000, 200_000, settings)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("expected result")
	}
	// The prompt should contain the previous summary.
	prompt := capturedReq.Messages[0].Content[0].Text
	if !strings.Contains(prompt, "old summary") {
		t.Fatalf("prompt should contain previous summary, got: %s", prompt[:200])
	}
}

type capturingProvider struct {
	response string
	capture  func(core.Request)
}

func (p *capturingProvider) Stream(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	if p.capture != nil {
		p.capture(req)
	}
	ch := make(chan core.AssistantEvent, 3)
	ch <- core.AssistantEvent{Type: core.ProviderEventStart}
	ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: p.response}
	ch <- core.AssistantEvent{
		Type:    core.ProviderEventDone,
		Message: &core.Message{Role: "assistant", Content: []core.Content{core.TextContent(p.response)}},
	}
	close(ch)
	return ch, nil
}

func TestCompact_FailureReturnsOriginalMessages(t *testing.T) {
	msgs := []core.AgentMessage{
		makeMsg("user", "old", 50000),
		makeMsg("assistant", "old", 50000),
		makeMsg("user", "recent", 5000),
	}
	settings := core.CompactionSettings{Enabled: true, ReserveTokens: 16384, KeepRecent: 10000}
	prov := &mockProvider{err: context.DeadlineExceeded}

	result, out, err := Compact(context.Background(), prov, core.Model{ID: "test"}, core.StreamOptions{}, msgs, 105000, 200_000, settings)
	if err == nil {
		t.Fatal("expected error")
	}
	if result != nil {
		t.Fatal("expected nil result on failure")
	}
	if len(out) != len(msgs) {
		t.Fatal("messages should be unchanged on failure")
	}
}

func TestCompact_MultiCompaction(t *testing.T) {
	// First compaction.
	msgs := []core.AgentMessage{
		makeMsg("user", "old1", 50000),
		makeMsg("assistant", "old2", 50000),
		makeMsg("user", "recent", 5000),
		makeMsg("assistant", "recent-resp", 5000),
	}
	settings := core.CompactionSettings{Enabled: true, ReserveTokens: 16384, KeepRecent: 10000}
	prov := &mockProvider{response: "first summary"}

	_, compacted, err := Compact(context.Background(), prov, core.Model{ID: "test"}, core.StreamOptions{}, msgs, 110000, 200_000, settings)
	if err != nil {
		t.Fatal(err)
	}
	if compacted[0].Role != "compaction_summary" {
		t.Fatal("first should be compaction_summary")
	}

	// Add more messages and compact again.
	compacted = append(compacted,
		makeMsg("user", "more1", 50000),
		makeMsg("assistant", "more2", 50000),
	)
	prov.response = "second summary"

	_, compacted2, err := Compact(context.Background(), prov, core.Model{ID: "test"}, core.StreamOptions{}, compacted, 110000, 200_000, settings)
	if err != nil {
		t.Fatal(err)
	}
	if compacted2[0].Role != "compaction_summary" {
		t.Fatal("should have new compaction_summary")
	}
	if !strings.Contains(compacted2[0].Content[0].Text, "second summary") {
		t.Fatal("should contain second summary")
	}
}

func TestGenerateSummary_ReturnsUsage(t *testing.T) {
	usage := &core.Usage{Input: 100, Output: 50, TotalTokens: 150}
	ch := make(chan core.AssistantEvent, 3)
	ch <- core.AssistantEvent{Type: core.ProviderEventStart}
	ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: "summary text"}
	ch <- core.AssistantEvent{
		Type: core.ProviderEventDone,
		Message: &core.Message{
			Role:    "assistant",
			Content: []core.Content{core.TextContent("summary text")},
			Usage:   usage,
		},
	}
	close(ch)

	prov := &channelProvider{ch: ch}
	msgs := []core.AgentMessage{
		{Message: core.Message{Role: "user", Content: []core.Content{core.TextContent("hello")}}},
	}
	_, gotUsage, err := GenerateSummary(context.Background(), prov, core.Model{ID: "test"}, core.StreamOptions{}, msgs, "")
	if err != nil {
		t.Fatal(err)
	}
	if gotUsage == nil {
		t.Fatal("expected non-nil usage")
	}
	if gotUsage.Input != 100 || gotUsage.Output != 50 {
		t.Errorf("usage = %+v, want Input:100 Output:50", gotUsage)
	}
}

func TestElideMiddle_KeepsHeadAndTail(t *testing.T) {
	text := strings.Repeat("A", 3000) + "FINAL_ANSWER"
	got := elideMiddle(text, toolResultBudget)
	if len(got) > toolResultBudget+64 {
		t.Fatalf("elided text too long: %d", len(got))
	}
	if !strings.HasPrefix(got, "AAAA") {
		t.Fatal("head not preserved")
	}
	// The tail is where a tool result states its outcome; the old flat
	// head-truncation dropped it.
	if !strings.Contains(got, "FINAL_ANSWER") {
		t.Fatal("tail not preserved")
	}
	if !strings.Contains(got, "chars elided") {
		t.Fatal("missing elision marker")
	}
}

func TestElideMiddle_ShortTextUnchanged(t *testing.T) {
	if got := elideMiddle("short", toolResultBudget); got != "short" {
		t.Fatalf("short text modified: %q", got)
	}
}

func TestSerializeForSummary_ToolResultKeepsOutcome(t *testing.T) {
	long := strings.Repeat("noise ", 2000) + "FAIL: assertion failed at line 42"
	msgs := []core.AgentMessage{
		{Message: core.Message{Role: "tool_result", ToolName: "bash",
			Content: []core.Content{core.TextContent(long)}}},
	}
	s := SerializeForSummary(msgs, 0)
	if !strings.Contains(s, "FAIL: assertion failed at line 42") {
		t.Fatal("tool result outcome lost in serialization")
	}
}

func TestSerializeForSummary_DropsOldestNotNewest(t *testing.T) {
	big := strings.Repeat("x", 100_000)
	var msgs []core.AgentMessage
	for i := 0; i < 6; i++ {
		msgs = append(msgs, core.AgentMessage{Message: core.Message{
			Role: "user", Content: []core.Content{core.TextContent(big)}}})
	}
	msgs[0].Content = []core.Content{core.TextContent("OLDEST " + big)}
	msgs[5].Content = []core.Content{core.TextContent("NEWEST " + big)}

	s := SerializeForSummary(msgs, 0) // 400k char limit, ~600k of content
	if strings.Contains(s, "OLDEST") {
		t.Fatal("oldest message should have been dropped first")
	}
	if !strings.Contains(s, "NEWEST") {
		t.Fatal("newest message must survive")
	}
}

func TestAppendCheckpoint_RewritesSummaryMessage(t *testing.T) {
	res := &Result{Summary: "## Goal\nship it"}
	compacted := []core.AgentMessage{
		{Message: core.Message{Role: "compaction_summary",
			Content: []core.Content{core.TextContent(res.Summary)}}},
		{Message: core.Message{Role: "user", Content: []core.Content{core.TextContent("hi")}}},
	}
	AppendCheckpoint(res, compacted, "waiting on user decision about X")

	if !strings.Contains(res.Summary, "waiting on user decision about X") {
		t.Fatal("checkpoint not appended to summary")
	}
	got := extractText(compacted[0].Message)
	if !strings.Contains(got, "waiting on user decision about X") {
		t.Fatal("summary message not rewritten: the appended checkpoint would be lost")
	}
	if !strings.Contains(got, checkpointBegin) || !strings.Contains(got, checkpointEnd) {
		t.Fatal("missing checkpoint delimiters")
	}
}

func TestAppendCheckpoint_NoopWhenEmpty(t *testing.T) {
	res := &Result{Summary: "## Goal\nship it"}
	compacted := []core.AgentMessage{
		{Message: core.Message{Role: "compaction_summary",
			Content: []core.Content{core.TextContent(res.Summary)}}},
	}
	AppendCheckpoint(res, compacted, "   ")
	if strings.Contains(res.Summary, checkpointBegin) {
		t.Fatal("empty checkpoint should not add delimiters")
	}
}

func TestElideMiddle_PreservesRuneBoundaries(t *testing.T) {
	// Spanish + emoji: byte-level cuts would split multi-byte runes and emit
	// invalid UTF-8, which some providers reject outright.
	text := strings.Repeat("configuración ñandú 🚀 ", 500)
	for _, budget := range []int{101, 257, 1001, 2003} {
		got := elideMiddle(text, budget)
		if !utf8.ValidString(got) {
			t.Fatalf("invalid UTF-8 produced at budget %d", budget)
		}
	}
}

func TestSerializeForSummary_RecentToolResultsGetMoreBudget(t *testing.T) {
	// Newest-first budgeting (gemini-cli's policy): a recent result keeps far
	// more detail than an equally sized ancient one.
	body := func(tag string) string {
		return tag + strings.Repeat(" filler", 3000) + " END_" + tag
	}
	var msgs []core.AgentMessage
	for i := 0; i < 60; i++ {
		tag := fmt.Sprintf("R%02d", i)
		msgs = append(msgs, core.AgentMessage{Message: core.Message{
			Role: "tool_result", ToolName: "bash",
			Content: []core.Content{core.TextContent(body(tag))}}})
	}
	s := SerializeForSummary(msgs, 0)

	// Both ends must still state how they ended.
	if !strings.Contains(s, "END_R59") {
		t.Fatal("newest tool result lost its outcome")
	}
	if !strings.Contains(s, "END_R00") {
		t.Fatal("oldest tool result lost its outcome")
	}
}

func TestToolResultAllowance_FallsBackToFloor(t *testing.T) {
	_, perResult := toolResultBudgets(defaultMaxSerializationChars)
	remaining := toolResultBudget / 2
	if got := toolResultAllowance(&remaining, perResult); got != toolResultBudget {
		t.Fatalf("exhausted budget should yield the floor, got %d", got)
	}
	full := toolResultGlobalCap
	if got := toolResultAllowance(&full, perResult); got != perResult {
		t.Fatalf("fresh budget should cap at %d, got %d", perResult, got)
	}
}

// TestToolResultBudgets_NeverExceedsTranscript locks H1: a fixed global budget
// larger than the serialization limit let a few recent tool results evict the
// entire user dialogue on small-context models.
func TestToolResultBudgets_NeverExceedsTranscript(t *testing.T) {
	for _, maxInput := range []int{32_000, 128_000, 200_000, 1_000_000} {
		limit := maxSerializationChars(maxInput)
		global, perResult := toolResultBudgets(limit)
		if global > limit/2 {
			t.Fatalf("maxInput=%d: tool budget %d exceeds half the transcript limit %d",
				maxInput, global, limit)
		}
		if perResult > global {
			t.Fatalf("maxInput=%d: per-result cap %d exceeds global %d", maxInput, perResult, global)
		}
	}
}

// TestSerializeForSummary_ToolsDoNotEvictDialogue verifies user turns survive a
// transcript dominated by huge tool results.
func TestSerializeForSummary_ToolsDoNotEvictDialogue(t *testing.T) {
	var msgs []core.AgentMessage
	for i := 0; i < 40; i++ {
		msgs = append(msgs, core.AgentMessage{Message: core.Message{
			Role: "user", Content: []core.Content{
				core.TextContent(fmt.Sprintf("USERTURN_%02d decide this", i))}}})
		msgs = append(msgs, core.AgentMessage{Message: core.Message{
			Role: "tool_result", ToolName: "bash", Content: []core.Content{
				core.TextContent(strings.Repeat("x", 60_000))}}})
	}
	s := SerializeForSummary(msgs, 128_000) // 256k char limit

	var kept int
	for i := 0; i < 40; i++ {
		if strings.Contains(s, fmt.Sprintf("USERTURN_%02d", i)) {
			kept++
		}
	}
	if kept < 30 {
		t.Fatalf("tool results evicted the dialogue: only %d/40 user turns survived", kept)
	}
}

// TestSerializeForSummary_NeverEmptyTranscript locks H5: a single message
// larger than the whole limit used to leave only the omission marker, so the
// summarizer replaced real history with a summary of nothing.
func TestSerializeForSummary_NeverEmptyTranscript(t *testing.T) {
	huge := "DECISION_MARKER " + strings.Repeat("x", 900_000)
	msgs := []core.AgentMessage{
		{Message: core.Message{Role: "user", Content: []core.Content{core.TextContent(huge)}}},
	}
	s := SerializeForSummary(msgs, 0)
	if !strings.Contains(s, "DECISION_MARKER") {
		t.Fatal("oversized lone message must be elided, not dropped")
	}
}

// TestSummaryTokenBudget_ScalesWithWindow locks H6: a flat 8k reserve made the
// cut point degenerate on small-window models.
func TestSummaryTokenBudget_ScalesWithWindow(t *testing.T) {
	if got := summaryTokenBudget(200_000); got != summaryMaxTokens {
		t.Fatalf("large window should get the full budget, got %d", got)
	}
	if got := summaryTokenBudget(32_000); got >= summaryMaxTokens {
		t.Fatalf("small window should get a reduced budget, got %d", got)
	}
	for _, w := range []int{16_000, 24_000, 32_000, 128_000, 1_000_000} {
		settings := core.CompactionSettings{KeepRecent: 20000, ReserveTokens: 16384}
		if maxKeep := w - settings.ReserveTokens - summaryTokenBudget(w); w > 24_000 && maxKeep <= 0 {
			t.Fatalf("window %d yields non-positive keep budget %d", w, maxKeep)
		}
	}
}

func TestElideMiddle_RespectsBudgetIncludingMarker(t *testing.T) {
	text := strings.Repeat("y", 50_000)
	for _, budget := range []int{1000, 2000, 20_000} {
		if got := elideMiddle(text, budget); len(got) > budget {
			t.Fatalf("budget %d exceeded: got %d chars", budget, len(got))
		}
	}
}

func TestAppendCheckpoint_CountsTowardTokensAfter(t *testing.T) {
	res := &Result{Summary: "## Goal\nship", TokensAfter: 1000}
	compacted := []core.AgentMessage{{Message: core.Message{Role: "compaction_summary",
		Content: []core.Content{core.TextContent(res.Summary)}}}}
	AppendCheckpoint(res, compacted, strings.Repeat("state ", 500))
	if res.TokensAfter <= 1000 {
		t.Fatal("checkpoint tokens must be reflected in TokensAfter")
	}
}
