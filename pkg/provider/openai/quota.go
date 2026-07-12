package openai

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

// usageLimitBody is the JSON returned by chatgpt.com/backend-api on a 429 when
// the account's usage limit is reached (not a transient rate limit).
//
//	{"error":{"type":"usage_limit_reached","message":"The usage limit has been
//	 reached","plan_type":"prolite","resets_at":1783872321,"resets_in_seconds":9399}}
type usageLimitBody struct {
	Error struct {
		Type            string `json:"type"`
		Message         string `json:"message"`
		PlanType        string `json:"plan_type"`
		ResetsAt        int64  `json:"resets_at"`
		ResetsInSeconds int64  `json:"resets_in_seconds"`
	} `json:"error"`
}

// isUsageLimitBody reports whether a 429 body is a terminal usage-limit
// exhaustion (which retrying will never clear) rather than a transient rate
// limit. Used both to veto retries and to build the typed error.
func isUsageLimitBody(body []byte) bool {
	var b usageLimitBody
	if err := json.Unmarshal(body, &b); err != nil {
		return false
	}
	return b.Error.Type == "usage_limit_reached"
}

// quotaErrorFrom builds a typed core.QuotaExceededError from a 429 usage-limit
// response. It prefers the structured x-codex-* headers (which identify the 5h
// "primary" vs weekly "secondary" window and give reset times) and falls back
// to the JSON body's resets_in_seconds/resets_at.
func quotaErrorFrom(resp *http.Response, body []byte) *core.QuotaExceededError {
	var b usageLimitBody
	_ = json.Unmarshal(body, &b)

	qe := &core.QuotaExceededError{
		Provider: "openai",
		Message:  b.Error.Message,
		PlanType: b.Error.PlanType,
	}
	if qe.Message == "" {
		qe.Message = "usage limit has been reached"
	}
	if qe.PlanType == "" {
		qe.PlanType = resp.Header.Get("x-codex-plan-type")
	}

	// Determine which window is exhausted from the x-codex-*-used-percent
	// headers, and take that window's reset time. Primary = 5h, secondary =
	// weekly (per x-codex-*-window-minutes: 300 / 10080). When BOTH are
	// exhausted, the later-resetting window is the binding constraint — clearing
	// the 5h window won't unblock you until the weekly one also resets — so
	// report that one.
	primaryExhausted := headerInt(resp, "x-codex-primary-used-percent") >= 100
	secondaryExhausted := headerInt(resp, "x-codex-secondary-used-percent") >= 100

	setPrimary := func() {
		qe.Window = windowLabel(headerInt(resp, "x-codex-primary-window-minutes"))
		qe.ResetsIn = headerSeconds(resp, "x-codex-primary-reset-after-seconds")
		qe.ResetsAt = headerUnix(resp, "x-codex-primary-reset-at")
	}
	setSecondary := func() {
		qe.Window = windowLabel(headerInt(resp, "x-codex-secondary-window-minutes"))
		qe.ResetsIn = headerSeconds(resp, "x-codex-secondary-reset-after-seconds")
		qe.ResetsAt = headerUnix(resp, "x-codex-secondary-reset-at")
	}

	switch {
	case primaryExhausted && secondaryExhausted:
		if headerSeconds(resp, "x-codex-secondary-reset-after-seconds") >=
			headerSeconds(resp, "x-codex-primary-reset-after-seconds") {
			setSecondary()
		} else {
			setPrimary()
		}
	case primaryExhausted:
		setPrimary()
	case secondaryExhausted:
		setSecondary()
	}

	// Fall back to the body's reset fields when headers are absent (e.g. API-key
	// requests to api.openai.com that don't emit x-codex-* headers).
	if qe.ResetsIn == 0 && b.Error.ResetsInSeconds > 0 {
		qe.ResetsIn = time.Duration(b.Error.ResetsInSeconds) * time.Second
	}
	if qe.ResetsAt.IsZero() && b.Error.ResetsAt > 0 {
		qe.ResetsAt = time.Unix(b.Error.ResetsAt, 0)
	}
	// Derive a missing field from the other so callers always have both when
	// possible.
	if qe.ResetsIn == 0 && !qe.ResetsAt.IsZero() {
		if d := time.Until(qe.ResetsAt); d > 0 {
			qe.ResetsIn = d
		}
	}
	if qe.ResetsAt.IsZero() && qe.ResetsIn > 0 {
		qe.ResetsAt = time.Now().Add(qe.ResetsIn)
	}
	return qe
}

// windowLabel maps a window duration in minutes to a friendly label. A
// non-positive value (absent/invalid header) is treated as unknown ("").
func windowLabel(minutes int) string {
	switch {
	case minutes == 300:
		return "5h"
	case minutes == 10080:
		return "weekly"
	case minutes <= 0:
		return ""
	default:
		return strconv.Itoa(minutes) + "m"
	}
}

func headerInt(resp *http.Response, key string) int {
	v := resp.Header.Get(key)
	if v == "" {
		return -1 // absent — distinct from a real 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return -1
	}
	return n
}

func headerSeconds(resp *http.Response, key string) time.Duration {
	v := resp.Header.Get(key)
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}

func headerUnix(resp *http.Response, key string) time.Time {
	v := resp.Header.Get(key)
	if v == "" {
		return time.Time{}
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.Unix(n, 0)
}
