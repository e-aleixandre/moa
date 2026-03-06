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
	return sl.style.Width(width).Render(content)
}

// --- Built-in segment updaters ---

// Segment keys for built-in segments. Extensions should use their own unique keys.
const (
	SegmentModel    = "model"
	SegmentThinking = "thinking"
	SegmentContext  = "context"
)

// Segment priorities (lower = further left).
const (
	PriorityModel    = 10
	PriorityThinking = 20
	PriorityContext  = 90 // rightmost of the built-ins
)

// Separator between segments.
var statusLineSep = statusLineSepStyle.Render("  ·  ")

// Styles
var (
	statusLineStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("7")).
			Background(lipgloss.Color("236"))

	statusLineSepStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("240"))

	statusLineKeyStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("8"))

	statusLineValueStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("15"))

	statusLineContextLowStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("2")) // green

	statusLineContextMedStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("3")) // yellow

	statusLineContextHighStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("1")) // red
)

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
