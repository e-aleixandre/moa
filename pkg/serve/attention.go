package serve

import (
	"net/http"
	"strings"
)

// subscribeAttention attaches a session to the server-owned attention service.
// Its detach callback shares the lifecycle list used by other serve subscribers:
// Delete runs it before runtime shutdown, so queued events from a removed
// session cannot outlive the session in the global attention roster.
func (m *Manager) subscribeAttention(sess *ManagedSession) {
	if m.attention == nil {
		return
	}
	detach := m.attention.Attach(sess.runtime.Bus, sess.ID, attentionAlias(sess.Title), sess.Title)
	sess.pushUnsubs = append(sess.pushUnsubs, detach)
}

func attentionAlias(title string) string {
	words := strings.Fields(strings.TrimSpace(title))
	if len(words) == 0 {
		return "a session"
	}
	if len(words) > 4 {
		words = words[:4]
	}
	return strings.Join(words, " ")
}

// handleAttention exposes the current unresolved, cross-session attention
// items as read-only state. It never resolves an item: owner-authorized clients
// use the existing generic per-session actions for answers and permissions.
func handleAttention(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if m.attention == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "attention unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": m.attention.Status()})
	}
}
