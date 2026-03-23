package tui

import (
	"fmt"
	"strings"

	"github.com/ealeixandre/moa/pkg/bus"
)

// branchPicker is an inline selector for conversation branch points.
type branchPicker struct {
	active  bool
	cursor  int
	entries []bus.BranchPoint
}

func (p *branchPicker) Open(points []bus.BranchPoint) {
	p.entries = points
	p.cursor = len(points) - 1 // start at most recent
	p.active = true
}

func (p *branchPicker) Close() { p.active = false }

func (p *branchPicker) MoveUp() {
	if p.cursor > 0 {
		p.cursor--
	}
}

func (p *branchPicker) MoveDown() {
	if p.cursor < len(p.entries)-1 {
		p.cursor++
	}
}

func (p *branchPicker) Selected() bus.BranchPoint {
	if p.cursor < len(p.entries) {
		return p.entries[p.cursor]
	}
	return bus.BranchPoint{}
}

func (p *branchPicker) Render(width int) string {
	if !p.active || len(p.entries) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("📍 Branch from a conversation point:\n\n")

	for i, e := range p.entries {
		prefix := "   "
		if i == p.cursor {
			prefix = " ▸ "
		}
		marker := ""
		if e.IsCurrentPath {
			marker = " ●"
		}
		branches := ""
		if e.BranchCount > 1 {
			branches = fmt.Sprintf(" (%d branches)", e.BranchCount)
		}

		label := e.Label
		maxLabel := width - len(prefix) - len(marker) - len(branches) - 20
		if maxLabel < 20 {
			maxLabel = 20
		}
		if len(label) > maxLabel {
			label = label[:maxLabel] + "…"
		}
		role := "💬"
		if e.Role == "assistant" {
			role = "🤖"
		}

		fmt.Fprintf(&sb, "%s#%d %s %s%s%s\n", prefix, i+1, role, label, branches, marker)
	}

	sb.WriteString("\n  ↑↓ navigate • Enter select • Esc cancel\n")
	return sb.String()
}
