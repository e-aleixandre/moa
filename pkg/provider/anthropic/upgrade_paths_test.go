package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func upgradePathsCapture(t *testing.T) (int, http.Header, string) {
	t.Helper()
	// Captured OAuth rejection from 2026-10-20; request identifiers omitted.
	data, err := os.ReadFile("testdata/weekly-upgrade-paths.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Status  int             `json:"status"`
		Headers http.Header     `json:"headers"`
		Body    json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	return capture.Status, capture.Headers, string(capture.Body)
}

func TestPlanUpgradePathsCapturedWeeklyBeforeSleep(t *testing.T) {
	status, headers, body := upgradePathsCapture(t)
	tr := &weeklyGateMemoryTransport{status: status, headers: headers, body: body}
	p := NewWithKind(weeklyGateDummyOAuth, true).WithHTTPClient(&http.Client{Transport: tr})
	_, err := p.Stream(context.Background(), core.Request{
		Model:    core.Model{ID: "claude-opus-5-5", Provider: "anthropic"},
		Messages: []core.Message{core.NewUserMessage("offline")},
		Options: core.StreamOptions{OnProviderRetry: func(_ context.Context, w core.ProviderWait) error {
			return &core.ProviderRetryReady{Attempt: w.Attempt, Wait: &w}
		}},
	})
	q, ok := core.AsQuotaExceeded(err)
	if !ok || q.Wait == nil || q.Wait.Scope != "seven_day" {
		t.Fatalf("captured weekly quota must be classified before retry sleep: %T %v", err, err)
	}
	if tr.calls != 1 || tr.closed != 1 {
		t.Fatalf("HTTP calls=%d closed responses=%d", tr.calls, tr.closed)
	}
}

func TestPlanUpgradePathsDoesNotBroadenQuotaGate(t *testing.T) {
	for _, condition := range []string{"unknown-header", "duplicate", "conflicting-case", "generic-429", "fast", "unauthorized", "forbidden", "api-key", "spend-cap"} {
		t.Run(condition, func(t *testing.T) {
			status, headers, body := upgradePathsCapture(t)
			oauth, fast := true, false
			switch condition {
			case "unknown-header":
				headers.Set(unifiedPrefix+"new-billing-flag", "true")
			case "duplicate":
				headers.Add(unifiedPrefix+"upgrade-paths", "overage")
			case "conflicting-case":
				headers["ANTHROPIC-RATELIMIT-UNIFIED-UPGRADE-PATHS"] = []string{"different"}
			case "generic-429":
				headers = http.Header{}
			case "fast":
				fast = true
			case "unauthorized":
				status = http.StatusUnauthorized
			case "forbidden":
				status = http.StatusForbidden
			case "api-key":
				oauth = false
			case "spend-cap":
				body = `{"error":{"type":"rate_limit_error","details":{"error_code":"enforced_spend_limit_reached"}}}`
			}
			if scope := planQuotaIdentity(headers, status, oauth, fast, weeklyReferenceRequest(t), []byte(body)); scope != "" {
				t.Fatalf("%s acquired plan quota identity: %s", condition, scope)
			}
		})
	}
}
