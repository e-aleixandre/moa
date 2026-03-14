package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ealeixandre/moa/pkg/core"
)

// voiceState tracks the voice recording lifecycle.
type voiceState int

const (
	voiceIdle         voiceState = iota
	voiceRecording               // sox/arecord is capturing audio
	voiceTranscribing            // audio sent to Whisper, waiting for text
)

// voiceStartMsg signals that recording has started successfully.
type voiceStartMsg struct{}

// voiceResultMsg carries the transcribed text (or error) back to the TUI.
type voiceResultMsg struct {
	Text string
	Err  error
}

// voiceRecorder manages audio capture and transcription.
// It is owned by appModel and accessed only from the Bubble Tea goroutine.
type voiceRecorder struct {
	state       voiceState
	transcriber core.Transcriber
	cancel      context.CancelFunc // cancels the recording process
	tmpFile     string             // path to temp audio file
}

// available returns true if voice input can be used (transcriber + recording tool).
func (v *voiceRecorder) available() bool {
	return v.transcriber != nil && findRecordCommand() != ""
}

// findRecordCommand returns the first available audio recording command.
// Prefers "rec" (SoX) on all platforms, falls back to "arecord" on Linux.
func findRecordCommand() string {
	if _, err := exec.LookPath("rec"); err == nil {
		return "rec"
	}
	if runtime.GOOS == "linux" {
		if _, err := exec.LookPath("arecord"); err == nil {
			return "arecord"
		}
	}
	return ""
}

// startRecording begins capturing audio to a temp file.
// Returns a Cmd that signals voiceStartMsg when recording begins.
// The actual stop + transcribe happens when stopAndTranscribe is called.
func (v *voiceRecorder) startRecording(parentCtx context.Context) tea.Cmd {
	if v.state != voiceIdle {
		return nil
	}

	recCmd := findRecordCommand()
	if recCmd == "" {
		return func() tea.Msg {
			return voiceResultMsg{Err: fmt.Errorf("no recording tool found (install sox)")}
		}
	}

	tmpFile, err := os.CreateTemp("", "moa-voice-*.wav")
	if err != nil {
		return func() tea.Msg {
			return voiceResultMsg{Err: fmt.Errorf("creating temp file: %w", err)}
		}
	}
	tmpFile.Close() //nolint:errcheck
	v.tmpFile = tmpFile.Name()

	ctx, cancel := context.WithCancel(parentCtx)
	v.cancel = cancel
	v.state = voiceRecording

	path := v.tmpFile

	return func() tea.Msg {
		var cmd *exec.Cmd
		switch recCmd {
		case "rec":
			// SoX: record to WAV, 16kHz mono (optimal for Whisper).
			cmd = exec.CommandContext(ctx, "rec",
				"-q",            // quiet
				"-r", "16000",   // sample rate
				"-c", "1",       // mono
				"-b", "16",      // 16-bit
				path,            // output file
			)
		case "arecord":
			// ALSA: record to WAV, 16kHz mono.
			cmd = exec.CommandContext(ctx, "arecord",
				"-q",            // quiet
				"-f", "S16_LE",  // format
				"-r", "16000",   // sample rate
				"-c", "1",       // mono
				path,            // output file
			)
		}

		// Detach from terminal (Bubble Tea owns it).
		cmd.Stdin = nil
		cmd.Stdout = nil
		cmd.Stderr = nil

		if err := cmd.Start(); err != nil {
			cancel()
			return voiceResultMsg{Err: fmt.Errorf("starting recorder: %w", err)}
		}

		// Signal that recording started, then wait for ctx cancel.
		// We don't return voiceStartMsg here because the Cmd must block
		// until recording is done. Instead we use a separate signal.
		_ = cmd.Wait() // blocks until cancel() kills the process

		return nil // stopAndTranscribe handles the result
	}
}

// stopAndTranscribe stops recording and sends audio to the transcriber.
// Returns a Cmd that produces voiceResultMsg.
func (v *voiceRecorder) stopAndTranscribe() tea.Cmd {
	if v.state != voiceRecording {
		return nil
	}

	// Kill the recording process.
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}

	v.state = voiceTranscribing
	path := v.tmpFile
	transcriber := v.transcriber

	return func() tea.Msg {
		defer os.Remove(path) //nolint:errcheck

		f, err := os.Open(path)
		if err != nil {
			return voiceResultMsg{Err: fmt.Errorf("opening recording: %w", err)}
		}
		defer f.Close() //nolint:errcheck

		// Check file has content.
		info, err := f.Stat()
		if err != nil {
			return voiceResultMsg{Err: fmt.Errorf("stat recording: %w", err)}
		}
		if info.Size() < 1000 {
			return voiceResultMsg{Err: fmt.Errorf("recording too short")}
		}

		text, err := transcriber.Transcribe(context.Background(), f, "recording.wav")
		if err != nil {
			return voiceResultMsg{Err: err}
		}
		return voiceResultMsg{Text: strings.TrimSpace(text)}
	}
}

// reset returns to idle state, cleaning up resources.
func (v *voiceRecorder) reset() {
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	if v.tmpFile != "" {
		os.Remove(v.tmpFile) //nolint:errcheck
		v.tmpFile = ""
	}
	v.state = voiceIdle
}

// handleVoiceToggle starts or stops voice recording (Ctrl+R).
func (m appModel) handleVoiceToggle() (tea.Model, tea.Cmd) {
	if !m.voice.available() {
		if m.voice.transcriber == nil {
			m.status.SetText("voice: no OpenAI API key configured")
		} else {
			m.status.SetText("voice: install sox (brew install sox / apt install sox)")
		}
		return m, nil
	}

	switch m.voice.state {
	case voiceRecording:
		// Stop recording → transcribe.
		m.status.SetText("transcribing...")
		return m, m.voice.stopAndTranscribe()
	case voiceTranscribing:
		// Already transcribing, ignore.
		return m, nil
	default:
		// Start recording — kick the spinner so it animates.
		m.status.SetText("recording — Ctrl+R to stop")
		return m, tea.Batch(m.voice.startRecording(m.baseCtx), m.status.spinner.Tick)
	}
}

// handleVoiceResult processes the transcription result.
func (m appModel) handleVoiceResult(msg voiceResultMsg) (tea.Model, tea.Cmd) {
	m.voice.state = voiceIdle
	if msg.Err != nil {
		m.status.SetText("voice: " + msg.Err.Error())
		return m, nil
	}
	if msg.Text == "" {
		m.status.SetText("voice: no speech detected")
		return m, nil
	}

	// Insert transcribed text at cursor position in the input.
	current := m.input.textarea.Value()
	if current != "" && !strings.HasSuffix(current, " ") {
		current += " "
	}
	m.input.textarea.SetValue(current + msg.Text)
	m.input.textarea.CursorEnd()
	m.status.SetText("")
	return m, nil
}
