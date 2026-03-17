package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/ealeixandre/moa/pkg/bus"
)

// askPrompt handles the UI for agent questions.
// Shows one question at a time with optional predefined choices,
// plus a free-text input. Enter moves forward, Shift+Tab goes back, Escape skips all.
type askPrompt struct {
	active    bool
	askID     string        // bus ask ID
	questions []bus.AskQuestion
	current   int      // index of the current question
	answers   []string // collected answers (one per question)
	cursor    int      // selected option index (len(options) = custom input)
	customBuf string   // free text input buffer
}

// ShowFromBus populates the prompt from a bus AskUserRequested event.
func (a *askPrompt) ShowFromBus(id string, questions []bus.AskQuestion) {
	if len(questions) == 0 {
		return
	}
	a.active = true
	a.askID = id
	a.questions = questions
	a.current = 0
	a.answers = make([]string, len(questions))
	a.cursor = 0
	a.customBuf = ""
}

func (a *askPrompt) question() bus.AskQuestion {
	return a.questions[a.current]
}

func (a *askPrompt) optionCount() int {
	return len(a.question().Options)
}

// isCustom returns true when the cursor is on the free-text input.
func (a *askPrompt) isCustom() bool {
	return a.cursor >= a.optionCount()
}

// Submit confirms the current question and moves to the next.
// Returns true when all questions are answered (caller should resolve via bus).
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

	if a.current >= len(a.questions) {
		// All done — caller resolves via bus.
		a.active = false
		return true
	}

	// Advance to next question — restore previous answer if going back and forth.
	a.cursor = 0
	a.customBuf = ""
	if a.answers[a.current] != "" {
		// Previously answered — pre-select.
		for i, opt := range a.question().Options {
			if opt == a.answers[a.current] {
				a.cursor = i
				break
			}
		}
	}
	return false
}

// CollectAnswers returns the answers. Only valid after Submit returns true.
func (a *askPrompt) CollectAnswers() []string {
	return a.answers
}

// Cancel closes the prompt. Caller should resolve via bus with empty/nil answers.
func (a *askPrompt) Cancel() {
	a.active = false
}

// Back moves to the previous question.
func (a *askPrompt) Back() {
	if !a.active || a.current == 0 {
		return
	}
	a.current--
	a.cursor = 0
	a.customBuf = ""
}

// CursorUp moves the option cursor up.
func (a *askPrompt) CursorUp() {
	if a.cursor > 0 {
		a.cursor--
	}
}

// CursorDown moves the option cursor down.
func (a *askPrompt) CursorDown() {
	maxIdx := a.optionCount() // options + custom input
	if a.cursor < maxIdx {
		a.cursor++
	}
}

// TypeRune adds a character to the free-text buffer.
func (a *askPrompt) TypeRune(r rune) {
	a.customBuf += string(r)
	a.cursor = a.optionCount() // move to custom input
}

// Backspace removes the last character from the free-text buffer.
func (a *askPrompt) Backspace() {
	if len(a.customBuf) > 0 {
		a.customBuf = a.customBuf[:len(a.customBuf)-1]
		if a.customBuf == "" && a.optionCount() > 0 {
			a.cursor = 0
		}
	}
}

// View renders the ask prompt.
func (a *askPrompt) View(width int, theme Theme) string {
	if !a.active || a.current >= len(a.questions) {
		return ""
	}

	q := a.question()
	dim := lipgloss.NewStyle().Foreground(theme.Overlay0)
	qStyle := lipgloss.NewStyle().Foreground(theme.Lavender).Bold(true)
	sel := lipgloss.NewStyle().Foreground(theme.Text).Bold(true)
	normal := lipgloss.NewStyle().Foreground(theme.Subtext1)
	num := lipgloss.NewStyle().Foreground(theme.Overlay1)
	body := lipgloss.NewStyle().Foreground(theme.Text)

	var lines []string

	// Progress indicator
	total := len(a.questions)
	if total > 1 {
		lines = append(lines, dim.Render(fmt.Sprintf("  Question %d/%d", a.current+1, total)))
	}

	// Question text
	lines = append(lines, qStyle.Render("  "+q.Text))
	lines = append(lines, "")

	// Options
	for i, opt := range q.Options {
		cursor := "  "
		if i == a.cursor {
			cursor = "▸ "
		}
		numStr := num.Render(fmt.Sprintf("%d.", i+1))
		var text string
		if i == a.cursor {
			text = sel.Render(opt)
		} else {
			text = normal.Render(opt)
		}
		lines = append(lines, fmt.Sprintf("%s%s %s", cursor, numStr, text))
	}

	// Free-text input option
	customCursor := "  "
	if a.isCustom() {
		customCursor = "▸ "
	}
	customLabel := "Type your answer"
	if a.customBuf != "" || a.isCustom() {
		customLabel = a.customBuf + "█"
	}
	if a.isCustom() {
		lines = append(lines, customCursor+body.Render(customLabel))
	} else {
		lines = append(lines, customCursor+dim.Render(customLabel))
	}

	lines = append(lines, "")
	hint := "Enter confirm · Shift+Tab back · Esc skip"
	lines = append(lines, dim.Render("  "+hint))

	content := strings.Join(lines, "\n")
	innerWidth := width - 4
	if innerWidth < 30 {
		innerWidth = 30
	}
	return pickerBorderStyle.Width(innerWidth).Render(content)
}
