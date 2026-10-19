package askuser

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseQuestionsCarriesContext(t *testing.T) {
	qs, err := ParseQuestions(map[string]any{"questions": []any{
		map[string]any{"question": "Ship?", "context": "  Staging is green.\nProd is frozen until 5.  "},
		map[string]any{"question": "Plain?"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if qs[0].Context != "Staging is green.\nProd is frozen until 5." || qs[1].Context != "" {
		t.Fatalf("contexts = %q, %q", qs[0].Context, qs[1].Context)
	}
	b, _ := json.Marshal(qs[1])
	if strings.Contains(string(b), "context") {
		t.Fatalf("an absent context is serialized: %s", b)
	}
}

func TestParseQuestionsBoundsContext(t *testing.T) {
	ok := strings.Repeat("a", MaxContextBytes)
	if _, err := ParseQuestions(map[string]any{"questions": []any{map[string]any{"question": "q", "context": ok}}}); err != nil {
		t.Fatalf("context at the limit: %v", err)
	}
	_, err := ParseQuestions(map[string]any{"questions": []any{map[string]any{"question": "q", "context": ok + "a"}}})
	if err == nil || !strings.Contains(err.Error(), "context too long") {
		t.Fatalf("context over the limit: err=%v", err)
	}
}
