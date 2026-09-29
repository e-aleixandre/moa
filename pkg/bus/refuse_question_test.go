package bus

import (
	"errors"
	"testing"
	"time"
)

func TestRefuseQuestionRejectsSteerAndPromptInPermission(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)
	sctx.State.ForceState(StateRunning)
	sctx.State.ForceState(StatePermission)

	if err := b.Execute(SteerAgent{ID: "s1", Text: "x", RefuseQuestion: true}); !errors.Is(err, ErrSessionQuestion) {
		t.Fatalf("SteerAgent = %v, want ErrSessionQuestion", err)
	}
	if err := b.Execute(SendPrompt{Text: "x", RefuseQuestion: true}); !errors.Is(err, ErrSessionQuestion) {
		t.Fatalf("SendPrompt = %v, want ErrSessionQuestion", err)
	}
	if fa.QueueLen() != 0 || fa.sendCalled {
		t.Fatalf("refused commands reached the agent: queue=%d send=%v", fa.QueueLen(), fa.sendCalled)
	}
}

func TestRefuseQuestionDefaultKeepsPermissionBehaviour(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)
	sctx.State.ForceState(StateRunning)
	sctx.State.ForceState(StatePermission)

	if err := b.Execute(SteerAgent{ID: "s1", Text: "queued"}); err != nil {
		t.Fatalf("SteerAgent without RefuseQuestion = %v", err)
	}
	if fa.QueueLen() != 1 {
		t.Fatalf("queue length = %d, want 1", fa.QueueLen())
	}
}

func TestRefuseQuestionPromptAdmittedWhenNotInPermission(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	if err := b.Execute(SendPrompt{Text: "go", RefuseQuestion: true}); err != nil {
		t.Fatalf("SendPrompt = %v", err)
	}
	ended, mu := collect[RunEnded](b)
	waitForLen(b, ended, mu, 1, 2*time.Second)
	if !fa.sendCalled {
		t.Fatal("prompt did not start a run")
	}
}
