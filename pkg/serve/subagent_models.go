package serve

import (
	"encoding/json"
	"net/http"

	"github.com/e-aleixandre/moa/pkg/core"
)

// subagentModelPolicy is the wire shape of the global subagent allowlist.
// An empty list means "every model is allowed": the restriction is opt-in, so
// the absence of a policy can never be read as "nothing is allowed".
type subagentModelPolicy struct {
	AllowedModels []string `json:"allowed_models"`
}

func handleSubagentModels(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, currentSubagentModelPolicy(core.LoadGlobalConfig().SubagentAllowedModels))
		case http.MethodPatch:
			handleSubagentModelsPatch(w, r, mgr)
		}
	}
}

func handleSubagentModelsPatch(w http.ResponseWriter, r *http.Request, mgr *Manager) {
	limitBody(w, r, maxJSONBodySize)
	// The whole list is replaced rather than toggled model by model: the UI
	// edits it as one policy, and a per-model PATCH would let a burst of
	// checkbox clicks interleave into a list the user never asked for.
	var body struct {
		AllowedModels *[]string `json:"allowed_models"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if body.AllowedModels == nil {
		http.Error(w, "allowed_models required", http.StatusBadRequest)
		return
	}
	models := make([]string, 0, len(*body.AllowedModels))
	seen := make(map[string]bool, len(*body.AllowedModels))
	for _, id := range *body.AllowedModels {
		if !isKnownModelID(id) {
			http.Error(w, "unknown model_id: "+id, http.StatusBadRequest)
			return
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		models = append(models, id)
	}

	mgr.configMutationMu.Lock()
	defer mgr.configMutationMu.Unlock()

	var policy subagentModelPolicy
	if err := core.SaveGlobalConfig(func(cfg *core.MoaConfig) {
		cfg.SubagentAllowedModels = models
		policy = currentSubagentModelPolicy(cfg.SubagentAllowedModels)
	}); err != nil {
		http.Error(w, "failed to save subagent model policy", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, policy)
}

func currentSubagentModelPolicy(models []string) subagentModelPolicy {
	return subagentModelPolicy{AllowedModels: append([]string{}, models...)}
}
