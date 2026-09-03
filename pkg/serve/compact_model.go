package serve

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/e-aleixandre/moa/pkg/core"
)

// compactModelPolicy is the wire shape of the global compaction summarizer.
//
// Model is either core.CompactModelSession ("the session's own model") or a
// model spec. Choices carries the models that can actually be picked: a
// selector listing a provider with no credential would offer a choice that
// silently degrades to the session's model on the next compaction.
type compactModelPolicy struct {
	Model   string               `json:"compact_model"`
	Choices []compactModelChoice `json:"choices"`
}

type compactModelChoice struct {
	Spec     string `json:"spec"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
}

func handleCompactModel(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		switch r.Method {
		case http.MethodGet:
			// GetCompactModel returns "" for "the session's model" because that
			// is what the resolver wants; the wire says the keyword, so a
			// client never has to infer meaning from an empty string.
			spec := core.GetCompactModel(core.LoadGlobalConfig())
			if spec == "" {
				spec = core.CompactModelSession
			}
			writeJSON(w, http.StatusOK, mgr.compactModelPolicy(spec))
		case http.MethodPatch:
			handleCompactModelPatch(w, r, mgr)
		}
	}
}

func handleCompactModelPatch(w http.ResponseWriter, r *http.Request, mgr *Manager) {
	limitBody(w, r, maxJSONBodySize)
	var body struct {
		Model *string `json:"compact_model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if body.Model == nil {
		http.Error(w, "compact_model required", http.StatusBadRequest)
		return
	}

	spec := strings.TrimSpace(*body.Model)
	// Empty and the keyword mean the same thing; store the keyword so the file
	// says what it means rather than leaving the reader to infer it.
	if spec == "" || strings.EqualFold(spec, core.CompactModelSession) {
		spec = core.CompactModelSession
	} else if _, ok := core.ResolveModel(spec); !ok {
		// Rejected here rather than accepted and quietly ignored later: an
		// unknown spec would only surface as a fallback notice hours later,
		// mid-compaction, when nobody is looking at Settings.
		http.Error(w, "unknown model", http.StatusBadRequest)
		return
	}

	mgr.configMutationMu.Lock()
	defer mgr.configMutationMu.Unlock()

	if err := core.SaveGlobalConfig(func(cfg *core.MoaConfig) {
		cfg.CompactModel = spec
	}); err != nil {
		http.Error(w, "failed to save compaction model", http.StatusInternalServerError)
		return
	}
	// Nothing to push into resident sessions: the summarizer resolver reads the
	// saved config on every compaction, so a change applies to open
	// conversations without rebuilding their agents.
	writeJSON(w, http.StatusOK, mgr.compactModelPolicy(spec))
}

// compactModelPolicy lists the models a client may offer. A model is offered
// only when its provider has a usable credential: a selector showing a model
// that cannot be reached would promise a choice that silently degrades to the
// session's model on the next compaction, hours later.
func (m *Manager) compactModelPolicy(spec string) compactModelPolicy {
	policy := compactModelPolicy{Model: spec, Choices: []compactModelChoice{}}
	if m.providerCredentialAvailable == nil {
		// Without a credential probe the server cannot tell which choices are
		// real, and guessing would be worse than offering none.
		return policy
	}
	usable := make(map[string]bool)
	for _, e := range core.ListModels() {
		provider := e.Model.Provider
		if provider == "" {
			continue
		}
		if _, known := usable[provider]; !known {
			usable[provider] = m.providerCredentialAvailable(provider)
		}
		if !usable[provider] {
			continue
		}
		// The alias is what a person recognises ('terra') and what reads best
		// in the config file; the id is the fallback when there is none.
		choice := compactModelChoice{Spec: e.Model.ID, Name: e.Model.Name, Provider: provider}
		if e.Alias != "" {
			choice.Spec = e.Alias
		}
		policy.Choices = append(policy.Choices, choice)
	}
	return policy
}
