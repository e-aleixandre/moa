package serve

import (
	"encoding/json"
	"net/http"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

// sessionFastState is the wire shape of a session's fast-mode setting.
// Supported and Note travel with it so the client renders the switch and its
// price from what this model actually offers, instead of a hardcoded table
// that drifts as the catalogue changes.
type sessionFastState struct {
	Fast      bool   `json:"fast"`
	Supported bool   `json:"supported"`
	Note      string `json:"note,omitempty"`
}

// handleSessionFast reads and sets whether a session buys premium speed.
//
// Unlike the model and thinking level, this is allowed while the agent is
// running. Those two rewrite the transcript when they change, so they have to
// wait for the turn to end; fast mode is only read when the next request is
// built. Waiting would also defeat the point — a long run already under way is
// exactly when speed is worth paying for.
func handleSessionFast(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		sess, ok := mgr.Get(r.PathValue("id"))
		if !ok {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		agent := sess.runtime.Context().Agent
		if agent == nil {
			http.Error(w, "session not ready", http.StatusConflict)
			return
		}
		model, _ := bus.QueryTyped[bus.GetModel, core.Model](sess.runtime.Bus, bus.GetModel{})

		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, fastState(agent.Fast(), model))
		case http.MethodPatch:
			limitBody(w, r, maxJSONBodySize)
			var body struct {
				Fast *bool `json:"fast"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Fast == nil {
				http.Error(w, "fast must be true or false", http.StatusBadRequest)
				return
			}
			// Turning it on for a model that can't serve it is not an error —
			// the provider drops the flag — but storing it would show the
			// session as fast when it is not.
			on := *body.Fast && core.SupportsFast(model.ID)
			agent.SetFast(on)
			writeJSON(w, http.StatusOK, fastState(on, model))
		}
	}
}

func fastState(on bool, model core.Model) sessionFastState {
	return sessionFastState{
		Fast:      on,
		Supported: core.SupportsFast(model.ID),
		Note:      core.FastNote(model.ID),
	}
}
