package serve

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/dreaming"
	"github.com/e-aleixandre/moa/pkg/owner"
)

// dreamingModelsPolicy is the wire shape of the nightly dreaming's two models.
//
// Reader and Organizer are "" when the user picked none: the night then uses
// Defaults, which depend on the providers connected when it runs. Defaults
// is what that would be right now, so a client can say it without guessing.
type dreamingModelsPolicy struct {
	Reader    string           `json:"reader"`
	Organizer string           `json:"organizer"`
	Defaults  dreamingDefaults `json:"defaults"`
	// Choices per role: the connected models whose reservation fits the
	// night's budget at that role's output cap. The two roles differ because
	// the reader reserves a larger output than the organizer.
	ReaderChoices    []compactModelChoice `json:"reader_choices"`
	OrganizerChoices []compactModelChoice `json:"organizer_choices"`
}

type dreamingDefaults struct {
	Reader    string `json:"reader"`
	Organizer string `json:"organizer"`
}

// dreamingSelectorBudget is the night's cap the settings are checked against:
// the default one. An owner with its own budget_usd is checked against that
// one when its night runs, with a note in the morning news. Tests override it.
var dreamingSelectorBudget = owner.DefaultDreamingBudgetUSD

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
	check := func(role string, p *string) (spec string, status int) {
		if p == nil {
			return "", 0
		}
		spec = strings.TrimSpace(*p)
		if spec == "" {
			return "", 0
		}
		m, ok := core.ResolveModel(spec)
		if !ok {
			return spec, http.StatusBadRequest
		}
		if !dreaming.RoleFits(role, m, dreamingSelectorBudget) {
			return spec, http.StatusUnprocessableEntity
		}
		return spec, 0
	}
	reader, st1 := check(core.DreamingRoleReader, body.Reader)
	organizer, st2 := check(core.DreamingRoleOrganizer, body.Organizer)
	if st1 == http.StatusBadRequest || st2 == http.StatusBadRequest {
		http.Error(w, "unknown model", http.StatusBadRequest)
		return
	}
	if st1 != 0 || st2 != 0 {
		http.Error(w, "model does not fit the night's budget", http.StatusUnprocessableEntity)
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
	all := m.compactModelPolicy("").Choices
	forRole := func(role string) []compactModelChoice {
		out := []compactModelChoice{}
		for _, c := range all {
			if model, ok := core.ResolveModel(c.Spec); ok && dreaming.RoleFits(role, model, dreamingSelectorBudget) {
				out = append(out, c)
			}
		}
		return out
	}
	return dreamingModelsPolicy{
		Reader:           strings.TrimSpace(cfg.DreamingReaderModel),
		Organizer:        strings.TrimSpace(cfg.DreamingOrganizerModel),
		Defaults:         dreamingDefaults{Reader: reader, Organizer: organizer},
		ReaderChoices:    forRole(core.DreamingRoleReader),
		OrganizerChoices: forRole(core.DreamingRoleOrganizer),
	}
}
