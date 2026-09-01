package core

import (
	"fmt"
	"sort"
	"strings"
)

// Known models with context window sizes and API details.
var knownModels = map[string]Model{
	// --- Anthropic ---
	"claude-fable-5-1": {
		ID: "claude-fable-5-1", Provider: "anthropic", API: "anthropic-messages",
		Name: "Claude Fable 5.1", MaxInput: 1_000_000, MaxOutput: 131072,
		// Same input/output as Fable 5; cache reads are a quarter of that
		// model's ($0.25 vs $1). Writes stay 1.25x / 2x of input.
		Pricing: &Pricing{Input: 10, Output: 50, CacheRead: 0.25, CacheWrite: 12.5, CacheWrite1h: 20},
	},
	"claude-fable-5": {
		ID: "claude-fable-5", Provider: "anthropic", API: "anthropic-messages",
		Name: "Claude Fable 5", MaxInput: 1_000_000, MaxOutput: 131072,
		// 90% prompt-cache discount on input applies (cache read ~= Input*0.1).
		// Cache writes: 1.25x input for the 5m window, 2x for the 1h window.
		Pricing: &Pricing{Input: 10, Output: 50, CacheRead: 1, CacheWrite: 12.5, CacheWrite1h: 20},
	},
	"claude-opus-5": {
		ID: "claude-opus-5", Provider: "anthropic", API: "anthropic-messages",
		Name: "Claude Opus 5", MaxInput: 1_000_000, MaxOutput: 131072,
		Pricing: &Pricing{Input: 5, Output: 25, CacheRead: 0.5, CacheWrite: 6.25, CacheWrite1h: 10},
	},
	"claude-opus-4-8": {
		ID: "claude-opus-4-8", Provider: "anthropic", API: "anthropic-messages",
		Name: "Claude Opus 4.8", MaxInput: 1_000_000, MaxOutput: 131072,
		Pricing: &Pricing{Input: 5, Output: 25, CacheRead: 0.5, CacheWrite: 6.25, CacheWrite1h: 10},
	},
	"claude-sonnet-5": {
		ID: "claude-sonnet-5", Provider: "anthropic", API: "anthropic-messages",
		Name: "Claude Sonnet 5", MaxInput: 1_000_000, MaxOutput: 131072,
		Pricing: &Pricing{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75, CacheWrite1h: 6},
	},
	"claude-haiku-4-5-20251001": {
		ID: "claude-haiku-4-5-20251001", Provider: "anthropic", API: "anthropic-messages",
		Name: "Claude Haiku 4.5", MaxInput: 200_000, MaxOutput: 65536,
		Pricing: &Pricing{Input: 1, Output: 5, CacheRead: 0.1, CacheWrite: 1.25, CacheWrite1h: 2},
	},

	// --- OpenAI ---
	"gpt-5.3-codex": {
		ID: "gpt-5.3-codex", Provider: "openai", API: "openai-chat",
		Name: "GPT-5.3 Codex", MaxInput: 400_000, MaxOutput: 16384,
		Pricing: &Pricing{Input: 1.75, Output: 14, CacheRead: 0.175},
	},
	"gpt-5.3-codex-spark": {
		ID: "gpt-5.3-codex-spark", Provider: "openai", API: "openai-chat",
		Name: "GPT-5.3 Codex Spark", MaxInput: 128_000, MaxOutput: 16384,
		Pricing: &Pricing{Input: 1.75, Output: 14, CacheRead: 0.175},
	},
	"gpt-5.2-codex": {
		ID: "gpt-5.2-codex", Provider: "openai", API: "openai-chat",
		Name: "GPT-5.2 Codex", MaxInput: 256_000, MaxOutput: 16384,
		Pricing: &Pricing{Input: 1.25, Output: 10, CacheRead: 0.125},
	},
	"gpt-5.6-sol": {
		ID: "gpt-5.6-sol", Provider: "openai", API: "openai-chat",
		Name: "GPT-5.6 Sol", MaxInput: 1_050_000, MaxOutput: 128_000,
		// Short-context (<=272K input) pricing shown here. Long-context
		// (>272K input) prompts are billed at 2x input and 1.5x output.
		Pricing: &Pricing{
			Input: 5, Output: 30, CacheRead: 0.5, CacheWrite: 6.25,
			Tiers: []PricingTier{
				{Threshold: 272_000, Input: 10, Output: 45, CacheRead: 1, CacheWrite: 12.5},
			},
		},
	},
	"gpt-5.6-terra": {
		ID: "gpt-5.6-terra", Provider: "openai", API: "openai-chat",
		Name: "GPT-5.6 Terra", MaxInput: 1_050_000, MaxOutput: 128_000,
		// Short-context (<=272K input) pricing shown here. Long-context
		// (>272K input) prompts are billed at 2x input and 1.5x output.
		Pricing: &Pricing{
			Input: 2, Output: 12, CacheRead: 0.2, CacheWrite: 2.5,
			Tiers: []PricingTier{
				{Threshold: 272_000, Input: 4, Output: 18, CacheRead: 0.4, CacheWrite: 5},
			},
		},
	},
	"gpt-5.6-luna": {
		ID: "gpt-5.6-luna", Provider: "openai", API: "openai-chat",
		Name: "GPT-5.6 Luna", MaxInput: 1_050_000, MaxOutput: 128_000,
		// Short-context (<=272K input) pricing shown here. Long-context
		// (>272K input) prompts are billed at 2x input and 1.5x output.
		Pricing: &Pricing{
			Input: 0.2, Output: 1.2, CacheRead: 0.02, CacheWrite: 0.25,
			Tiers: []PricingTier{
				{Threshold: 272_000, Input: 0.4, Output: 1.8, CacheRead: 0.04, CacheWrite: 0.5},
			},
		},
	},
	"gpt-5.5": {
		ID: "gpt-5.5", Provider: "openai", API: "openai-chat",
		Name: "GPT-5.5", MaxInput: 1_050_000, MaxOutput: 128_000,
		// Short-context (<=200K input) pricing shown here. Long-context
		// (>200K input) prompts are billed at higher rates.
		Pricing: &Pricing{
			Input: 5, Output: 30, CacheRead: 0.5,
			Tiers: []PricingTier{
				{Threshold: 200_000, Input: 10, Output: 45, CacheRead: 1},
			},
		},
	},
	"gpt-5.4-mini": {
		ID: "gpt-5.4-mini", Provider: "openai", API: "openai-chat",
		Name: "GPT-5.4 Mini", MaxInput: 400_000, MaxOutput: 128_000,
		Pricing: &Pricing{Input: 0.75, Output: 4.5, CacheRead: 0.075},
	},

	// --- xAI ---
	// Grok 4.5 charges its long-context rate to the whole request once the
	// prompt reaches 200K tokens, including cached input.
	"grok-4.5": {
		ID: "grok-4.5", Provider: "xai", API: "xai-responses",
		Name: "Grok 4.5", MaxInput: 500_000,
		Pricing: &Pricing{
			Input: 2, Output: 6, CacheRead: 0.3,
			Tiers: []PricingTier{
				{Threshold: 200_000, Input: 4, Output: 12, CacheRead: 0.6},
			},
		},
	},
	// Grok 4.6 applies the same whole-request long-context rule as 4.5, but its
	// cached input costs more (0.5 vs 0.3), which dominates in long sessions.
	"grok-4.6": {
		ID: "grok-4.6", Provider: "xai", API: "xai-responses",
		Name: "Grok 4.6", MaxInput: 500_000,
		Pricing: &Pricing{
			Input: 2, Output: 6, CacheRead: 0.5,
			Tiers: []PricingTier{
				{Threshold: 200_000, Input: 4, Output: 12, CacheRead: 1},
			},
		},
	},
}

