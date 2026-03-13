package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/ealeixandre/moa/pkg/askuser"
)

// askPrompt handles the UI for agent questions.
// Shows one question at a time with optional predefined choices,
// plus a free-text input. Enter moves forward, Shift+Tab goes back, Escape skips all.
type askPrompt struct {
	active    bool
	prompt    askuser.Prompt
	current   int      // index of the current question
	answers   []string // collected answers (one per question)
	cursor    int      // selected option index (len(options) = custom input)
	customBuf string   // free text input buffer
}

func (a *askPrompt) Show(p askuser.Prompt) {
	a.active = true
	a.prompt = p
	a.current = 0
	a.answers = make([]string, len(p.Questions))
	a.cursor = 0
	a.customBuf = ""
}

func (a *askPrompt) question() askuser.Question {
	return a.prompt.Questions[a.current]
}

func (a *askPrompt) optionCount() int {
	return len(a.question().Options)
}

// isCustom returns true when the cursor is on the free-text input.
func (a *askPrompt) isCustom() bool {
	return a.cursor >= a.optionCount()
}

// Submit confirms the current question and moves to the next.
// Returns true when all questions are answered.
func (a *askPrompt) Submit() bool {
	if !a.active {
		return false
	}

	var answer string
	if a.isCustom() {
		answer = strings.TrimSpace(a.customBuf)
		if answer == "" {
			return false // don't allow empty custom answer
		}
	} else {
		answer = a.question().Options[a.cursor]
	}

	a.answers[a.current] = answer
	a.current++

	if a.current >= len(a.prompt.Questions) {
		// All done — send answers back.
		a.prompt.Response <- a.answers
		a.active = false
		return true
	}

	// Advance to next question — restore previous answer if going back and forth.
	a.resetForCurrent()
	return false
}

// Back goes to the previous question.
func (a *askPrompt) Back() {
	if a.current > 0 {
		a.current--
		a.resetForCurrent()
	}
}

// Cancel skips all questions.
func (a *askPrompt) Cancel() {
	if !a.active {
		return
	}
	for i := range a.answers {
		if a.answers[i] == "" {
			a.answers[i] = "(skipped)"
		}
	}
	a.prompt.Response <- a.answers
	a.active = false
}

func (a *askPrompt) CursorUp() {
	if a.cursor > 0 {
		a.cursor--
		// When moving off custom to an option, clear custom buf.
		if !a.isCustom() {
			a.customBuf = ""
		}
	}
}

func (a *askPrompt) CursorDown() {
	max := a.optionCount() // custom is at index == optionCount
	if a.cursor < max {
		a.cursor++
	}
}

func (a *askPrompt) TypeRune(r rune) {
	// Typing always goes to custom input.
	if !a.isCustom() {
		a.cursor = a.optionCount()
	}
	a.customBuf += string(r)
}

func (a *askPrompt) Backspace() {
	if a.isCustom() && len(a.customBuf) > 0 {
		a.customBuf = a.customBuf[:len(a.customBuf)-1]
	}
}

// resetForCurrent resets cursor/buf for the current question,
// restoring previous answer if one exists.
func (a *askPrompt) resetForCurrent() {
	prev := a.answers[a.current]
	a.customBuf = ""
	a.cursor = 0

	if prev == "" {
		return
	}

	// If previous answer matches an option, select it.
	for i, opt := range a.question().Options {
		if opt == prev {
			a.cursor = i
			return
		}
	}
	// Otherwise it was a custom answer.
	a.cursor = a.optionCount()
	a.customBuf = prev
}

func (a *askPrompt) View(width int, theme Theme) string {
	if !a.active {
		return ""
	}

	accent := lipgloss.NewStyle().Foreground(theme.Blue).Bold(true)
	body := lipgloss.NewStyle().Foreground(theme.Text)
	dim := lipgloss.NewStyle().Foreground(theme.Overlay0)
	num := lipgloss.NewStyle().Foreground(theme.Overlay1)
	sel := lipgloss.NewStyle().Foreground(theme.Text).Bold(true)
	normal := lipgloss.NewStyle().Foreground(theme.Subtext1)

	q := a.question()
	var lines []string

	// Header with progress.
	header := q.Text
	if len(a.prompt.Questions) > 1 {
		header = fmt.Sprintf("[%d/%d] %s", a.current+1, len(a.prompt.Questions), q.Text)
	}
	lines = append(lines, accent.Render("  ? "+header))
	lines = append(lines, "")

	// Options.
	for i, opt := range q.Options {
		pointer := "  "
		if i == a.cursor {
			pointer = "▸ "
		}
		n := num.Render(fmt.Sprintf("%d.", i+1))
		var text string
		if i == a.cursor {
			text = sel.Render(opt)
		} else {
			text = normal.Render(opt)
		}
		lines = append(lines, fmt.Sprintf("%s%s %s", pointer, n, text))
	}

	// Custom input (always last).
	customPointer := "  "
	if a.isCustom() {
		customPointer = "▸ "
	}
	customLabel := "Write your own answer"
	if a.isCustom() {
		lines = append(lines, fmt.Sprintf("%s%s", customPointer, body.Render(fmt.Sprintf("> %s█", a.customBuf))))
	} else {
		lines = append(lines, fmt.Sprintf("%s%s", customPointer, dim.Render(customLabel)))
	}

	// Hints.
	lines = append(lines, "")
	var hints []string
	hints = append(hints, "Enter confirm")
	if a.current > 0 {
		hints = append(hints, "Shift+Tab back")
	}
	hints = append(hints, "Esc skip")
	lines = append(lines, dim.Render("  "+strings.Join(hints, " · ")))

	content := strings.Join(lines, "\n")
	innerWidth := width - 4
	if innerWidth < 30 {
		innerWidth = 30
	}
	return pickerBorderStyle.Width(innerWidth).Render(content)
}
