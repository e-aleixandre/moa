package bus

import (
	"errors"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// An idle-only prompt must be rejected while the queue rail holds items, and
// must not land on that rail: the whole point is that the caller keeps the text
// instead of having it steered into somebody else's turn.
func TestSendPromptIdleOnlyRefusesAQueuedSessionWithoutSteering(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	fa.Steer(core.SteerItem{ID: "s1", Text: "queued first"})

	err := b.Execute(SendPrompt{Text: "reports", IdleOnly: true})
	if !errors.Is(err, ErrNotIdle) {
		t.Fatalf("SendPrompt(IdleOnly) = %v, want ErrNotIdle", err)
	}
	if got := fa.QueueLen(); got != 1 {
		t.Fatalf("queue length = %d, want the single pre-existing item", got)
	}
	if got := fa.steered; got != "queued first" {
		t.Fatalf("the refused prompt was queued as a steer: %q", got)
	}
	if fa.sendCalled {
		t.Fatal("the refused prompt started a run")
	}
}

// A running session refuses an idle-only prompt too, for the same reason.
func TestSendPromptIdleOnlyRefusesARunningSession(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)
	sctx.State.ForceState(StateRunning)

	if err := b.Execute(SendPrompt{Text: "reports", IdleOnly: true}); !errors.Is(err, ErrNotIdle) {
		t.Fatalf("SendPrompt(IdleOnly) = %v, want ErrNotIdle", err)
	}
	if got := fa.QueueLen(); got != 0 {
		t.Fatalf("queue length = %d, want 0", got)
	}
}

// An idle session takes it as an ordinary run.
func TestSendPromptIdleOnlyRunsWhenIdle(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	if err := b.Execute(SendPrompt{Text: "reports", IdleOnly: true}); err != nil {
		t.Fatalf("SendPrompt(IdleOnly) on an idle session = %v", err)
	}
	ended, mu := collect[RunEnded](b)
	waitForLen(b, ended, mu, 1, 2*time.Second)
	if !fa.sendCalled {
		t.Fatal("the prompt did not start a run")
	}
}

func TestSendPromptIdleOnlyKeepsVerifiersRunningDespiteBackgroundExemption(t *testing.T) {
	for _, verifier := range []string{"auto", "goal"} {
		t.Run(verifier, func(t *testing.T) {
			b := NewLocalBus()
			defer b.Close()
			fa := &fakeAgent{}
			sctx := newTestSessionContextWithState(b, fa)
			RegisterHandlers(sctx)
			if verifier == "auto" {
				sctx.beginAutoVerify()
				defer sctx.endAutoVerify()
			} else {
				sctx.beginGoalVerify()
				defer sctx.endGoalVerify()
			}
			if err := b.Execute(SendPrompt{Text: "reports", IdleOnly: true, AllowBackgroundWork: true}); !errors.Is(err, ErrNotIdle) {
				t.Fatalf("SendPrompt during %s verification = %v, want ErrNotIdle", verifier, err)
			}
			if fa.sendCalled {
				t.Fatal("the report interrupted verification")
			}
		})
	}
}

// An explicit human prompt (InterruptVerification) is admitted atomically like
// any idle-only prompt, but unlike an owner report it does not wait for a
// verifier: it reaches the same hooks as a typed message, which cancel it.
func TestSendPromptIdleOnlyInterruptsVerifiersWhenAsked(t *testing.T) {
	for _, verifier := range []string{"auto", "goal"} {
		t.Run(verifier, func(t *testing.T) {
			b := NewLocalBus()
			defer b.Close()
			fa := &fakeAgent{}
			sctx := newTestSessionContextWithState(b, fa)
			canceled := false
			RegisterHandlers(sctx)
			sctx.cancelGoalVerify = func() { canceled = true }
			if verifier == "auto" {
				sctx.beginAutoVerify()
				defer sctx.endAutoVerify()
			} else {
				sctx.beginGoalVerify()
				defer sctx.endGoalVerify()
			}
			err := b.Execute(SendPrompt{Text: "skill", Custom: map[string]any{"source": "skill"}, IdleOnly: true, AllowBackgroundWork: true, InterruptVerification: true})
			if err != nil {
				t.Fatalf("SendPrompt during %s verification = %v", verifier, err)
			}
			ended, mu := collect[RunEnded](b)
			waitForLen(b, ended, mu, 1, 2*time.Second)
			if !fa.sendCalled {
				t.Fatal("the prompt did not start a run")
			}
			if verifier == "goal" && !canceled {
				t.Fatal("the goal verification was not asked to stop")
			}
		})
	}
}
