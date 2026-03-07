package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/session"
)

const maxSessionPreviewMessages = 4

type sessionBrowser struct {
	active     bool
	loading    bool
	filter     string
	summaries  []session.Summary
	matches    []int
	cursor     int
	scroll     int
	preview    *session.Session
	previewID  string
	loadErr    string
	previewErr string
}

func newSessionBrowser() sessionBrowser {
	return sessionBrowser{}
}

func (b *sessionBrowser) Open() {
	b.active = true
	b.loading = true
	b.filter = ""
	b.summaries = nil
	b.matches = nil
	b.cursor = 0
	b.scroll = 0
	b.preview = nil
	b.previewID = ""
	b.loadErr = ""
	b.previewErr = ""
}

func (b *sessionBrowser) Close() {
	b.active = false
	b.loading = false
	b.loadErr = ""
	b.previewErr = ""
}

func (b *sessionBrowser) SetSummaries(summaries []session.Summary) {
	b.loading = false
	b.loadErr = ""
	b.summaries = summaries
	b.rebuildMatches()
}

func (b *sessionBrowser) SetLoadError(err error) {
	b.loading = false
	if err == nil {
		b.loadErr = ""
		return
	}
	b.loadErr = err.Error()
}

func (b *sessionBrowser) SetPreview(sess *session.Session, err error) {
	if err != nil {
		b.preview = nil
		b.previewID = ""
		b.previewErr = err.Error()
		return
	}
	b.preview = sess
	b.previewErr = ""
	if sess != nil {
		b.previewID = sess.ID
	}
}

func (b *sessionBrowser) Selected() *session.Summary {
	if len(b.matches) == 0 || b.cursor < 0 || b.cursor >= len(b.matches) {
		return nil
	}
	return &b.summaries[b.matches[b.cursor]]
}

func (b *sessionBrowser) SelectedID() string {
	sel := b.Selected()
	if sel == nil {
		return ""
	}
	return sel.ID
}

func (b *sessionBrowser) MoveUp() bool {
	if b.cursor == 0 {
		return false
	}
	b.cursor--
	if b.cursor < b.scroll {
		b.scroll = b.cursor
	}
	b.previewErr = ""
	return true
}

func (b *sessionBrowser) MoveDown(maxVisible int) bool {
	if b.cursor >= len(b.matches)-1 {
		return false
	}
	b.cursor++
	if maxVisible < 1 {
		maxVisible = 1
	}
	if b.cursor >= b.scroll+maxVisible {
		b.scroll = b.cursor - maxVisible + 1
	}
	b.previewErr = ""
	return true
}

func (b *sessionBrowser) AppendFilter(text string) bool {
	oldID := b.SelectedID()
	b.filter += text
	b.rebuildMatches()
	return oldID != b.SelectedID()
}

func (b *sessionBrowser) BackspaceFilter() bool {
	if b.filter == "" {
		return false
	}
	oldID := b.SelectedID()
	b.filter = b.filter[:len(b.filter)-1]
	b.rebuildMatches()
	return oldID != b.SelectedID()
}

func (b *sessionBrowser) rebuildMatches() {
	needle := strings.ToLower(strings.TrimSpace(b.filter))
	b.matches = b.matches[:0]
	for i, sum := range b.summaries {
		title := sessionTitle(sum)
		if needle == "" || strings.Contains(strings.ToLower(title), needle) || strings.Contains(strings.ToLower(sum.ID), needle) {
			b.matches = append(b.matches, i)
		}
	}
	if len(b.matches) == 0 {
		b.cursor = 0
		b.scroll = 0
		b.preview = nil
		b.previewID = ""
		return
	}
	if b.cursor >= len(b.matches) {
		b.cursor = len(b.matches) - 1
	}
	if b.cursor < 0 {
		b.cursor = 0
	}
	if b.scroll > b.cursor {
		b.scroll = b.cursor
	}
	if b.scroll >= len(b.matches) {
		b.scroll = max(0, len(b.matches)-1)
	}
}

