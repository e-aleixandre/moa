package serve

import (
	"net/http"
)

// anthropicPlanKey is the Anthropic API key stored next to the plan sign-in.
// The plan stays the primary; the key takes over at a 5h or weekly limit.
type anthropicPlanKey struct {
	Generation string `json:"generation"`
	State      string `json:"state"`
}

// removeAPIKey deletes the key stored next to the plan sign-in. For Anthropic
// a primary API key (no plan sign-in) is not removable here, as before; for
// OpenAI and xAI a primary API key is removed when it is the only key.
func (pc *providerCredentials) removeAPIKey(w http.ResponseWriter, r *http.Request, provider string) {
	var body struct {
		ExpectedGeneration *string `json:"expected_generation"`
	}
	if !decodeProviderBody(w, r, provider, &body) {
		return
	}
	if body.ExpectedGeneration == nil {
		writeProviderError(w, http.StatusBadRequest, provider, "invalid_request")
		return
	}
	var err error
	switch provider {
	case "anthropic":
		err = pc.store.RemoveAnthropicAPIKey(*body.ExpectedGeneration)
	case "openai", "xai":
		err = pc.store.RemoveProviderAPIKey(provider, *body.ExpectedGeneration)
	default:
		writeProviderError(w, http.StatusBadRequest, provider, "invalid_request")
		return
	}
	if err != nil {
		writeProviderFailure(w, provider, "api_key", err)
		return
	}
	writeJSON(w, http.StatusOK, pc.row(pc.store.ProviderStatus(provider), true, true))
}
