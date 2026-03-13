package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/ealeixandre/moa/pkg/tasks"
)

const taskWidgetMaxLines = 6

// taskWidget renders a compact task list above the input area.
type taskWidget struct{}

// View renders the task widget. Returns "" if nothing to show.
func (w taskWidget) View(taskList []tasks.Task, mode tasks.WidgetMode, width int) string {
	if len(taskList) == 0 || mode == tasks.WidgetHidden {
		return ""
	}

	t := ActiveTheme
	doneStyle := lipgloss.NewStyle().Foreground(t.Overlay0).Strikethrough(true)
	doneIcon := lipgloss.NewStyle().Foreground(t.Green).Render("☑ ")
	pendingIcon := lipgloss.NewStyle().Foreground(t.Overlay1).Render("☐ ")

	done := 0
	for _, task := range taskList {
		if task.Status == "done" {
			done++
		}
	}

	if mode == tasks.WidgetCurrent {
		// Single line: first non-done task + progress.
		for _, task := range taskList {
			if task.Status != "done" {
				line := pendingIcon + task.Title +
					lipgloss.NewStyle().Foreground(t.Overlay0).Render(fmt.Sprintf(" (%d/%d)", done, len(taskList)))
				return w.wrap(line, width)
			}
		}
		// All done.
		line := lipgloss.NewStyle().Foreground(t.Green).Render(fmt.Sprintf("✅ All %d tasks complete", len(taskList)))
		return w.wrap(line, width)
	}

	// "all" mode: show tasks with max height cap.
	var lines []string
	for _, task := range taskList {
		if task.Status == "done" {
			lines = append(lines, doneIcon+doneStyle.Render(task.Title))
		} else {
			lines = append(lines, pendingIcon+task.Title)
		}
	}

	if len(lines) > taskWidgetMaxLines {
		visible := lines[:taskWidgetMaxLines-1]
		overflow := len(lines) - (taskWidgetMaxLines - 1)
		visible = append(visible,
			lipgloss.NewStyle().Foreground(t.Overlay0).Render(fmt.Sprintf("  +%d more...", overflow)))
		lines = visible
	}

	return w.wrap(strings.Join(lines, "\n"), width)
}

func (w taskWidget) wrap(content string, width int) string {
	innerWidth := width - 4 // border + padding
	if innerWidth < 10 {
		innerWidth = 10
	}
	return pickerBorderStyle.Width(innerWidth).Render(content)
}
