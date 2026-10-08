package anthropic

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

const unifiedPrefix = "anthropic-ratelimit-unified-"

var weeklyKnown = map[string]bool{
	"status": true, "representative-claim": true, "7d-status": true,
	"7d-utilization": true, "overage-status": true, "5h-status": true,
	"5h-utilization": true, "7d-reset": true, "reset": true,
	"5h-reset": true, "7d-surpassed-threshold": true,
	"fallback-percentage": true, "overage-disabled-reason": true,
}

func rawSingleton(h http.Header, name string) (string, bool) {
	var value string
	count := 0
	for k, values := range h {
		if strings.EqualFold(k, name) {
			count += len(values)
			if len(values) == 1 {
				value = values[0]
			}
		}
	}
	return value, count == 1
}

var exactDecimal = regexp.MustCompile(`^([+-]?)([0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE]([+-]?[0-9]+))?$`)

// Compare decimal text without rounding a value just above one down to one.
func decimalEquals(value string, one bool) bool {
	m := exactDecimal.FindStringSubmatch(value)
	if m == nil {
		return false
	}
	digits := strings.ReplaceAll(m[2], ".", "")
	trimmed := strings.TrimLeft(digits, "0")
	if trimmed == "" {
		return !one
	}
	if !one || m[1] == "-" {
		return false
	}
	if strings.TrimRight(trimmed, "0") != "1" {
		return false
	}
	exponent := int64(0)
	if m[3] != "" {
		var err error
		exponent, err = strconv.ParseInt(m[3], 10, 64)
		if err != nil {
			return false
		}
	}
	frac := 0
	if dot := strings.IndexByte(m[2], '.'); dot >= 0 {
		frac = len(m[2]) - dot - 1
	}
	trailing := len(trimmed) - 1
	return exponent == int64(frac-trailing)
}

func weeklyIdentity(h http.Header, status int, oauth, fast bool, request *http.Request) bool {
	if request == nil || request.URL == nil || request.Method != http.MethodPost || request.URL.String() != "https://api.anthropic.com/v1/messages" || status != http.StatusTooManyRequests || !oauth || fast {
		return false
	}
	for name := range h {
		lower := strings.ToLower(name)
		if !strings.HasPrefix(lower, unifiedPrefix) {
			continue
		}
		suffix := strings.TrimPrefix(lower, unifiedPrefix)
		if !weeklyKnown[suffix] {
			return false
		}
		if suffix != "reset" && suffix != "7d-reset" {
			if _, ok := rawSingleton(h, lower); !ok {
				return false
			}
		}
	}
	for suffix, expected := range map[string]string{
		"status": "rejected", "representative-claim": "seven_day", "7d-status": "rejected",
		"overage-status": "rejected", "5h-status": "allowed",
	} {
		v, ok := rawSingleton(h, unifiedPrefix+suffix)
		if !ok || v != expected {
			return false
		}
	}
	seven, ok7 := rawSingleton(h, unifiedPrefix+"7d-utilization")
	five, ok5 := rawSingleton(h, unifiedPrefix+"5h-utilization")
	return ok7 && ok5 && decimalEquals(seven, true) && decimalEquals(five, false)
}

func positiveSeconds(v string) (int64, bool) {
	if len(v) == 0 || len(v) > 19 {
		return 0, false
	}
	for _, ch := range v {
		if ch < '0' || ch > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(v, 10, 64)
	return n, err == nil && n > 0 && n <= int64((1<<63-1)/time.Second)
}

func weeklyTiming(h http.Header, observed time.Time, backoff time.Duration) core.ProviderWait {
	if backoff <= 0 {
		panic("weekly backoff must be positive")
	}
	w := core.ProviderWait{Kind: "quota_confirmed", Scope: "seven_day", ObservedAt: observed,
		NextAttemptAt: observed.Add(backoff), Status: http.StatusTooManyRequests}
	global, gok := rawSingleton(h, unifiedPrefix+"reset")
	seven, sok := rawSingleton(h, unifiedPrefix+"7d-reset")
	g, gv := positiveSeconds(global)
	s, sv := positiveSeconds(seven)
	if gok && sok && gv && sv && g == s {
		reset := time.Unix(g, 0).UTC()
		if reset.After(observed) {
			w.ResetAt = &reset
			w.ResetSource = "anthropic_unified_7d_reset"
			if reset.After(w.NextAttemptAt) {
				w.NextAttemptAt = reset
			}
		}
	}
	if value, ok := rawSingleton(h, "Retry-After"); ok {
		var until time.Time
		if seconds, valid := positiveSeconds(value); valid {
			until = observed.Add(time.Duration(seconds) * time.Second)
		} else if date, err := http.ParseTime(value); err == nil && date.After(observed) {
			until = date
		}
		if !until.IsZero() {
			w.RetryAfterAt = &until
			w.RetrySource = "retry_after"
			if until.After(w.NextAttemptAt) {
				w.NextAttemptAt = until
			}
		}
	}
	return w
}
