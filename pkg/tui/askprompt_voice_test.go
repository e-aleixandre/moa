package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/e-aleixandre/moa/pkg/bus"
)

func TestAskPrompt_DictateAppendsAndReplacesOption(t *testing.T) {
	var a askPrompt
	a.ShowFromBus("ask-1", []bus.AskQuestion{{Text: "Q?", Options: []string{"yes", "no"}}})

	// Cursor starts on an option; dictating must move to the free-text answer,
	// since the spoken words are the real answer.
	a.Dictate("actually it depends")
	if !a.isCustom() {
		t.Fatal("dictating should move the cursor to the free-text answer")
	}
	if a.customBuf != "actually it depends" {
		t.Fatalf("got %q", a.customBuf)
	}

	// A second pass appends rather than replacing.
	a.Dictate("on the weather")
	if a.customBuf != "actually it depends on the weather" {
		t.Fatalf("dictation should append, got %q", a.customBuf)
	}

	// No double separator when the buffer already ends in a space.
	a.customBuf = "trailing "
	a.Dictate("word")
	if a.customBuf != "trailing word" {
		t.Fatalf("got %q", a.customBuf)
	}

	// Blank speech changes nothing.
	before := a.customBuf
	a.Dictate("   ")
	if a.customBuf != before {
		t.Fatalf("blank dictation must not change the buffer, got %q", a.customBuf)
	}
}

// The prompt swallows every key so it can drive its own navigation; Ctrl+R has
// to be the exception or dictation is unreachable exactly where a long answer
// is most likely.
func TestAskPrompt_CtrlRReachesVoiceInsteadOfBeingSwallowed(t *testing.T) {
	m := newSwitchTestApp(t)
	m.askPrompt.ShowFromBus("ask-1", []bus.AskQuestion{{Text: "Q?"}})

	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlR})
	rm := updated.(appModel)

	// Without a transcriber configured, voice reports why instead of recording;
	// either way the key must not have been consumed as prompt input.
	if rm.askPrompt.customBuf != "" {
		t.Fatalf("Ctrl+R must not be typed into the answer, got %q", rm.askPrompt.customBuf)
	}
	if !strings.Contains(rm.status.text, "voice") {
		t.Fatalf("Ctrl+R should have reached the voice handler, status was %q", rm.status.text)
	}
}

// A transcript that arrives while a question is open belongs to that question,
// not to the composer sitting behind it.
func TestVoiceResult_RoutesToTheOpenQuestion(t *testing.T) {
	m := newSwitchTestApp(t)
	m.askPrompt.ShowFromBus("ask-1", []bus.AskQuestion{{Text: "Q?"}})
	m.voice.askID = "ask-1" // recording started under this question

	updated, _ := m.handleVoiceResult(voiceResultMsg{Text: "spoken answer"})
	rm := updated.(appModel)

	if rm.askPrompt.customBuf != "spoken answer" {
		t.Fatalf("transcript should fill the answer, got %q", rm.askPrompt.customBuf)
	}
	if v := rm.input.textarea.Value(); v != "" {
		t.Fatalf("the composer must stay untouched, got %q", v)
	}
}

// Transcription is slow enough that the question can be answered, or replaced
// by the next one, while the audio is still being processed. Such a transcript
// belongs to nothing: typing it into whatever question came next would answer
// the wrong question with words meant for another.
func TestVoiceResult_DiscardsTranscriptFromAnAnsweredQuestion(t *testing.T) {
	t.Run("question replaced by a newer batch", func(t *testing.T) {
		m := newSwitchTestApp(t)
		m.askPrompt.ShowFromBus("ask-2", []bus.AskQuestion{{Text: "New question?"}})
		m.voice.askID = "ask-1" // the recording belonged to the previous batch

		updated, _ := m.handleVoiceResult(voiceResultMsg{Text: "stale answer"})
		rm := updated.(appModel)

		if rm.askPrompt.customBuf != "" {
			t.Fatalf("a stale transcript must not answer the new question, got %q", rm.askPrompt.customBuf)
		}
		if !strings.Contains(rm.status.text, "discarded") {
			t.Fatalf("the user should be told it was dropped, status was %q", rm.status.text)
		}
	})

	t.Run("question already answered", func(t *testing.T) {
		m := newSwitchTestApp(t)
		m.voice.askID = "ask-1" // prompt is gone (answered) by the time text lands

		updated, _ := m.handleVoiceResult(voiceResultMsg{Text: "late answer"})
		rm := updated.(appModel)

		if v := rm.input.textarea.Value(); v != "" {
			t.Fatalf("a question's transcript must not fall through to the composer, got %q", v)
		}
	})
}

func TestVoiceResult_StillFillsComposerWithoutAQuestion(t *testing.T) {
	m := newSwitchTestApp(t)

	updated, _ := m.handleVoiceResult(voiceResultMsg{Text: "hello"})
	rm := updated.(appModel)

	if v := rm.input.textarea.Value(); v != "hello" {
		t.Fatalf("normal dictation should still reach the composer, got %q", v)
	}
}

// Cancelling a question while dictating must drop the recording too: the mic
// would otherwise keep capturing for a prompt nobody will answer, and the next
// Ctrl+R would stop that ghost take instead of starting a fresh dictation.
func TestAskPrompt_CancelStopsARecordingStartedForTheQuestion(t *testing.T) {
	m := newSwitchTestApp(t)
	m.askPrompt.ShowFromBus("ask-1", []bus.AskQuestion{{Text: "Q?"}})
	m.voice.askID = "ask-1"
	m.voice.state = voiceRecording

	updated, _ := m.handleAskKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	rm := updated.(appModel)

	if rm.voice.state != voiceIdle {
		t.Fatalf("the recording should have been dropped, state = %v", rm.voice.state)
	}
	if rm.voice.askID != "" {
		t.Fatalf("the question stamp should be cleared, got %q", rm.voice.askID)
	}
}

// A failed or empty transcription must not leave the question stamp behind, or
// the next composer dictation would be mistaken for an answer to it.
func TestVoiceResult_ClearsTheQuestionStampOnEveryPath(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  voiceResultMsg
	}{
		{"transcription error", voiceResultMsg{Err: errors.New("boom")}},
		{"no speech detected", voiceResultMsg{Text: ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newSwitchTestApp(t)
			m.voice.askID = "ask-1"

			updated, _ := m.handleVoiceResult(tc.msg)
			if got := updated.(appModel).voice.askID; got != "" {
				t.Fatalf("stamp should be cleared, got %q", got)
			}
		})
	}
}
