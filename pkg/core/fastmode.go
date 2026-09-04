package core

import "strings"

// FastCostMultiplier returns the premium-tier token-price multiplier for a
// model. Anthropic's fast-mode documentation table lists Opus 5 / 4.8 at
// $10 input / $50 output versus $5 / $25 standard. GPT-6 Astra's documented
// fast rate differs from the older OpenAI GPT-5 generation.
func FastCostMultiplier(model Model) float64 {
	switch model.Provider {
	case "anthropic", "xai":
		return 2
	case "openai":
		if model.ID == "gpt-6-astra" {
			return 2
		}
		return 2.5
	default:
		return 1
	}
}

// SupportsFast reports whether a model can be served in fast mode.
//
// Fast mode is the same model at a premium speed and price, and each provider
// gates it differently:
//
//   - Anthropic serves it on Opus only; every other model rejects the `speed`
//     field outright ("does not support the `speed` parameter").
//   - OpenAI offers it on the GPT-5.4 generation and later.
//   - xAI accepts the priority tier across its catalogue.
//
// An unknown model is reported as unsupported: offering a switch that the API
// will reject is worse than not offering it.
func SupportsFast(modelID string) bool {
	m, ok := ResolveModel(modelID)
	if !ok {
		return false
	}
	return supportsFastModel(m)
}

func supportsFastModel(m Model) bool {
	switch m.Provider {
	case "anthropic":
		return strings.Contains(m.ID, "opus")
	case "openai":
		// Reasoning-era models only; the mini and codex variants price
		// differently and are not offered a tier.
		return strings.HasPrefix(m.ID, "gpt-5.4") ||
			strings.HasPrefix(m.ID, "gpt-5.5") ||
			strings.HasPrefix(m.ID, "gpt-5.6") ||
			strings.HasPrefix(m.ID, "gpt-6")
	case "xai":
		return true
	}
	return false
}

// FastNote describes what fast mode costs on a given model, for the UI to show
// at the moment of turning it on. The wording is per-provider because the
// trade-off genuinely differs: OpenAI bills it against the plan's credits,
// while Anthropic charges usage credits that sit outside the subscription.
func FastNote(modelID string) string {
	m, ok := ResolveModel(modelID)
	if !ok {
		return ""
	}
	// A model that can't serve fast mode has no price to quote: returning its
	// provider's wording would price an option this model doesn't offer.
	if !SupportsFast(m.ID) {
		return ""
	}
	switch m.Provider {
	case "anthropic":
		return "2.5× faster · billed as separate usage credits"
	case "openai":
		if m.ID == "gpt-6-astra" {
			return "Fast mode · 2× the token rate"
		}
		return "1.5× faster · burns credits 2.5×"
	case "xai":
		return "Priority queue · 2× the token rate"
	}
	return ""
}
