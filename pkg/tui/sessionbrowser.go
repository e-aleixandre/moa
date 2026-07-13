package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/session"
)

const maxSessionPreviewMessages = 4

// recentSessionWindow bounds which sessions appear in the browser without an
// active filter: older ones are hidden to keep the list scannable but remain
// findable by typing a filter. Mirrors the web RECENT_DAYS window.
const recentSessionWindow = 7 * 24 * time.Hour

// isRecentSummary reports whether a session was updated within the recent
// window. A zero timestamp counts as recent so it never silently vanishes.
func isRecentSummary(sum session.Summary) bool {
	if sum.Updated.IsZero() {
		return true
	}
	return time.Since(sum.Updated) <= recentSessionWindow
}

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
	// confirmDelete holds the session ID pending a delete confirmation, or ""
	// when no delete is in flight. Set by a first delete keypress, cleared by
	// confirming (enter) or any other navigation/filter key.
	confirmDelete string
	// showArchived toggles whether archived ("closed") sessions appear in the
	// list. Off by default so closed sessions stay out of the way; ctrl+v
	// reveals them (rendered with an "[archived]" tag).
	showArchived bool
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
	b.confirmDelete = ""
	b.showArchived = false
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

// RemoveSummary drops a session from the local list after it was deleted on
// disk, so the browser reflects the change without a full reload.
func (b *sessionBrowser) RemoveSummary(id string) {
	b.confirmDelete = ""
	filtered := b.summaries[:0]
	for _, sum := range b.summaries {
		if sum.ID != id {
			filtered = append(filtered, sum)
		}
	}
	b.summaries = filtered
	b.rebuildMatches()
	if b.preview != nil && b.previewID == id {
		b.preview = nil
		b.previewID = ""
	}
}

// ToggleArchived flips whether archived sessions are shown, then rebuilds the
// visible list. Cursor/scroll reset because the visible set changes.
func (b *sessionBrowser) ToggleArchived() {
	b.confirmDelete = ""
	b.showArchived = !b.showArchived
	b.cursor = 0
	b.scroll = 0
	b.rebuildMatches()
}

// SetArchivedLocal updates a session's archived flag in the in-memory list
// after a successful store write, so the browser reflects the change without a
// full reload.
func (b *sessionBrowser) SetArchivedLocal(id string, archived bool) {
	b.confirmDelete = ""
	for i := range b.summaries {
		if b.summaries[i].ID == id {
			b.summaries[i].Archived = archived
			break
		}
	}
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
	b.confirmDelete = ""
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
	b.confirmDelete = ""
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
	b.confirmDelete = ""
	oldID := b.SelectedID()
	b.filter += text
	b.rebuildMatches()
	return oldID != b.SelectedID()
}

func (b *sessionBrowser) BackspaceFilter() bool {
	b.confirmDelete = ""
	if b.filter == "" {
		return false
	}
	oldID := b.SelectedID()
	b.filter = trimLastRune(b.filter) // byte-slice would split multibyte runes (ñ, accents)
	b.rebuildMatches()
	return oldID != b.SelectedID()
}

func (b *sessionBrowser) rebuildMatches() {
	needle := strings.ToLower(strings.TrimSpace(b.filter))
	b.matches = b.matches[:0]
	for i, sum := range b.summaries {
		if sum.Archived && !b.showArchived {
			continue
		}
		// With no filter, hide sessions older than the recent window to keep the
		// list scannable; typing a filter searches everything so old sessions
		// stay findable. (Parity with the web session lists.)
		if needle == "" && !isRecentSummary(sum) {
			continue
		}
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
			archTag := ""
			if sum.Archived {
				archTag = " [archived]"
			}
			titleWidth := innerWidth - len(cursor) - len(metaText) - len(archTag) - 2
			if titleWidth < 10 {
				titleWidth = 10
			}
			meta := pickerDimStyle.Render(metaText)
			title := truncateLine(sessionTitle(sum), titleWidth)
			line := fmt.Sprintf("%s%s  %s", cursor, meta, title)
			if archTag != "" {
				line += pickerDimStyle.Render(archTag)
			}
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
	if b.confirmDelete != "" {
		lines = append(lines, pickerSelectedStyle.Render("Delete this session? ctrl+d again to confirm · any other key to cancel"))
	} else {
		help := "↑↓ navigate · type to filter · enter open · ctrl+n new · ctrl+d delete · ctrl+a archive · ctrl+v archived · esc exit"
		if b.showArchived {
			help += " (showing archived)"
		}
		lines = append(lines, pickerDimStyle.Render(help))
	}

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
	case "goal":
		return "GOAL", firstTextContent(msg.Content)
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
	return truncateDisplay(s, width)
}
