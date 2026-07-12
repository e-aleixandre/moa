package openai

import (
	"net/http"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

// Real 429 body captured live from chatgpt.com/backend-api/codex/responses when
// the 5h primary window was exhausted (see tmp-evidence/openai-429-*).
const realUsageLimitBody = `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"prolite","resets_at":1783872321,"eligible_promo":null,"resets_in_seconds":9399}}`

func TestIsUsageLimitBody(t *testing.T) {
	if !isUsageLimitBody([]byte(realUsageLimitBody)) {
		t.Fatal("real usage_limit_reached body must be recognized")
	}
	// A transient rate limit (different type) or arbitrary JSON must not match.
	for _, b := range []string{
		`{"error":{"type":"rate_limit_exceeded","message":"slow down"}}`,
		`{"detail":"Store must be set to false"}`,
		`not json`,
		``,
	} {
		if isUsageLimitBody([]byte(b)) {
			t.Fatalf("body should NOT be usage-limit: %q", b)
		}
	}
}

func TestQuotaErrorFrom_HeadersPrimaryWindow(t *testing.T) {
	// Mirror the real 429 headers: primary (5h) window at 100%.
	resp := &http.Response{StatusCode: 429, Header: http.Header{}}
	resp.Header.Set("x-codex-plan-type", "prolite")
	resp.Header.Set("x-codex-primary-used-percent", "100")
	resp.Header.Set("x-codex-secondary-used-percent", "51")
	resp.Header.Set("x-codex-primary-window-minutes", "300")
	resp.Header.Set("x-codex-secondary-window-minutes", "10080")
	resp.Header.Set("x-codex-primary-reset-after-seconds", "9400")
	resp.Header.Set("x-codex-primary-reset-at", "1783872322")

	qe := quotaErrorFrom(resp, []byte(realUsageLimitBody))
	if qe.Provider != "openai" {
		t.Fatalf("provider = %q, want openai", qe.Provider)
	}
	if qe.Window != "5h" {
		t.Fatalf("window = %q, want 5h", qe.Window)
	}
	if qe.PlanType != "prolite" {
		t.Fatalf("planType = %q, want prolite", qe.PlanType)
	}
	// Header reset (9400s) is preferred over the body's resets_in_seconds (9399).
	if qe.ResetsIn != 9400*time.Second {
		t.Fatalf("resetsIn = %s, want 9400s", qe.ResetsIn)
	}
	if qe.ResetsAt.Unix() != 1783872322 {
		t.Fatalf("resetsAt = %d, want 1783872322", qe.ResetsAt.Unix())
	}
	// It must be recognized as a typed quota error through errors.As.
	if _, ok := core.AsQuotaExceeded(qe); !ok {
		t.Fatal("quotaErrorFrom result must be a *core.QuotaExceededError")
	}
}

func TestQuotaErrorFrom_SecondaryWindow(t *testing.T) {
	// Weekly (secondary) window exhausted, primary fine.
	resp := &http.Response{StatusCode: 429, Header: http.Header{}}
	resp.Header.Set("x-codex-primary-used-percent", "40")
	resp.Header.Set("x-codex-secondary-used-percent", "100")
	resp.Header.Set("x-codex-secondary-window-minutes", "10080")
	resp.Header.Set("x-codex-secondary-reset-after-seconds", "493148")

	qe := quotaErrorFrom(resp, []byte(realUsageLimitBody))
	if qe.Window != "weekly" {
		t.Fatalf("window = %q, want weekly", qe.Window)
	}
	if qe.ResetsIn != 493148*time.Second {
		t.Fatalf("resetsIn = %s, want 493148s", qe.ResetsIn)
	}
}

func TestQuotaErrorFrom_BothWindowsExhaustedPicksLaterReset(t *testing.T) {
	// Both 5h (primary) and weekly (secondary) at 100%: the later-resetting
	// window (weekly) is the binding constraint and must be reported.
	resp := &http.Response{StatusCode: 429, Header: http.Header{}}
	resp.Header.Set("x-codex-primary-used-percent", "100")
	resp.Header.Set("x-codex-secondary-used-percent", "100")
	resp.Header.Set("x-codex-primary-window-minutes", "300")
	resp.Header.Set("x-codex-secondary-window-minutes", "10080")
	resp.Header.Set("x-codex-primary-reset-after-seconds", "9400")
	resp.Header.Set("x-codex-secondary-reset-after-seconds", "493148")

	qe := quotaErrorFrom(resp, []byte(realUsageLimitBody))
	if qe.Window != "weekly" {
		t.Fatalf("window = %q, want weekly (the later-resetting binding window)", qe.Window)
	}
	if qe.ResetsIn != 493148*time.Second {
		t.Fatalf("resetsIn = %s, want 493148s (weekly)", qe.ResetsIn)
	}
}

func TestQuotaErrorFrom_MissingWindowMinutesNoFalseLabel(t *testing.T) {
	// used-percent present but window-minutes absent: the window label must be
	// empty, never "-1m".
	resp := &http.Response{StatusCode: 429, Header: http.Header{}}
	resp.Header.Set("x-codex-primary-used-percent", "100")
	resp.Header.Set("x-codex-primary-reset-after-seconds", "9400")

	qe := quotaErrorFrom(resp, []byte(realUsageLimitBody))
	if qe.Window != "" {
		t.Fatalf("window = %q, want empty (no window-minutes header)", qe.Window)
	}
}

func TestQuotaErrorFrom_BodyFallbackNoHeaders(t *testing.T) {
	// API-key path: no x-codex-* headers, fall back to the body's reset fields.
	resp := &http.Response{StatusCode: 429, Header: http.Header{}}
	qe := quotaErrorFrom(resp, []byte(realUsageLimitBody))
	if qe.ResetsIn != 9399*time.Second {
		t.Fatalf("resetsIn = %s, want 9399s (from body)", qe.ResetsIn)
	}
	if qe.ResetsAt.Unix() != 1783872321 {
		t.Fatalf("resetsAt = %d, want 1783872321 (from body)", qe.ResetsAt.Unix())
	}
	if qe.Message != "The usage limit has been reached" {
		t.Fatalf("message = %q", qe.Message)
	}
}
