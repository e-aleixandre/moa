package openai

import (
	"context"
	"errors"
	"net"
)

// transcribeError is the only error shape dictation returns. Its text is
// fixed and its cause is deliberately dropped: transport and provider errors
// can echo bytes of the request or response (URLs, status lines, bodies), and
// these errors are logged and returned to clients.
type transcribeError struct {
	msg        string
	reason     string
	noFallback bool
}

func (e *transcribeError) Error() string { return e.msg }

// LogReason is the category safe to log.
func (e *transcribeError) LogReason() string { return e.reason }

// NoFallback implements core.NoFallbackError.
func (e *transcribeError) NoFallback() bool { return e.noFallback }

// transportError classifies a failure of http.Client.Do. parent is the
// caller's context, so a budget timeout of a derived context is not confused
// with the caller leaving.
func transportError(prefix string, parent context.Context, err error) *transcribeError {
	var ne net.Error
	switch {
	case parent.Err() != nil:
		return &transcribeError{msg: prefix + ": canceled", reason: "canceled"}
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return &transcribeError{msg: prefix + ": timeout", reason: "timeout"}
	default:
		return &transcribeError{msg: prefix + ": network error", reason: "network"}
	}
}
