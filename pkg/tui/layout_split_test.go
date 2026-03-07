package tui

import (
	"strings"
	"testing"
)

func TestSplitRenderUserMessage_HasYOULabel(t *testing.T) {
	l := SplitLayout{}
	out := l.RenderUserMessage("hello world", 80, CatppuccinMocha)
	if out == "" {
		t.Fatal("empty output")
	}
	if !strings.Contains(out, "YOU") {
		t.Error("split user message should contain YOU label")
	}
	if !strings.Contains(out, "hello world") {
		t.Error("split user message should contain the text")
	}
}

func TestSplitRenderUserMessage_MultilineIndented(t *testing.T) {
	l := SplitLayout{}
	out := l.RenderUserMessage("line one\nline two\nline three", 80, CatppuccinMocha)
	lines := strings.Split(out, "\n")
	if len(lines) != 4 { // YOU label + 3 content lines
		t.Fatalf("lines = %d, want 4", len(lines))
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, "│") {
			t.Errorf("line %d should start with │, got %q", i, line)
		}
	}
}

func TestSplitRenderToolBlock_NonEmpty(t *testing.T) {
	l := SplitLayout{}
	data := ToolBlockData{
		ToolName: "bash",
		Action:   "bash",
		Target:   "ls -la",
		Body:     "file1.go\nfile2.go",
		Done:     true,
	}
	out := l.RenderToolBlock(data, 80, CatppuccinMocha)
	if out == "" {
		t.Fatal("empty output")
	}
	if !strings.Contains(out, "bash") {
		t.Error("should contain action")
	}
	if !strings.Contains(out, "ls -la") {
		t.Error("should contain target")
	}
}

func TestSplitRenderToolBlock_BadgePresent(t *testing.T) {
	l := SplitLayout{}

	// Done badge
	data := ToolBlockData{Action: "bash", Target: "ls", Done: true}
	out := l.RenderToolBlock(data, 80, CatppuccinMocha)
	if !strings.Contains(out, "DONE") {
		t.Error("done tool should have DONE badge")
	}

	// Running badge
	data = ToolBlockData{Action: "bash", Target: "ls", Done: false}
	out = l.RenderToolBlock(data, 80, CatppuccinMocha)
	if !strings.Contains(out, "RUNNING") {
		t.Error("running tool should have RUNNING badge")
	}

	// Error badge
	data = ToolBlockData{Action: "bash", Target: "ls", Done: true, IsError: true}
	out = l.RenderToolBlock(data, 80, CatppuccinMocha)
	if !strings.Contains(out, "ERROR") {
		t.Error("error tool should have ERROR badge")
	}
}

func TestSplitRenderToolBlock_ErrorBodyMaroon(t *testing.T) {
	l := SplitLayout{}
	data := ToolBlockData{
		Action:  "bash",
		Target:  "cat /nope",
		Body:    "permission denied",
		Done:    true,
		IsError: true,
	}
	out := l.RenderToolBlock(data, 80, CatppuccinMocha)
	if !strings.Contains(out, "permission denied") {
		t.Error("error body content should be present")
	}
}

func TestSplitRenderToolBlock_NarrowWidth(t *testing.T) {
	l := SplitLayout{}
	data := ToolBlockData{
		Action: "bash",
		Target: "/very/long/path/to/some/deep/nested/directory/file.go",
		Done:   true,
	}
	out := l.RenderToolBlock(data, 30, CatppuccinMocha)
	if out == "" {
		t.Fatal("empty output at narrow width")
	}
	if !strings.Contains(out, "DONE") {
		t.Error("badge should still fit at narrow width")
	}
	if !strings.Contains(out, "bash") {
		t.Error("action should still be present at narrow width")
	}
}

func TestSplitRenderToolBlock_UnknownTool(t *testing.T) {
	l := SplitLayout{}
	data := ToolBlockData{
		ToolName: "custom_tool",
		Action:   "custom_tool",
		Target:   "arg=value",
		Body:     "result",
		Done:     true,
	}
	// Should not panic, should render something
	out := l.RenderToolBlock(data, 80, CatppuccinMocha)
	if out == "" {
		t.Fatal("empty output for unknown tool")
	}
	if !strings.Contains(out, "custom_tool") {
		t.Error("should contain custom tool name")
	}
}

func TestSplitRenderToolBlock_WithHeaderAndFooter(t *testing.T) {
	l := SplitLayout{}
	data := ToolBlockData{
		Action: "bash",
		Target: "cat big.txt",
		Header: "… (20 previous lines, 30 total)",
		Body:   "line 21\nline 22",
		Footer: "… (8 more lines)",
		Done:   true,
	}
	out := l.RenderToolBlock(data, 80, CatppuccinMocha)
	if !strings.Contains(out, "20 previous lines") {
		t.Error("header notice should be present")
	}
	if !strings.Contains(out, "8 more lines") {
		t.Error("footer notice should be present")
	}
}

func TestSplitRenderUserMessage_LongLineWraps(t *testing.T) {
	l := SplitLayout{}
	longText := strings.Repeat("word ", 30) // ~150 chars
	out := l.RenderUserMessage(longText, 60, CatppuccinMocha)
	lines := strings.Split(out, "\n")
	// Should have wrapped into multiple lines, all with bar prefix
	if len(lines) < 4 { // blank + YOU + at least 2 content lines
		t.Fatalf("lines = %d, expected wrapping into more lines", len(lines))
	}
	for i := 1; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "│") {
			t.Errorf("line %d should start with │, got %q", i, lines[i])
		}
	}
}

func TestSplitRenderThinking_NonEmpty(t *testing.T) {
	l := SplitLayout{}
	out := l.RenderThinking("considering options", 80, CatppuccinMocha)
	if out == "" {
		t.Fatal("empty output")
	}
	if !strings.Contains(out, "considering options") {
		t.Error("should contain thinking text")
	}
}

func TestSplitRenderAssistantText_AddsPadding(t *testing.T) {
	l := SplitLayout{}
	out := l.RenderAssistantText("hello world", 80)
	if !strings.HasPrefix(out, "  ") {
		t.Error("assistant text should have left padding")
	}
	if !strings.Contains(out, "hello world") {
		t.Error("should contain the text")
	}
}

func TestSplitRenderAssistantText_EmptyPassThrough(t *testing.T) {
	l := SplitLayout{}
	if l.RenderAssistantText("", 80) != "" {
		t.Error("empty input should return empty")
	}
}

func TestSplitRenderError_NonEmpty(t *testing.T) {
	l := SplitLayout{}
	out := l.RenderError("something went wrong", 80, CatppuccinMocha)
	if out == "" {
		t.Fatal("empty output")
	}
	if !strings.Contains(out, "something went wrong") {
		t.Error("should contain error text")
	}
}

func TestSplitRenderStatus_NonEmpty(t *testing.T) {
	l := SplitLayout{}
	out := l.RenderStatus("processing", 80, CatppuccinMocha)
	if !strings.Contains(out, "processing") {
		t.Error("should contain status text")
	}
}

func TestSplitRenderLiveNotice_NonEmpty(t *testing.T) {
	l := SplitLayout{}
	out := l.RenderLiveNotice("compacting context...", 80, CatppuccinMocha)
	if !strings.Contains(out, "compacting context...") {
		t.Error("should contain notice text")
	}
}

func TestSplitRenderLiveNotice_StripsNewlines(t *testing.T) {
	l := SplitLayout{}
	out := l.RenderLiveNotice("line1\nline2", 80, CatppuccinMocha)
	if strings.Contains(out, "\n") {
		t.Error("live notice should not contain newlines")
	}
}
