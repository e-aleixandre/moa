package serve

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/e-aleixandre/moa/pkg/core"
)

// dreamingModelsPolicy is the wire shape of the nightly dreaming's two models.
//
// Reader and Organizer are "" when the user picked none: the night then uses
// Defaults, which depend on the providers connected when it runs. Defaults
// is what that would be right now, so a client can say it without guessing.
type dreamingModelsPolicy struct {
	Reader    string               `json:"reader"`
	Organizer string               `json:"organizer"`
	Defaults  dreamingDefaults     `json:"defaults"`
	Choices   []compactModelChoice `json:"choices"`
}

type dreamingDefaults struct {
	Reader    string `json:"reader"`
	Organizer string `json:"organizer"`
}

func handleDreamingModels(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, mgr.dreamingModelsPolicy(core.LoadGlobalConfig()))
		case http.MethodPatch:
			handleDreamingModelsPatch(w, r, mgr)
		}
	}
}

func handleDreamingModelsPatch(w http.ResponseWriter, r *http.Request, mgr *Manager) {
	limitBody(w, r, maxJSONBodySize)
	var body struct {
		Reader    *string `json:"reader"`
		Organizer *string `json:"organizer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if body.Reader == nil && body.Organizer == nil {
		http.Error(w, "reader or organizer required", http.StatusBadRequest)
		return
	}
	// An unknown spec is refused now rather than accepted and quietly replaced
	// by the default on a night nobody is watching.
	check := func(p *string) (string, bool) {
		if p == nil {
			return "", true
		}
		spec := strings.TrimSpace(*p)
		if spec == "" {
			return "", true
		}
		_, ok := core.ResolveModel(spec)
		return spec, ok
	}
	reader, ok1 := check(body.Reader)
	organizer, ok2 := check(body.Organizer)
	if !ok1 || !ok2 {
		http.Error(w, "unknown model", http.StatusBadRequest)
		return
	}

	mgr.configMutationMu.Lock()
	defer mgr.configMutationMu.Unlock()

	var saved core.MoaConfig
	if err := core.SaveGlobalConfig(func(cfg *core.MoaConfig) {
		if body.Reader != nil {
			cfg.DreamingReaderModel = reader
		}
		if body.Organizer != nil {
			cfg.DreamingOrganizerModel = organizer
		}
		saved = *cfg
	}); err != nil {
		http.Error(w, "failed to save dreaming models", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, mgr.dreamingModelsPolicy(saved))
}

func (m *Manager) dreamingModelsPolicy(cfg core.MoaConfig) dreamingModelsPolicy {
	reader, organizer, _ := core.DreamingDefaultModels(m.providerCredentialAvailable)
	return dreamingModelsPolicy{
		Reader:    strings.TrimSpace(cfg.DreamingReaderModel),
		Organizer: strings.TrimSpace(cfg.DreamingOrganizerModel),
		Defaults:  dreamingDefaults{Reader: reader, Organizer: organizer},
		Choices:   m.compactModelPolicy("").Choices,
	}
}
