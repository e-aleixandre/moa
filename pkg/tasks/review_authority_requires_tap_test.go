package tasks

import (
	"errors"
	"testing"

	"github.com/e-aleixandre/moa/pkg/askuser"
)

func TestReviewAuthorityWithdrawDoesNotWeakenRequiresTap(t *testing.T) {
	r := newRepo(t)
	a := actor("requester", "p")
	q := newQuestion(a.SessionID, "tap-call")
	q.RequiresTap = true
	question, _, err := r.InsertQuestion(bg, q)
	if err != nil {
		t.Fatal(err)
	}
	req, err := r.AgentAsk(bg, a, AgentInput{Title: "ordinary request"})
	if err != nil {
		t.Fatal(err)
	}
	owner := actor("root-owner", "p")
	owner.Owner = true
	if _, err := r.AgentWithdraw(bg, owner, "Owner", req.ID, "resolved"); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetQuestion(bg, question.ID)
	if err != nil || got.State != QuestionOpen || !got.RequiresTap {
		t.Fatalf("withdraw touched tap question: %+v %v", got, err)
	}
	for _, who := range []askuser.Responder{{Kind: askuser.ResponderOwner, ID: "root-owner"}, {Kind: askuser.ResponderAutomation}, {Kind: askuser.ResponderVoice, CallID: "voice"}} {
		if _, err := r.AnswerQuestion(bg, a.SessionID, question.ID, []string{"yes"}, who, renderTest); !errors.Is(err, askuser.ErrRequiresTap) {
			t.Fatalf("%+v bypassed requires_tap: %v", who, err)
		}
	}
	if _, err := r.AnswerQuestion(bg, a.SessionID, question.ID, []string{"yes"}, human, renderTest); err != nil {
		t.Fatalf("human tap rejected: %v", err)
	}
}
