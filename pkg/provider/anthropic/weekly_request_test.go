package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/e-aleixandre/moa/pkg/core"
)

type reviewRedirectTransport struct {
	status   int
	location string
	headers  http.Header
	calls    []string
}

func (tr *reviewRedirectTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	tr.calls = append(tr.calls, r.Method+" "+r.URL.String())
	if r.Body != nil {
		_ = r.Body.Close()
	}
	if len(tr.calls) == 1 {
		return &http.Response{StatusCode: tr.status, Header: http.Header{"Location": []string{tr.location}}, Body: io.NopCloser(strings.NewReader("redirect")), Request: r}, nil
	}
	return &http.Response{StatusCode: 429, Header: tr.headers.Clone(), Body: io.NopCloser(strings.NewReader(`{"error":{"type":"rate_limit_error"}}`)), Request: r}, nil
}
func TestReviewWeeklyPredicateUsesActualResponseRequest(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		location string
	}{
		{"redirected-GET-messages", 302, "https://api.anthropic.com/v1/messages"},
		{"redirected-POST-other-endpoint", 307, "https://api.anthropic.com/v1/complete"},
		{"redirected-POST-other-origin", 307, "https://offline.invalid/v1/messages"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var capture struct {
					Headers http.Header `json:"rate_limit_headers"`
				}
				if err := json.Unmarshal([]byte(weeklyGateCapturedResponse), &capture); err != nil {
					t.Fatal(err)
				}
				tr := &reviewRedirectTransport{status: tc.status, location: tc.location, headers: capture.Headers}
				p := NewWithKind("offline-redirect-dummy-not-real", true)
				// Keep the production client's redirect policy; replace only sockets.
				p.client.Transport = tr
				_, err := p.Stream(context.Background(), core.Request{Model: core.Model{ID: "claude-opus-5-5", Provider: "anthropic"}, Messages: []core.Message{core.NewUserMessage("offline")}, Options: core.StreamOptions{OnProviderRetry: func(_ context.Context, w core.ProviderWait) error {
					return &core.ProviderRetryReady{Attempt: w.Attempt, Wait: &w}
				}}})
				t.Logf("actual fake requests=%v result=%T/%v", tr.calls, err, err)
				if _, ok := core.AsQuotaExceeded(err); ok {
					t.Errorf("unsupported actual request acquired weekly quota policy")
				}
				var ready *core.ProviderRetryReady
				if !errors.As(err, &ready) || ready.Wait == nil || ready.Wait.Kind != "transport_retry" {
					t.Errorf("want finite generic retry, got %v", err)
				}
				if len(tr.calls) != 2 {
					t.Fatalf("unexpected admissions=%v", tr.calls)
				}
			})
		})
	}
}

func TestWeeklyMissingEffectiveRequestFailsClosed(t *testing.T) {
	var capture struct {
		Headers http.Header `json:"rate_limit_headers"`
	}
	if err := json.Unmarshal([]byte(weeklyGateCapturedResponse), &capture); err != nil {
		t.Fatal(err)
	}
	for _, request := range []*http.Request{nil, {Method: http.MethodPost}, {URL: weeklyReferenceRequest(t).URL}} {
		if weeklyIdentity(capture.Headers, http.StatusTooManyRequests, true, false, request) {
			t.Fatal("missing effective request context acquired weekly policy")
		}
	}
}
