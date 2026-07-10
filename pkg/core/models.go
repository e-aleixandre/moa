package core

import (
	"fmt"
	"sort"
	"strings"
)

// Known models with context window sizes and API details.
var knownModels = map[string]Model{
	// --- Anthropic ---
	"claude-fable-5": {
		ID: "claude-fable-5", Provider: "anthropic", API: "anthropic-messages",
		Name: "Claude Fable 5", MaxInput: 1_000_000, MaxOutput: 131072,
		// 90% prompt-cache discount on input applies (cache read ~= Input*0.1).
		Pricing: &Pricing{Input: 10, Output: 50, CacheRead: 1, CacheWrite: 12.5},
	},
	"claude-opus-4-8": {
		ID: "claude-opus-4-8", Provider: "anthropic", API: "anthropic-messages",
		Name: "Claude Opus 4.8", MaxInput: 1_000_000, MaxOutput: 131072,
		Pricing: &Pricing{Input: 5, Output: 25, CacheRead: 0.5, CacheWrite: 6.25},
	},
	"claude-sonnet-5": {
		ID: "claude-sonnet-5", Provider: "anthropic", API: "anthropic-messages",
		Name: "Claude Sonnet 5", MaxInput: 1_000_000, MaxOutput: 131072,
		Pricing: &Pricing{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75},
	},
	"claude-haiku-4-5-20251001": {
		ID: "claude-haiku-4-5-20251001", Provider: "anthropic", API: "anthropic-messages",
		Name: "Claude Haiku 4.5", MaxInput: 200_000, MaxOutput: 65536,
		Pricing: &Pricing{Input: 1, Output: 5, CacheRead: 0.1, CacheWrite: 1.25},
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
		// (>272K input) prompts are billed at 2x input and 1.5x output
		// (Input: 10, Output: 45, CacheRead: 1, CacheWrite: 12.5).
		Pricing: &Pricing{Input: 5, Output: 30, CacheRead: 0.5, CacheWrite: 6.25},
	},
	"gpt-5.6-terra": {
		ID: "gpt-5.6-terra", Provider: "openai", API: "openai-chat",
		Name: "GPT-5.6 Terra", MaxInput: 1_050_000, MaxOutput: 128_000,
		// Short-context (<=272K input) pricing shown here. Long-context
		// (>272K input) prompts are billed at 2x input and 1.5x output
		// (Input: 5, Output: 22.5, CacheRead: 0.5, CacheWrite: 6.25).
		Pricing: &Pricing{Input: 2.5, Output: 15, CacheRead: 0.25, CacheWrite: 3.125},
	},
	"gpt-5.6-luna": {
		ID: "gpt-5.6-luna", Provider: "openai", API: "openai-chat",
		Name: "GPT-5.6 Luna", MaxInput: 1_050_000, MaxOutput: 128_000,
		// Short-context (<=272K input) pricing shown here. Long-context
		// (>272K input) prompts are billed at 2x input and 1.5x output
		// (Input: 2, Output: 9, CacheRead: 0.2, CacheWrite: 2.5).
		Pricing: &Pricing{Input: 1, Output: 6, CacheRead: 0.1, CacheWrite: 1.25},
	},
	"gpt-5.5": {
		ID: "gpt-5.5", Provider: "openai", API: "openai-chat",
		Name: "GPT-5.5", MaxInput: 1_050_000, MaxOutput: 128_000,
		// Short-context (<=200K input) pricing shown here. Long-context
		// (>200K input) prompts are billed at Input: 10, Output: 45, CacheRead: 1.
		Pricing: &Pricing{Input: 5, Output: 30, CacheRead: 0.5},
	},
	"gpt-5.4-mini": {
		ID: "gpt-5.4-mini", Provider: "openai", API: "openai-chat",
		Name: "GPT-5.4 Mini", MaxInput: 400_000, MaxOutput: 128_000,
		Pricing: &Pricing{Input: 0.75, Output: 4.5, CacheRead: 0.075},
	},
}

// Short aliases → full model ID.
var modelAliases = map[string]string{
	// Anthropic
	"sonnet": "claude-sonnet-5",
	"opus":   "claude-opus-4-8",
	"haiku":  "claude-haiku-4-5-20251001",
	"fable":  "claude-fable-5",
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
	// Check alias first.
	if full, ok := modelAliases[spec]; ok {
		if m, ok2 := knownModels[full]; ok2 {
			return m, true, false
		}
	}

	// Direct lookup.
	if m, ok := knownModels[spec]; ok {
		return m, true, false
	}

	// Fallback: match by display Name (handles legacy session data
	// that stored "Claude Sonnet 4.6" instead of "claude-sonnet-4-6").
	for _, m := range knownModels {
		if m.Name == spec {
			return m, true, false
		}
	}

	// Try provider/model format.
	if idx := strings.IndexByte(spec, '/'); idx > 0 {
		provider := spec[:idx]
		modelID := spec[idx+1:]

		// Alias after stripping provider.
		if full, ok := modelAliases[modelID]; ok {
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
		if m, ok := knownModels[modelID]; ok {
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

	result := make([]ModelEntry, 0, len(byID))
	for _, m := range byID {
		result = append(result, ModelEntry{
			Model: m,
			Alias: aliases[m.ID],
		})
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Model.Provider != result[j].Model.Provider {
			return result[i].Model.Provider < result[j].Model.Provider
		}
		return result[i].Model.Name < result[j].Model.Name
	})

	return result
}
