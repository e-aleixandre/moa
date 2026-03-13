package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/planmode"
)

// --- Viewport ---

// updateViewport re-renders conversation blocks into the viewport.
// Only durable content (blocks + streaming text). Ephemeral content
// (spinner, notices) is rendered outside the viewport in View().
// Also recalculates viewport dimensions from current terminal size.
// refreshTaskDisplay updates the status bar task segment and plan segment with current progress.
func (m *appModel) refreshTaskDisplay() {
	if m.taskStore == nil {
		return
	}
	done, total := m.taskStore.Progress()
	m.topBar.UpdateTasksSegment(done, total)
	if m.planMode != nil && m.planMode.Mode() == planmode.ModeExecuting {
		if total > 0 {
			m.topBar.UpdatePlanSegment(fmt.Sprintf("executing 📋 %d/%d", done, total))
		} else {
			m.topBar.UpdatePlanSegment("executing")
		}
	}
	m.s.viewportDirty = true
}

func (m *appModel) updateViewport() {
	// Check scroll position BEFORE resizing — resizing can change maxYOffset
	// and make AtBottom() return false even though the user was at the bottom.
	wasAtBottom := m.viewport.AtBottom() || m.viewport.TotalLineCount() == 0
	m.resizeViewport()
	content := m.renderViewportContent()
	m.viewport.SetContent(content)
	if wasAtBottom {
		m.viewport.GotoBottom()
	}
}

// resizeViewport recalculates viewport dimensions from terminal size and chrome heights.
func (m *appModel) resizeViewport() {
	if m.width == 0 || m.height == 0 {
		return
	}
	chromeH := m.computeChromeHeight()
	vpH := m.height - chromeH
	if vpH < 1 {
		vpH = 1
	}
	m.viewport.Width = m.width
	m.viewport.Height = vpH
	if m.viewport.PastBottom() {
		m.viewport.GotoBottom()
	}
}

// computeChromeHeight returns the total lines used by non-viewport chrome.
// Must match View()'s bottom chrome components exactly.
func (m *appModel) computeChromeHeight() int {
	h := 0

	l := GetActiveLayout()
	if sv := m.status.View(); sv != "" {
		h += lipgloss.Height(sv)
	}
	if m.s.pendingStatus != "" {
		h += lipgloss.Height(l.RenderLiveNotice(m.s.pendingStatus, m.width, ActiveTheme))
	}
	if m.s.pendingTimeline != nil {
		h += lipgloss.Height(l.RenderLiveNotice(m.s.pendingTimeline.Text, m.width, ActiveTheme))
	}
	if m.s.asyncSubagents > 0 {
		h++
	}
	// Task widget height.
	if m.taskStore != nil {
		taskList := m.taskStore.Tasks()
		widgetMode := m.taskStore.GetWidgetMode()
		if tv := m.taskWidget.View(taskList, widgetMode, m.width); tv != "" {
			h += lipgloss.Height(tv)
		}
	}
	if m.permPrompt.active {
		if pv := m.permPrompt.View(m.width, ActiveTheme); pv != "" {
			h += lipgloss.Height(pv)
		}
	} else if m.picker.active {
		if pv := m.picker.View(m.width); pv != "" {
			h += lipgloss.Height(pv)
		}
	} else if m.thinkingPicker.active {
		if pv := m.thinkingPicker.View(m.width, ActiveTheme); pv != "" {
			h += lipgloss.Height(pv)
		}
	} else if m.planMenu.active {
		if pv := m.planMenu.View(m.width, ActiveTheme); pv != "" {
			h += lipgloss.Height(pv)
		}
	} else {
		if iv := m.input.View(); iv != "" {
			h += lipgloss.Height(iv)
		}
	}
	if m.topBar != nil {
		if tv := m.topBar.View(m.width); tv != "" {
			h += lipgloss.Height(tv)
		}
	}
	if m.bottomBar != nil {
		if bv := m.bottomBar.View(m.width); bv != "" {
			h += lipgloss.Height(bv)
		}
	}
	if pv := m.cmdPalette.View(m.width, ActiveTheme); pv != "" {
		h += lipgloss.Height(pv)
	}

	h++ // gap between viewport and bottom chrome
	return h
}

// renderViewportContent renders blocks for the viewport (last N turns + streaming).
func (m *appModel) renderViewportContent() string {
	blocks := m.visibleBlocks()

	var parts []string
	for _, block := range blocks {
		if s := renderSingleBlockEx(block, m.renderer, m.s.showThinking, m.s.expanded); s != "" {
			parts = append(parts, s)
		}
	}

	// Append streaming content
	if m.s.thinkingText != "" && m.s.showThinking {
		parts = append(parts, GetActiveLayout().RenderThinking(m.s.thinkingText, m.width, ActiveTheme))
	}
	if m.s.streamCache != "" {
		parts = append(parts, GetActiveLayout().RenderAssistantText(m.s.streamCache, m.width))
	}

	return strings.Join(parts, "\n\n")
}

// visibleBlocks returns all blocks for the viewport. The viewport scrolls,
// so no turn-based limiting is needed.
func (m *appModel) visibleBlocks() []messageBlock {
	return m.s.blocks
}

const transcriptTurnLimit = 10

