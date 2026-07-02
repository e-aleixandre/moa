package anthropic

import (
	"math"
	"net/http"
	"strconv"

	"github.com/ealeixandre/moa/pkg/core"
)

// parseRateLimit extracts the unified rate-limit state from an Anthropic
// response's headers (anthropic-ratelimit-unified-*). It returns nil when none
// of those headers are present (e.g. non-OAuth requests or an endpoint change),
// so callers degrade gracefully.
//
// These headers are undocumented (reconstructed from observing the API); parse
// defensively and treat missing/garbage values as zero.
func parseRateLimit(h http.Header) *core.RateLimit {
	const prefix = "anthropic-ratelimit-unified-"
	status := h.Get(prefix + "status")
	claim := h.Get(prefix + "representative-claim")
	overageStatus := h.Get(prefix + "overage-status")
	if status == "" && claim == "" && overageStatus == "" {
		return nil
	}
	return &core.RateLimit{
		Status:              status,
		RepresentativeClaim: claim,
		FiveHourUtil:        parseUtil(h.Get(prefix + "5h-utilization")),
		SevenDayUtil:        parseUtil(h.Get(prefix + "7d-utilization")),
		OverageStatus:       overageStatus,
		OverageUtil:         parseUtil(h.Get(prefix + "overage-utilization")),
	}
}

// parseUtil parses a [0,1] utilization fraction. It returns -1 ("unknown") for
// empty, unparseable, or non-finite input, and clamps valid values to [0,1] so
// a malformed header can never corrupt the displayed percentage.
func parseUtil(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return -1
	}
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}
