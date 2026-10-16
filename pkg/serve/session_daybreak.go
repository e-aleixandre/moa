package serve

import (
	"encoding/json"
	"net/http"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

// sessionDaybreakState is the wire shape of a session's Daybreak setting.
// Supported is whether the session's current model would carry the program.
type sessionDaybreakState struct {
	Daybreak  bool `json:"daybreak"`
	Supported bool `json:"supported"`
}

func (s *ManagedSession) daybreakAvailability() core.DaybreakAvailability {
	if s == nil || s.daybreakCatalog == nil {
		return core.DaybreakAvailability{}
	}
	return s.daybreakCatalog()
}

// handleSessionDaybreak reads and sets whether a session asks for Daybreak.
// Like fast mode it is allowed while running: it is read when the next
// request is built, and the one in flight keeps the program it was sent with.
func handleSessionDaybreak(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		sess, ok := mgr.Get(r.PathValue("id"))
		if !ok {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		sess.lifecycle.RLock()
		defer sess.lifecycle.RUnlock()
		if sess.closing.Load() {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		agent := sess.runtime.Context().Agent
		if agent == nil {
			http.Error(w, "session not ready", http.StatusConflict)
			return
		}

		var on *bool
		if r.Method == http.MethodPatch {
			limitBody(w, r, maxJSONBodySize)
			var body struct {
				Daybreak *bool `json:"daybreak"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Daybreak == nil {
				http.Error(w, "daybreak must be true or false", http.StatusBadRequest)
				return
			}
			on = body.Daybreak
		}
		// The same lock as fast mode and model changes: the model checked here
		// is the one the change applies to.
		sess.fastConfigMu.Lock()
		defer sess.fastConfigMu.Unlock()
		model, _ := bus.QueryTyped[bus.GetModel, core.Model](sess.runtime.Bus, bus.GetModel{})
		availability := sess.daybreakAvailability()
		supported := availability.Sendable(model)
		if on == nil {
			writeJSON(w, http.StatusOK, sessionDaybreakState{Daybreak: agent.Daybreak(), Supported: supported})
			return
		}
		if *on && availability.Rejects(model) {
			name := model.Name
			if name == "" {
				name = model.ID
			}
			http.Error(w, name+" doesn't support Daybreak. Choose GPT-6 Sol", http.StatusConflict)
			return
		}
		agent.SetDaybreak(*on)
		state := *on
		sess.runtime.Bus.Publish(bus.ConfigChanged{
			SessionID:         sess.ID,
			Daybreak:          &state,
			DaybreakSupported: &supported,
		})
		writeJSON(w, http.StatusOK, sessionDaybreakState{Daybreak: state, Supported: supported})
	}
}
