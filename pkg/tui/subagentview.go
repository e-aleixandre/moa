package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ealeixandre/moa/pkg/bus"
)

// subagentTranscript holds the live/completed sub-conversation of a single
// subagent job, built by applying bus.SubagentEvent.Inner events.
type subagentTranscript struct {
	jobID      string
	task       string
	model      string
	status     string // "running", "completed", "failed", "cancelled"
	async      bool
	blocks     []messageBlock
	streamText string // current streaming assistant text (not yet materialized)
}

// ensureSubagent returns the transcript for jobID, creating it if absent.
func (m *appModel) ensureSubagent(jobID string) *subagentTranscript {
	if m.s.subagents == nil {
		m.s.subagents = make(map[string]*subagentTranscript)
	}
	t, ok := m.s.subagents[jobID]
	if !ok {
		t = &subagentTranscript{jobID: jobID, status: "running"}
		m.s.subagents[jobID] = t
	}
	return t
}

// handleSubagentStarted records a new subagent job.
func (m *appModel) handleSubagentStarted(e bus.SubagentStarted) {
	t := m.ensureSubagent(e.JobID)
	t.task = e.Task
	t.model = e.Model
	t.async = e.Async
	t.status = "running"
}

// handleSubagentEnded finalizes a transcript's status.
func (m *appModel) handleSubagentEnded(e bus.SubagentEnded) {
	t := m.ensureSubagent(e.JobID)
	if e.Status != "" {
		t.status = e.Status
	} else {
		t.status = "completed"
	}
	t.streamText = ""
	if m.s.viewingSubagent == e.JobID {
		m.s.viewportDirty = true
	}
}

// applySubagentInner applies a single already-unwrapped bus event (the Inner
// field of bus.SubagentEvent) to jobID's transcript. This is a simplified
// block builder — it only handles the event kinds needed to render a
// readable transcript; thinking deltas and tool-call-arg streaming are
// ignored for simplicity.
func (m *appModel) applySubagentInner(jobID string, inner any) {
	t := m.ensureSubagent(jobID)

	switch e := inner.(type) {
	case bus.TextDelta:
		t.streamText += e.Delta

	case bus.MessageEnded:
		text := t.streamText
		if text == "" {
			text = e.FullText
		}
		if text != "" {
			t.blocks = append(t.blocks, messageBlock{Type: "assistant", Raw: text})
		}
		t.streamText = ""

	case bus.ToolExecStarted:
		t.blocks = append(t.blocks, messageBlock{
			Type:       "tool",
			ToolCallID: e.ToolCallID,
			ToolName:   e.ToolName,
			ToolArgs:   e.Args,
		})

	case bus.ToolExecUpdate:
		for i := len(t.blocks) - 1; i >= 0; i-- {
			b := &t.blocks[i]
			if b.Type == "tool" && b.ToolCallID == e.ToolCallID {
				b.ToolResult += e.Delta
				b.touch()
				break
			}
		}

	case bus.ToolExecEnded:
		for i := len(t.blocks) - 1; i >= 0; i-- {
			b := &t.blocks[i]
			if b.Type == "tool" && b.ToolCallID == e.ToolCallID {
				b.ToolDone = true
				b.IsError = e.IsError
				b.ToolResult = e.Result
				b.touch()
				break
			}
		}

	default:
		// ThinkingDelta, ToolCallStreaming, ToolCallDelta, etc — ignored.
	}

	if m.s.viewingSubagent == jobID {
		m.s.viewportDirty = true
	}
}

// --- Picker ---

// subagentPickerEntry is one row in the subagent picker.
type subagentPickerEntry struct {
	jobID  string
	task   string
	model  string
	status string
	async  bool
}

// subagentPicker is an inline picker listing currently-live subagents,
// opened with Ctrl+G.
type subagentPicker struct {
	active  bool
	entries []subagentPickerEntry
	cursor  int
}

// Open populates the picker from the given transcripts, keeping only the
// ones that are still live ("running" or "cancelling").
func (p *subagentPicker) Open(subagents map[string]*subagentTranscript) {
	p.entries = p.entries[:0]
	for jobID, t := range subagents {
		if t.status != "running" && t.status != "cancelling" {
			continue
		}
		p.entries = append(p.entries, subagentPickerEntry{
			jobID:  jobID,
			task:   t.task,
			model:  t.model,
			status: t.status,
			async:  t.async,
		})
	}
	sort.Slice(p.entries, func(i, j int) bool { return p.entries[i].jobID < p.entries[j].jobID })
	p.cursor = 0
	p.active = true
}

