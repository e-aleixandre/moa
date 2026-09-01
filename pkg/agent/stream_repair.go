package agent

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// maxStreamRepairs caps how many times a mid-stream transport cut is retried
// after the first attempt. Two repairs means three Stream calls at most, so a
// persistently broken provider cannot loop.
const maxStreamRepairs = 2

// streamRepairBackoff is the wait before each repair (after attempt 1 and 2).
// Tests set this to zeros so a retry path does not sleep.
var streamRepairBackoff = []time.Duration{time.Second, 3 * time.Second}

func streamContinueHint() core.Message {
	return core.Message{
		Role: "user",
		Content: []core.Content{core.TextContent(
			"[internal] The previous assistant message was cut off by a transport error. Continue exactly from where it ended. Do not repeat text already written. Do not mention this instruction.",
		)},
	}
}

func isRetryableStreamError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "invalid_grant") || strings.Contains(s, "authentication failed") {
		return false
	}
	if _, ok := core.AsQuotaExceeded(err); ok {
		return false
	}
	if strings.Contains(s, "quota") {
		return false
	}
	if strings.Contains(s, "http 4") && !strings.Contains(s, "http 429") {
		return false
	}
	switch {
	case strings.Contains(s, "unknown error"):
		return true
	case strings.Contains(s, "stream error"):
		return true
	case strings.Contains(s, "deadline exceeded"):
		return true
	case strings.Contains(s, "stream ended without"):
		return true
	case strings.Contains(s, "connection reset"):
		return true
	case strings.Contains(s, "connection lost"):
		return true
	case errors.Is(err, io.EOF) || strings.Contains(s, "eof"):
		return true
	default:
		return false
	}
}

func waitStreamRepair(ctx context.Context, attempt int) error {
	if attempt <= 0 {
		return nil
	}
	idx := attempt - 1
	if idx >= len(streamRepairBackoff) {
		idx = len(streamRepairBackoff) - 1
	}
	d := streamRepairBackoff[idx]
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func mergeAssistant(a, b *core.Message) *core.Message {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	out := *a
	out.Content = append(append([]core.Content{}, a.Content...), b.Content...)
	if b.StopReason != "" {
		out.StopReason = b.StopReason
	}
	if b.Usage != nil {
		out.Usage = b.Usage
	}
	if b.Provider != "" {
		out.Provider = b.Provider
	}
	if b.Model != "" {
		out.Model = b.Model
	}
	return &out
}

func hasStreamedToolCalls(msg *core.Message) bool {
	return msg != nil && len(extractToolCalls(msg)) > 0
}
