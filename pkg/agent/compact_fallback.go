package agent

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"

	"github.com/e-aleixandre/moa/pkg/compaction"
	"github.com/e-aleixandre/moa/pkg/core"
)

// abandonedAttempt is a summarization the fallback gave up on. Its reported
// usage was spent at its own model's rates, and is billed even though the
// summary is discarded.
type abandonedAttempt struct {
	model core.Model
	usage *core.Usage
}

// unrecoverableForOtherModel reports why a summarization failure would repeat
// on any model, or "" when another model may well succeed. It is a short
// denylist rather than a list of known transient formats: providers word
// network, overload and server failures too many ways to enumerate, and the
// point of the fallback is that the session can always compact. Typed errors
// and the HTTP status decide where they exist; the rest is text, because
// context overflow and refusals have no type.
func unrecoverableForOtherModel(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, ErrContextCapacity) {
		return true
	}
	if _, ok := core.AsProviderCredentialError(err); ok {
		return true
	}
	var authErr *core.ProviderAuthError
	if errors.As(err, &authErr) {
		return true
	}
	s := strings.ToLower(err.Error())
	// A 4xx is the request's fault, except 429 which is the server's load.
	for _, code := range []string{"http 400", "http 401", "http 403", "http 404", "http 405", "http 409", "http 413", "http 422"} {
		if strings.Contains(s, code) {
			return true
		}
	}
	for _, marker := range []string{
		"invalid_grant", "authentication failed", "invalid_api_key", "invalid api key",
		"context_length", "context length", "context window", "prompt is too long",
		"too many tokens", "maximum context", "exceeds the context", "request too large",
		"refusal", "invalid_request",
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

// fallbackReason names, for the reader, why the configured summarizer was
// replaced. It is a label only; the decision is unrecoverableForOtherModel.
func fallbackReason(err error) string {
	if _, ok := core.AsQuotaExceeded(err); ok {
		return "usage limit reached"
	}
	s := strings.ToLower(err.Error())
	var netErr net.Error
	switch {
	case strings.Contains(s, "overloaded") || strings.Contains(s, "http 529"):
		return "overloaded"
	case strings.Contains(s, "http 429") || strings.Contains(s, "rate limit") || strings.Contains(s, "rate_limit"):
		return "rate limited"
	case strings.Contains(s, "http 5") || strings.Contains(s, "server error") || strings.Contains(s, "service unavailable"):
		return "server error"
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) ||
		strings.Contains(s, "timeout") || strings.Contains(s, "connection") || strings.Contains(s, "eof") ||
		strings.Contains(s, "no such host") || strings.Contains(s, "idle"):
		return "network error"
	}
	return "provider error"
}

// compactWithSessionFallback runs a compaction summary and, when the summarizer
// differs from the session's model and fails with anything but an
// unrecoverable error, retries once with the session's own. The configuration
// is untouched: the next compaction tries the configured summarizer again.
//
// It returns the model that produced the outcome, whose pricing bills it, and
// the notice to record: the caller's own when no fallback happened, the
// "was unavailable" one when it did. A fallback also returns the abandoned
// first attempt when it reported usage.
func compactWithSessionFallback(
	ctx context.Context,
	sumProvider core.Provider, sumModel core.Model, sumOpts core.StreamOptions, notice string,
	sessProvider core.Provider, sessModel core.Model, sessOpts core.StreamOptions,
	msgs []core.AgentMessage, tokens, window int, settings core.CompactionSettings, focus string,
) (*compaction.Result, []core.AgentMessage, core.Model, string, *abandonedAttempt, error) {
	result, compacted, err := compaction.Compact(ctx, sumProvider, sumModel, sumOpts, msgs, tokens, window, settings, focus)
	if err == nil || sumModel.ID == sessModel.ID || ctx.Err() != nil || unrecoverableForOtherModel(err) {
		return result, compacted, sumModel, notice, nil, err
	}
	reason := fallbackReason(err)
	slog.Warn("compaction summarizer unavailable; retrying with the session model",
		"summarizer", sumModel.ID, "session_model", sessModel.ID, "reason", reason, "error", err)
	var abandoned *abandonedAttempt
	if result != nil && result.Usage != nil {
		abandoned = &abandonedAttempt{model: sumModel, usage: result.Usage}
	}
	result, compacted, err = compaction.Compact(ctx, sessProvider, sessModel, sessOpts, msgs, tokens, window, settings, focus)
	return result, compacted, sessModel, core.CompactModelUnavailableNotice(sumModel, sessModel, reason), abandoned, err
}

// attach records the abandoned attempt's spend on the payload that is adopted.
func (b *abandonedAttempt) attach(p *core.CompactionPayload) {
	if b == nil {
		return
	}
	p.AbandonedUsage, p.AbandonedPricing = b.usage, b.model.Pricing
}