// Short aliases → full model ID.
var modelAliases = map[string]string{
	// Anthropic
	"sonnet": "claude-sonnet-5",
	"opus":   "claude-opus-5",
	"haiku":  "claude-haiku-4-5-20251001",
	"fable":  "claude-fable-5-1",
	// OpenAI
	"codex":       "gpt-5.3-codex",
	"codex-spark": "gpt-5.3-codex-spark",
	"codex-5.2":   "gpt-5.2-codex",
	"sol":         "gpt-5.6-sol",
	"terra":       "gpt-5.6-terra",
	"luna":        "gpt-5.6-luna",
	"gpt-5.6":     "gpt-5.6-sol",
	"gpt5":        "gpt-5.5",
	"gpt5-mini":   "gpt-5.4-mini",
	"gpt5.5":      "gpt-5.5",
	// xAI
	"grok": "grok-4.6",
	// The subscription backend names the same models "-build"; the API backend
	// omits the suffix. Same model, same pricing, so both spellings resolve to
	// one entry and cost/identity work whichever backend answered.
	"grok-4.6-build": "grok-4.6",
	"grok-4.5-build": "grok-4.5",
}

// ResolveModel resolves a model specifier to a fully-populated Model.
//
// Accepted formats:
//   - "sonnet"                     → alias lookup
//   - "claude-sonnet-4-6"   → direct registry lookup
//   - "anthropic/claude-sonnet-4"  → provider prefix (strips prefix, looks up rest)
//   - "openai/gpt-5.3-codex"      → provider prefix
//
// For unknown models, returns a Model with MaxInput=0 and ok=false.
//
// When a "provider/model" spec resolves to a known model whose registered
// Provider differs from the requested prefix (e.g. "openai/sonnet", where
// "sonnet" is an Anthropic model), ok is false — a provider/model mismatch on
// a *known* model name is treated as caller error, not as an intentional
// custom model. A provider/model pair that resolves to no known model at all
// is still accepted as a legitimate custom model spec (ok=false, but
// Provider/ID are populated verbatim so callers can still use it — pricing
// and context-window metadata will simply be absent). Use ValidateModelSpec
// to distinguish these two ok=false cases when that matters (e.g. to decide
// whether to fail fast at config-parse time).
func ResolveModel(spec string) (Model, bool) {
	m, ok, _ := resolveModelSpec(spec)
	return m, ok
}

