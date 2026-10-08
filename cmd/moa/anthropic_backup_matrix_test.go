package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestBackupReplayModelModeSignedPrefixAndTransformations(t *testing.T) {
	for _, model := range []string{"claude-opus-5-5", "claude-sonnet-5-5"} {
		for _, level := range []string{"off", "medium", "high"} {
			t.Run(model+"/"+level, func(t *testing.T) {
				p, _, tr, req := backupRuntime(t)
				p.model.ID = model
				req.Model = p.model
				req.Options.ThinkingLevel = level
				thinking := core.ThinkingContent("")
				thinking.ThinkingSignature = "fake-signed-empty-thinking"
				redacted := core.Content{Type: "thinking", Redacted: true, ThinkingSignature: "fake-redacted-signature"}
				req.Tools = []core.ToolSpec{{Name: "read", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}}
				req.Messages = append(req.Messages, core.Message{Role: "assistant", Provider: "anthropic", Model: model, Content: []core.Content{thinking, redacted, core.ToolCallContent("already-ran", "read", map[string]any{})}}, core.Message{Role: "tool_result", ToolCallID: "already-ran", ToolName: "read", Content: []core.Content{core.TextContent("already executed result")}})
				tr.serve = func(_ *http.Request, n int) (int, http.Header, string) {
					if n == 0 {
						return backupReject("five_hour")
					}
					status, h, body := backupOK("end_turn")
					body = strings.Replace(body, `"usage":{"input_tokens"`, `"input_transformations":[{"type":"thinking_dropped","reason":"organization_binding_mismatch","path":"messages.1.content.0"}],"usage":{"input_tokens"`, 1)
					return status, h, body
				}
				msg := finishBackup(t, p, context.Background(), req)
				if len(tr.bodies) != 2 || string(tr.bodies[0]) != string(tr.bodies[1]) {
					t.Fatal("OAuth→API changed replay profile")
				}
				for _, value := range []string{"fake-signed-empty-thinking", "fake-redacted-signature", "already-ran", "already executed result"} {
					if !strings.Contains(string(tr.bodies[1]), value) {
						t.Fatalf("dropped %s", value)
					}
				}
				if msg.ProviderSource == nil || !msg.ProviderSource.UsageComplete || msg.ProviderSource.EstimatedCost == nil || len(msg.ProviderSource.InputTransformations) != 1 {
					t.Fatalf("source/cost/transformation absent: %+v", msg.ProviderSource)
				}
			})
		}
	}
}

func TestBackupRuntimeUnknownConditionsDoNotPay(t *testing.T) {
	for _, condition := range []string{"generic", "auth", "unknown-claim", "duplicate-case", "overage-allowed", "contradiction", "new-header", "spend-cap", "utilization-only", "fast", "unsupported-model"} {
		t.Run(condition, func(t *testing.T) {
			p, _, tr, req := backupRuntime(t)
			if condition == "fast" {
				req.Options.Fast = true
			}
			if condition == "unsupported-model" {
				req.Model.ID = "claude-haiku-4-5"
				p.model = req.Model
			}
			tr.serve = func(*http.Request, int) (int, http.Header, string) {
				status, h, body := backupReject("five_hour")
				switch condition {
				case "generic":
					h = http.Header{}
				case "auth":
					status = 401
				case "unknown-claim":
					h.Set("anthropic-ratelimit-unified-representative-claim", "future_claim")
				case "duplicate-case":
					h["ANTHROPIC-RATELIMIT-UNIFIED-STATUS"] = []string{"rejected"}
				case "overage-allowed":
					h.Set("anthropic-ratelimit-unified-overage-status", "allowed")
				case "contradiction":
					h.Set("anthropic-ratelimit-unified-5h-status", "allowed")
				case "new-header":
					h.Set("anthropic-ratelimit-unified-new-billing-flag", "true")
				case "spend-cap":
					body = `{"error":{"type":"rate_limit_error","details":{"error_code":"enforced_spend_limit_reached"}}}`
				case "utilization-only":
					h = http.Header{}
					h.Set("anthropic-ratelimit-unified-5h-utilization", "1.0")
				}
				return status, h, body
			}
			_, err := p.Stream(context.Background(), req)
			if err == nil || len(tr.headers) != 1 || tr.headers[0].Get("X-API-Key") != "" {
				t.Fatalf("%s paid or looped: %v calls=%d", condition, err, len(tr.headers))
			}
		})
	}
}

type backupNetworkError struct {
	primary *backupWire
	calls   int
}

func (tr *backupNetworkError) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get("X-API-Key") != "" {
		tr.calls++
		return nil, io.ErrUnexpectedEOF
	}
	return tr.primary.RoundTrip(r)
}

