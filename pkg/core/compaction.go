package core

// CompactionSettings controls automatic context compaction.
type CompactionSettings struct {
	Enabled       bool `json:"enabled"`
	ReserveTokens int  `json:"reserve_tokens"` // keep free for model output + thinking
	KeepRecent    int  `json:"keep_recent"`    // tokens of recent context to keep verbatim
}

// DefaultCompactionSettings provides sensible defaults.
var DefaultCompactionSettings = CompactionSettings{
	Enabled:       true,
	ReserveTokens: 16384,
	KeepRecent:    20000,
}

// CompactionPayload is the typed result of a compaction event.
type CompactionPayload struct {
	Summary       string   `json:"summary"`
	TokensBefore  int      `json:"tokens_before"`
	TokensAfter   int      `json:"tokens_after"`
	ReadFiles     []string `json:"read_files,omitempty"`
	ModifiedFiles []string `json:"modified_files,omitempty"`
}

// ShouldCompact returns true if context tokens exceed the safe threshold.
// Returns false for disabled settings, zero/negative context windows, or
// degenerate settings where reserve >= window.
func ShouldCompact(contextTokens, contextWindow int, settings CompactionSettings) bool {
	if !settings.Enabled || contextWindow <= 0 {
		return false
	}
	effective := contextWindow - settings.ReserveTokens
	if effective <= 0 {
		return false
	}
	return contextTokens > effective
}
