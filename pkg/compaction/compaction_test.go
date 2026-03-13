package compaction

import (
	"context"
	"strings"
	"testing"

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
	if !strings.Contains(s, "[...truncated]") {
		t.Fatal("expected truncation marker")
	}
	if strings.Contains(s, "second") {
		t.Fatal("second message should be truncated")
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
	if !strings.Contains(s, "[...truncated]") {
		t.Fatal("expected truncation for 128k model")
	}
	if strings.Contains(s, "tail") {
		t.Fatal("tail message should be truncated")
	}

	// Same messages with a 400k model should NOT truncate.
	s2 := SerializeForSummary(msgs, 400_000) // limit = 800k chars
	if strings.Contains(s2, "[...truncated]") {
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
