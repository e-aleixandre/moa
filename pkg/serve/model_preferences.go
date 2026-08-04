package serve

import (
	"encoding/json"
	"net/http"

	"github.com/e-aleixandre/moa/pkg/core"
)

type modelPreferences struct {
	PinnedModels []string `json:"pinned_models"`
}

func handleModelPreferences(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, currentModelPreferences(core.LoadGlobalConfig().PinnedModels))
		case http.MethodPatch:
			handleModelPreferencePatch(w, r, mgr)
		}
	}
}

func handleModelPreferencePatch(w http.ResponseWriter, r *http.Request, mgr *Manager) {
	limitBody(w, r, maxJSONBodySize)
	var body struct {
		ModelID string `json:"model_id"`
		Pinned  *bool  `json:"pinned"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if body.ModelID == "" {
		http.Error(w, "model_id required", http.StatusBadRequest)
		return
	}
	if body.Pinned == nil {
		http.Error(w, "pinned required", http.StatusBadRequest)
		return
	}
	if *body.Pinned && !isKnownModelID(body.ModelID) {
		http.Error(w, "unknown model_id", http.StatusBadRequest)
		return
	}

	mgr.configMutationMu.Lock()
	defer mgr.configMutationMu.Unlock()

	var preferences modelPreferences
	if err := core.SaveGlobalConfig(func(cfg *core.MoaConfig) {
		cfg.PinnedModels = core.UpdatePinnedModels(cfg.PinnedModels, body.ModelID, *body.Pinned)
		preferences = currentModelPreferences(cfg.PinnedModels)
	}); err != nil {
		http.Error(w, "failed to save model preferences", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, preferences)
}

func isKnownModelID(id string) bool {
	for _, entry := range core.ListModels() {
		if entry.Model.ID == id {
			return true
		}
	}
	return false
}

func currentModelPreferences(models []string) modelPreferences {
	return modelPreferences{PinnedModels: append([]string{}, models...)}
}