// ValidateModelSpec reports whether spec can possibly be used to build a
// provider, without needing pricing/context metadata for it. It rejects two
// cases ResolveModel alone can't distinguish by its return value:
//   - a bare (no "provider/" prefix) spec that isn't a known alias, model
//     ID, or display name
//   - a "provider/model" spec whose model portion IS a known model but
//     registered under a *different* provider (almost certainly a typo,
//     e.g. "openai/sonnet" — sonnet is an Anthropic model)
//
// A "provider/model" spec whose model portion is simply absent from the
// registry is accepted (nil error): it's treated as a legitimate custom
// model, just without pricing/context-window metadata.
func ValidateModelSpec(spec string) error {
	_, ok, mismatch := resolveModelSpec(spec)
	if ok {
		return nil
	}
	if mismatch {
		return fmt.Errorf("model %q: provider/model mismatch (that model is registered under a different provider)", spec)
	}
	if strings.IndexByte(spec, '/') > 0 {
		// Explicit provider + unknown model ID: accepted as custom.
		return nil
	}
	return fmt.Errorf("unknown model %q (use \"<provider>/<model-id>\" for a custom model)", spec)
}

// resolveModelSpec is the shared implementation behind ResolveModel and
// ValidateModelSpec. mismatch is true only when spec had an explicit
// "provider/" prefix whose model portion matched a *known* model registered
// under a different provider.
func resolveModelSpec(spec string) (m Model, ok bool, mismatch bool) {
	// Models write these names themselves ("ask Sol to review this"), so a spec
	// arrives capitalized or padded as often as not. Every alias, known ID and
	// provider name in the registry is lowercase, so folding case here only
	// makes specs resolve that would otherwise fail — it cannot change an
	// existing match. Custom "provider/model" IDs are NOT folded below: those
	// reach a real provider API, where case can be significant.
	spec = strings.TrimSpace(spec)
	lower := strings.ToLower(spec)

	// Check alias first.
	if full, ok := modelAliases[lower]; ok {
		if m, ok2 := knownModels[full]; ok2 {
			return m, true, false
		}
	}

	// Direct lookup.
	if m, ok := knownModels[lower]; ok {
		return m, true, false
	}

	// Fallback: match by display Name (handles legacy session data
	// that stored "Claude Sonnet 4.6" instead of "claude-sonnet-4-6").
	for _, m := range knownModels {
		if strings.EqualFold(m.Name, spec) {
			return m, true, false
		}
	}

	// Try provider/model format.
	if idx := strings.IndexByte(spec, '/'); idx > 0 {
		provider := strings.ToLower(spec[:idx])
		modelID := strings.TrimSpace(spec[idx+1:])
		// Only the lookup is case-folded; modelID keeps its original case so an
		// unknown one can still be passed through to its provider verbatim.
		lowerID := strings.ToLower(modelID)

		// Alias after stripping provider.
		if full, ok := modelAliases[lowerID]; ok {
			if m, ok2 := knownModels[full]; ok2 {
				if m.Provider != provider {
					// Explicit provider mismatches the provider of the known
					// model the alias resolves to (e.g. "openai/sonnet"). This
					// is very likely a typo, not a real custom model — surface
					// it as unresolved rather than silently ignoring the
					// requested provider.
					return Model{ID: modelID, Provider: provider}, false, true
				}
				return m, true, false
			}
		}

		// Direct lookup of model ID part.
		if m, ok := knownModels[lowerID]; ok {
			if m.Provider != provider {
				return Model{ID: modelID, Provider: provider}, false, true
			}
			return m, true, false
		}

		// Unknown model with explicit provider: treated as a valid custom
		// model spec (provider/model), just without pricing/context metadata.
		return Model{ID: modelID, Provider: provider}, false, false
	}

	return Model{ID: spec}, false, false
}

