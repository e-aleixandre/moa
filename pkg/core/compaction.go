package core

// CompactionSettings controls automatic context compaction.
type CompactionSettings struct {
	Enabled       bool `json:"enabled"`
	ReserveTokens int  `json:"reserve_tokens"`       // keep free for model output + thinking
	KeepRecent    int  `json:"keep_recent"`          // tokens of recent context to keep verbatim
	CompactAt     int  `json:"compact_at,omitempty"` // soft threshold in tokens; 0 = use the model window
	// DefaultCompactAt is the fallback threshold (the global config setting)
	// used when this session has no CompactAt of its own. Kept as a separate
	// field rather than folded into CompactAt because only CompactAt is the
	// session's OWN choice and only CompactAt is persisted as session metadata:
	// merging them would freeze today's global value into every session, so a
	// later change to the global setting would never reach them.
	DefaultCompactAt int `json:"default_compact_at,omitempty"`
	// TrimDisabled turns off the deterministic tool-result elision that runs
	// before compaction. Phrased as a negative so the zero value keeps it on:
	// settings are built explicitly in many places (tests, subagents, the CLI),
	// and a positive TrimEnabled would have silently disabled the feature in
	// every one of them that was written before it existed.
	TrimDisabled bool `json:"trim_disabled,omitempty"`
}

// DefaultCompactionSettings provides sensible defaults.
var DefaultCompactionSettings = CompactionSettings{
	Enabled:       true,
	ReserveTokens: 16384,
	KeepRecent:    20000,
}

// CompactionWithDefault returns the standard settings carrying globalCompactAt
// as the fallback threshold. Callers build agents this way instead of assigning
// DefaultCompactAt by hand so the global setting cannot be wired into one agent
// construction site and forgotten in another.
func CompactionWithDefault(globalCompactAt int) *CompactionSettings {
	settings := DefaultCompactionSettings
	settings.DefaultCompactAt = globalCompactAt
	return &settings
}

// CompactionFromConfig is CompactionWithDefault plus the trim switch, so the
// one setting that can turn off a feature on the critical path of every
// session is read in the same place as the threshold it guards — and can be
// flipped by editing the config rather than by rebuilding the binary.
func CompactionFromConfig(globalCompactAt int, trimDisabled bool) *CompactionSettings {
	settings := CompactionWithDefault(globalCompactAt)
	settings.TrimDisabled = trimDisabled
	return settings
}

// ResolveCompactAt picks the threshold to apply when the session value and the
// global default come from different places — the parent agent and the config
// file, as when spawning a subagent. Session value wins, then the global one,
// then 0 meaning "compact at the model window".
func ResolveCompactAt(sessionCompactAt, globalCompactAt int) int {
	if sessionCompactAt > 0 {
		return sessionCompactAt
	}
	if globalCompactAt > 0 {
		return globalCompactAt
	}
	return 0
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
	// SummarizerNotice explains, for the reader, that the summary above was
	// NOT written by the configured summarizer — an expired credential, a
	// model no longer in the catalog. Empty in the ordinary case, including
	// when a configured model was honoured: the compaction card is the whole
	// story there, and a line on every compaction would be noise.
	//
	// It is part of the payload because it has to outlive the run that
	// produced it: the reader usually comes back hours later, and a summary
	// whose provenance is not what they configured is exactly what they need
	// to know when judging it.
	SummarizerNotice string `json:"summarizer_notice,omitempty"`
	Usage            *Usage `json:"usage,omitempty"`
}

// compactionTailMargin is the extra headroom (≈2× the summary-message estimate)
// the effective window must leave above ReserveTokens + KeepRecent so that, after
// a compaction, the retained tail sits BELOW the threshold. Without it a very low
// CompactAt lands in a degenerate band where post-compaction context still
// exceeds the threshold and compaction retriggers every single turn.
const compactionTailMargin = 4000

// MinCompactAt is the lowest CompactAt that still behaves as asked: below it
// EffectiveWindow silently raises the threshold to avoid per-turn thrash. A UI
// offering a threshold has to read this rather than assume, since it moves with
// ReserveTokens and KeepRecent — a control that let you pick below it would be
// promising a compaction point the engine will not honor.
func (s CompactionSettings) MinCompactAt() int {
	return s.ReserveTokens + s.KeepRecent + compactionTailMargin
}

