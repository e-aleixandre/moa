package serve

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/push"
)

// nativeRelay stands in for relay/: it checks the signature over the exact
// bytes and opens the envelope with the device key, as the NSE would.
type nativeRelay struct {
	t    *testing.T
	keys push.DeviceKeys
	mu   sync.Mutex
	got  []push.EnvelopeContent
	raw  []string
}

func (f *nativeRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if r.URL.Path != "/v1/send" || r.Header.Get("X-Moa-Sig") != push.SignSend(f.keys.Send, body) {
		f.t.Errorf("relay got an unsigned or misrouted request: %s", r.URL)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req struct {
		E push.Envelope `json:"e"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		f.t.Error(err)
	}
	content, err := push.OpenEnvelope(f.keys, req.E, time.Now())
	if err != nil {
		f.t.Errorf("envelope does not open: %v", err)
	}
	f.mu.Lock()
	f.got = append(f.got, content)
	f.raw = append(f.raw, string(body))
	f.mu.Unlock()
	_, _ = io.WriteString(w, `{"ok":true}`)
}

func (f *nativeRelay) delivered() ([]push.EnvelopeContent, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]push.EnvelopeContent(nil), f.got...), append([]string(nil), f.raw...)
}

type nativeFixture struct {
	mgr      *Manager
	handler  http.Handler
	owner    *http.Cookie
	relay    *nativeRelay
	relayURL string
	secret   []byte
	store    string
}

// newNativeFixture wires native push the way serve does: a sender to the
// relay, a policy over it, the device store from NewServer.
func newNativeFixture(t *testing.T, provider core.Provider) *nativeFixture {
	t.Helper()
	if !deviceStoreLockSupported() {
		t.Skip("device auth fails closed where advisory process locks are unavailable")
	}
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i + 9)
	}
	keys, _ := push.DeriveDeviceKeys(secret)
	relay := &nativeRelay{t: t, keys: keys}
	srv := httptest.NewServer(relay)
	t.Cleanup(srv.Close)
	relayURL, err := push.NormalizeRelayURL(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	mgr := newTestManagerWithConfig(t, context.Background(), provider, t.TempDir(),
		core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"})
	storePath := filepath.Join(t.TempDir(), "push_native.json")
	store, err := push.NewNativeStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	mgr.pushNative = push.NewNativeSender(store, relayURL)
	mgr.pushPolicy = push.NewPolicy(mgr.pushNative, push.PolicyConfig{Inline: true})
	handler := NewServer(mgr, WithAuthToken("owner", false), WithDeviceStorePath(filepath.Join(t.TempDir(), "devices.json")))
	return &nativeFixture{mgr: mgr, handler: handler, owner: &http.Cookie{Name: authCookieName, Value: "owner"}, relay: relay, relayURL: relayURL, secret: secret, store: storePath}
}

func (f *nativeFixture) registration(relayURL string) string {
	return fmt.Sprintf(`{"relay_url":%q,"handle":%q,"expires_at":%d,"secret":%q,"env":"sandbox"}`,
		relayURL, strings.Repeat("H", 80), time.Now().Add(90*24*time.Hour).Unix(), base64.RawURLEncoding.EncodeToString(f.secret))
}

func TestNativePushRegistrationRequiresTheNativeHeader(t *testing.T) {
	f := newNativeFixture(t, newMockProvider(simpleResponseHandler("ok")))
	device := pairedDevice(t, f.handler, f.owner, "phone")
	body := f.registration(f.relayURL)

	if rec := pairingRequest(f.handler, http.MethodPost, "/api/push/native", body, nil, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d, want 401", rec.Code)
	}
	if rec := pairingRequest(f.handler, http.MethodPost, "/api/push/native", body, f.owner, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("owner token = %d, want 403", rec.Code)
	}
	// The WebView holds a device session cookie; it must not swap the keys.
	cookie := deviceBrowserSession(t, f.handler, device.Credential)
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		if rec := pairingRequest(f.handler, method, "/api/push/native", body, cookie, ""); rec.Code != http.StatusForbidden {
			t.Fatalf("%s with device cookie = %d, want 403", method, rec.Code)
		}
	}
	if rec := pairingRequest(f.handler, http.MethodGet, "/api/push/native/status", "", cookie, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("status with device cookie = %d, want 403", rec.Code)
	}
	if _, err := os.Stat(f.store); !os.IsNotExist(err) {
		t.Fatal("a refused registration touched the store")
	}

	if rec := pairingRequest(f.handler, http.MethodPost, "/api/push/native", body, nil, device.Credential); rec.Code != http.StatusNoContent {
		t.Fatalf("native register = %d: %s", rec.Code, rec.Body.String())
	}
	info, err := os.Stat(f.store)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("store not private: %v %v", info, err)
	}
	status := pairingRequest(f.handler, http.MethodGet, "/api/push/native/status", "", nil, device.Credential)
	if status.Code != http.StatusOK {
		t.Fatalf("status = %d", status.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(status.Body.Bytes(), &got)
	if got["registered"] != true || got["relay_url"] != f.relayURL || got["env"] != "sandbox" {
		t.Fatalf("status = %s", status.Body.String())
	}
	if strings.Contains(status.Body.String(), base64.RawURLEncoding.EncodeToString(f.secret)) || strings.Contains(status.Body.String(), "HHHH") {
		t.Fatal("status leaks the secret or the handle")
	}

	if rec := pairingRequest(f.handler, http.MethodDelete, "/api/push/native", "", nil, device.Credential); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", rec.Code)
	}
	if _, ok := f.mgr.pushNative.Store().Get(device.DeviceID); ok {
		t.Fatal("delete kept the registration")
	}
}

func TestNativePushRegistrationRejectsForeignRelayAndBadInput(t *testing.T) {
	f := newNativeFixture(t, newMockProvider(simpleResponseHandler("ok")))
	device := pairedDevice(t, f.handler, f.owner, "phone")
	good := f.registration(f.relayURL)
	secret := base64.RawURLEncoding.EncodeToString(f.secret)
	bad := map[string]string{
		"foreign relay":  f.registration("https://push.example.com"),
		"relay path":     f.registration(f.relayURL + "/v1"),
		"short secret":   strings.Replace(good, secret, secret[:20], 1),
		"bad env":        strings.Replace(good, `"sandbox"`, `"dev"`, 1),
		"handle chars":   strings.Replace(good, strings.Repeat("H", 80), strings.Repeat("H", 79)+"/", 1),
		"expired handle": fmt.Sprintf(`{"relay_url":%q,"handle":%q,"expires_at":%d,"secret":%q,"env":"sandbox"}`, f.relayURL, strings.Repeat("H", 80), time.Now().Add(-time.Minute).Unix(), secret),
		"far expiry":     fmt.Sprintf(`{"relay_url":%q,"handle":%q,"expires_at":%d,"secret":%q,"env":"sandbox"}`, f.relayURL, strings.Repeat("H", 80), time.Now().Add(200*24*time.Hour).Unix(), secret),
		"unknown field":  strings.Replace(good, `"env"`, `"extra":1,"env"`, 1),
	}
	for name, body := range bad {
		rec := pairingRequest(f.handler, http.MethodPost, "/api/push/native", body, nil, device.Credential)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, rec.Code)
		}
	}
	if len(f.mgr.pushNative.Store().All()) != 0 {
		t.Fatal("a rejected registration was stored")
	}
}

func TestNativePushRegistrationDiesWithTheDevice(t *testing.T) {
	f := newNativeFixture(t, newMockProvider(simpleResponseHandler("ok")))
	device := pairedDevice(t, f.handler, f.owner, "phone")
	if rec := pairingRequest(f.handler, http.MethodPost, "/api/push/native", f.registration(f.relayURL), nil, device.Credential); rec.Code != http.StatusNoContent {
		t.Fatalf("register = %d", rec.Code)
	}
	revoke := pairingRequest(f.handler, http.MethodPost, "/api/pulse/devices/"+device.DeviceID+"/revoke", `{}`, f.owner, "")
	if revoke.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d", revoke.Code)
	}
	if _, ok := f.mgr.pushNative.Store().Get(device.DeviceID); ok {
		t.Fatal("revoking the device kept its push registration")
	}
	if rec := pairingRequest(f.handler, http.MethodPost, "/api/push/native", f.registration(f.relayURL), nil, device.Credential); rec.Code != http.StatusUnauthorized {
		t.Fatalf("register after revoke = %d, want 401", rec.Code)
	}
	if len(f.mgr.pushNative.Store().All()) != 0 {
		t.Fatal("a revoked device registered again")
	}
}

func TestNativePushDropsRegistrationsOfUnknownDevicesAtStartup(t *testing.T) {
	if !deviceStoreLockSupported() {
		t.Skip("device auth fails closed where advisory process locks are unavailable")
	}
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	store, _ := push.NewNativeStore(filepath.Join(t.TempDir(), "push_native.json"))
	_ = store.Put(push.NativeRegistration{DeviceID: "gone", RelayURL: push.DefaultRelayURL, Handle: "h", ExpiresAt: time.Now().Add(time.Hour), Secret: "s", Env: "sandbox"})
	mgr.pushNative = push.NewNativeSender(store, push.DefaultRelayURL)
	NewServer(mgr, WithAuthToken("owner", false), WithDeviceStorePath(filepath.Join(t.TempDir(), "devices.json")))
	if len(store.All()) != 0 {
		t.Fatal("a registration of a device that no longer exists survived startup")
	}
}

// TestNativePushDeliversAQuestionEndToEnd goes through the real entry points:
// pairing, native registration, a real ask_user in a session, the policy, the
// sender, and a relay that opens the envelope as the NSE would.
func TestNativePushDeliversAQuestionEndToEnd(t *testing.T) {
	f := newNativeFixture(t, newMockProvider(
		toolCallHandlerFor("tc-ask", "ask_user", map[string]any{
			"questions": []any{map[string]any{"question": "Continue?", "options": []any{"yes", "no"}}},
		}),
		func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
			return simpleResponse("ok"), nil
		},
	))
	device := pairedDevice(t, f.handler, f.owner, "phone")
	if rec := pairingRequest(f.handler, http.MethodPost, "/api/push/native", f.registration(f.relayURL), nil, device.Credential); rec.Code != http.StatusNoContent {
		t.Fatalf("register = %d", rec.Code)
	}
	sess, err := f.mgr.CreateSession(CreateOpts{Title: "Proyecto confidencial"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := f.mgr.Send(sess.ID, "ask me", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var got []push.EnvelopeContent
	var raw []string
	for time.Now().Before(deadline) {
		if got, raw = f.relay.delivered(); len(got) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(got) != 1 {
		t.Fatalf("relay got %d notifications, want the question", len(got))
	}
	if got[0].Kind != push.KindAsk || got[0].Level != push.LevelUrgent || got[0].Dest != "session" || got[0].Sess != sess.ID || got[0].Body != "Proyecto confidencial" {
		t.Fatalf("envelope = %+v", got[0])
	}
	// Base64 can spell short words by chance; the long ones cannot.
	for _, clear := range []string{"confidencial", sess.ID} {
		if strings.Contains(raw[0], clear) {
			t.Fatalf("relay saw %q in clear", clear)
		}
	}
	if last, ok := f.mgr.pushNative.LastResult(device.DeviceID); !ok || last.Result != "ok" {
		t.Fatalf("last result = %+v", last)
	}
}
