package anthropic

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// Claude Code 2.1.1's h50/Ai8/Gi8 identify five_hour and seven_day plan
// claims. This narrow raw-response contract does not use utilization/cache
// as authorization and does not claim the synthetic 5h fixture was observed.
func planQuotaIdentity(h http.Header, status int, oauth, fast bool, request *http.Request, body []byte) string {
	if request == nil || request.URL == nil || request.Method != http.MethodPost || request.URL.String() != "https://api.anthropic.com/v1/messages" || status != 429 || !oauth || fast {
		return ""
	}
	for name := range h {
		lower := strings.ToLower(name)
		if !strings.HasPrefix(lower, unifiedPrefix) {
			continue
		}
		suffix := strings.TrimPrefix(lower, unifiedPrefix)
		if !weeklyKnown[suffix] {
			return ""
		}
		if strings.HasSuffix(suffix, "reset") {
			continue
		}
		if _, ok := rawSingleton(h, lower); !ok {
			return ""
		}
	}
	for _, suffix := range []string{"status", "overage-status"} {
		v, ok := rawSingleton(h, unifiedPrefix+suffix)
		if !ok || v != "rejected" {
			return ""
		}
	}
	claim, ok := rawSingleton(h, unifiedPrefix+"representative-claim")
	if !ok || (claim != "five_hour" && claim != "seven_day") {
		return ""
	}
	for _, window := range []string{"5h", "7d"} {
		v, present := rawSingleton(h, unifiedPrefix+window+"-status")
		if present && v != "allowed" && v != "rejected" {
			return ""
		}
		if present && v == "allowed" && ((claim == "five_hour" && window == "5h") || (claim == "seven_day" && window == "7d")) {
			return ""
		}
	}
	var payload struct {
		Error struct {
			Type    string `json:"type"`
			Details struct {
				Code string `json:"error_code"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Error.Type != "rate_limit_error" || payload.Error.Details.Code != "" {
		return ""
	}
	return claim
}

func planQuotaTiming(h http.Header, scope string, observed time.Time, backoff time.Duration) core.ProviderWait {
	if backoff <= 0 {
		backoff = time.Second
	}
	w := core.ProviderWait{Kind: "quota_confirmed", Scope: scope, Status: http.StatusTooManyRequests, ObservedAt: observed, NextAttemptAt: observed.Add(backoff)}
	window := "5h"
	if scope == "seven_day" {
		window = "7d"
	}
	date, one := rawSingleton(h, "Date")
	origin, dateErr := http.ParseTime(date)
	hasOrigin := one && dateErr == nil
	// Compare an origin epoch against the origin clock before anchoring its
	// interval at reception. A fast local clock cannot erase a known bound.
	bound := func(epoch time.Time) (time.Time, bool) {
		if hasOrigin {
			if !epoch.After(origin) {
				return time.Time{}, false
			}
			return observed.Add(epoch.Sub(origin)), true
		}
		if !epoch.After(observed) {
			return time.Time{}, false
		}
		return epoch, true
	}
	var latest time.Time
	latestWindow := window
	complete := true
	for _, candidate := range []string{"5h", "7d"} {
		status, _ := rawSingleton(h, unifiedPrefix+candidate+"-status")
		if candidate != window && status != "rejected" {
			continue
		}
		raw, singleton := rawSingleton(h, unifiedPrefix+candidate+"-reset")
		epoch, valid := positiveSeconds(raw)
		reset := time.Unix(epoch, 0).UTC()
		until, future := bound(reset)
		if !singleton || !valid || !future {
			complete = false
			continue
		}
		if reset.After(latest) {
			latest = reset
			latestWindow = candidate
		}
		if until.After(w.NextAttemptAt) {
			w.NextAttemptAt = until
		}
	}
	// Global cannot clear a blocking window, and a disagreement is not a
	// complete recovery date. Retry-After remains an independent lower bound.
	global, singleton := rawSingleton(h, unifiedPrefix+"reset")
	if singleton {
		epoch, valid := positiveSeconds(global)
		if !valid || (!latest.IsZero() && time.Unix(epoch, 0).UTC() != latest) {
			complete = false
		}
	}
	if complete && !latest.IsZero() {
		w.ResetAt = &latest
		w.ResetSource = "anthropic_unified_" + latestWindow + "_reset"
	}
	if raw, singleton := rawSingleton(h, "Retry-After"); singleton {
		var until time.Time
		valid := false
		if seconds, duration := positiveSeconds(raw); duration {
			until = observed.Add(time.Duration(seconds) * time.Second)
			valid = true
		} else if epoch, err := http.ParseTime(raw); err == nil {
			until, valid = bound(epoch)
		}
		if valid {
			w.RetryAfterAt = &until
			w.RetrySource = "retry_after"
			if until.After(w.NextAttemptAt) {
				w.NextAttemptAt = until
			}
		}
	}
	return w
}
