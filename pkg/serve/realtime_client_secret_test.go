package serve

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

type realtimeRoundTripper func(*http.Request) (*http.Response, error)

func (f realtimeRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const realtimeBrokerCredentialContractFixture = `{"client_secret":"ek_test-secret","expires_at":1900000000,"transport":"websocket","endpoint":"wss://api.openai.com/v1/realtime?model=gpt-realtime-2.1-mini","model":"gpt-realtime-2.1-mini"}`

func TestRealtimeBrokerCredentialContractFixture(t *testing.T) {
	var fixture struct {
		Endpoint string `json:"endpoint"`
		Model    string `json:"model"`
	}
	if err := json.Unmarshal([]byte(realtimeBrokerCredentialContractFixture), &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Endpoint != realtimeEndpoint || fixture.Model != realtimeModel {
		t.Fatalf("fixture does not match broker policy: %#v", fixture)
	}
}

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
		expires := time.Now().Add(time.Minute).Unix()
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"value":"ephemeral-secret","expires_at":%d}`, expires)))}, nil
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
	if response.ClientSecret != "ephemeral-secret" || response.Transport != "websocket" || response.Endpoint != "wss://api.openai.com/v1/realtime?model=gpt-realtime-2.1-mini" || response.Model != "gpt-realtime-2.1-mini" {
		t.Fatalf("unexpected response: %#v", response)
	}
	if response.Endpoint != realtimeEndpoint || response.Model != realtimeModel {
		t.Fatalf("broker DTO drifted from realtime policy: %#v", response)
	}
}

func TestMintRealtimeClientSecretRejectsUnsafeUpstreamResponses(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	for name, body := range map[string]string{
		"malformed":     `{`,
		"trailing JSON": `{"value":"secret","expires_at":1783944060} {}`,
		"missing value": `{"expires_at":1783944060}`,
		"expired":       `{"value":"secret","expires_at":1783943999}`,
		"too far":       `{"value":"secret","expires_at":1783944066}`,
		"oversize":      strings.Repeat("x", realtimeResponseLimit+1),
	} {
		t.Run(name, func(t *testing.T) {
			client := &http.Client{Transport: realtimeRoundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			if _, _, status, _ := mintRealtimeClientSecret(context.Background(), client, "key", "safety", func() time.Time { return now }); status == 0 {
				t.Fatal("unsafe response was accepted")
			}
		})
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

func TestRealtimeClientSecretSecurityBoundaryResponsesAreNoStore(t *testing.T) {
	mgr := newTestManagerWithConfig(t, context.Background(), newMockProvider(simpleResponseHandler("ok")), t.TempDir(), core.MoaConfig{DisableSandbox: true})
	h := NewServer(mgr, WithAuthToken("owner", false), WithDeviceStorePath(filepath.Join(t.TempDir(), "devices.json")))
	for name, mutate := range map[string]func(*http.Request){
		"missing authentication": func(*http.Request) {},
		"bad host":               func(r *http.Request) { r.Host = "attacker.example" },
		"missing csrf":           func(r *http.Request) { r.Header.Set("Authorization", "Moa-Device invalid.credential") },
	} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/pulse/realtime/client-secret", strings.NewReader(`{}`))
			r.Host, r.RemoteAddr = "localhost", "127.0.0.1:1"
			r.Header.Set("Content-Type", "application/json")
			mutate(r)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestRealtimeClientSecretDeviceTransportAndRequestShape(t *testing.T) {
	if !deviceStoreLockSupported() {
		t.Skip("device auth fails closed where advisory process locks are unavailable")
	}
	mgr := newTestManagerWithConfig(t, context.Background(), newMockProvider(simpleResponseHandler("ok")), t.TempDir(), core.MoaConfig{DisableSandbox: true})
	h := NewServer(mgr, WithAuthToken("owner", false), WithDeviceStorePath(filepath.Join(t.TempDir(), "devices.json")))
	device := pulseOperationDevice(t, h, &http.Cookie{Name: authCookieName, Value: "owner"}, "phone")
	request := func(body string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/pulse/realtime/client-secret", strings.NewReader(body))
		r.Host, r.RemoteAddr = "localhost", "127.0.0.1:1"
		r.Header.Set("Authorization", deviceAuthorizationScheme+" "+device.Credential)
		r.Header.Set("X-Moa-Request", "1")
		r.Header.Set("Content-Type", "application/json")
		return r
	}
	for name, change := range map[string]func(*http.Request){
		"wrong media":   func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") },
		"multiple json": func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader(`{} {}`)) },
		"oversize": func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(strings.Repeat(" ", realtimeClientSecretBodyLimit+1)))
		},
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, func() *http.Request { r := request(`{}`); change(r); return r }())
			if rec.Code < 400 || rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d cache=%q", rec.Code, rec.Header().Get("Cache-Control"))
			}
		})
	}
	query := httptest.NewRequest(http.MethodPost, "/api/pulse/realtime/client-secret?x=1", strings.NewReader(`{}`))
	query.Host, query.RemoteAddr = "localhost", "127.0.0.1:1"
	query.Header.Set("Authorization", deviceAuthorizationScheme+" "+device.Credential)
	query.Header.Set("X-Moa-Request", "1")
	query.Header.Set("Content-Type", "application/json")
	queryRec := httptest.NewRecorder()
	h.ServeHTTP(queryRec, query)
	if queryRec.Code != http.StatusBadRequest {
		t.Fatalf("query status=%d body=%q url=%q", queryRec.Code, queryRec.Body.String(), query.URL.String())
	}
	r := request(`{}`)
	r.RemoteAddr = "192.0.2.1:1"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUpgradeRequired {
		t.Fatalf("non-TLS remote device status = %d", rec.Code)
	}
	r = request(`{}`)
	r.RemoteAddr, r.TLS = "192.0.2.1:1", &tls.ConnectionState{}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("TLS device status = %d", rec.Code)
	}
}

func TestRealtimeAdmissionLimitsAndRetry(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	a := newRealtimeAdmission()
	a.now = func() time.Time { return now }
	for i := 0; i < 4; i++ {
		if _, ok := a.acquire(fmt.Sprintf("d%d", i)); !ok {
			t.Fatal("unexpected concurrent admission rejection")
		}
	}
	if retry, ok := a.acquire("other"); ok || retry != 60 {
		t.Fatalf("concurrency admission = (%d, %v)", retry, ok)
	}
	for i := 0; i < 4; i++ {
		a.release()
	}
	a = newRealtimeAdmission()
	a.now = func() time.Time { return now }
	for i := 0; i < 6; i++ {
		if _, ok := a.acquire("one"); !ok {
			t.Fatal("per-device setup rejected")
		}
		a.release()
	}
	if _, ok := a.acquire("one"); ok {
		t.Fatal("per-device limit was not enforced")
	}
	a = newRealtimeAdmission()
	a.now = func() time.Time { return now }
	for i := 0; i < 30; i++ {
		if _, ok := a.acquire(fmt.Sprintf("g%d", i)); !ok {
			t.Fatal("global setup rejected")
		}
		a.release()
	}
	if _, ok := a.acquire("last"); ok {
		t.Fatal("global limit was not enforced")
	}
}

func TestRealtimeSecretFinalDeliverySharesRevocationBoundary(t *testing.T) {
	if !deviceStoreLockSupported() {
		t.Skip("device auth fails closed where advisory process locks are unavailable")
	}
	store, err := openDeviceStore(filepath.Join(t.TempDir(), "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	id, err := newDeviceID()
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.state.Devices = append(store.state.Devices, durableDevice{ID: id, ExpiresAt: time.Now().Add(time.Hour)})
	if err := store.saveLocked(); err != nil {
		store.mu.Unlock()
		t.Fatal(err)
	}
	store.mu.Unlock()

	entered, release, revoked := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_ = store.withActiveDevice(id, func() error { close(entered); <-release; return nil })
	}()
	<-entered
	go func() { revoked <- store.revoke(id, "owner") }()
	select {
	case err := <-revoked:
		t.Fatalf("revoke crossed final delivery boundary early: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-revoked; err != nil {
		t.Fatal(err)
	}
	if err := store.withActiveDevice(id, func() error { return nil }); err == nil {
		t.Fatal("revoked device passed final delivery boundary")
	}
}
