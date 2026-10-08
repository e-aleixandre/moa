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

// removeAPIKey deletes the key stored next to the plan sign-in. A primary API
// key (no plan sign-in) is not removable here, as before.
func (pc *providerCredentials) removeAPIKey(w http.ResponseWriter, r *http.Request, provider string) {
	var body struct {
		ExpectedGeneration *string `json:"expected_generation"`
	}
	if !decodeProviderBody(w, r, provider, &body) {
		return
	}
	if provider != "anthropic" || body.ExpectedGeneration == nil {
		writeProviderError(w, http.StatusBadRequest, provider, "invalid_request")
		return
	}
	if err := pc.store.RemoveAnthropicAPIKey(*body.ExpectedGeneration); err != nil {
		writeProviderFailure(w, provider, "api_key", err)
		return
	}
	writeJSON(w, http.StatusOK, pc.row(pc.store.ProviderStatus(provider), true, true))
}
