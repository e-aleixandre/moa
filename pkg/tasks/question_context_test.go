package tasks

import (
	"testing"

	"github.com/e-aleixandre/moa/pkg/askuser"
)

// A question's context is stored with the question and read back by another
// process, unchanged.
func TestQuestionContextRoundTrips(t *testing.T) {
	r := newRepo(t)
	id := NewQuestionID()
	if _, _, err := r.InsertQuestion(bg, Question{ID: id, SessionID: "s", OriginToolCallID: "tc", OriginTool: OriginAskUser,
		Questions: []askuser.Question{{Text: "Ship?", Context: "Staging is green.", Options: []string{"yes", "no"}}, {Text: "Plain?"}}}); err != nil {
		t.Fatal(err)
	}
	other := openRepo(t, r.Path())
	got, err := other.GetQuestion(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Questions[0].Context != "Staging is green." || got.Questions[1].Context != "" {
		t.Fatalf("questions = %+v", got.Questions)
	}
}
