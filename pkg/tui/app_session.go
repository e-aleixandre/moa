package tui

import (
	"fmt"
	"slices"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ealeixandre/moa/pkg/core"
)

// --- Pinned models ---

// savePinnedIfChanged only persists if the set actually changed.
func (m appModel) savePinnedIfChanged(prev, curr map[string]bool) tea.Cmd {
	if pinnedSetsEqual(prev, curr) {
		return nil
	}
	return m.savePinnedModels(curr)
}

// savePinnedModels runs the OnPinnedModelsChange callback in the background.
// Only fires if a callback is configured. Returns nil otherwise.
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

// pinnedModelsToSet converts a slice of model IDs to the map used internally.
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

// saveSession returns a Cmd that asynchronously saves the session to disk.
// Takes a snapshot of messages to avoid races with the BT goroutine.
// Returns nil if persistence is disabled.
func (m *appModel) saveSession(msgs []core.AgentMessage) tea.Cmd {
	if m.sessionStore == nil || m.session == nil {
		return nil
	}
	// Snapshot: copy session metadata + messages for the async goroutine.
	// The BT goroutine may modify m.session before the write completes.
	snapshot := *m.session
	snapshot.Messages = make([]core.AgentMessage, len(msgs))
	copy(snapshot.Messages, msgs)
	snapshot.CompactionEpoch = m.agent.CompactionEpoch()
	// Deep-copy metadata map to avoid races with model switches.
	if snapshot.Metadata != nil {
		meta := make(map[string]any, len(snapshot.Metadata))
		for k, v := range snapshot.Metadata {
			meta[k] = v
		}
		snapshot.Metadata = meta
	}
	// Persist plan mode state.
	if m.planMode != nil {
		pmState := m.planMode.SaveState()
		if snapshot.Metadata == nil {
			snapshot.Metadata = make(map[string]any)
		}
		for k, v := range pmState {
			snapshot.Metadata[k] = v
		}
	}
	// Persist task store state.
	if m.taskStore != nil {
		tsState := m.taskStore.SaveToMetadata()
		if snapshot.Metadata == nil {
			snapshot.Metadata = make(map[string]any)
		}
		for k, v := range tsState {
			snapshot.Metadata[k] = v
		}
	}

	store := m.sessionStore
	return func() tea.Msg {
		err := store.Save(&snapshot)
		return sessionSavedMsg{err: err}
	}
}

func (m *appModel) commitPendingTimelineEvent() error {
	if m.s.pendingTimeline == nil {
		return nil
	}
	if err := m.agent.AppendMessage(m.s.pendingTimeline.Message); err != nil {
		return err
	}
	m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: m.s.pendingTimeline.Text})
	m.s.pendingTimeline = nil
	return nil
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

