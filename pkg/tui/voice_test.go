package tui

import (
	"context"
	"io"
	"strings"
	"testing"
)

// mockTranscriber is a test double for core.Transcriber.
type mockTranscriber struct {
	text string
	err  error
}

func (m *mockTranscriber) Transcribe(_ context.Context, _ io.Reader, _ string) (string, error) {
	return m.text, m.err
}

func TestVoiceRecorder_AvailableWithoutTranscriber(t *testing.T) {
	v := voiceRecorder{}
	if v.available() {
		t.Error("should not be available without transcriber")
	}
}

func TestVoiceRecorder_AvailableWithTranscriber(t *testing.T) {
	v := voiceRecorder{transcriber: &mockTranscriber{}}
	// available() depends on finding rec/arecord in PATH.
	// We just verify it doesn't panic.
	_ = v.available()
}

func TestVoiceToggle_NoTranscriber(t *testing.T) {
	m := newTestModel()
	m.voice = voiceRecorder{} // no transcriber

	rm, _ := m.handleVoiceToggle()
	result := rm.(appModel)
	// Should show error in status.
	_ = result
}

func TestVoiceToggle_NoRecordingTool(t *testing.T) {
	m := newTestModel()
	m.voice = voiceRecorder{transcriber: &mockTranscriber{}}

	if findRecordCommand() == "" {
		// No sox installed — toggle should show install message.
		rm, _ := m.handleVoiceToggle()
		_ = rm.(appModel)
	}
}

func TestVoiceResult_InsertsText(t *testing.T) {
	m := newTestModel()
	m.voice.state = voiceTranscribing

	rm, _ := m.handleVoiceResult(voiceResultMsg{Text: "hello world"})
	result := rm.(appModel)

	got := result.input.textarea.Value()
	if !strings.Contains(got, "hello world") {
		t.Errorf("expected input to contain 'hello world', got %q", got)
	}
	if result.voice.state != voiceIdle {
		t.Errorf("expected voiceIdle, got %d", result.voice.state)
	}
}

func TestVoiceResult_AppendsToExisting(t *testing.T) {
	m := newTestModel()
	m.voice.state = voiceTranscribing
	m.input.textarea.SetValue("existing text")

	rm, _ := m.handleVoiceResult(voiceResultMsg{Text: "more words"})
	result := rm.(appModel)

	got := result.input.textarea.Value()
	if got != "existing text more words" {
		t.Errorf("expected 'existing text more words', got %q", got)
	}
}

func TestVoiceResult_Error(t *testing.T) {
	m := newTestModel()
	m.voice.state = voiceTranscribing

	rm, _ := m.handleVoiceResult(voiceResultMsg{Err: io.ErrUnexpectedEOF})
	result := rm.(appModel)

	if result.voice.state != voiceIdle {
		t.Error("should reset to idle on error")
	}
}

func TestVoiceResult_EmptyText(t *testing.T) {
	m := newTestModel()
	m.voice.state = voiceTranscribing

	rm, _ := m.handleVoiceResult(voiceResultMsg{Text: ""})
	result := rm.(appModel)

	if result.input.textarea.Value() != "" {
		t.Error("should not insert empty text")
	}
}

func TestVoiceRecorder_Reset(t *testing.T) {
	v := voiceRecorder{
		state:       voiceRecording,
		transcriber: &mockTranscriber{},
	}
	v.reset()
	if v.state != voiceIdle {
		t.Error("should be idle after reset")
	}
}

func TestFindRecordCommand(t *testing.T) {
	// Just verify it doesn't panic and returns a valid string or empty.
	cmd := findRecordCommand()
	if cmd != "" && cmd != "rec" && cmd != "arecord" {
		t.Errorf("unexpected record command: %q", cmd)
	}
}
