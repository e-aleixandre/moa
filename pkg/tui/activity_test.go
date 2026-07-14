package tui

import (
	"testing"
	"time"
)

func TestGerundFor(t *testing.T) {
	if got := gerundFor(0); got != workingGerunds[0] {
		t.Fatalf("gerundFor(0) = %q, want %q", got, workingGerunds[0])
	}
	if got := gerundFor(3999 * time.Millisecond); got != workingGerunds[0] {
		t.Fatalf("gerundFor(3.999s) = %q, want %q", got, workingGerunds[0])
	}
	if got := gerundFor(4 * time.Second); got != workingGerunds[1] {
		t.Fatalf("gerundFor(4s) = %q, want %q", got, workingGerunds[1])
	}
	if got := gerundFor(8 * time.Second); got != workingGerunds[2] {
		t.Fatalf("gerundFor(8s) = %q, want %q", got, workingGerunds[2])
	}
	// wraps around
	if got := gerundFor(time.Duration(len(workingGerunds)) * gerundPeriod); got != workingGerunds[0] {
		t.Fatalf("gerundFor(full cycle) = %q, want %q", got, workingGerunds[0])
	}
	// negative clamps to first
	if got := gerundFor(-5 * time.Second); got != workingGerunds[0] {
		t.Fatalf("gerundFor(negative) = %q, want %q", got, workingGerunds[0])
	}
}

func TestFormatElapsed(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{8 * time.Second, "8s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m"},
		{63 * time.Second, "1m03s"},
		{134 * time.Second, "2m14s"},
		{time.Hour, "1h"},
		{time.Hour + time.Minute, "1h01m"},
		{-1, ""},
	}
	for _, c := range cases {
		if got := formatElapsed(c.in); got != c.want {
			t.Errorf("formatElapsed(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestActivityText(t *testing.T) {
	if got := activityText(phaseThinking, 0); got != "Thinking" {
		t.Errorf("thinking = %q", got)
	}
	if got := activityText(phaseWaiting, 0); got != "Waiting for you" {
		t.Errorf("waiting = %q", got)
	}
	if got := activityText(phaseCompacting, 0); got != "Compacting context" {
		t.Errorf("compacting = %q", got)
	}
	if got := activityText(phaseVerifying, 0); got != "Running auto-verify" {
		t.Errorf("verifying = %q", got)
	}
	if got := activityText(phaseWorking, 8*time.Second); got != workingGerunds[2] {
		t.Errorf("working@8s = %q, want %q", got, workingGerunds[2])
	}
	if got := activityText("", 0); got != "" {
		t.Errorf("empty phase = %q, want empty", got)
	}
}

func TestStatusModelPhaseView(t *testing.T) {
	m := newStatus()
	start := time.Now().Add(-8 * time.Second)
	m.SetPhase(phaseWorking, start)
	if !m.active() {
		t.Fatal("status should be active in phase mode")
	}
	view := m.View()
	// Working phase with a timer shows the elapsed counter separator.
	if !contains(view, "·") {
		t.Errorf("working view %q should contain a timer", view)
	}

	// Waiting phase has no timer even with a start time.
	m.SetPhase(phaseWaiting, time.Time{})
	if got := m.View(); !contains(got, "Waiting for you") {
		t.Errorf("waiting view = %q", got)
	}

	// Plain text mode still works and leaves phase mode.
	m.SetText("clipboard copied")
	if m.phase != "" {
		t.Errorf("SetText should clear phase, got %q", m.phase)
	}
	m.Clear()
	if m.active() {
		t.Error("Clear should blank the line")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
