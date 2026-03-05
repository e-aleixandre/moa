package anthropic

import (
	"os"
	"strings"
	"testing"
)

func TestParseSSEFrames_SimpleText(t *testing.T) {
	data, err := os.ReadFile("../../../testdata/sse/simple_text.txt")
	if err != nil {
		t.Fatal(err)
	}

	var frames []sseFrame
	err = parseSSEFrames(strings.NewReader(string(data)), func(eventType, d string) {
		frames = append(frames, sseFrame{eventType, d})
	})
	if err != nil {
		t.Fatal(err)
	}

	expectedTypes := []string{
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	}

	if len(frames) != len(expectedTypes) {
		t.Fatalf("expected %d frames, got %d", len(expectedTypes), len(frames))
	}
	for i, expected := range expectedTypes {
		if frames[i].eventType != expected {
			t.Errorf("frame %d: expected event=%q, got %q", i, expected, frames[i].eventType)
		}
	}
}

func TestParseSSEFrames_ToolCall(t *testing.T) {
	data, err := os.ReadFile("../../../testdata/sse/tool_call.txt")
	if err != nil {
		t.Fatal(err)
	}

	var frames []sseFrame
	err = parseSSEFrames(strings.NewReader(string(data)), func(eventType, d string) {
		frames = append(frames, sseFrame{eventType, d})
	})
	if err != nil {
		t.Fatal(err)
	}

	// Should have: message_start, block_start(text), delta(text), block_stop,
	//              block_start(tool_use), delta(json), delta(json), block_stop,
	//              message_delta, message_stop
	if len(frames) != 10 {
		t.Fatalf("expected 10 frames, got %d", len(frames))
	}
}

func TestParseSSEFrames_MultilineData(t *testing.T) {
	data, err := os.ReadFile("../../../testdata/sse/multiline_data.txt")
	if err != nil {
		t.Fatal(err)
	}

	var frames []sseFrame
	err = parseSSEFrames(strings.NewReader(string(data)), func(eventType, d string) {
		frames = append(frames, sseFrame{eventType, d})
	})
	if err != nil {
		t.Fatal(err)
	}

	// First frame (message_start) has 2 data: lines joined by \n
	if len(frames) == 0 {
		t.Fatal("expected at least 1 frame")
	}
	if !strings.Contains(frames[0].data, "message_start") {
		t.Error("first frame should contain message_start data")
	}
	// Verify data lines were joined
	if !strings.Contains(frames[0].data, "\n") {
		t.Error("expected multiline data to be joined with \\n")
	}
}

func TestParseSSEFrames_Keepalive(t *testing.T) {
	data, err := os.ReadFile("../../../testdata/sse/keepalive.txt")
	if err != nil {
		t.Fatal(err)
	}

	var frames []sseFrame
	err = parseSSEFrames(strings.NewReader(string(data)), func(eventType, d string) {
		frames = append(frames, sseFrame{eventType, d})
	})
	if err != nil {
		t.Fatal(err)
	}

	// Comments should be stripped; should still have the same events
	for _, f := range frames {
		if strings.HasPrefix(f.data, ":") {
			t.Error("comment leaked through as data")
		}
	}
	if len(frames) != 6 {
		t.Fatalf("expected 6 frames (comments filtered), got %d", len(frames))
	}
}

func TestParseSSEFrames_LargeDelta(t *testing.T) {
	// Generate a delta >64KB to test no truncation
	bigText := strings.Repeat("x", 100_000)
	sse := "event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + bigText + `"}}` + "\n\n"

	var frames []sseFrame
	err := parseSSEFrames(strings.NewReader(sse), func(eventType, d string) {
		frames = append(frames, sseFrame{eventType, d})
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}
	if !strings.Contains(frames[0].data, bigText) {
		t.Error("large delta was truncated")
	}
}

func TestParseSSEFrames_Empty(t *testing.T) {
	var frames []sseFrame
	err := parseSSEFrames(strings.NewReader(""), func(eventType, d string) {
		frames = append(frames, sseFrame{eventType, d})
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 0 {
		t.Fatalf("expected 0 frames, got %d", len(frames))
	}
}

type sseFrame struct {
	eventType string
	data      string
}
