package anthropic

import (
	"context"
	"net/http"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestPlanFiveHourBeforeSleepSynthetic(t *testing.T) {
	// A product fixture, not a capture or evidence of current upstream state.
	h := http.Header{}
	for name, value := range map[string]string{"status": "rejected", "representative-claim": "five_hour", "overage-status": "rejected", "5h-status": "rejected", "7d-status": "allowed"} {
		h.Set(unifiedPrefix+name, value)
	}
	tr := &weeklyGateMemoryTransport{headers: h, status: http.StatusTooManyRequests, body: `{"error":{"type":"rate_limit_error"}}`}
	p := NewWithKind(weeklyGateDummyOAuth, true).WithHTTPClient(&http.Client{Transport: tr})
	_, err := p.Stream(context.Background(), core.Request{Model: core.Model{ID: "claude-opus-5-5", Provider: "anthropic"}, Messages: []core.Message{core.NewUserMessage("offline")}, Options: core.StreamOptions{OnProviderRetry: func(_ context.Context, w core.ProviderWait) error {
		return &core.ProviderRetryReady{Attempt: w.Attempt, Wait: &w}
	}}})
	q, ok := core.AsQuotaExceeded(err)
	if !ok || q.Wait == nil || q.Wait.Scope != "five_hour" {
		t.Fatalf("want 5h quota before sleep, got %T %v", err, err)
	}
	if tr.calls != 1 || tr.closed != 1 {
		t.Fatalf("calls=%d closed=%d", tr.calls, tr.closed)
	}
}
