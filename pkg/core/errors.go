package core

import (
	"errors"
	"fmt"
	"time"
)

// QuotaExceededError is returned by a provider when the account's usage limit
// has been reached (e.g. a ChatGPT/Codex subscription 5-hour or weekly window),
// as opposed to a transient rate limit that a retry would clear. It is NOT a
// user cancellation: callers must surface it as an actionable "limit reached,
// resets in X" message rather than a generic error or an interruption marker.
type QuotaExceededError struct {
	// Provider is the provider name (e.g. "openai", "anthropic").
	Provider string
	// Message is the human-readable message from the provider, if any.
	Message string
	// PlanType is the subscription plan reported by the provider (may be empty).
	PlanType string
	// ResetsIn is the time until the exhausted window resets (0 if unknown).
	ResetsIn time.Duration
	// ResetsAt is the wall-clock reset time (zero if unknown).
	ResetsAt time.Time
	// Window labels which limit was hit ("5h", "weekly", or "" if unknown).
	Window string
}

func (e *QuotaExceededError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = "usage limit reached"
	}
	prefix := "quota exceeded"
	if e.Provider != "" {
		prefix = e.Provider + " quota exceeded"
	}
	if e.ResetsIn > 0 {
		return fmt.Sprintf("%s: %s (resets in %s)", prefix, msg, humanizeDuration(e.ResetsIn))
	}
	return fmt.Sprintf("%s: %s", prefix, msg)
}

// Is reports whether target is a *QuotaExceededError, enabling errors.Is checks
// against the ErrQuotaExceeded sentinel.
func (e *QuotaExceededError) Is(target error) bool {
	_, ok := target.(*QuotaExceededError)
	return ok
}

// ErrQuotaExceeded is a sentinel for errors.Is(err, core.ErrQuotaExceeded).
var ErrQuotaExceeded = &QuotaExceededError{}

// AsQuotaExceeded extracts a *QuotaExceededError from an error chain, if present.
func AsQuotaExceeded(err error) (*QuotaExceededError, bool) {
	var qe *QuotaExceededError
	if errors.As(err, &qe) {
		return qe, true
	}
	return nil, false
}

// humanizeDuration renders a duration as a compact "2h 36m" / "45m" / "30s"
// string for user-facing quota messages.
func humanizeDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	h := int(d / time.Hour)
	m := int((d % time.Hour) / time.Minute)
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}