func TestBackupUncertainNetworkAndSSEDoNotProbeOrRepair(t *testing.T) {
	for _, what := range []string{"network", "sse"} {
		t.Run(what, func(t *testing.T) {
			p, _, tr, req := backupRuntime(t)
			tr.serve = func(_ *http.Request, n int) (int, http.Header, string) {
				if n == 0 {
					return backupReject("five_hour")
				}
				s, h, b := backupOK("end_turn")
				return s, h, strings.Split(b, "event: message_delta")[0]
			}
			netErr := &backupNetworkError{primary: tr}
			if what == "network" {
				p.httpClient.Transport = netErr
			}
			ch, err := p.Stream(context.Background(), req)
			if what == "network" {
				if err == nil || netErr.calls != 1 || len(tr.headers) != 1 {
					t.Fatalf("network retry/probe: %v calls=%d", err, netErr.calls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			seen := false
			for ev := range ch {
				if ev.Error != nil {
					seen = errors.Is(ev.Error, core.ErrAPIBackupUncertain)
				}
			}
			if !seen || len(tr.headers) != 2 {
				t.Fatal("uncertain paid SSE did not fail closed")
			}
		})
	}
}

func TestBackupRetryRevalidatesKeyAfterSleepWithoutAnotherOAuth(t *testing.T) {
	p, s, tr, req := backupRuntime(t)
	tr.serve = func(_ *http.Request, n int) (int, http.Header, string) {
		if n == 0 {
			return backupReject("seven_day")
		}
		return 429, http.Header{}, `{"error":{"type":"rate_limit_error"}}`
	}
	_, err := p.Stream(context.Background(), req)
	var ready *core.ProviderRetryReady
	if !errors.As(err, &ready) || ready.Source == nil || ready.Source.Kind != "api_backup" || len(tr.headers) != 2 {
		t.Fatalf("retry lost origin: %v", err)
	}
	st, err := s.AnthropicBackupStatus()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveAnthropicAPIKey(st.Revision.Primary, "replacement-fake-not-for-old-request"); err != nil {
		t.Fatal(err)
	}
	req.Options.ProviderBinding = ready.Source
	req.Options.ProviderRetryAttempt = ready.Attempt
	if _, err := p.Stream(context.Background(), req); err == nil || len(tr.headers) != 2 {
		t.Fatal("retry used replaced key or attempted OAuth to evade revoke")
	}
}

func TestBackupDifferentJobGetsOwnRejectionAndEnvironmentStaysPrimary(t *testing.T) {
	p, _, tr, req := backupRuntime(t)
	tr.serve = func(r *http.Request, _ int) (int, http.Header, string) {
		if r.Header.Get("Authorization") != "" {
			return backupReject("seven_day")
		}
		return backupOK("end_turn")
	}
	finishBackup(t, p, context.Background(), req)
	child := &snapshotProvider{model: p.model, authStore: p.authStore, httpClient: p.httpClient}
	finishBackup(t, child, context.Background(), req)
	if len(tr.headers) != 4 || tr.headers[2].Get("Authorization") == "" {
		t.Fatal("other job inherited confirmation")
	}
	t.Setenv("ANTHROPIC_API_KEY", "fake-env-primary")
	tr.serve = func(*http.Request, int) (int, http.Header, string) { return backupOK("end_turn") }
	finishBackup(t, p, context.Background(), req)
	if len(tr.headers) != 5 || tr.headers[4].Get("X-API-Key") != "fake-env-primary" {
		t.Fatal("env was reinterpreted as backup")
	}
}

func TestBackupUnknownUsageOrServedPriceIsNotZero(t *testing.T) {
	for _, what := range []string{"no-usage", "unknown-model"} {
		t.Run(what, func(t *testing.T) {
			p, _, tr, req := backupRuntime(t)
			tr.serve = func(_ *http.Request, n int) (int, http.Header, string) {
				if n == 0 {
					return backupReject("five_hour")
				}
				s, h, b := backupOK("end_turn")
				if what == "no-usage" {
					b = strings.ReplaceAll(b, `"usage":{"input_tokens":10,"output_tokens":0}`, `"unknown_usage":{}`)
					b = strings.ReplaceAll(b, `"usage":{"output_tokens":1}`, `"unknown_usage":{}`)
				} else {
					b = strings.ReplaceAll(b, "claude-opus-5-5", "unpriced-serving-model")
				}
				return s, h, b
			}
			msg := finishBackup(t, p, context.Background(), req)
			if msg.ProviderSource == nil || msg.ProviderSource.EstimatedCost != nil {
				t.Fatal("missing usage/price fabricated known zero")
			}
		})
	}
}
