package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// This RED replays raw captured headers, not the display parser's clamped values.
// Source: live-smoke-sanitized.json, SHA-256 14e7187c71eb5ecb19810bef4964c63898e66067179ff6099509e9e12325e40a.
const weeklyGateCapturedResponse = `{
  "status": 429,
  "retry_after": "65525",
  "response_utc": "2026-10-07T15:47:54.515025316Z",
  "error": {
    "message": "This request would exceed your account's rate limit. Please try again later.",
    "type": "rate_limit_error"
  },
  "rate_limit_headers": {
    "anthropic-ratelimit-unified-5h-reset": ["1791405600"],
    "anthropic-ratelimit-unified-5h-status": ["allowed"],
    "anthropic-ratelimit-unified-5h-utilization": ["0.0"],
    "anthropic-ratelimit-unified-7d-reset": ["1791453600"],
    "anthropic-ratelimit-unified-7d-status": ["rejected"],
    "anthropic-ratelimit-unified-7d-surpassed-threshold": ["1.0"],
    "anthropic-ratelimit-unified-7d-utilization": ["1.0"],
    "anthropic-ratelimit-unified-fallback-percentage": ["0.5"],
    "anthropic-ratelimit-unified-overage-disabled-reason": ["[redacted]"],
    "anthropic-ratelimit-unified-overage-status": ["rejected"],
    "anthropic-ratelimit-unified-representative-claim": ["seven_day"],
    "anthropic-ratelimit-unified-reset": ["1791453600"],
    "anthropic-ratelimit-unified-status": ["rejected"]
  }
}`

const weeklyGateDummyOAuth = "sk-ant-oat-weekly-gate-dummy-not-a-credential"

type weeklyGateMemoryTransport struct {
	headers http.Header
	body    string
	status  int
	calls   int
	closed  int
}

func (tr *weeklyGateMemoryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	tr.calls++
	if r.Method != http.MethodPost || r.URL.String() != "https://api.anthropic.com/v1/messages" {
		return nil, fmt.Errorf("weekly gate: rejected unexpected endpoint/method")
	}
	if r.Header.Get("Authorization") != "Bearer "+weeklyGateDummyOAuth || r.Header.Get("X-API-Key") != "" || r.Header.Get("Proxy-Authorization") != "" {
		return nil, fmt.Errorf("weekly gate: rejected non-dummy OAuth authentication")
	}
	defer r.Body.Close() //nolint:errcheck
	var body struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
		Speed  string `json:"speed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("weekly gate: decode request: %w", err)
	}
	if body.Model != "claude-opus-5-5" || !body.Stream || body.Speed != "" || strings.Contains(r.Header.Get("anthropic-beta"), "fast-mode-") {
		return nil, fmt.Errorf("weekly gate: rejected unexpected request mode/model")
	}
	return &http.Response{
		StatusCode: tr.status,
		Header:     tr.headers.Clone(),
		Body:       &weeklyGateReplayBody{Reader: strings.NewReader(tr.body), transport: tr},
		Request:    r,
	}, nil
}

type weeklyGateReplayBody struct {
	io.Reader
	transport *weeklyGateMemoryTransport
	closed    bool
}

func (b *weeklyGateReplayBody) Close() error {
	if !b.closed {
		b.closed = true
		b.transport.closed++
	}
	return nil
}

type weeklyGateRejectTransport struct{}

func (weeklyGateRejectTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("weekly gate: default transport/network is forbidden")
}

func TestWeeklyGateObservedOAuthSevenDayBeforeRetrySleep(t *testing.T) {
	// No credential resolver, store, proxy, socket, or httptest server is used.
	originalTransport := http.DefaultTransport
	http.DefaultTransport = weeklyGateRejectTransport{}
	defer func() { http.DefaultTransport = originalTransport }()

	var captured struct {
		Status   int                 `json:"status"`
		Retry    string              `json:"retry_after"`
		Error    json.RawMessage     `json:"error"`
		Headers  map[string][]string `json:"rate_limit_headers"`
		Observed string              `json:"response_utc"`
	}
	if err := json.Unmarshal([]byte(weeklyGateCapturedResponse), &captured); err != nil {
		t.Fatal(err)
	}
	if _, err := time.Parse(time.RFC3339Nano, captured.Observed); err != nil {
		t.Fatal(err)
	}
	headers := make(http.Header)
	for name, values := range captured.Headers {
		for _, value := range values {
			headers.Add(name, value)
		}
	}
	headers.Set("Retry-After", captured.Retry)

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		tr := &weeklyGateMemoryTransport{
			headers: headers,
			body:    `{"error":` + string(captured.Error) + `}`,
			status:  captured.Status,
		}
		a := NewWithKind(weeklyGateDummyOAuth, true).WithHTTPClient(&http.Client{
			Transport: tr,
			Timeout:   10 * time.Minute,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("weekly gate: redirects are forbidden")
			},
		})
		type outcome struct {
			ch  <-chan core.AssistantEvent
			err error
		}
		done := make(chan outcome, 1)
		started := time.Now()
		go func() {
			ch, err := a.Stream(ctx, core.Request{
				Model:    core.Model{ID: "claude-opus-5-5", Provider: "anthropic"},
				Messages: []core.Message{core.NewUserMessage("offline weekly gate")},
				Options:  core.StreamOptions{Fast: false, ThinkingLevel: "off"},
			})
			done <- outcome{ch: ch, err: err}
		}()
		synctest.Wait()
		if elapsed := time.Since(started); elapsed != 0 {
			t.Errorf("virtual time advanced before gate assertion: %s", elapsed)
		}
		select {
		case got := <-done:
			quota, ok := core.AsQuotaExceeded(got.err)
			if !ok || got.ch != nil {
				t.Errorf("expected pre-stream typed QuotaExceededError, got channel=%v error=%v", got.ch != nil, got.err)
			} else if quota.Provider != "anthropic" || quota.Window != "weekly" {
				t.Errorf("wrong quota attribution: provider=%q window=%q", quota.Provider, quota.Window)
			}
		default:
			t.Errorf("weekly quota still blocked before first retry sleep: fakeRequests=%d virtualElapsed=%s; want typed QuotaExceededError now", tr.calls, time.Since(started))
			cancel()
			synctest.Wait()
			select {
			case got := <-done:
				if got.ch != nil || !errors.Is(got.err, context.Canceled) {
					t.Errorf("cleanup: expected cancellation with no stream, got channel=%v error=%v", got.ch != nil, got.err)
				}
			default:
				t.Fatal("cleanup: adapter goroutine did not exit after cancellation")
			}
		}
		if tr.calls != 1 || tr.closed != 1 {
			t.Errorf("expected exactly one fake request and one closed response, got %d/%d", tr.calls, tr.closed)
		}
		t.Logf("cleanup: fakeRequests=%d closedResponses=%d virtualElapsed=%s; no reset guarantee asserted", tr.calls, tr.closed, time.Since(started))
	})
}
