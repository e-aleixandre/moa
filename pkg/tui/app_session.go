package tui

import (
	"fmt"
	"slices"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ealeixandre/moa/pkg/core"
)

// --- Pinned models ---

func (m appModel) savePinnedIfChanged(prev, curr map[string]bool) tea.Cmd {
	if pinnedSetsEqual(prev, curr) {
		return nil
	}
	return m.savePinnedModels(curr)
}

func (m appModel) savePinnedModels(ids map[string]bool) tea.Cmd {
	fn := m.onPinnedModelsChange
	if fn == nil {
		return nil
	}
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	slices.Sort(list)
	return func() tea.Msg {
		return pinnedModelsSavedMsg{err: fn(list)}
	}
}

func pinnedModelsToSet(ids []string) map[string]bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

func pinnedSetsEqual(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for id := range a {
		if !b[id] {
			return false
		}
	}
	return true
}

// --- Session persistence ---
// Note: Session saving is now handled by the bus persistence reactor.
// The TUI no longer calls saveSession() directly.

func newModelSwitchEvent(model core.Model) *pendingTimelineEvent {
	name := model.Name
	if name == "" {
		name = model.ID
	}
	text := fmt.Sprintf("✓ Switched to %s (%s)", name, model.Provider)
	return &pendingTimelineEvent{
		Text: text,
		Message: core.AgentMessage{
			Message: core.Message{
				Role:      "session_event",
				Content:   []core.Content{core.TextContent(text)},
				Timestamp: time.Now().Unix(),
			},
			Custom: map[string]any{
				"event":    "model_switch",
				"model_id": model.ID,
				"provider": model.Provider,
			},
		},
	}
}

func eventType(custom map[string]any) string {
	if custom == nil {
		return ""
	}
	event, _ := custom["event"].(string)
	return event
}

func firstTextContent(content []core.Content) string {
	for _, c := range content {
		if c.Type == "text" && c.Text != "" {
			return c.Text
		}
	}
	return ""
}
