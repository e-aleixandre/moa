package tui

import (
	"fmt"
	"time"
)

// Activity indicator copy. Mirrors the web frontend's util/activity.js so the
// two frontends stay in lockstep (different runtimes, so each replicates the
// presentation logic). Three coarse phases plus the compacting/auto-verify
// specials; the "working" phase rotates playful gerunds by elapsed time so a
// long run never looks stuck. The gerunds are cosmetic and never name a
// specific tool — those are already visible in the transcript.

const (
	phaseThinking   = "thinking"
	phaseWorking    = "working"
	phaseWaiting    = "waiting"
	phaseCompacting = "compacting"
	phaseVerifying  = "verifying"
)

var workingGerunds = []string{
	"Percolating",
	"Noodling",
	"Simmering",
	"Cooking",
	"Brewing",
	"Tinkering",
	"Crunching",
	"Churning",
	"Wrangling",
	"Conjuring",
	"Marinating",
	"Whirring",
}

const gerundPeriod = 4 * time.Second

// gerundFor picks the working-phase word for a given elapsed time so the label
// advances every gerundPeriod, wrapping around the list.
func gerundFor(elapsed time.Duration) string {
	if elapsed < 0 {
		elapsed = 0
	}
	i := int(elapsed/gerundPeriod) % len(workingGerunds)
	return workingGerunds[i]
}

// formatElapsed renders a compact counter: "8s", "1m03s", "12m", "1h01m".
func formatElapsed(elapsed time.Duration) string {
	if elapsed < 0 {
		return ""
	}
	total := int(elapsed / time.Second)
	if total < 60 {
		return fmt.Sprintf("%ds", total)
	}
	m := total / 60
	s := total % 60
	if m < 60 {
		if s == 0 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	h := m / 60
	mm := m % 60
	if mm == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%02dm", h, mm)
}

// activityText builds the label for a phase at a given elapsed time.
func activityText(phase string, elapsed time.Duration) string {
	switch phase {
	case phaseThinking:
		return "Thinking"
	case phaseWaiting:
		return "Waiting for you"
	case phaseCompacting:
		return "Compacting context"
	case phaseVerifying:
		return "Running auto-verify"
	case phaseWorking:
		return gerundFor(elapsed)
	default:
		return ""
	}
}
