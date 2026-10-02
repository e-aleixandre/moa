package core

// CacheUsageSummary describes cache activity across a session's completed
// assistant turns.
type CacheUsageSummary struct {
	Available bool
	Ratio     float64
	Read      int
	Written   int
	Streak    int
	Alert     bool

	// Misses counts turns whose cache read fell short of what the previous
	// turn had cached; MissCostUSD is what re-caching those tokens cost over
	// reading them. LastMiss is the most recent one, nil when there is none.
	Misses      int
	MissCostUSD float64
	LastMiss    *CacheMiss
}

// SummarizeCacheUsage returns cache usage aggregated from valid assistant
// turns. Error turns are ignored. Available is false when no turn reported
// input or cache tokens, so Ratio is not a synthetic percentage. Streak and
// Alert describe only the current trailing sequence of writes without reads.
func SummarizeCacheUsage(messages []AgentMessage) CacheUsageSummary {
	var acc CacheUsageAccumulator
	for i := range messages {
		acc.Add(&messages[i])
	}
	return acc.Summary()
}

// CacheUsageAccumulator is SummarizeCacheUsage fed one message at a time, so a
// caller can aggregate a transcript it does not want to materialise.
type CacheUsageAccumulator struct {
	summary     CacheUsageSummary
	denominator int
	miss        cacheMissTracker
}

// Add folds one message, in transcript order, into the summary.
func (a *CacheUsageAccumulator) Add(message *AgentMessage) {
	if message.Role != "assistant" || message.StopReason == "error" || message.Usage == nil {
		return
	}
	usage := message.Usage
	turnTokens := usage.CacheRead + usage.CacheWrite + usage.Input
	if turnTokens <= 0 {
		return
	}

	a.summary.Available = true
	if m := a.miss.observe(message); m != nil {
		a.summary.Misses++
		a.summary.MissCostUSD += m.CostUSD
		a.summary.LastMiss = m
	}
	a.summary.Read += usage.CacheRead
	a.summary.Written += usage.CacheWrite
	a.denominator += turnTokens

	if usage.CacheWrite > 0 && usage.CacheRead == 0 {
		a.summary.Streak++
	} else {
		a.summary.Streak = 0
	}
}

// Summary returns the aggregate of everything added so far.
func (a *CacheUsageAccumulator) Summary() CacheUsageSummary {
	summary := a.summary
	if summary.Available {
		summary.Ratio = float64(summary.Read) / float64(a.denominator)
	}
	summary.Alert = summary.Streak >= 3
	return summary
}