func (p *subagentPicker) Close() {
	p.active = false
}

func (p *subagentPicker) MoveUp() {
	if p.cursor > 0 {
		p.cursor--
	}
}

func (p *subagentPicker) MoveDown() {
	if p.cursor < len(p.entries)-1 {
		p.cursor++
	}
}

// Selected returns the job ID of the highlighted entry, or "" if none.
func (p *subagentPicker) Selected() string {
	if p.cursor < 0 || p.cursor >= len(p.entries) {
		return ""
	}
	return p.entries[p.cursor].jobID
}

// View renders the subagent picker list.
func (p subagentPicker) View(width int) string {
	if !p.active || len(p.entries) == 0 {
		return ""
	}

	var lines []string
	lines = append(lines, pickerHeaderStyle.Render("Live Subagents — ↑↓ navigate · enter view · esc close"))
	lines = append(lines, "")

	for i, e := range p.entries {
		cursor := "  "
		if i == p.cursor {
			cursor = "▸ "
		}
		task := e.task
		if task == "" {
			task = "(no task description)"
		}
		suffix := ""
		if e.model != "" {
			suffix += " · " + e.model
		}
		suffix += " · " + e.status

		text := fmt.Sprintf("%s%s%s", cursor, task, suffix)
		if i == p.cursor {
			lines = append(lines, pickerSelectedStyle.Render(text))
		} else {
			lines = append(lines, text)
		}
	}

	content := strings.Join(lines, "\n")
	innerWidth := width - 4
	if innerWidth < 30 {
		innerWidth = 30
	}
	return pickerBorderStyle.Width(innerWidth).Render(content)
}

// --- Key handling ---

// handleSubagentPickerKey handles keys while the subagent picker is active.
func (m appModel) handleSubagentPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.subagentPicker.Close()
		return m, nil

	case tea.KeyUp:
		m.subagentPicker.MoveUp()
	case tea.KeyDown:
		m.subagentPicker.MoveDown()

	case tea.KeyEnter:
		selected := m.subagentPicker.Selected()
		m.subagentPicker.Close()
		if selected != "" {
			m.s.viewingSubagent = selected
			m.updateViewport()
		}
		return m, nil

	case tea.KeyRunes:
		switch string(msg.Runes) {
		case "j":
			m.subagentPicker.MoveDown()
		case "k":
			m.subagentPicker.MoveUp()
		}
	}
	return m, nil
}

// hasLiveSubagents reports whether at least one subagent is currently running.
func (m *appModel) hasLiveSubagents() bool {
	for _, t := range m.s.subagents {
		if t.status == "running" || t.status == "cancelling" {
			return true
		}
	}
	return false
}

// handleCtrlG opens the live-subagent picker, closes the current subagent
// view, or reports that there is nothing to show.
func (m appModel) handleCtrlG() (tea.Model, tea.Cmd) {
	if m.s.viewingSubagent != "" {
		m.s.viewingSubagent = ""
		m.updateViewport()
		return m, nil
	}
	if !m.hasLiveSubagents() {
		m.status.SetText("no live subagents")
		return m, nil
	}
	m.subagentPicker.Open(m.s.subagents)
	return m, nil
}

// --- Render ---

// renderSubagentViewportContent renders the transcript currently being
// viewed (m.s.viewingSubagent), including a header and any in-flight
// streaming text.
func (m *appModel) renderSubagentViewportContent() string {
	t := m.s.subagents[m.s.viewingSubagent]
	if t == nil {
		return ""
	}

	task := t.task
	if task == "" {
		task = "(no task description)"
	}
	header := pickerHeaderStyle.Render(fmt.Sprintf("◂ Subagent: %s (%s) — Ctrl+G to return", task, t.status))

	parts := []string{header}
	for i := range t.blocks {
		if s := renderSingleBlockCached(&t.blocks[i], m.renderer, m.s.showThinking, m.s.expanded); s != "" {
			parts = append(parts, s)
		}
	}
	if t.streamText != "" {
		parts = append(parts, GetActiveLayout().RenderAssistantText(m.renderer.RenderMarkdown(t.streamText), m.width))
	}

	return strings.Join(parts, "\n\n")
}
