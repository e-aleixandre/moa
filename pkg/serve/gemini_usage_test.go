package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/provider/antigravity"
	"github.com/e-aleixandre/moa/pkg/usage"
)

// Given agy signed in, an ordinary /api/usage poll never runs agy; the Usage
// page's ?refresh=antigravity runs it once and the answer carries Gemini's
// 5h and weekly used percent and renewal, as any provider's quotas.
func TestGeminiUsageReadOnlyWhenAsked(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wire := filepath.Join(t.TempDir(), "wire.log")
	t.Setenv("MOA_AGY_BIN", exe)
	t.Setenv("FAKE_AGY", "1")
	t.Setenv("FAKE_AGY_LOG", wire)
	mgr := &Manager{usagePoller: &usage.MultiPoller{OnDemand: map[string]*usage.Poller{"antigravity": usage.NewProviderPoller(
		func(context.Context) (string, bool, error) { return "", true, nil },
		func(ctx context.Context, _ *http.Client, _ string) (*usage.Snapshot, error) {
			return antigravity.ReadUsage(ctx)
		})}}}
	get := func(path string) usageResponse {
		res := httptest.NewRecorder()
		handleUsage(mgr).ServeHTTP(res, httptest.NewRequest(http.MethodGet, path, nil))
		var body usageResponse
		if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	runs := func() int {
		b, _ := os.ReadFile(wire)
		return strings.Count(string(b), `{"print":["-p","/usage","--output-format","json"]}`)
	}

	if body := get("/api/usage"); runs() != 0 || body.Providers["antigravity"] != nil || body.ProviderStatus["antigravity"].Reason != "pending" {
		t.Fatalf("an ordinary poll ran agy (%d) or invented a reading: %+v", runs(), body.ProviderStatus)
	}
	body := get("/api/usage?refresh=antigravity")
	get("/api/usage?refresh=antigravity")
	if runs() != 1 {
		t.Fatalf("agy ran %d times, want once within the cache", runs())
	}
	s := body.Providers["antigravity"]
	if s == nil || len(s.Quotas) != 2 {
		t.Fatalf("antigravity usage = %+v (%+v)", s, body.ProviderStatus)
	}
	if q := s.Quotas[0]; q.PeriodKind != "five_hour" || *q.Utilization < 60 || *q.Utilization > 62 || q.PeriodEnd == nil {
		t.Errorf("5h = %+v", q)
	}
	if q := s.Quotas[1]; q.PeriodKind != "week" || *q.Utilization < 15 || *q.Utilization > 17 || q.PeriodEnd == nil {
		t.Errorf("week = %+v", q)
	}
	if !body.Available {
		t.Error("available = false with a Gemini reading")
	}
}

func TestGeminiUsageSignedOutIsQuiet(t *testing.T) {
	exe, _ := os.Executable()
	t.Setenv("MOA_AGY_BIN", exe)
	t.Setenv("FAKE_AGY", "1")
	t.Setenv("FAKE_AGY_USAGE", "signed_out")
	mgr := &Manager{usagePoller: &usage.MultiPoller{OnDemand: map[string]*usage.Poller{"antigravity": usage.NewProviderPoller(
		func(context.Context) (string, bool, error) { return "", true, nil },
		func(ctx context.Context, _ *http.Client, _ string) (*usage.Snapshot, error) {
			return antigravity.ReadUsage(ctx)
		})}}}
	res := httptest.NewRecorder()
	handleUsage(mgr).ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/usage?refresh=antigravity", nil))
	var body usageResponse
	_ = json.NewDecoder(res.Body).Decode(&body)
	if res.Code != http.StatusOK || body.ProviderStatus["antigravity"].Reason != "signed_out" || body.Error != "" {
		t.Errorf("signed out: %d %+v %q", res.Code, body.ProviderStatus, body.Error)
	}
}
