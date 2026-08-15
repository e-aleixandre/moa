package core

import (
	"fmt"
	"strings"
)

// AuxiliaryModelAvailable reports whether normal completion credentials are
// available for a provider. Transcription-only credentials must not satisfy it.
type AuxiliaryModelAvailable func(provider string) bool

// ResolveAuxiliaryModel resolves an auto-title or session-brief model setting.
// Empty is equivalent to "auto". Auto deliberately considers only the two
// inexpensive completion models: Luna first, then Haiku. It never selects Grok
// merely because xAI credentials happen to be present.
func ResolveAuxiliaryModel(spec string, available AuxiliaryModelAvailable) (Model, bool, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || strings.EqualFold(spec, "auto") {
		if available != nil && available("openai") {
			model, _ := ResolveModel("luna")
			return model, true, nil
		}
		if available != nil && available("anthropic") {
			model, _ := ResolveModel("haiku")
			return model, true, nil
		}
		return Model{}, false, nil
	}
	if strings.EqualFold(spec, "off") {
		return Model{}, false, nil
	}
	if err := ValidateModelSpec(spec); err != nil {
		return Model{}, false, fmt.Errorf("auxiliary model: %w", err)
	}
	model, _ := ResolveModel(spec)
	// Explicit selection controls which provider receives the snippet, but does
	// not bypass the normal-completion credential requirement. In particular a
	// transcription-only OpenAI key must never activate Luna here.
	if available == nil || !available(model.Provider) {
		return Model{}, false, nil
	}
	return model, true, nil
}

// ValidateAuxiliaryModelSpec validates a config value without requiring
// credentials. It accepts auto, off, and every normal model spec.
func ValidateAuxiliaryModelSpec(spec string) error {
	spec = strings.TrimSpace(spec)
	if spec == "" || strings.EqualFold(spec, "auto") || strings.EqualFold(spec, "off") {
		return nil
	}
	if err := ValidateModelSpec(spec); err != nil {
		return fmt.Errorf("auxiliary model: %w", err)
	}
	return nil
}
