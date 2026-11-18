package core

import (
	"fmt"
	"strings"
)

// DreamingDefaultModels says which two models the nightly dreaming uses when
// the user picked none: the cheap reader and the organizer, drawn from the
// providers that are connected right now. Someone who pays one provider must
// get a night that runs on it; with both, the pilot's pairing (cheap OpenAI
// reader, Anthropic organizer) is kept. ok is false when neither provider is
// connected: there is nothing to run the night on.
//
// A nil probe means "cannot tell" and counts every provider as connected, so a
// build without credential checks keeps the pilot's pairing.
func DreamingDefaultModels(available AuxiliaryModelAvailable) (reader, organizer string, ok bool) {
	has := func(provider string) bool { return available == nil || available(provider) }
	anthropic, openai := has("anthropic"), has("openai")
	switch {
	case anthropic && openai:
		return "luna", "sonnet", true
	case anthropic:
		return "claude-haiku-5-5", "sonnet", true
	case openai:
		return "luna", "sol", true
	}
	return "", "", false
}

// The two roles of the night, as the callers name them.
const (
	DreamingRoleReader    = "reader"
	DreamingRoleOrganizer = "organizer"
)

// DreamingFits says whether a model can run a role within the night's budget.
// Nil means every model does.
type DreamingFits func(role string, m Model) bool

// ResolveDreamingModels picks the night's two models. An empty setting is the
// default for the connected providers. A setting that names an unknown model,
// or one whose provider has no credential, or one that does not fit the
// night's budget, falls back to that default and says
// so in notes: the night must neither break nor change models in silence.
// ok is false when no model can run the night at all.
func ResolveDreamingModels(cfg MoaConfig, available AuxiliaryModelAvailable, fits DreamingFits) (reader, organizer Model, notes []string, ok bool) {
	defReader, defOrganizer, ok := DreamingDefaultModels(available)
	if !ok {
		return Model{}, Model{}, nil, false
	}
	pick := func(label, role, spec, def string) (Model, bool) {
		spec = strings.TrimSpace(spec)
		if spec != "" {
			m, found := ResolveModel(spec)
			switch {
			case !found || m.ID == "":
				notes = append(notes, fmt.Sprintf("%s: «%s» no es un modelo conocido; se usó el de por defecto.", label, spec))
			case available != nil && !available(m.Provider):
				notes = append(notes, fmt.Sprintf("%s: «%s» no tiene credenciales ahora; se usó el de por defecto.", label, spec))
			case fits != nil && !fits(role, m):
				notes = append(notes, fmt.Sprintf("%s: «%s» no cabe en el tope de la noche; se usó el de por defecto.", label, spec))
			default:
				return m, true
			}
		}
		m, found := ResolveModel(def)
		return m, found && m.ID != ""
	}
	var ok1, ok2 bool
	reader, ok1 = pick("Lectura", DreamingRoleReader, cfg.DreamingReaderModel, defReader)
	organizer, ok2 = pick("Organización", DreamingRoleOrganizer, cfg.DreamingOrganizerModel, defOrganizer)
	return reader, organizer, notes, ok1 && ok2
}
