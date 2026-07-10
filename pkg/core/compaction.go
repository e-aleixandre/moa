package core

// CompactionSettings controls automatic context compaction.
type CompactionSettings struct {
	Enabled       bool `json:"enabled"`
	ReserveTokens int  `json:"reserve_tokens"`       // keep free for model output + thinking
	KeepRecent    int  `json:"keep_recent"`          // tokens of recent context to keep verbatim
	CompactAt     int  `json:"compact_at,omitempty"` // soft threshold in tokens; 0 = use the model window
}

// DefaultCompactionSettings provides sensible defaults.
var DefaultCompactionSettings = CompactionSettings{
	Enabled:       true,
	ReserveTokens: 16384,
	KeepRecent:    20000,
}

// CompactionPayload is the typed result of a compaction event.
type CompactionPayload struct {
	Summary        string   `json:"summary"`
	TokensBefore   int      `json:"tokens_before"`
	TokensAfter    int      `json:"tokens_after"`
	ReadFiles      []string `json:"read_files,omitempty"`
	ModifiedFiles  []string `json:"modified_files,omitempty"`
	SummaryMsgID   string   `json:"summary_msg_id,omitempty"`
	FirstKeptMsgID string   `json:"first_kept_msg_id,omitempty"`
}

// compactionTailMargin is the extra headroom (≈2× the summary-message estimate)
// the effective window must leave above ReserveTokens + KeepRecent so that, after
// a compaction, the retained tail sits BELOW the threshold. Without it a very low
// CompactAt lands in a degenerate band where post-compaction context still
// exceeds the threshold and compaction retriggers every single turn.
const compactionTailMargin = 4000

// EffectiveWindow returns the context window to use for compaction decisions.
// When CompactAt is set (>0) it caps the model's real window so compaction fires
// earlier; it is clamped to maxInput, so an over-large value harmlessly degrades
// to plain overflow protection rather than disabling compaction. It is also
// floored so a too-low CompactAt can't cause per-turn compaction thrash.
func (s CompactionSettings) EffectiveWindow(maxInput int) int {
	if s.CompactAt > 0 && s.CompactAt < maxInput {
		eff := s.CompactAt
		if floor := s.ReserveTokens + s.KeepRecent + compactionTailMargin; eff < floor {
			eff = floor
		}
		if eff < maxInput {
			return eff
		}
	}
	return maxInput
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
