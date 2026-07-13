package serve

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
)

type realtimeRoundTripper func(*http.Request) (*http.Response, error)

func (f realtimeRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRealtimeClientSecretDeviceOnlySchemaAndNoStore(t *testing.T) {
	if !deviceStoreLockSupported() {
		t.Skip("device auth fails closed where advisory process locks are unavailable")
	}
	var gotSafety string
	client := &http.Client{Transport: realtimeRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.openai.com/v1/realtime/client_secrets" || r.Header.Get("Authorization") != "Bearer permanent-key" {
			t.Fatalf("unexpected upstream request: %s authorization=%q", r.URL, r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["expires_after"].(map[string]any)["seconds"] != float64(60) || body["session"].(map[string]any)["model"] != realtimeModel {
			t.Fatalf("unexpected upstream schema: %#v", body)
		}
		gotSafety, _ = body["session"].(map[string]any)["safety_identifier"].(string)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"value":"ephemeral-secret","expires_at":4102444800}`))}, nil
	})}
	mgr := newTestManagerWithConfig(t, context.Background(), newMockProvider(simpleResponseHandler("ok")), t.TempDir(), core.MoaConfig{DisableSandbox: true})
	h := NewServer(mgr, WithAuthToken("owner", false), WithDeviceStorePath(filepath.Join(t.TempDir(), "devices.json")), WithRealtimeClientSecretBroker(func() (string, bool) { return "permanent-key", true }, client))
	device := pulseOperationDevice(t, h, &http.Cookie{Name: authCookieName, Value: "owner"}, "phone")

	ownerReq := httptest.NewRequest(http.MethodPost, "/api/pulse/realtime/client-secret", strings.NewReader(`{}`))
	ownerReq.Host, ownerReq.RemoteAddr = "localhost", "127.0.0.1:12345"
	ownerReq.Header.Set("Content-Type", "application/json")
	ownerReq.Header.Set("X-Moa-Request", "1")
	ownerReq.AddCookie(&http.Cookie{Name: authCookieName, Value: "owner"})
	owner := httptest.NewRecorder()
	h.ServeHTTP(owner, ownerReq)
	if owner.Code != http.StatusForbidden {
		t.Fatalf("owner mint = %d", owner.Code)
	}
	rec := pulseOperationRequest(h, http.MethodPost, "/api/pulse/realtime/client-secret", `{}`, device.Credential)
	if rec.Code != http.StatusCreated {
		t.Fatalf("mint = %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" || gotSafety == "" || strings.Contains(gotSafety, device.DeviceID) {
		t.Fatalf("unsafe response/safety id: cache=%q safety=%q", rec.Header().Get("Cache-Control"), gotSafety)
	}
	if strings.Contains(rec.Body.String(), "permanent-key") {
		t.Fatalf("permanent key leaked")
	}
	var response struct {
		ClientSecret string `json:"client_secret"`
		Transport    string `json:"transport"`
		Endpoint     string `json:"endpoint"`
		Model        string `json:"model"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.ClientSecret != "ephemeral-secret" || response.Transport != "websocket" || response.Endpoint != realtimeEndpoint || response.Model != realtimeModel {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestRealtimeClientSecretRejectsClientParametersAndUnavailableKey(t *testing.T) {
	if !deviceStoreLockSupported() {
		t.Skip("device auth fails closed where advisory process locks are unavailable")
	}
	mgr := newTestManagerWithConfig(t, context.Background(), newMockProvider(simpleResponseHandler("ok")), t.TempDir(), core.MoaConfig{DisableSandbox: true})
	h := NewServer(mgr, WithAuthToken("owner", false), WithDeviceStorePath(filepath.Join(t.TempDir(), "devices.json")), WithRealtimeClientSecretBroker(func() (string, bool) { return "", false }, nil))
	device := pulseOperationDevice(t, h, &http.Cookie{Name: authCookieName, Value: "owner"}, "phone")
	if rec := pulseOperationRequest(h, http.MethodPost, "/api/pulse/realtime/client-secret", `{"model":"attacker"}`, device.Credential); rec.Code != http.StatusBadRequest {
		t.Fatalf("parameters = %d", rec.Code)
	}
	if rec := pulseOperationRequest(h, http.MethodPost, "/api/pulse/realtime/client-secret", `{}`, device.Credential); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing key = %d", rec.Code)
	}
}
