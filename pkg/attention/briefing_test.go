package attention

import (
	"errors"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/bus"
)

// briefingsOf returns all "briefing" messages the client has received.
func briefingsOf(f *fakeClient) []Briefing {
	var out []Briefing
	for _, m := range f.messages() {
		if m.Type == "briefing" && m.Briefing != nil {
			out = append(out, *m.Briefing)
		}
	}
	return out
}

func TestRunOKEmitsProgressBriefing(t *testing.T) {
	s := newTestService(t)
	b := bus.NewLocalBus()
	defer b.Close()
	defer s.Attach(b, "s", "facturas", "Facturas")()

	client := &fakeClient{cid: 1}
	s.SetActiveClient(client)

	b.Publish(bus.RunEnded{SessionID: "s", FinalText: "Added the invoice endpoint and tests.", HadEdits: true})

	eventually(t, "run_ok briefing", func() bool {
		for _, br := range briefingsOf(client) {
			if br.Kind == KindRunOK && br.Priority == P2Progress &&
				contains(br.Spoken, "made changes") {
				return true
			}
		}
		return false
	})
	// A progress briefing must NOT show up as a tracked pending item.
	if items := s.Status(); len(items) != 0 {
		t.Fatalf("progress briefing must not be tracked as P0, got %+v", items)
	}
}

func TestProgressBriefingIsLiveOnly(t *testing.T) {
	s := newTestService(t)
	b := bus.NewLocalBus()
	defer b.Close()
	defer s.Attach(b, "s", "x", "X")()

	// NO client connected: a finished run must produce no stored state and,
	// crucially, must not block or buffer.
	b.Publish(bus.RunEnded{SessionID: "s", FinalText: "done", HadEdits: false})
	time.Sleep(40 * time.Millisecond)
	if items := s.Status(); len(items) != 0 {
		t.Fatalf("no client: nothing should be tracked, got %+v", items)
	}

	// Now connect a client; the past run is NOT replayed (ephemeral).
	client := &fakeClient{cid: 1}
	s.SetActiveClient(client)
	time.Sleep(40 * time.Millisecond)
	if brs := briefingsOf(client); len(brs) != 0 {
		t.Fatalf("ephemeral briefings must not be replayed on connect, got %+v", brs)
	}
}

func TestProgressBriefingNoveltyFilter(t *testing.T) {
	s := newTestService(t)
	b := bus.NewLocalBus()
	defer b.Close()
	defer s.Attach(b, "s", "x", "X")()
	client := &fakeClient{cid: 1}
	s.SetActiveClient(client)

	// Two identical run ends (same edits + same text length signature) -> one
	// briefing only.
	b.Publish(bus.RunEnded{SessionID: "s", FinalText: "same result here", HadEdits: false})
	b.Publish(bus.RunEnded{SessionID: "s", FinalText: "same result here", HadEdits: false})
	eventually(t, "first briefing", func() bool { return len(briefingsOf(client)) >= 1 })
	time.Sleep(40 * time.Millisecond)
	if n := len(briefingsOf(client)); n != 1 {
		t.Fatalf("novelty filter should collapse identical briefings, got %d", n)
	}
}

func TestGoalEndedBriefing(t *testing.T) {
	s := newTestService(t)
	b := bus.NewLocalBus()
	defer b.Close()
	defer s.Attach(b, "s", "goikbot", "Goikbot")()
	client := &fakeClient{cid: 1}
	s.SetActiveClient(client)

	b.Publish(bus.GoalEnded{SessionID: "s", Reason: "objective met"})
	eventually(t, "goal_ended briefing", func() bool {
		for _, br := range briefingsOf(client) {
			if br.Kind == KindGoalEnded && br.Priority == P1Terminal && contains(br.Spoken, "objective met") {
				return true
			}
		}
		return false
	})
}

