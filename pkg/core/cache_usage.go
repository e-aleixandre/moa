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
}

// SummarizeCacheUsage returns cache usage aggregated from valid assistant
// turns. Error turns are ignored. Available is false when no turn reported
// input or cache tokens, so Ratio is not a synthetic percentage. Streak and
// Alert describe only the current trailing sequence of writes without reads.
func SummarizeCacheUsage(messages []AgentMessage) CacheUsageSummary {
	var summary CacheUsageSummary
	denominator := 0

	for _, message := range messages {
		if message.Role != "assistant" || message.StopReason == "error" {
			continue
		}
		if message.Usage == nil {
			continue
		}

		usage := message.Usage
		turnTokens := usage.CacheRead + usage.CacheWrite + usage.Input
		if turnTokens <= 0 {
			continue
		}

		summary.Available = true
		summary.Read += usage.CacheRead
		summary.Written += usage.CacheWrite
		denominator += turnTokens

		if usage.CacheWrite > 0 && usage.CacheRead == 0 {
			summary.Streak++
		} else {
			summary.Streak = 0
		}
	}

	if summary.Available {
		summary.Ratio = float64(summary.Read) / float64(denominator)
	}
	summary.Alert = summary.Streak >= 3
	return summary
}
