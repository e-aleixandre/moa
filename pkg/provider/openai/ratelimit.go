package openai

import (
	"math"
	"net/http"
	"strconv"

	"github.com/e-aleixandre/moa/pkg/core"
)

// parseRateLimit extracts the unified rate-limit state from a ChatGPT/Codex
// response's x-codex-* headers (present on every /codex/responses reply, not
// just 429s). The headers include their own window durations, so use those
// rather than assuming that "primary" always means 5h: OpenAI can swap or
// temporarily disable either plan window. ChatGPT has no pay-as-you-go overage
// concept, so OverageStatus/OverageUtil are left empty/-1.
//
// Returns nil when neither used-percent header is present (e.g. an API-key
// request to api.openai.com, which never emits these headers), so callers
// degrade gracefully and never show a bogus 0%.
//
// These headers are undocumented (reconstructed from the Codex client); parse
// defensively and treat missing/garbage values as unknown (-1).
func parseRateLimit(h http.Header) *core.RateLimit {
	primary := h.Get("x-codex-primary-used-percent")
	secondary := h.Get("x-codex-secondary-used-percent")
	if primary == "" && secondary == "" {
		return nil
	}
	rl := &core.RateLimit{FiveHourUtil: -1, SevenDayUtil: -1, OverageUtil: -1}
	primaryUtil := parseUsedPercent(primary)
	secondaryUtil := parseUsedPercent(secondary)
	primaryMinutes, primaryDurationPresent := parseWindowMinutes(h.Get("x-codex-primary-window-minutes"))
	secondaryMinutes, secondaryDurationPresent := parseWindowMinutes(h.Get("x-codex-secondary-window-minutes"))
	// Prefer every declared duration before falling back to the historical slot
	// order. This avoids an undated primary claim masking a secondary header
	// that explicitly identifies itself as the 5h window.
	assignDeclaredCodexWindow(rl, primaryUtil, primaryMinutes)
	assignDeclaredCodexWindow(rl, secondaryUtil, secondaryMinutes)
	assignFallbackCodexWindow(rl, primaryUtil, primaryDurationPresent, true)
	assignFallbackCodexWindow(rl, secondaryUtil, secondaryDurationPresent, false)
	return rl
}

const (
	fiveHourMinutes = 5 * 60
	sevenDayMinutes = 7 * 24 * 60
)

func assignDeclaredCodexWindow(rl *core.RateLimit, utilization float64, minutes int) {
	if utilization < 0 {
		return
	}
	switch minutes {
	case fiveHourMinutes:
		if rl.FiveHourUtil < 0 {
			rl.FiveHourUtil = utilization
		}
	case sevenDayMinutes:
		if rl.SevenDayUtil < 0 {
			rl.SevenDayUtil = utilization
		}
	}
}

// assignFallbackCodexWindow keeps compatibility with older responses that
// omitted duration headers. If the other header declared one window, its
// unknown partner fills the remaining slot rather than overwriting it.
func assignFallbackCodexWindow(rl *core.RateLimit, utilization float64, durationPresent, primary bool) {
	if utilization < 0 || durationPresent {
		return
	}
	if primary {
		if rl.FiveHourUtil < 0 {
			rl.FiveHourUtil = utilization
		} else if rl.SevenDayUtil < 0 {
			rl.SevenDayUtil = utilization
		}
		return
	}
	if rl.SevenDayUtil < 0 {
		rl.SevenDayUtil = utilization
	} else if rl.FiveHourUtil < 0 {
		rl.FiveHourUtil = utilization
	}
}

// parseWindowMinutes distinguishes omitted legacy headers from a header that
// OpenAI supplied but disabled or malformed. Only the former may use the
// historical slot fallback.
func parseWindowMinutes(s string) (minutes int, present bool) {
	if s == "" {
		return 0, false
	}
	minutes, err := strconv.Atoi(s)
	if err != nil || minutes <= 0 {
		return 0, true
	}
	return minutes, true
}

// parseUsedPercent converts a used-percent header (0-100, possibly fractional)
// into a [0,1] utilization fraction. It returns -1 ("unknown") for empty,
// unparseable, or non-finite input, and clamps to [0,1] so a malformed or
// over-100 value (seen at exhaustion) can never corrupt the displayed meter.
func parseUsedPercent(s string) float64 {
	if s == "" {
		return -1
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return -1
	}
	f /= 100
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}