// ListModels returns all unique known models, deduplicated by ID,
// sorted by provider then name. Each model also carries its shortest alias.
type ModelEntry struct {
	Model Model
	Alias string // shortest alias, empty if none
}

// modelDisplayOrder is the explicit order models appear in selectors (web
// dropdown). Grouped by provider, and within each provider roughly by
// generation and then by capability/power — not strict release date (e.g.
// Fable 5.1 before Fable 5 before Opus 5 before Sonnet 5, and GPT-5.6 Sol
// before Terra before Luna). Models not listed here fall to the end, in
// provider-then-name order.
var modelDisplayOrder = []string{
	// Anthropic
	"claude-fable-5-1",
	"claude-fable-5",
	"claude-opus-5",
	"claude-sonnet-5",
	"claude-opus-4-8",
	"claude-haiku-4-5-20251001",
	// OpenAI
	"gpt-5.6-sol",
	"gpt-5.6-terra",
	"gpt-5.6-luna",
	"gpt-5.5",
	"gpt-5.4-mini",
	"gpt-5.3-codex",
	"gpt-5.3-codex-spark",
	"gpt-5.2-codex",
	// xAI
	"grok-4.5",
}

func ListModels() []ModelEntry {
	// Deduplicate by canonical ID.
	byID := make(map[string]Model)
	for _, m := range knownModels {
		byID[m.ID] = m
	}

	// Build reverse alias map: canonical ID → shortest alias.
	aliases := make(map[string]string)
	for alias, canonicalID := range modelAliases {
		if existing, ok := aliases[canonicalID]; !ok || len(alias) < len(existing) {
			aliases[canonicalID] = alias
		}
	}

	// Explicit display rank: listed models keep modelDisplayOrder; the rest
	// sort after them (provider then name).
	rank := make(map[string]int, len(modelDisplayOrder))
	for i, id := range modelDisplayOrder {
		rank[id] = i
	}

	result := make([]ModelEntry, 0, len(byID))
	for _, m := range byID {
		result = append(result, ModelEntry{
			Model: m,
			Alias: aliases[m.ID],
		})
	}

	sort.Slice(result, func(i, j int) bool {
		ri, oki := rank[result[i].Model.ID]
		rj, okj := rank[result[j].Model.ID]
		if oki && okj {
			return ri < rj
		}
		if oki != okj {
			return oki // ranked models come before unranked
		}
		if result[i].Model.Provider != result[j].Model.Provider {
			return result[i].Model.Provider < result[j].Model.Provider
		}
		return result[i].Model.Name < result[j].Model.Name
	})

	return result
}

