package tui

import (
	"fmt"
	"sort"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

// --- Pinned models ---

func (m appModel) savePinnedIfChanged(prev, curr map[string]bool) tea.Cmd {
	changes := pinnedModelChanges(prev, curr)
	if len(changes) == 0 {
		return nil
	}
	return m.savePinnedModelChanges(changes)
}

// PinnedModelChange is one ordered add/remove operation for the global pin set.
type PinnedModelChange struct {
	ID     string
	Pinned bool
}

func (m appModel) savePinnedModelChanges(changes []PinnedModelChange) tea.Cmd {
	fn := m.onPinnedModelsChange
	if fn == nil {
		return nil
	}
	return func() tea.Msg {
		return pinnedModelsSavedMsg{err: fn(changes)}
	}
}

func pinnedModelChanges(prev, curr map[string]bool) []PinnedModelChange {
	var changes []PinnedModelChange
	removed := make([]string, 0)
	for id := range prev {
		if !curr[id] {
			removed = append(removed, id)
		}
	}
	sort.Strings(removed)
	for _, id := range removed {
		changes = append(changes, PinnedModelChange{ID: id})
	}
	added := make([]string, 0)
	for id := range curr {
		if !prev[id] {
			added = append(added, id)
		}
	}
	sort.Strings(added)
	for _, id := range added {
		changes = append(changes, PinnedModelChange{ID: id, Pinned: true})
	}
	return changes
}

func (m *appModel) refreshPinnedModels() {
	if m.loadPinnedModels != nil {
		m.scopedModels = pinnedModelsToSet(m.loadPinnedModels())
	}
}

func (m *appModel) openModelPicker(purpose pickerPurpose) {
	m.refreshPinnedModels()
	model, _ := bus.QueryTyped[bus.GetModel, core.Model](m.runtime.Bus, bus.GetModel{})
	m.picker.Open(model.ID, m.scopedModels)
	m.pickerPurpose = purpose
}

func pinnedModelsToSet(ids []string) map[string]bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

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
