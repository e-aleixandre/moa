package anthropic

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestReviewContradictoryDuplicateGlobalIsNotReliable(t *testing.T) {
	observed := time.Date(2026, 10, 20, 15, 0, 0, 0, time.UTC)
	reset := observed.Add(2 * time.Hour)
	h := http.Header{}
	h.Set("anthropic-ratelimit-unified-5h-status", "rejected")
	h.Set("anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(reset.Unix(), 10))
	h.Add("anthropic-ratelimit-unified-reset", strconv.FormatInt(reset.Unix(), 10))
	h.Add("anthropic-ratelimit-unified-reset", strconv.FormatInt(reset.Add(time.Hour).Unix(), 10))
	w := planQuotaTiming(h, "five_hour", observed, time.Second)
	if w.ResetAt != nil {
		t.Fatalf("contradictory duplicate global resets published a reliable ResetAt=%s NextAttemptAt=%s", w.ResetAt, w.NextAttemptAt)
	}
}
