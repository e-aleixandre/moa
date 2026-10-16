package voice

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The backend gets the newest part of the call within a character budget,
// oldest first, with the cut marked; multi-byte text is never split.
func TestVoiceTranscript_TailKeepsNewestWithinBudget(t *testing.T) {
	var tr Transcript
	for _, ev := range []Event{
		{Kind: EventTranscript, Role: "user", ItemID: "u1", Text: "OLDEST request", Final: true},
		{Kind: EventTranscript, Role: "assistant", ItemID: "a1", Text: "¿Lo paso? mañana", Final: true},
		{Kind: EventTranscript, Role: "user", ItemID: "u2", Text: "Yes, pass it", Final: true},
	} {
		if err := tr.Add(ev); err != nil {
			t.Fatal(err)
		}
	}
	full := tr.Tail(1000)
	if full != "User: OLDEST request\nVoice: ¿Lo paso? mañana\nUser: Yes, pass it" {
		t.Fatalf("full tail = %q", full)
	}
	cut := tr.Tail(45)
	if strings.Contains(cut, "OLDEST") || !strings.HasPrefix(cut, "[earlier transcript omitted]\n") || !strings.HasSuffix(cut, "Voice: ¿Lo paso? mañana\nUser: Yes, pass it") {
		t.Fatalf("cut tail = %q", cut)
	}
	tiny := tr.Tail(5)
	if !utf8.ValidString(tiny) || !strings.HasSuffix(tiny, "s it") {
		t.Fatalf("tiny tail = %q", tiny)
	}
	var empty Transcript
	if empty.Tail(100) != "" {
		t.Fatal("empty transcript rendered text")
	}
}
