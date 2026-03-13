package tui

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
)

// StatusLine renders a horizontal bar composed of ordered segments.
// Designed for extensibility: segments can be added/removed/replaced by key.
// Two instances are used: one above the input (top) and one below (bottom).
//
// Thread-safe: segments can be updated from any goroutine (e.g., agent
// event subscribers). The View renders a consistent snapshot.
type StatusLine struct {
	mu       sync.RWMutex
	segments map[string]Segment
	style    lipgloss.Style
}

// Segment is a single piece of the status line, rendered at a given priority.
// Lower priority values render further left.
type Segment struct {
	Text     string // rendered text (may include ANSI via lipgloss)
	Priority int    // sort order: lower = further left
}

// NewStatusLine creates an empty status line with the given base style.
func NewStatusLine(style lipgloss.Style) *StatusLine {
	return &StatusLine{
		segments: make(map[string]Segment),
		style:    style,
	}
}

// Set adds or replaces a segment by key.
func (sl *StatusLine) Set(key, text string, priority int) {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	if text == "" {
		delete(sl.segments, key)
		return
	}
	sl.segments[key] = Segment{Text: text, Priority: priority}
}

// Remove deletes a segment by key.
func (sl *StatusLine) Remove(key string) {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	delete(sl.segments, key)
}

// Clear removes all segments.
func (sl *StatusLine) Clear() {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	sl.segments = make(map[string]Segment)
}

// IsEmpty returns true if no segments are set.
func (sl *StatusLine) IsEmpty() bool {
	sl.mu.RLock()
	defer sl.mu.RUnlock()
	return len(sl.segments) == 0
}

// View renders the status line. Returns empty string if no segments.
func (sl *StatusLine) View(width int) string {
	sl.mu.RLock()
	if len(sl.segments) == 0 {
		sl.mu.RUnlock()
		return ""
	}

	// Snapshot segments under read lock.
	ordered := make([]Segment, 0, len(sl.segments))
	for _, seg := range sl.segments {
		ordered = append(ordered, seg)
	}
	sl.mu.RUnlock()

	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].Priority < ordered[j].Priority
	})

	parts := make([]string, len(ordered))
	for i, seg := range ordered {
		parts[i] = seg.Text
	}

	content := strings.Join(parts, statusLineSep)
	// Force single physical line — multiline reflow causes visual corruption
	// with tea.Println-based scrollback in non-alt-screen mode.
	content = strings.ReplaceAll(content, "\n", " ")
	if width > 0 {
		content = truncateVisible(content, width-2) // account for style padding
	}
	return sl.style.Width(width).Render(content)
}

// truncateVisible truncates a string to maxWidth visible characters,
// preserving ANSI escape sequences. Uses lipgloss width measurement.
func truncateVisible(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	w := lipgloss.Width(s)
	if w <= maxWidth {
		return s
	}
	// Walk runes, tracking visible width. Keep all ANSI escapes.
	var result strings.Builder
	inEscape := false
	visible := 0
	for _, r := range s {
		if r == '\x1b' {
			inEscape = true
			result.WriteRune(r)
			continue
		}
		if inEscape {
			result.WriteRune(r)
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEscape = false
			}
			continue
		}
		if visible >= maxWidth {
			break
		}
		result.WriteRune(r)
		visible++
	}
	return result.String()
}

// --- Built-in segment updaters ---

// Segment keys for built-in segments. Extensions should use their own unique keys.
const (
	SegmentModel       = "model"
	SegmentThinking    = "thinking"
	SegmentPermissions = "permissions"
	SegmentPlan        = "plan"
	SegmentTasks       = "tasks"
	SegmentCost        = "cost"
	SegmentContext     = "context"
)

// Segment priorities (lower = further left).
const (
	PriorityModel       = 10
	PriorityThinking    = 20
	PriorityPermissions = 30
	PriorityPlan        = 40
	PriorityTasks       = 45
	PriorityCost        = 80
	PriorityContext     = 90 // rightmost of the built-ins
)

// statusLineSep is rebuilt by RebuildUI via rebuildStatusLineVars.
var statusLineSep string

// Context usage level styles — rebuilt by RebuildUI.
var (
	statusLineStyle            lipgloss.Style
	statusLineContextLowStyle  lipgloss.Style
	statusLineContextMedStyle  lipgloss.Style
	statusLineContextHighStyle lipgloss.Style
)

// rebuildStatusLineVars is called by RebuildUI to update derived vars.
func rebuildStatusLineVars() {
	t := ActiveTheme
	statusLineSep = statusLineSepStyle.Render("  ·  ")
	statusLineStyle = lipgloss.NewStyle().Foreground(t.Text).Background(t.Surface0)
	statusLineContextLowStyle = lipgloss.NewStyle().Foreground(t.Green)
	statusLineContextMedStyle = lipgloss.NewStyle().Foreground(t.Yellow)
	statusLineContextHighStyle = lipgloss.NewStyle().Foreground(t.Red)
}

// UpdateModelSegment sets the model segment.
func (sl *StatusLine) UpdateModelSegment(name string) {
	text := statusLineKeyStyle.Render("model ") + statusLineValueStyle.Render(name)
	sl.Set(SegmentModel, text, PriorityModel)
}

// UpdateThinkingSegment sets the thinking level segment.
func (sl *StatusLine) UpdateThinkingSegment(level string) {
	text := statusLineKeyStyle.Render("thinking ") + statusLineValueStyle.Render(level)
	sl.Set(SegmentThinking, text, PriorityThinking)
}

// UpdatePermissionsSegment sets the permissions mode segment.
func (sl *StatusLine) UpdatePermissionsSegment(mode string) {
	if mode == "" {
		mode = "yolo"
	}
	sl.Set(SegmentPermissions, mode, PriorityPermissions)
}

// UpdatePlanSegment sets the plan mode segment. Pass "" to remove.
func (sl *StatusLine) UpdatePlanSegment(mode string) {
	if mode == "" {
		sl.Remove(SegmentPlan)
		return
	}
	text := statusLineKeyStyle.Render("plan ") + statusLineValueStyle.Render(mode)
	sl.Set(SegmentPlan, text, PriorityPlan)
}

// UpdateCostSegment sets the session cost segment.
func (sl *StatusLine) UpdateCostSegment(cost float64) {
	if cost <= 0 {
		sl.Remove(SegmentCost)
		return
	}
	var val string
	if cost < 0.01 {
		val = fmt.Sprintf("$%.4f", cost)
	} else {
		val = fmt.Sprintf("$%.2f", cost)
	}
	text := statusLineKeyStyle.Render("cost ") + statusLineValueStyle.Render(val)
	sl.Set(SegmentCost, text, PriorityCost)
}

// UpdateTasksSegment sets the task progress segment.
func (sl *StatusLine) UpdateTasksSegment(done, total int) {
	if total == 0 {
		sl.Remove(SegmentTasks)
		return
	}
	text := statusLineKeyStyle.Render("📋 ") + statusLineValueStyle.Render(fmt.Sprintf("%d/%d", done, total))
	sl.Set(SegmentTasks, text, PriorityTasks)
}

// UpdateContextSegment sets the context usage segment.
// pct is 0-100. Color changes based on usage level.
func (sl *StatusLine) UpdateContextSegment(pct int) {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}

	style := statusLineContextLowStyle
	if pct >= 80 {
		style = statusLineContextHighStyle
	} else if pct >= 50 {
		style = statusLineContextMedStyle
	}

	text := statusLineKeyStyle.Render("context ") + style.Render(fmt.Sprintf("%d%%", pct))
	sl.Set(SegmentContext, text, PriorityContext)
}