// EffectiveWindow returns the context window to use for compaction decisions.
// When a threshold is set (>0) it caps the model's real window so compaction
// fires earlier; it is clamped to maxInput, so an over-large value harmlessly
// degrades to plain overflow protection rather than disabling compaction. It is
// also floored so a too-low threshold can't cause per-turn compaction thrash.
// The threshold is the session's own CompactAt, falling back to the global
// DefaultCompactAt: session → global → model window, resolved in one place so
// the floor and the clamp apply identically whichever level set the value.
func (s CompactionSettings) EffectiveWindow(maxInput int) int {
	at := s.CompactAt
	if at <= 0 {
		at = s.DefaultCompactAt
	}
	if at > 0 && at < maxInput {
		eff := at
		if floor := s.MinCompactAt(); eff < floor {
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

// compactionWarnRatio is how full the context has to be before the agent is
// warned. At 85% there is room for a few more turns — enough to write something
// down — while still being late enough that most runs never see the notice.
const compactionWarnRatio = 0.85

// minWarnBandTokens is the smallest useful warning band. The ratio alone leaves
// a band proportional to the window, and with a low threshold that band gets
// narrower than a single tool result: measured against a real server at
// compact_at=45k the band was 4.3k tokens while reading one 1800-line file cost
// 5.4k, so the context jumped from under the band to over the threshold and the
// agent was never warned. Below this size the band is widened instead.
const minWarnBandTokens = 20_000

// trimMinGainRatio is the fraction of the effective threshold a trim has to
// win to be worth doing. A trim costs one full prefix-cache rewrite (~T
// tokens) and, on providers that sign reasoning, the loss of thinking blocks
// after the watermark. That cost is proportional to T, so the threshold that
// justifies paying it has to be proportional too — and large enough that the
// context cannot cross the threshold again after a handful of turns, which
// would pay the cost repeatedly for almost no room.
const trimMinGainRatio = 0.20

// TrimMinGain is the smallest saving that justifies a trim for these settings.
// It reuses minWarnBandTokens as a floor, the same "below this a band is not
// worth acting on" precedent the warning band uses.
func TrimMinGain(contextWindow int, settings CompactionSettings) int {
	effective := contextWindow - settings.ReserveTokens
	if effective <= 0 {
		return 0
	}
	gain := int(float64(effective) * trimMinGainRatio)
	if gain < minWarnBandTokens {
		gain = minWarnBandTokens
	}
	return gain
}

// AcceptTrim reports whether a planned trim should be applied instead of
// compacting.
//
// Winning minGain is not enough on its own. The threshold check only says the
// context is ABOVE T, not by how much: a single huge result can take it far
// past T in one step, and a trim that wins minGain from there can still leave
// the request over the threshold — sent, because the loop evaluates compaction
// once per iteration and then proceeds. Requiring the projected size to land
// at least minGain BELOW T is what makes the low-water mark real, and with it
// the guarantee that at least minGain of new tokens must arrive before the
// next event.
func AcceptTrim(estimateBefore, tokensRemoved, contextWindow int, settings CompactionSettings) bool {
	effective := contextWindow - settings.ReserveTokens
	if effective <= 0 {
		return false
	}
	minGain := TrimMinGain(contextWindow, settings)
	if tokensRemoved < minGain {
		return false
	}
	return estimateBefore-tokensRemoved <= effective-minGain
}

// ShouldWarnBeforeCompact reports whether the agent is close enough to the
// compaction threshold to be told about it, and how many tokens remain.
//
// An automatic compaction arrives with no warning mid-task, so whatever the
// agent had worked out but not written down is replaced by a summary. This is
// what gives it the chance to persist it first.
func ShouldWarnBeforeCompact(contextTokens, contextWindow int, settings CompactionSettings) (warn bool, remaining int) {
	if !settings.Enabled || contextWindow <= 0 {
		return false, 0
	}
	effective := contextWindow - settings.ReserveTokens
	if effective <= 0 {
		return false, 0
	}
	// Past the threshold it is too late to warn: compaction happens this turn.
	if contextTokens > effective {
		return false, 0
	}
	band := float64(effective) * (1 - compactionWarnRatio)
	if band < minWarnBandTokens {
		band = minWarnBandTokens
	}
	if float64(effective-contextTokens) > band {
		return false, 0
	}
	return true, effective - contextTokens
}
