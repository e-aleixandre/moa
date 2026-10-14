package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"

	"github.com/e-aleixandre/moa/pkg/compaction"
	"github.com/e-aleixandre/moa/pkg/core"
)

// transientProviderReason classifies err as a temporary provider failure and
// names it for the reader, or returns "" when it is not one. It builds on the
// stream-repair classification (credentials, quota and 4xx other than 429 are
// never transient) and adds what that one leaves out because the HTTP retry
// layer normally absorbs it: a 5xx or an overload that outlived its retries.
func transientProviderReason(err error) string {
	if err == nil || errors.Is(err, context.Canceled) {
		return ""
	}
	if _, ok := core.AsProviderCredentialError(err); ok {
		return ""
	}
	if _, ok := core.AsQuotaExceeded(err); ok {
		return ""
	}
	var authErr *core.ProviderAuthError
	if errors.As(err, &authErr) {
		return ""
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "invalid_grant") || strings.Contains(s, "authentication failed") || strings.Contains(s, "quota") {
		return ""
	}
	if strings.Contains(s, "http 4") && !strings.Contains(s, "http 429") {
		return ""
	}
	switch {
	case strings.Contains(s, "overloaded") || strings.Contains(s, "http 529"):
		return "overloaded"
	case strings.Contains(s, "http 429") || strings.Contains(s, "rate limit") || strings.Contains(s, "rate_limit"):
		return "rate limited"
	case strings.Contains(s, "http 5") || strings.Contains(s, "service unavailable") || strings.Contains(s, "server_error"):
		return "server error"
	}
	if isRetryableStreamError(err) {
		if errors.Is(err, io.EOF) || strings.Contains(s, "deadline exceeded") || strings.Contains(s, "connection") || strings.Contains(s, "eof") {
			return "network error"
		}
		return "provider error"
	}
	return ""
}

// compactWithSessionFallback runs a compaction summary and, when the summarizer
// differs from the session's model and fails with a temporary provider error,
// retries once with the session's own. The configuration is untouched: the next
// compaction tries the configured summarizer again.
//
// It returns the model that produced the outcome, whose pricing bills it, and
// the notice to record: the caller's own when no fallback happened, the
// "was unavailable" one when it did.
func compactWithSessionFallback(
	ctx context.Context,
	sumProvider core.Provider, sumModel core.Model, sumOpts core.StreamOptions, notice string,
	sessProvider core.Provider, sessModel core.Model, sessOpts core.StreamOptions,
	msgs []core.AgentMessage, tokens, window int, settings core.CompactionSettings, focus string,
) (*compaction.Result, []core.AgentMessage, core.Model, string, error) {
	result, compacted, err := compaction.Compact(ctx, sumProvider, sumModel, sumOpts, msgs, tokens, window, settings, focus)
	if err == nil || sumModel.ID == sessModel.ID || ctx.Err() != nil {
		return result, compacted, sumModel, notice, err
	}
	reason := transientProviderReason(err)
	if reason == "" {
		return result, compacted, sumModel, notice, err
	}
	slog.Warn("compaction summarizer unavailable; retrying with the session model",
		"summarizer", sumModel.ID, "session_model", sessModel.ID, "reason", reason, "error", err)
	result, compacted, err = compaction.Compact(ctx, sessProvider, sessModel, sessOpts, msgs, tokens, window, settings, focus)
	return result, compacted, sessModel, core.CompactModelUnavailableNotice(sumModel, sessModel, reason), err
}