func TestGoalStalledBriefing(t *testing.T) {
	s := newTestService(t)
	b := bus.NewLocalBus()
	defer b.Close()
	defer s.Attach(b, "s", "x", "X")()
	client := &fakeClient{cid: 1}
	s.SetActiveClient(client)

	// Stalled < 2 -> nothing; stalled >= 2 -> a P1 briefing.
	b.Publish(bus.GoalChanged{SessionID: "s", Active: true, Stalled: 1})
	time.Sleep(30 * time.Millisecond)
	if len(briefingsOf(client)) != 0 {
		t.Fatalf("stalled=1 should not brief")
	}
	b.Publish(bus.GoalChanged{SessionID: "s", Active: true, Stalled: 2})
	eventually(t, "stalled briefing", func() bool {
		for _, br := range briefingsOf(client) {
			if br.Kind == KindGoalStalled {
				return true
			}
		}
		return false
	})
}

func TestVerifyFailBriefingAndPassSilent(t *testing.T) {
	s := newTestService(t)
	b := bus.NewLocalBus()
	defer b.Close()
	defer s.Attach(b, "s", "ui", "UI")()
	client := &fakeClient{cid: 1}
	s.SetActiveClient(client)

	// All pass -> silence.
	b.Publish(bus.AutoVerifyEnded{SessionID: "s", AllPass: true})
	time.Sleep(30 * time.Millisecond)
	if len(briefingsOf(client)) != 0 {
		t.Fatalf("all-pass verify must be silent")
	}
	// Failure -> a P1 verify_fail briefing.
	b.Publish(bus.AutoVerifyEnded{SessionID: "s", AllPass: false, Summary: "2 tests failed in pkg/foo"})
	eventually(t, "verify_fail briefing", func() bool {
		for _, br := range briefingsOf(client) {
			if br.Kind == KindVerifyFail && contains(br.Spoken, "2 tests failed") {
				return true
			}
		}
		return false
	})
}

func TestGoalIterationErrorBriefsButRoutineSilent(t *testing.T) {
	s := newTestService(t)
	b := bus.NewLocalBus()
	defer b.Close()
	defer s.Attach(b, "s", "x", "X")()
	client := &fakeClient{cid: 1}
	s.SetActiveClient(client)

	// Routine satisfied/not-satisfied verdicts are too noisy -> silent.
	b.Publish(bus.GoalIterationEnded{SessionID: "s", Iteration: 1, Satisfied: false, Feedback: "keep going"})
	b.Publish(bus.GoalIterationEnded{SessionID: "s", Iteration: 2, Satisfied: true})
	time.Sleep(40 * time.Millisecond)
	if len(briefingsOf(client)) != 0 {
		t.Fatalf("routine goal iterations must be silent for voice, got %+v", briefingsOf(client))
	}

	// A verifier-unavailable pause (Err set) is worth a heads-up.
	b.Publish(bus.GoalIterationEnded{SessionID: "s", Iteration: 3, Err: errors.New("verifier down")})
	eventually(t, "iteration-error briefing", func() bool {
		return len(briefingsOf(client)) == 1
	})
}

// Progress briefings must never interfere with the P0 blocking channel.
func TestProgressBriefingDoesNotDisturbP0(t *testing.T) {
	s := newTestService(t)
	b := bus.NewLocalBus()
	defer b.Close()
	defer s.Attach(b, "s", "x", "X")()
	client := &fakeClient{cid: 1}
	s.SetActiveClient(client)

	// A pending permission (P0) plus a stream of progress events.
	b.Publish(bus.PermissionRequested{SessionID: "s", ID: "perm_1", ToolName: "bash", Args: map[string]any{"command": "ls"}})
	b.Publish(bus.RunEnded{SessionID: "s", FinalText: "did stuff", HadEdits: true})
	b.Publish(bus.GoalEnded{SessionID: "s", Reason: "done"})

	eventually(t, "P0 still tracked", func() bool {
		items := s.Status()
		return len(items) == 1 && items[0].Kind == KindPermission
	})
	// And the briefings were delivered alongside, not swallowed.
	eventually(t, "briefings delivered", func() bool { return len(briefingsOf(client)) >= 1 })
}