// renderTranscriptBlocks renders blocks for transcript mode.
// fullHistory=false shows last N turns, fullHistory=true shows everything.
// Always rendered expanded.
func (m *appModel) renderTranscriptBlocks(fullHistory bool) string {
	blocks := m.s.blocks
	if !fullHistory && len(blocks) > 0 {
		turns := 0
		start := 0
		for i := len(blocks) - 1; i >= 0; i-- {
			if blocks[i].Type == "user" || blocks[i].Type == "subagent" {
				turns++
				if turns > transcriptTurnLimit {
					break
				}
				start = i
			}
		}
		blocks = blocks[start:]
	}
	return renderBlocks(blocks, m.renderer, m.s.showThinking, true)
}

// recomputeInputEnabled sets input enabled/disabled based on current state.
// Used when exiting transcript mode to avoid unconditionally enabling input.
func (m *appModel) recomputeInputEnabled() {
	enabled := !m.s.running && !m.permPrompt.active && !m.picker.active && !m.sessionBrowser.active && !m.planMenu.active && !m.thinkingPicker.active
	m.input.SetEnabled(enabled)
}

// --- Helpers ---

// waitForEvent returns a Cmd that blocks until the next agent event.
// The run generation comes FROM the tagged event (stamped at production time),
// not captured at Cmd creation time.
// forceRepaint sends a synthetic WindowSizeMsg to force Bubble Tea to
// fully repaint. Fixes ghost lines when the view height shrinks
// (e.g. command palette closing).
func (m appModel) forceRepaint() tea.Cmd {
	w, h := m.width, m.height
	return func() tea.Msg {
		return tea.WindowSizeMsg{Width: w, Height: h}
	}
}

func (m appModel) waitForEvent() tea.Cmd {
	eventCh := m.eventCh
	quit := m.quit
	return func() tea.Msg {
		select {
		case tagged, ok := <-eventCh:
			if !ok {
				return agentDoneMsg{}
			}
			return agentEventMsg{Event: tagged.event, RunGen: tagged.gen}
		case <-quit:
			return agentDoneMsg{}
		}
	}
}

// renderTick returns a Cmd that fires after renderInterval (~60fps).
func renderTick() tea.Cmd {
	return tea.Tick(renderInterval, func(time.Time) tea.Msg {
		return renderTickMsg{}
	})
}

// cleanup releases resources. Idempotent — safe to call multiple times.
func (m *appModel) cleanup() {
	m.s.cleanupOnce.Do(func() {
		close(m.quit)
		if m.unsub != nil {
			m.unsub()
		}
		m.agent.Abort()
	})
}

// accumulateCost sums Usage from all new assistant messages added during the
// last run (msgs[runStartMsgCount:]) and adds the cost to the session total.
func (m *appModel) accumulateCost(msgs []core.AgentMessage) {
	if msgs == nil || m.agent == nil {
		return
	}
	model := m.agent.Model()
	if model.Pricing == nil {
		return
	}
	start := m.s.runStartMsgCount
	if start > len(msgs) {
		return
	}
	for _, msg := range msgs[start:] {
		if msg.Role == "assistant" && msg.Usage != nil {
			m.s.sessionCost += model.Pricing.Cost(*msg.Usage)
		}
	}
	m.topBar.UpdateCostSegment(m.s.sessionCost)
}

// refreshContextSegment recalculates the context usage percentage and updates
// the top bar segment. Called after agent runs and model switches.
func (m *appModel) refreshContextSegment() {
	if m.agent == nil {
		return
	}
	model := m.agent.Model()
	if model.MaxInput <= 0 {
		m.topBar.Remove(SegmentContext)
		return
	}
	msgs := m.agent.Messages()
	estimate := core.EstimateContextTokens(msgs, "", nil, m.agent.CompactionEpoch())
	pct := (estimate.Tokens * 100) / model.MaxInput
	m.topBar.UpdateContextSegment(pct)
}

// thinkingLevels defines the cycle order for Shift+Tab.
var thinkingLevels = []string{"off", "minimal", "low", "medium", "high"}

// cycleThinkingLevel advances to the next thinking level, wrapping at the end.
func cycleThinkingLevel(current string) string {
	for i, level := range thinkingLevels {
		if level == current {
			return thinkingLevels[(i+1)%len(thinkingLevels)]
		}
	}
	return "medium" // fallback
}

// parseSubagentNotification detects steer messages formatted as subagent
// completion notifications and extracts the components. Returns false for
// user-typed steer messages.
func parseSubagentNotification(text string) (task, status, result string, ok bool) {
	prefixes := map[string]string{
		"[subagent completed] ": "completed",
		"[subagent failed] ":    "failed",
		"[subagent cancelled] ": "cancelled",
	}
	for prefix, s := range prefixes {
		if strings.HasPrefix(text, prefix) {
			status = s
			rest := text[len(prefix):]
			// Extract task from "Job <id> finished.\nTask: <task>\n..."
			lines := strings.SplitN(rest, "\n", 3)
			if len(lines) >= 2 {
				taskLine := lines[1]
				if strings.HasPrefix(taskLine, "Task: ") {
					task = strings.TrimPrefix(taskLine, "Task: ")
				}
			}
			// Everything after the task line is the result
			if len(lines) >= 3 {
				result = strings.TrimSpace(lines[2])
				// Strip known prefixes
				for _, p := range []string{"Result (last 50 lines):\n", "Error: "} {
					if strings.HasPrefix(result, p) {
						result = strings.TrimSpace(result[len(p):])
						break
					}
				}
			}
			return task, status, result, true
		}
	}
	return "", "", "", false
}
