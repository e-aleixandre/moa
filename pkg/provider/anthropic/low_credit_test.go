package anthropic

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestLowCreditClassification(t *testing.T) {
	const credit = `{"error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing."}}`
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"credit400", 400, credit, true},
		{"short_credit400", 400, `{"error":{"type":"invalid_request_error","message":"Your credit balance is too low"}}`, true},
		{"generic400", 400, `{"error":{"type":"invalid_request_error","message":"unrelated invalid request"}}`, false},
		{"quoted_credit400", 400, `{"error":{"type":"invalid_request_error","message":"input contains Your credit balance is too low"}}`, false},
		{"wrong_type400", 400, `{"error":{"type":"other_error","message":"Your credit balance is too low"}}`, false},
		{"malformed400", 400, `Your credit balance is too low`, false},
		{"credit401", 401, credit, false},
		{"credit403", 403, credit, false},
		{"credit429", 429, credit, false},
		{"credit500", 500, credit, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			p := NewWithBaseURL("fake-api-key", srv.URL)
			req := prewarmRequest()
			req.Options.AnthropicWireProfile = "oauth"
			req.Options.OnProviderRetry = func(context.Context, core.ProviderWait) error { return errors.New("no test retry") }
			_, streamErr := p.Stream(context.Background(), req)
			_, pingErr := p.Prewarm(context.Background(), req)
			for _, err := range []error{streamErr, pingErr} {
				if err == nil || errors.Is(fmt.Errorf("wrapped: %w", err), core.ErrAnthropicLowCredit) != tc.want {
					t.Errorf("low credit=%v want=%v err=%v", errors.Is(err, core.ErrAnthropicLowCredit), tc.want, err)
				}
			}
			var pe *PrewarmError
			if !errors.As(pingErr, &pe) || pe.Status != tc.status {
				t.Fatalf("lost prewarm error contract: %v", pingErr)
			}
		})
	}
}
