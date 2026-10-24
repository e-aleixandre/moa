package serve

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/e-aleixandre/moa/pkg/askuser"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// F10: the inventory is bounded on what is serialized. One post may carry 32
// questions of 400 characters that all expand when escaped; the block the model
// reads on every request stays within the declared bound.
func TestReviewInventoryDeclaredEightBy400Bound(t *testing.T) {
	f1Isolate(t)
	items := make([]any, 32)
	for i := range items {
		items[i] = map[string]any{"question": strings.Repeat("&", 400)}
	}
	p := &f1Provider{script: func(req core.Request) <-chan core.AssistantEvent {
		if strings.HasPrefix(f1LastUser(req), "F1-TRIGGER") && !f1HasResult(req) {
			return f1ToolCall(map[string]any{"questions": items, "requires_tap": true})
		}
		return f1Text("F1-AFTER-ACK")
	}}
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), p)
	s := e.session(f1KindHuman)
	e.send(s, "F1-TRIGGER please ask 32 questions")
	ask := waitPendingAsk(t, s)
	f1Settle(t, s)
	text := ""
	for _, message := range p.lastSession(t, s.ID).Messages {
		if body := message.Content[0].Text; strings.HasPrefix(body, "<foryou_snapshot>") {
			start := strings.Index(body, "<open_questions>")
			end := strings.Index(body, "</open_questions>")
			if start >= 0 && end > start {
				text = body[start : end+len("</open_questions>")]
			}
		}
	}
	if text == "" {
		t.Fatal("real provider request has no open-questions section")
	}
	if len(text) > inventoryMaxBytes {
		t.Fatalf("inventory is %d bytes, bound is %d", len(text), inventoryMaxBytes)
	}
	line := text[strings.Index(text, "- "+ask.ID):]
	line = line[:strings.Index(line, "\n")]
	if body := line[strings.Index(line, ": ")+2:]; len(body) > inventoryMaxText {
		t.Fatalf("a question's text is %d serialized bytes, bound is %d", len(body), inventoryMaxText)
	}
	if !strings.Contains(line, "requires the user's own tap") {
		t.Fatalf("the tap requirement was lost: %s", line)
	}
	if strings.Contains(text, "&a...") || strings.Contains(text, "&am...") {
		t.Fatal("the cut split an entity")
	}
}

func TestEscapedPrefixCutsAfterEscapingOnWholeCharacters(t *testing.T) {
	for _, tc := range []struct{ name, in string }{
		{"entities", strings.Repeat("&", 400)},
		{"angle", strings.Repeat("<>", 300)},
		{"multibyte", strings.Repeat("é", 500)},
		{"mixed", strings.Repeat("a<é&", 200)},
	} {
		got := escapedPrefix(tc.in, inventoryMaxText)
		if len(got) > inventoryMaxText || !utf8.ValidString(got) {
			t.Errorf("%s: %d bytes, valid=%v", tc.name, len(got), utf8.ValidString(got))
		}
		if !strings.HasSuffix(got, "...") {
			t.Errorf("%s: a cut text must say so: %q", tc.name, got[len(got)-10:])
		}
		body := strings.TrimSuffix(got, "...")
		if i := strings.LastIndexByte(body, '&'); i >= 0 && !strings.Contains(body[i:], ";") {
			t.Errorf("%s: entity split at the cut: %q", tc.name, body[i:])
		}
	}
	if got := escapedPrefix("short  one\n", inventoryMaxText); got != "short one" {
		t.Errorf("a short text must pass whitespace-normalized and uncut: %q", got)
	}
}

// F11: the answer event carries the questions as asked, literally, in its own
// metadata and across both projections, so its row never depends on finding
// the call that posted them.
func TestReviewAnswerEventCarriesTheQuestionsLiterally(t *testing.T) {
	q := tasks.Question{
		ID: "q_1", AskedAt: 1, ClosedAt: 2,
		Questions: []askuser.Question{{Text: "Deploy <now> & tell?"}, {Text: "Which env?"}},
		Answers:   []string{"Yes", "prod"},
		Responder: askuser.Responder{Kind: askuser.ResponderUser},
	}
	custom := questionAnswerCustom(tasks.Notice{ID: "e1", Title: "t"}, q, true, false)
	want := []string{"Deploy <now> & tell?", "Which env?"}
	if got, _ := custom["questions"].([]string); !slices.Equal(got, want) {
		t.Fatalf("event questions = %v, want %v", custom["questions"], want)
	}
	// Live (typed) and after a reload (JSON round trip) both reach the client.
	raw, _ := json.Marshal(custom)
	var reloaded map[string]any
	if err := json.Unmarshal(raw, &reloaded); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]map[string]any{"live": custom, "reloaded": reloaded} {
		projected := projectWSMessageCustom(c)
		if got, _ := projected["questions"].([]string); !slices.Equal(got, want) {
			t.Errorf("%s: history projection questions = %v", name, projected["questions"])
		}
	}
}