// ModelAliases returns every alias that resolves to a known model, in the
// curated display order. Callers that need to *tell* a model which names are
// valid must derive the list from here rather than writing one by hand: a
// hand-kept copy silently drifts from modelAliases as models come and go.
func ModelAliases() []string {
	seen := make(map[string]bool, len(modelAliases))
	var aliases []string
	for _, entry := range ListModels() {
		if entry.Alias != "" && !seen[entry.Alias] {
			seen[entry.Alias] = true
			aliases = append(aliases, entry.Alias)
		}
	}
	return aliases
}

// AllowedModelAliases returns the aliases of the models whose IDs appear in
// allowedIDs, in the same curated order as ModelAliases.
//
// An empty (or nil) allowedIDs means "no restriction" and yields every alias:
// the allowlist is opt-in, so an unset one must never shrink what callers may
// offer. IDs matching no known model are skipped rather than reported — the
// registry changes over time and a stale saved entry must not invalidate the
// rest of the list.
func AllowedModelAliases(allowedIDs []string) []string {
	if len(allowedIDs) == 0 {
		return ModelAliases()
	}
	allowed := make(map[string]bool, len(allowedIDs))
	for _, id := range allowedIDs {
		allowed[id] = true
	}
	seen := make(map[string]bool, len(allowedIDs))
	var aliases []string
	for _, entry := range ListModels() {
		if entry.Alias == "" || seen[entry.Alias] || !allowed[entry.Model.ID] {
			continue
		}
		seen[entry.Alias] = true
		aliases = append(aliases, entry.Alias)
	}
	return aliases
}

// IsModelAllowed reports whether model may be used under allowedIDs. An empty
// list means unrestricted, so existing installs keep working untouched.
func IsModelAllowed(model Model, allowedIDs []string) bool {
	if len(allowedIDs) == 0 {
		return true
	}
	for _, id := range allowedIDs {
		if id == model.ID {
			return true
		}
	}
	return false
}

// SuggestModelAlias returns the alias a misspelled spec most likely meant, or
// "" when nothing is close enough. It exists so an unknown-model error can
// teach the correct name instead of only rejecting the wrong one: agents write
// these names from memory and a near miss ("sonet", "grok/4") is far more
// common than an unknown model.
//
// Only unambiguous matches are offered: a suggestion that is wrong is worse
// than none, because it invites a second failed attempt.
func SuggestModelAlias(spec string) string {
	return SuggestAliasFrom(spec, ModelAliases())
}

// SuggestAliasFrom is SuggestModelAlias over an explicit alias set, so a
// caller that may only offer some models (a subagent allowlist) never teaches
// a name it would then refuse.
func SuggestAliasFrom(spec string, aliases []string) string {
	spec = strings.ToLower(strings.TrimSpace(spec))
	if idx := strings.IndexByte(spec, '/'); idx > 0 {
		// A "provider/model" typo is about the model portion; the provider is
		// checked separately by ValidateModelSpec.
		spec = strings.TrimSpace(spec[idx+1:])
	}
	if spec == "" {
		return ""
	}

	best, bestDistance, tied := "", 0, false
	for _, alias := range aliases {
		d := editDistance(spec, alias)
		// Tolerate more typing in longer names, but never so much that
		// unrelated short aliases match each other.
		budget := 1 + len(alias)/4
		if d > budget {
			continue
		}
		switch {
		case best == "" || d < bestDistance:
			best, bestDistance, tied = alias, d, false
		case d == bestDistance:
			tied = true
		}
	}
	if tied {
		return ""
	}
	return best
}

// editDistance is the Levenshtein distance between a and b, over bytes: model
// aliases are ASCII, so bytes and characters coincide.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, min(curr[j-1]+1, prev[j-1]+cost))
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}
