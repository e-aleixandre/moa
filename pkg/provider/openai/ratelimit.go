package openai

import (
	"math"
	"net/http"
	"strconv"

	"github.com/ealeixandre/moa/pkg/core"
)

// parseRateLimit extracts the unified rate-limit state from a ChatGPT/Codex
// response's x-codex-* headers (present on every /codex/responses reply, not
// just 429s). It maps the "primary" window (5h) to FiveHourUtil and the
// "secondary" window (weekly) to SevenDayUtil. ChatGPT has no pay-as-you-go
// overage concept, so OverageStatus/OverageUtil are left empty/-1.
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
	return &core.RateLimit{
		FiveHourUtil: parseUsedPercent(primary),
		SevenDayUtil: parseUsedPercent(secondary),
		OverageUtil:  -1, // ChatGPT/Codex has no overage bucket
	}
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
