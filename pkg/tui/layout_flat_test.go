package tui

import (
	"strings"
	"testing"
)

func TestFlatRenderUserMessage_HasChevron(t *testing.T) {
	l := FlatLayout{}
	out := l.RenderUserMessage("hello world", 80, CatppuccinMocha)
	if out == "" {
		t.Fatal("empty output")
	}
	if !strings.Contains(out, "❯") {
		t.Error("flat user message should contain ❯ prefix")
	}
	if !strings.Contains(out, "hello world") {
		t.Error("flat user message should contain the text")
	}
}

func TestFlatRenderUserMessage_NoYOULabel(t *testing.T) {
	l := FlatLayout{}
	out := l.RenderUserMessage("hello", 80, CatppuccinMocha)
	if strings.Contains(out, "YOU") {
		t.Error("flat layout should not contain YOU label")
	}
}

func TestFlatRenderToolBlock_NonEmpty(t *testing.T) {
	l := FlatLayout{}
	data := ToolBlockData{
		Action: "bash",
		Target: "ls -la",
		Body:   "file1.go\nfile2.go",
		Done:   true,
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

func TestFlatRenderToolBlock_NoBadges(t *testing.T) {
	l := FlatLayout{}

	data := ToolBlockData{Action: "bash", Target: "ls", Done: true}
	out := l.RenderToolBlock(data, 80, CatppuccinMocha)
	if strings.Contains(out, "DONE") {
		t.Error("flat layout should not have DONE badge")
	}

	data = ToolBlockData{Action: "bash", Target: "ls", Done: false}
	out = l.RenderToolBlock(data, 80, CatppuccinMocha)
	if strings.Contains(out, "RUNNING") {
		t.Error("flat layout should not have RUNNING badge")
	}
	// Should have inline "running…" instead
	if !strings.Contains(out, "running…") {
		t.Error("flat layout should have inline running indicator")
	}
}

func TestFlatRenderToolBlock_ErrorBody(t *testing.T) {
	l := FlatLayout{}
	data := ToolBlockData{
		Action:  "bash",
		Target:  "cat /nope",
		Body:    "permission denied",
		Done:    true,
		IsError: true,
	}
	out := l.RenderToolBlock(data, 80, CatppuccinMocha)
	if !strings.Contains(out, "permission denied") {
		t.Error("error body should be present")
	}
}

func TestFlatRenderThinking_NonEmpty(t *testing.T) {
	l := FlatLayout{}
	out := l.RenderThinking("considering", 80, CatppuccinMocha)
	if out == "" || !strings.Contains(out, "considering") {
		t.Error("thinking should be non-empty and contain text")
	}
}

func TestFlatRenderAssistantText_AddsPadding(t *testing.T) {
	l := FlatLayout{}
	out := l.RenderAssistantText("hello world", 80)
	if !strings.HasPrefix(out, "  ") {
		t.Error("assistant text should have left padding")
	}
	if !strings.Contains(out, "hello world") {
		t.Error("should contain the text")
	}
}

func TestFlatRenderError_NonEmpty(t *testing.T) {
	l := FlatLayout{}
	out := l.RenderError("bad things", 80, CatppuccinMocha)
	if !strings.Contains(out, "bad things") {
		t.Error("should contain error text")
	}
}

func TestFlatRenderStatus_NonEmpty(t *testing.T) {
	l := FlatLayout{}
	out := l.RenderStatus("loading", 80, CatppuccinMocha)
	if !strings.Contains(out, "loading") {
		t.Error("should contain status text")
	}
}

func TestFlatRenderLiveNotice_NonEmpty(t *testing.T) {
	l := FlatLayout{}
	out := l.RenderLiveNotice("working...", 80, CatppuccinMocha)
	if !strings.Contains(out, "working...") {
		t.Error("should contain notice text")
	}
}
