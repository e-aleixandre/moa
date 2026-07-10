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
	kind       string // "subagent" or "bash"
	blocks     []messageBlock
	streamText string // current streaming assistant text (not yet materialized)
	costUSD    float64
	tokens     int // input+output tokens, populated on end
}

// acceptSessionScopedAsyncEvent keeps background subagent work bound to the
// session that launched it. The TUI reuses one runtime while switching saved
// sessions, so a promoted job from the old conversation can otherwise keep
// streaming into (or start a notification run in) the newly opened one.
func (m *appModel) acceptSessionScopedAsyncEvent(seq uint64, event any) bool {
	if m.s.subagentEpoch == nil {
		m.s.subagentEpoch = make(map[string]uint64)
	}
	owner := func(jobID string) bool {
		epoch, ok := m.s.subagentEpoch[jobID]
		return ok && epoch == m.s.sessionEpoch
	}
	switch e := event.(type) {
	case bus.SubagentStarted:
		// A start published before the switch may still be waiting in Bubble
		// Tea's queue. It belongs to the old session, not whichever session is
		// active when the queue is eventually consumed.
		if seq <= m.s.sessionEventFloor {
			return false
		}
		m.s.subagentEpoch[e.JobID] = m.s.sessionEpoch
		return true
	case bus.SubagentEvent:
		return owner(e.JobID)
	case bus.SubagentEnded:
		return owner(e.JobID)
	case bus.SubagentCompleted:
		return owner(e.JobID)
	case bus.BashJobStarted:
		if seq <= m.s.sessionEventFloor {
			return false
		}
		m.s.subagentEpoch[e.JobID] = m.s.sessionEpoch
		return true
	case bus.BashJobOutput:
		return owner(e.JobID)
	case bus.BashJobEnded:
		return owner(e.JobID)
	default:
		return true
	}
}

func (m *appModel) refreshAsyncSubagentCount() {
	count := 0
	for jobID, t := range m.s.subagents {
		if m.s.subagentEpoch[jobID] == m.s.sessionEpoch && t.async && (t.status == "running" || t.status == "cancelling") {
			count++
		}
	}
	m.s.asyncSubagents = count
}

func (m *appModel) handleBashJobStarted(e bus.BashJobStarted) {
	t := m.ensureSubagent(e.JobID)
	t.task = e.Command
	t.model = "bash"
	t.kind = "bash"
	t.async = true
	t.status = "running"
	t.blocks = []messageBlock{{Type: "tool", ToolCallID: e.JobID, ToolName: "bash", ToolArgs: map[string]any{"command": e.Command, "cwd": e.CWD}}}
}

func (m *appModel) handleBashJobOutput(e bus.BashJobOutput) {
	t := m.ensureSubagent(e.JobID)
	t.kind = "bash"
	for i := len(t.blocks) - 1; i >= 0; i-- {
		if t.blocks[i].Type == "tool" && t.blocks[i].ToolCallID == e.JobID {
			t.blocks[i].ToolResult += e.Delta
			t.blocks[i].touch()
			break
		}
	}
	if m.s.viewingSubagent == e.JobID {
		m.s.viewportDirty = true
	}
}

func (m *appModel) handleBashJobEnded(e bus.BashJobEnded) {
	t := m.ensureSubagent(e.JobID)
	t.kind = "bash"
	t.status = e.Status
	for i := len(t.blocks) - 1; i >= 0; i-- {
		if t.blocks[i].Type == "tool" && t.blocks[i].ToolCallID == e.JobID {
			t.blocks[i].ToolDone = true
			t.blocks[i].IsError = e.Status != "completed"
			t.blocks[i].ToolResult = e.Output
			t.blocks[i].touch()
			break
		}
	}
	if m.s.viewingSubagent == e.JobID {
		m.s.viewportDirty = true
	}
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
	if e.Task != "" {
		t.task = e.Task
	}
	if e.Model != "" {
		t.model = e.Model
	}
	t.async = e.Async
	// Race: promoting a subagent right as it finishes can deliver this
	// SubagentStarted (Async:true, echoing the promotion) AFTER the
	// SubagentEnded that already marked it terminal. Never downgrade a
	// terminal status back to "running" — only a live/absent job may.
	if t.status != "completed" && t.status != "failed" && t.status != "cancelled" {
		t.status = "running"
	}
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
	t.costUSD = e.CostUSD
	if e.Usage != nil {
		t.tokens = e.Usage.Input + e.Usage.Output
	}
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
		// FullText is authoritative: streaming deltas are lossy (may be dropped
		// under backpressure), so prefer FullText whenever the server provides
		// it, falling back to the accumulated stream only if it's empty.
		text := e.FullText
		if text == "" {
			text = t.streamText
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

// handleCtrlB promotes a synchronous (blocking) subagent to run in the
// background, unblocking its parent turn. The candidate is the subagent
// currently being viewed, or — if none is open — the single sync/running
// subagent, if there's exactly one.
func (m appModel) handleCtrlB() (tea.Model, tea.Cmd) {
	jobID := m.s.viewingSubagent
	if jobID == "" {
		var candidates []string
		for id, t := range m.s.subagents {
			if !t.async && t.status == "running" {
				candidates = append(candidates, id)
			}
		}
		switch len(candidates) {
		case 0:
			m.status.SetText("no sync subagent to promote")
			return m, nil
		case 1:
			jobID = candidates[0]
		default:
			m.status.SetText("multiple sync subagents — open one with Ctrl+G first")
			return m, nil
		}
	}

	t := m.s.subagents[jobID]
	if t == nil {
		m.status.SetText("no sync subagent to promote")
		return m, nil
	}
	if t.async {
		m.status.SetText("subagent already running in background")
		return m, nil
	}
	if t.status != "running" {
		m.status.SetText("subagent already finished")
		return m, nil
	}

	if err := m.runtime.Bus.Execute(bus.PromoteSubagent{JobID: jobID}); err != nil {
		m.status.SetText("promote failed: " + err.Error())
		return m, nil
	}
	m.status.SetText("subagent promoted to background: " + jobID)
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
	statusText := t.status
	if t.costUSD > 0 || t.tokens > 0 {
		statusText += fmt.Sprintf(" · $%.4f · %d tok", t.costUSD, t.tokens)
	}
	kind := "Subagent"
	action := "Ctrl+B to promote"
	if t.kind == "bash" {
		kind = "Background Bash"
		action = "c to cancel"
	}
	header := pickerHeaderStyle.Render(fmt.Sprintf("◂ %s: %s (%s) — Ctrl+G to return · %s", kind, task, statusText, action))

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
