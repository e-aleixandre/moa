package serve

import "net/http"

// sessionDaybreakState is the wire shape of a session's Daybreak setting.
type sessionDaybreakState struct {
	Daybreak  bool `json:"daybreak"`
	Supported bool `json:"supported"`
}

func handleSessionDaybreak(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not implemented", http.StatusNotImplemented)
	}
}
