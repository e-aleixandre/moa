package core

import (
	"strconv"
	"strings"
)

// openAIFastMultipliers lists the catalogue's OpenAI models that OpenAI's
// pricing page ("Fast mode" tab) offers in fast mode, with the multiplier over
// the Standard price. It is keyed by exact ID because the tier does not follow
// the naming: gpt-5.4-mini has it while gpt-5.4-nano and the -pro models do
// not. A catalogue model missing here is not offered fast mode.
var openAIFastMultipliers = map[string]float64{
	"gpt-6-astra":   2,
	"gpt-6-sol":     2,
	"gpt-6-luna":    2,
	"gpt-5.6-sol":   2,
	"gpt-5.6-terra": 2,
	"gpt-5.6-luna":  2,
	"gpt-5.5":       2.5,
	"gpt-5.4-mini":  2,
}

// FastCostMultiplier returns the premium-tier token-price multiplier for a
// model, or 1 when the model has no fast tier. Anthropic's fast-mode
// documentation table lists Opus 5 / 4.8 at $10 input / $50 output versus
// $5 / $25 standard.
func FastCostMultiplier(model Model) float64 {
	switch model.Provider {
	case "anthropic", "xai":
		return 2
	case "openai":
		if m, ok := openAIFastMultipliers[model.ID]; ok {
			return m
		}
	}
	return 1
}

// SupportsFast reports whether a model can be served in fast mode.
//
// Fast mode is the same model at a premium speed and price, and each provider
// gates it differently:
//
//   - Anthropic serves it on Opus only; every other model rejects the `speed`
//     field outright ("does not support the `speed` parameter").
//   - OpenAI offers it per model; see openAIFastMultipliers.
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
		_, ok := openAIFastMultipliers[m.ID]
		return ok
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
		rate := strconv.FormatFloat(FastCostMultiplier(m), 'f', -1, 64) + "×"
		if strings.HasPrefix(m.ID, "gpt-6-") {
			return "Fast mode · " + rate + " the token rate"
		}
		return "1.5× faster · burns credits " + rate
	case "xai":
		return "Priority queue · 2× the token rate"
	}
	return ""
}
