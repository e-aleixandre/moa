package askuser

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAskUser_RequiresTapIsOptionalRequestInput(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(NewTool(NewBridge()).Parameters, &schema); err != nil {
		t.Fatal(err)
	}
	property, ok := schema.Properties["requires_tap"]
	if !ok || property.Type != "boolean" {
		t.Errorf("ask_user request schema requires_tap = %+v (present=%v), want an optional boolean at the request root", property, ok)
	}
	for _, name := range schema.Required {
		if name == "requires_tap" {
			t.Error("requires_tap must remain optional for existing ask_user callers")
		}
	}
}

func TestAskUser_VoiceAnswerIsAttributed(t *testing.T) {
	var responder Responder
	if err := json.Unmarshal([]byte(`{"kind":"voice","via":"voice","call_id":"call-attribution","quote":"Sí, mañana, no hoy."}`), &responder); err != nil {
		t.Fatal(err)
	}
	const answer = "Mañana"
	text, _ := askOnce(t, []any{map[string]any{"question": "¿Hoy o mañana?"}},
		Reply{Answers: []string{answer}, Responder: responder})
	line, rest, hasLine := strings.Cut(text, "\n")
	if !hasLine || !strings.HasPrefix(line, "[Answered by voice relay") || !strings.Contains(line, "not a tap or typed confirmation") {
		t.Errorf("model-visible voice answer = %q, want a voice-relay attribution explicitly distinct from tap/typed confirmation", text)
	}
	if hasLine && rest != answer {
		t.Errorf("attributed answer = %q, want the original answer %q", rest, answer)
	}
}