func (b sessionBrowser) View(width, height int) string {
	innerWidth := width - 4
	if innerWidth < 40 {
		innerWidth = 40
	}

	listRows := b.visibleListRows(height)
	var lines []string

	lines = append(lines, pickerHeaderStyle.Render("Sessions"))
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("Filter: %s█", b.filter))
	lines = append(lines, "")

	if b.loading {
		lines = append(lines, pickerDimStyle.Render("Loading sessions..."))
	} else if b.loadErr != "" {
		lines = append(lines, pickerDimStyle.Render("Could not load sessions: "+b.loadErr))
	} else if len(b.matches) == 0 {
		if len(b.summaries) == 0 {
			lines = append(lines, pickerDimStyle.Render("No saved sessions yet."))
		} else {
			lines = append(lines, pickerDimStyle.Render("No sessions match the current filter."))
		}
	} else {
		end := b.scroll + listRows
		if end > len(b.matches) {
			end = len(b.matches)
		}
		for row, idx := range b.matches[b.scroll:end] {
			sum := b.summaries[idx]
			cursor := "  "
			if b.scroll+row == b.cursor {
				cursor = "▸ "
			}
			metaText := sessionWhen(sum.Updated) + " · " + shortSessionID(sum.ID)
			titleWidth := innerWidth - len(cursor) - len(metaText) - 2
			if titleWidth < 10 {
				titleWidth = 10
			}
			meta := pickerDimStyle.Render(metaText)
			title := truncateLine(sessionTitle(sum), titleWidth)
			line := fmt.Sprintf("%s%s  %s", cursor, meta, title)
			if b.scroll+row == b.cursor {
				line = pickerSelectedStyle.Render(line)
			}
			lines = append(lines, line)
		}
	}

	lines = append(lines, "")
	lines = append(lines, pickerHeaderStyle.Render("Preview"))
	lines = append(lines, "")
	lines = append(lines, b.previewLines(innerWidth, height)...)
	lines = append(lines, "")
	lines = append(lines, pickerDimStyle.Render("↑↓ navigate · type to filter · enter open · ctrl+n new · esc exit"))

	return pickerBorderStyle.Width(innerWidth).Render(strings.Join(lines, "\n"))
}

func (b sessionBrowser) visibleListRows(height int) int {
	rows := 7
	if height > 0 {
		rows = max(4, min(8, height/3))
	}
	if len(b.matches) > 0 && rows > len(b.matches) {
		rows = len(b.matches)
	}
	if rows < 1 {
		rows = 1
	}
	return rows
}

func (b sessionBrowser) previewLines(width, height int) []string {
	previewRows := 8
	if height > 0 {
		previewRows = max(6, min(12, height/2))
	}
	if b.previewErr != "" {
		return []string{pickerDimStyle.Render("Could not load preview: " + b.previewErr)}
	}
	if b.preview == nil {
		if b.loading {
			return []string{pickerDimStyle.Render("Loading preview...")}
		}
		if len(b.matches) == 0 {
			return []string{pickerDimStyle.Render("Select a session to preview.")}
		}
		return []string{pickerDimStyle.Render("Loading preview...")}
	}

	msgs := b.preview.Messages
	if len(msgs) == 0 {
		return []string{pickerDimStyle.Render("This session is empty.")}
	}

	start := max(0, len(msgs)-maxSessionPreviewMessages)
	var lines []string
	for _, msg := range msgs[start:] {
		label, text := previewMessage(msg)
		if label == "" && text == "" {
			continue
		}
		lines = append(lines, label)
		for _, line := range strings.Split(text, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			lines = append(lines, "  "+truncateLine(line, max(20, width-2)))
		}
		lines = append(lines, "")
	}
	if len(lines) == 0 {
		return []string{pickerDimStyle.Render("No preview available.")}
	}
	if len(lines) > previewRows {
		lines = lines[len(lines)-previewRows:]
	}
	return lines
}

func previewMessage(msg core.AgentMessage) (string, string) {
	switch msg.Role {
	case "user":
		return "YOU", firstTextContent(msg.Content)
	case "assistant":
		if text := firstTextContent(msg.Content); text != "" {
			return "ASSISTANT", text
		}
		var tools []string
		for _, c := range msg.Content {
			if c.Type == "tool_call" && c.ToolName != "" {
				tools = append(tools, c.ToolName)
			}
		}
		if len(tools) > 0 {
			return "ASSISTANT", "Called tools: " + strings.Join(tools, ", ")
		}
	case "tool_result":
		text := firstTextContent(msg.Content)
		if text == "" {
			text = msg.ToolName
		}
		return "TOOL", text
	case "session_event":
		return "EVENT", firstTextContent(msg.Content)
	case "compaction_summary":
		return "EVENT", "conversation compacted"
	}
	return "", ""
}

func sessionTitle(sum session.Summary) string {
	if strings.TrimSpace(sum.Title) != "" {
		return sum.Title
	}
	return "Untitled session"
}

func sessionWhen(ts time.Time) string {
	now := time.Now()
	if sameDay(now, ts) {
		return "today " + ts.Format("15:04")
	}
	if sameDay(now.AddDate(0, 0, -1), ts) {
		return "yesterday " + ts.Format("15:04")
	}
	return ts.Format("2006-01-02 15:04")
}

func sameDay(a, b time.Time) bool {
	y1, m1, d1 := a.Date()
	y2, m2, d2 := b.Date()
	return y1 == y2 && m1 == m2 && d1 == d2
}

func shortSessionID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func truncateLine(s string, width int) string {
	if width <= 0 || len(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return s[:width-1] + "…"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
