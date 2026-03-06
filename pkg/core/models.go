package core

// Known models with context window sizes and API details.
var knownModels = map[string]Model{
	"claude-sonnet-4-20250514": {
		ID: "claude-sonnet-4-20250514", Provider: "anthropic", API: "anthropic-messages",
		Name: "Claude Sonnet 4", MaxInput: 200_000, MaxOutput: 16384,
	},
	"claude-sonnet-4-0": {
		ID: "claude-sonnet-4-20250514", Provider: "anthropic", API: "anthropic-messages",
		Name: "Claude Sonnet 4", MaxInput: 200_000, MaxOutput: 16384,
	},
	"claude-opus-4-20250514": {
		ID: "claude-opus-4-20250514", Provider: "anthropic", API: "anthropic-messages",
		Name: "Claude Opus 4", MaxInput: 200_000, MaxOutput: 16384,
	},
	"claude-opus-4-0": {
		ID: "claude-opus-4-20250514", Provider: "anthropic", API: "anthropic-messages",
		Name: "Claude Opus 4", MaxInput: 200_000, MaxOutput: 16384,
	},
	"claude-haiku-3.5-20241022": {
		ID: "claude-haiku-3.5-20241022", Provider: "anthropic", API: "anthropic-messages",
		Name: "Claude Haiku 3.5", MaxInput: 200_000, MaxOutput: 8192,
	},
}

// ResolveModel returns a fully-populated Model for a known model ID.
// For unknown models, returns a Model with MaxInput=0 (disables context
// management) and ok=false so callers can warn.
func ResolveModel(id string) (Model, bool) {
	if m, ok := knownModels[id]; ok {
		return m, true
	}
	return Model{ID: id}, false
}
