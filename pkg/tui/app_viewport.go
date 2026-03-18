package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/tasks"
)

// --- Viewport ---

// updateViewport re-renders conversation blocks into the viewport.
// Only durable content (blocks + streaming text). Ephemeral content
// (spinner, notices) is rendered outside the viewport in View().
// Also recalculates viewport dimensions from current terminal size.
// refreshTaskDisplay updates the status bar task segment and plan segment with current progress.
// refreshTaskDisplay is now in app.go (uses bus queries)

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
	m.s.viewportCacheDirty = true // viewport content/scroll changed → re-render on next View()
}

// resizeViewport recalculates viewport dimensions from terminal size and chrome heights.
func (m *appModel) resizeViewport() {
	if m.width == 0 || m.height == 0 {
		return
	}
	// Build chrome once — reused by View() and for height measurement.
	m.s.chromeCache = m.buildBottomChrome()
	m.s.chromeCacheDirty = false

	chromeH := lipgloss.Height(m.s.chromeCache) + 1 // +1 gap
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

// bottomChrome returns the cached bottom chrome string, rebuilding if dirty.
func (m *appModel) bottomChrome() string {
	if m.s.chromeCacheDirty {
		m.s.chromeCache = m.buildBottomChrome()
		m.s.chromeCacheDirty = false
	}
	return m.s.chromeCache
}

// buildBottomChrome renders all bottom chrome components into a single string.
func (m *appModel) buildBottomChrome() string {
	var parts []string

	l := GetActiveLayout()
	if sv := m.status.View(); sv != "" {
		parts = append(parts, sv)
	}
	if m.s.pendingStatus != "" {
		parts = append(parts, l.RenderLiveNotice(m.s.pendingStatus, m.width, ActiveTheme))
	}
	if m.s.pendingTimeline != nil {
		parts = append(parts, l.RenderLiveNotice(m.s.pendingTimeline.Text, m.width, ActiveTheme))
	}
	if m.s.asyncSubagents > 0 {
		label := fmt.Sprintf("⟳ %d subagent running", m.s.asyncSubagents)
		if m.s.asyncSubagents > 1 {
			label = fmt.Sprintf("⟳ %d subagents running", m.s.asyncSubagents)
		}
		parts = append(parts, l.RenderLiveNotice(label, m.width, ActiveTheme))
	}
	// Task widget.
	if m.runtime != nil {
		if taskList, err := bus.QueryTyped[bus.GetTasks, []tasks.Task](m.runtime.Bus, bus.GetTasks{}); err == nil && len(taskList) > 0 {
			if tv := m.taskWidget.View(taskList, m.taskWidgetMode, m.width); tv != "" {
				parts = append(parts, tv)
			}
		}
	}
	if len(m.s.queuedSteers) > 0 {
		parts = append(parts, m.renderQueuedSteers())
	}

	if m.permPrompt.active {
		if pv := m.permPrompt.View(m.width, ActiveTheme); pv != "" {
			parts = append(parts, pv)
		}
	} else if m.askPrompt.active {
		if av := m.askPrompt.View(m.width, ActiveTheme); av != "" {
			parts = append(parts, av)
		}
	} else if m.picker.active {
		if pv := m.picker.View(m.width); pv != "" {
			parts = append(parts, pv)
		}
	} else if m.thinkingPicker.active {
		if pv := m.thinkingPicker.View(m.width, ActiveTheme); pv != "" {
			parts = append(parts, pv)
		}
	} else if m.planMenu.active {
		if pv := m.planMenu.View(m.width, ActiveTheme); pv != "" {
			parts = append(parts, pv)
		}
	} else if m.settingsMenu.active {
		if sv := m.settingsMenu.View(m.width, ActiveTheme); sv != "" {
			parts = append(parts, sv)
		}
	} else {
		if iv := m.input.View(); iv != "" {
			parts = append(parts, iv)
		}
	}
	if m.statusBar != nil {
		if tv := m.statusBar.View(m.width); tv != "" {
			parts = append(parts, tv)
		}
	}
	if pv := m.cmdPalette.View(m.width, ActiveTheme); pv != "" {
		parts = append(parts, pv)
	}
	if fv := m.filePicker.View(m.width, ActiveTheme); fv != "" {
		parts = append(parts, fv)
	}

	return strings.Join(parts, "\n")
}

// renderViewportContent renders blocks for the viewport (last N turns + streaming).
func (m *appModel) renderViewportContent() string {
	blocks := m.visibleBlocks()

	var parts []string
	for i := range blocks {
		if s := renderSingleBlockCached(&blocks[i], m.renderer, m.s.showThinking, m.s.expanded); s != "" {
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
// forceRepaint, waitForBusEvent, renderTick, cleanup, accumulateCost,
// refreshContextSegment, refreshTaskDisplay are now in app.go (bus-based)

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

// Cached styles for renderQueuedSteers — built once per theme.
var (
	steerTextStyle  = lipgloss.NewStyle()
	steerBadgeStyle = lipgloss.NewStyle()
	steerStylesInit bool
)

func initSteerStyles() {
	t := ActiveTheme
	steerTextStyle = lipgloss.NewStyle().Foreground(t.Overlay1)
	steerBadgeStyle = lipgloss.NewStyle().Foreground(t.Surface2).Background(t.Overlay0).
		PaddingLeft(1).PaddingRight(1)
	steerStylesInit = true
}

// renderQueuedSteers renders the queued steer messages shown above the input.
func (m appModel) renderQueuedSteers() string {
	if !steerStylesInit {
		initSteerStyles()
	}
	text := steerTextStyle
	badge := steerBadgeStyle

	n := len(m.s.queuedSteers)
	last := m.s.queuedSteers[n-1]

	// Truncate long messages to one line.
	if len(last) > 60 {
		last = last[:57] + "…"
	}

	tag := badge.Render("queued")
	if n > 1 {
		tag = badge.Render(fmt.Sprintf("queued ×%d", n))
	}
	return "  " + tag + " " + text.Render(last)
}
