package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// planAction identifies a user choice from the plan action menu.
type planAction int

const (
	planActionNone planAction = iota
	planActionExecuteClean
	planActionExecuteKeep
	planActionReview
	planActionRefine
	planActionEditor

	// Post-review-rejected actions
	planActionAutoRefine     // send reviewer feedback to model
	planActionRefineWithOwn  // let user add own instructions + feedback
	planActionExecAnywayClean
	planActionExecAnywayKeep
	planActionStayInPlanMode
)

type planActionEntry struct {
	icon   string
	label  string
	action planAction
}

type planMenuVariant int

const (
	menuPostSubmit planMenuVariant = iota
	menuPostReviewApproved
	menuPostReviewRejected
)

var postSubmitActions = []planActionEntry{
	{"🚀", "Execute (clean context)", planActionExecuteClean},
	{"▶️", "Execute (keep context)", planActionExecuteKeep},
	{"🔍", "Send to review", planActionReview},
	{"✏️", "Keep refining", planActionRefine},
	{"📝", "Open in $EDITOR", planActionEditor},
}

var postReviewApprovedActions = []planActionEntry{
	{"🚀", "Execute (clean context)", planActionExecuteClean},
	{"▶️", "Execute (keep context)", planActionExecuteKeep},
	{"🔍", "Review again", planActionReview},
	{"✏️", "Keep refining", planActionRefine},
	{"📝", "Open in $EDITOR", planActionEditor},
}

var postReviewRejectedActions = []planActionEntry{
	{"🔄", "Send feedback to model (auto-refine)", planActionAutoRefine},
	{"✏️", "Add my own instructions too", planActionRefineWithOwn},
	{"🚀", "Execute anyway (clean context)", planActionExecAnywayClean},
	{"▶️", "Execute anyway (keep context)", planActionExecAnywayKeep},
	{"⏸", "Stay in plan mode", planActionStayInPlanMode},
}

// planMenu is an inline action menu shown after plan submission or review.
type planMenu struct {
	active  bool
	cursor  int
	variant planMenuVariant

	// Review config override (per-run). Empty = use defaults from config.
	reviewModel    string // display name, e.g. "Claude Sonnet 4"
	reviewModelID  string // resolved model ID
	reviewThinking string // thinking level override
}

func (m *planMenu) OpenPostSubmit() {
	m.active = true
	m.cursor = 0
	m.variant = menuPostSubmit
}

func (m *planMenu) OpenPostReviewApproved() {
	m.active = true
	m.cursor = 0
	m.variant = menuPostReviewApproved
}

func (m *planMenu) OpenPostReviewRejected() {
	m.active = true
	m.cursor = 0
	m.variant = menuPostReviewRejected
}

func (m *planMenu) Close() {
	m.active = false
	m.cursor = 0
}

func (m *planMenu) actions() []planActionEntry {
	var base []planActionEntry
	switch m.variant {
	case menuPostReviewApproved:
		base = postReviewApprovedActions
	case menuPostReviewRejected:
		base = postReviewRejectedActions
	default:
		base = postSubmitActions
	}

	// Patch the review label with current model/thinking config.
	out := make([]planActionEntry, len(base))
	copy(out, base)
	for i := range out {
		if out[i].action == planActionReview {
			out[i].label = m.reviewLabel()
			break
		}
	}
	return out
}

func (m *planMenu) reviewLabel() string {
	label := "Send to review"
	if m.variant == menuPostReviewApproved {
		label = "Review again"
	}
	if m.reviewModel != "" || m.reviewThinking != "" {
		model := m.reviewModel
		if model == "" {
			model = "default"
		}
		thinking := m.reviewThinking
		if thinking == "" {
			thinking = "default"
		}
		label += " (" + model + " · " + thinking + ")"
	}
	return label
}

func (m *planMenu) MoveUp() {
	if m.cursor > 0 {
		m.cursor--
	}
}

func (m *planMenu) MoveDown() {
	actions := m.actions()
	if m.cursor < len(actions)-1 {
		m.cursor++
	}
}

func (m *planMenu) Selected() planAction {
	actions := m.actions()
	if !m.active || m.cursor >= len(actions) {
		return planActionNone
	}
	return actions[m.cursor].action
}

func (m *planMenu) View(width int, theme Theme) string {
	if !m.active {
		return ""
	}

	header := lipgloss.NewStyle().Foreground(theme.Mauve).Bold(true)
	dim := lipgloss.NewStyle().Foreground(theme.Overlay0)
	sel := lipgloss.NewStyle().Foreground(theme.Text).Bold(true)

	var title string
	switch m.variant {
	case menuPostReviewApproved:
		title = "✅ Plan approved — choose an action:"
	case menuPostReviewRejected:
		title = "⚠️  Changes requested — choose an action:"
	default:
		title = "📋 Plan submitted — choose an action:"
	}

	var lines []string
	lines = append(lines, header.Render(title))
	lines = append(lines, "")

	actions := m.actions()
	for i, entry := range actions {
		cursor := "  "
		if i == m.cursor {
			cursor = "▸ "
		}
		text := cursor + entry.icon + " " + entry.label
		if i == m.cursor {
			lines = append(lines, sel.Render(text))
		} else {
			lines = append(lines, dim.Render(text))
		}
	}

	// Show hint when cursor is on a review action.
	if m.cursor < len(actions) {
		a := actions[m.cursor].action
		if a == planActionReview {
			lines = append(lines, "")
			lines = append(lines, dim.Render("  Tab: change review model/thinking"))
		}
	}

	content := strings.Join(lines, "\n")
	innerWidth := width - 4
	if innerWidth < 30 {
		innerWidth = 30
	}
	return pickerBorderStyle.Width(innerWidth).Render(content)
}
