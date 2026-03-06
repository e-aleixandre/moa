package core

import (
	"sort"
	"strings"
)

// Known models with context window sizes and API details.
var knownModels = map[string]Model{
	// --- Anthropic ---
	"claude-opus-4-6": {
		ID: "claude-opus-4-6", Provider: "anthropic", API: "anthropic-messages",
		Name: "Claude Opus 4.6", MaxInput: 200_000, MaxOutput: 16384,
	},
	"claude-sonnet-4-6": {
		ID: "claude-sonnet-4-6", Provider: "anthropic", API: "anthropic-messages",
		Name: "Claude Sonnet 4.6", MaxInput: 200_000, MaxOutput: 16384,
	},
	"claude-haiku-4-5": {
		ID: "claude-haiku-4-5", Provider: "anthropic", API: "anthropic-messages",
		Name: "Claude Haiku 4.5", MaxInput: 200_000, MaxOutput: 8192,
	},

	// --- OpenAI ---
	"gpt-5.3-codex": {
		ID: "gpt-5.3-codex", Provider: "openai", API: "openai-chat",
		Name: "GPT-5.3 Codex", MaxInput: 400_000, MaxOutput: 16384,
	},
	"gpt-5.3-codex-spark": {
		ID: "gpt-5.3-codex-spark", Provider: "openai", API: "openai-chat",
		Name: "GPT-5.3 Codex Spark", MaxInput: 128_000, MaxOutput: 16384,
	},
	"gpt-5.2-codex": {
		ID: "gpt-5.2-codex", Provider: "openai", API: "openai-chat",
		Name: "GPT-5.2 Codex", MaxInput: 256_000, MaxOutput: 16384,
	},
	"o3": {
		ID: "o3", Provider: "openai", API: "openai-chat",
		Name: "o3", MaxInput: 200_000, MaxOutput: 100_000,
	},
	"o4-mini": {
		ID: "o4-mini", Provider: "openai", API: "openai-chat",
		Name: "o4-mini", MaxInput: 200_000, MaxOutput: 100_000,
	},
}

// Short aliases → full model ID.
var modelAliases = map[string]string{
	// Anthropic
	"sonnet": "claude-sonnet-4-6",
	"opus":   "claude-opus-4-6",
	"haiku":  "claude-haiku-4-5",
	// OpenAI
	"codex":       "gpt-5.3-codex",
	"codex-spark": "gpt-5.3-codex-spark",
	"codex-5.2":   "gpt-5.2-codex",
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
func ResolveModel(spec string) (Model, bool) {
	// Check alias first.
	if full, ok := modelAliases[spec]; ok {
		if m, ok2 := knownModels[full]; ok2 {
			return m, true
		}
	}

	// Direct lookup.
	if m, ok := knownModels[spec]; ok {
		return m, true
	}

	// Try provider/model format.
	if idx := strings.IndexByte(spec, '/'); idx > 0 {
		provider := spec[:idx]
		modelID := spec[idx+1:]

		// Alias after stripping provider.
		if full, ok := modelAliases[modelID]; ok {
			if m, ok2 := knownModels[full]; ok2 {
				return m, true
			}
		}

		// Direct lookup of model ID part.
		if m, ok := knownModels[modelID]; ok {
			return m, true
		}

		// Unknown model with explicit provider.
		return Model{ID: modelID, Provider: provider}, false
	}

	return Model{ID: spec}, false
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
