package attention

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

func TestRunTerminationIsReplayedUntilAcknowledged(t *testing.T) {
	s := newTestService(t)
	b := bus.NewLocalBus()
	defer b.Close()
	defer s.Attach(b, "s", "x", "X")()

	// NO client connected: the normal briefing is absent, but completion is
	// retained for the next init so a mobile network flap cannot lose it.
	b.Publish(bus.RunEnded{SessionID: "s", RunGen: 7, FinalText: "done", HadEdits: false})
	time.Sleep(40 * time.Millisecond)
	if items := s.Status(); len(items) != 0 {
		t.Fatalf("completion must not become a P0 item, got %+v", items)
	}

	// Now connect: init carries the completion. A connection drop before speech
	// acknowledgement must not make it disappear.
	client := &fakeClient{cid: 1}
	s.SetActiveClient(client)
	var term RunTermination
	eventually(t, "init carries termination", func() bool {
		m, ok := client.lastOfType("init")
		if !ok || len(m.Terminations) != 1 || m.Terminations[0].Ref.RunGen != 7 {
			return false
		}
		term = m.Terminations[0]
		return true
	})

	// A reconnect before ack receives the same durable ID again.
	s.ClearActiveClient(client)
	client2 := &fakeClient{cid: 2}
	s.SetActiveClient(client2)
	eventually(t, "second init", func() bool {
		m, ok := client2.lastOfType("init")
		return ok && len(m.Terminations) == 1 && m.Terminations[0].ID == term.ID
	})

	// Only the explicit post-speech acknowledgement removes it.
	if !s.AckTerminationForClient(client2, term.ID) {
		t.Fatal("active client acknowledgement was rejected")
	}
	s.ClearActiveClient(client2)
	client3 := &fakeClient{cid: 3}
	s.SetActiveClient(client3)
	eventually(t, "acknowledged termination absent", func() bool {
		m, ok := client3.lastOfType("init")
		return ok && len(m.Terminations) == 0
	})
}

func TestRunTerminationContainsBoundedUnchangedSummaryAndDedupes(t *testing.T) {
	s := newTestService(t)
	b := bus.NewLocalBus()
	defer b.Close()
	defer s.Attach(b, "s", "x", "X")()

	client := &fakeClient{cid: 1}
	s.SetActiveClient(client)
	final := "Finished the API.\n```go\nfmt.Println(\"secret code\")\n```\ndiff --git a/a b/a\n+leaked diff"
	b.Publish(bus.RunEnded{SessionID: "s", RunGen: 9, FinalText: final, HadEdits: true})
	b.Publish(bus.RunEnded{SessionID: "s", RunGen: 9, FinalText: final, HadEdits: true})

	eventually(t, "one structured termination", func() bool {
		return len(briefingsOf(client)) == 1 && briefingsOf(client)[0].Termination != nil
	})
	term := briefingsOf(client)[0].Termination
	if len(term.Summary) > 512 || !contains(term.Summary, "secret code") || !contains(term.Summary, "leaked diff") {
		t.Fatalf("termination summary changed content: %#v", term.Summary)
	}
	if term.Ref.SessionID != "s" || term.Ref.RunGen != 9 || term.Ref.MessagesURL != "/api/sessions/s/messages" || term.ID == "" {
		t.Fatalf("bad termination ref: %+v", term)
	}
}

func TestTerminationSummaryRespectsByteBoundAndUTF8(t *testing.T) {
	if got := terminationSummary(strings.Repeat("a", 513)); len(got) != 512 || got != strings.Repeat("a", 509)+"…" {
		t.Fatalf("ASCII summary = %q (%d bytes)", got, len(got))
	}
	input := strings.Repeat("界", 171) // 513 bytes
	got := terminationSummary(input)
	if len(got) > 512 || !utf8.ValidString(got) || !strings.HasSuffix(got, "…") {
		t.Fatalf("UTF-8 summary = %q (%d bytes)", got, len(got))
	}
}

func TestAllOfflineTerminationsAppearInInit(t *testing.T) {
	s := newTestService(t)
	b1 := bus.NewLocalBus()
	b2 := bus.NewLocalBus()
	defer b1.Close()
	defer b2.Close()
	defer s.Attach(b1, "s1", "one", "One")()
	defer s.Attach(b2, "s2", "two", "Two")()

	b1.Publish(bus.RunEnded{SessionID: "s1", RunGen: 1, FinalText: "first"})
	b1.Publish(bus.RunEnded{SessionID: "s1", RunGen: 2, FinalText: "second"})
	b2.Publish(bus.RunEnded{SessionID: "s2", RunGen: 1, FinalText: "third"})
	eventually(t, "offline terminations retained", func() bool {
		return len(s.Snapshot().Terminations) == 3
	})

	client := &fakeClient{cid: 1}
	s.SetActiveClient(client)
	eventually(t, "all offline terminations in init", func() bool {
		m, ok := client.lastOfType("init")
		if !ok || len(m.Terminations) != 3 {
			return false
		}
		seen := make(map[string]uint64)
		for _, term := range m.Terminations {
			seen[term.SessionID] = term.Ref.RunGen
		}
		return seen["s1"] == 2 && seen["s2"] == 1
	})
	// Both different runs from s1 are present, rather than just its latest.
	m, _ := client.lastOfType("init")
	for i := 1; i < len(m.Terminations); i++ {
		if m.Terminations[i].CreatedAt.Before(m.Terminations[i-1].CreatedAt) {
			t.Fatalf("terminations are not time ordered: %+v", m.Terminations)
		}
	}
	var s1Runs []uint64
	for _, term := range m.Terminations {
		if term.SessionID == "s1" {
			s1Runs = append(s1Runs, term.Ref.RunGen)
		}
	}
	if len(s1Runs) != 2 || s1Runs[0] != 1 || s1Runs[1] != 2 {
		t.Fatalf("s1 terminations = %+v", s1Runs)
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
