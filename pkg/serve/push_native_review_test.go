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

// This fixture uses the real stores, auth/route middleware, native handlers,
// pairing handlers and lifecycle hook. Unlike NewServer's opaque closure it
// also exposes the device store for deterministic scheduling of lifecycle tests.
type reviewLifecycle struct {
	mgr        *Manager
	devices    *deviceStore
	handler    http.Handler
	nativePath string
	owner      *http.Cookie
}

func reviewLifecycleFixture(t *testing.T, relayHandler http.Handler) *reviewLifecycle {
	t.Helper()
	if !deviceStoreLockSupported() {
		t.Skip("device advisory locks unavailable")
	}
	relay := httptest.NewServer(relayHandler)
	t.Cleanup(relay.Close)
	path := filepath.Join(t.TempDir(), "push_native.json")
	store, err := push.NewNativeStore(path)
	if err != nil {
		t.Fatal(err)
	}
	devices, err := openDeviceStore(filepath.Join(t.TempDir(), "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := devices.Close(); err != nil {
			t.Error(err)
		}
	})
	mgr := &Manager{pushNative: push.NewNativeSender(store, relay.URL)}
	mgr.attachNativePush(devices)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/push/native", handlePushNativeRegister(mgr))
	mux.HandleFunc("DELETE /api/push/native", handlePushNativeDelete(mgr))
	mux.HandleFunc("GET /api/push/native/status", handlePushNativeStatus(mgr))
	mux.HandleFunc("POST /api/pulse/pairings", handlePulsePairing(devices))
	mux.HandleFunc("POST /api/pulse/pairings/claim", handlePulsePairingClaim(devices))
	mux.HandleFunc("POST /api/pulse/devices/{id}/revoke", handlePulseDeviceRevoke(devices))
	h := pulseNoStoreMiddleware(hostMiddleware(nil, authMiddleware("owner", false, devices, routeAuthorizationMiddleware(csrfMiddleware(bodyTimeoutMiddleware(mux))))))
	return &reviewLifecycle{mgr: mgr, devices: devices, handler: h, nativePath: path, owner: &http.Cookie{Name: authCookieName, Value: "owner"}}
}

func (f *reviewLifecycle) register(t *testing.T, device deviceCredentialResult, handle string) {
	t.Helper()
	body := fmt.Sprintf(`{"relay_url":%q,"handle":%q,"expires_at":%d,"secret":%q,"env":"sandbox"}`,
		f.mgr.pushNative.RelayURL(), handle, time.Now().Add(90*24*time.Hour).Unix(), base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("s", 32))))
	rec := pairingRequest(f.handler, http.MethodPost, "/api/push/native", body, nil, device.Credential)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("register = %d %s", rec.Code, rec.Body.String())
	}
}

func reviewAwait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("test scheduling barrier timed out")
	}
}

// Adapted from the review's RED: with sends admitted under the sender's lock,
// a revoke racing an admission waits for it, so the pause point is the relay
// (a send in flight) and the check is that nothing reaches the relay after
// the revoke returned.
func TestReviewRevocationIsASendBoundary(t *testing.T) {
	for _, pauseAt := range []string{"admission", "http"} {
		t.Run(pauseAt, func(t *testing.T) {
			var mu sync.Mutex
			var revoked bool
			late := 0
			entered, resume := make(chan struct{}, 1), make(chan struct{})
			f := reviewLifecycleFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				if revoked {
					late++
				}
				mu.Unlock()
				if pauseAt == "http" {
					entered <- struct{}{}
					select {
					case <-resume:
					case <-r.Context().Done():
					}
				}
				_, _ = io.WriteString(w, `{"ok":true}`)
			}))
			device := pairedDevice(t, f.handler, f.owner, "revocation boundary")
			f.register(t, device, strings.Repeat("R", 80))
			checked := make(chan struct{})
			if pauseAt == "admission" {
				f.mgr.pushNative.SetActive(func(id string) bool {
					active := f.devices.isActive(id)
					close(checked)
					<-resume
					return active
				})
			}
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				f.mgr.pushNative.Notify(context.Background(), push.Notification{Title: "racing revoke", Kind: push.KindAsk, Level: push.LevelUrgent})
			}()
			if pauseAt == "admission" {
				reviewAwait(t, checked)
			} else {
				reviewAwait(t, entered)
			}
			done := make(chan int, 1)
			go func() {
				rec := pairingRequest(f.handler, http.MethodPost, "/api/pulse/devices/"+device.DeviceID+"/revoke", `{}`, f.owner, "")
				mu.Lock()
				revoked = true
				mu.Unlock()
				done <- rec.Code
			}()
			code := 0
			if pauseAt == "admission" {
				// Let the revoke finish if it can while the admission is held
				// (it must not: it waits for the admission), then release it.
				select {
				case code = <-done:
				case <-time.After(300 * time.Millisecond):
				}
				close(resume)
			}
			// A send in flight is cancelled by the revoke, which returns
			// without waiting for the relay to answer.
			if code == 0 {
				select {
				case code = <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("revoke did not return while a send was in flight")
				}
			}
			if code != http.StatusNoContent {
				t.Fatalf("revoke = %d", code)
			}
			if pauseAt == "http" {
				close(resume)
			}
			reviewAwait(t, finished)
			f.mgr.pushNative.Notify(context.Background(), push.Notification{Title: "after revoke"})
			if _, ok := f.mgr.pushNative.Store().Get(device.DeviceID); ok {
				t.Error("revoke kept registration")
			}
			mu.Lock()
			defer mu.Unlock()
			if late != 0 {
				t.Fatalf("relay received %d send(s) after revoke returned 204", late)
			}
		})
	}
}

func TestReviewNativeDeleteInvalidatesUnsentSnapshot(t *testing.T) {
	started, resume := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var handles []string
	f := reviewLifecycleFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			H string `json:"h"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		mu.Lock()
		handles = append(handles, body.H)
		index := len(handles)
		mu.Unlock()
		if index == 1 {
			close(started)
			<-resume
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	a := pairedDevice(t, f.handler, f.owner, "first")
	b := pairedDevice(t, f.handler, f.owner, "second")
	if a.DeviceID > b.DeviceID {
		a, b = b, a
	}
	f.register(t, a, strings.Repeat("A", 80))
	f.register(t, b, strings.Repeat("B", 80))
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		f.mgr.pushNative.Notify(context.Background(), push.Notification{Title: "snapshot"})
	}()
	reviewAwait(t, started)
	deleted := pairingRequest(f.handler, http.MethodDelete, "/api/push/native", "", nil, b.Credential)
	if deleted.Code != http.StatusNoContent {
		close(resume)
		reviewAwait(t, finished)
		t.Fatalf("delete = %d", deleted.Code)
	}
	if _, ok := f.mgr.pushNative.Store().Get(b.DeviceID); ok {
		t.Error("delete did not remove registration")
	}
	close(resume)
	reviewAwait(t, finished)
	mu.Lock()
	got := append([]string(nil), handles...)
	mu.Unlock()
	for _, h := range got {
		if h == strings.Repeat("B", 80) {
			t.Fatal("RED: relay received a not-yet-started send to B after DELETE returned 204 and removed B")
		}
	}
}

func TestReviewRenewalClearsThePreviousHandlesExpiredResult(t *testing.T) {
	f := newNativeFixture(t, newMockProvider(simpleResponseHandler("ok")))
	device := pairedDevice(t, f.handler, f.owner, "renewal")
	if rec := pairingRequest(f.handler, http.MethodPost, "/api/push/native", f.registration(f.relayURL), nil, device.Credential); rec.Code != http.StatusNoContent {
		t.Fatal(rec.Code)
	}
	r, _ := f.mgr.pushNative.Store().Get(device.DeviceID)
	r.ExpiresAt = time.Now().Add(-time.Second)
	if err := f.mgr.pushNative.Store().Put(r); err != nil {
		t.Fatal(err)
	}
	f.mgr.pushNative.Notify(context.Background(), push.Notification{Title: "old handle"})
	body := strings.Replace(f.registration(f.relayURL), strings.Repeat("H", 80), strings.Repeat("N", 80), 1)
	if rec := pairingRequest(f.handler, http.MethodPost, "/api/push/native", body, nil, device.Credential); rec.Code != http.StatusNoContent {
		t.Fatal(rec.Code)
	}
	status := pairingRequest(f.handler, http.MethodGet, "/api/push/native/status", "", nil, device.Credential)
	var got struct {
		Registered bool              `json:"registered"`
		ExpiresAt  int64             `json:"expires_at"`
		Last       push.NativeResult `json:"last"`
	}
	if err := json.Unmarshal(status.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Registered || got.ExpiresAt <= time.Now().Unix() {
		t.Fatal("fixture did not renew registration")
	}
	if got.Last.Result == "handle_expired" {
		t.Fatalf("RED: fresh handle (90 days left) still advertises previous handle_expired: %s", status.Body.String())
	}
}

func TestReviewOldSendCannotPoisonNewRegistrationStatus(t *testing.T) {
	started, resume := make(chan struct{}), make(chan struct{})
	f := reviewLifecycleFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-resume
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"handle_expired"}`)
	}))
	device := pairedDevice(t, f.handler, f.owner, "late result")
	f.register(t, device, strings.Repeat("O", 80))
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		f.mgr.pushNative.Notify(context.Background(), push.Notification{Title: "old generation"})
	}()
	reviewAwait(t, started)
	f.register(t, device, strings.Repeat("N", 80))
	close(resume)
	reviewAwait(t, finished)
	if r, _ := f.mgr.pushNative.Store().Get(device.DeviceID); r.Handle != strings.Repeat("N", 80) {
		t.Fatal("fresh registration lost")
	}
	status := pairingRequest(f.handler, http.MethodGet, "/api/push/native/status", "", nil, device.Credential)
	if strings.Contains(status.Body.String(), "handle_expired") {
		t.Fatalf("RED: late response for O marks fresh N expired: %s", status.Body.String())
	}
}

func TestReviewRestartRestoresNativeRegistrationExpiryCleanup(t *testing.T) {
	if !deviceStoreLockSupported() {
		t.Skip("device advisory locks unavailable")
	}
	devicePath := filepath.Join(t.TempDir(), "devices.json")
	devices, err := openDeviceStore(devicePath)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := devices.createPairing("token", 350*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	device, err := devices.claim("127.0.0.1", pair.PairingID, pairingPayloadSecret(t, pair), "expires across restart")
	if err != nil {
		t.Fatal(err)
	}
	if err := devices.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := push.NewNativeStore(filepath.Join(t.TempDir(), "push_native.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(push.NativeRegistration{DeviceID: device.DeviceID, RelayURL: push.DefaultRelayURL, Handle: strings.Repeat("H", 80), Secret: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("s", 32))), ExpiresAt: time.Now().Add(time.Hour), Env: "sandbox"}); err != nil {
		t.Fatal(err)
	}
	devices, err = openDeviceStore(devicePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = devices.Close() })
	mgr := &Manager{pushNative: push.NewNativeSender(store, push.DefaultRelayURL)}
	mgr.attachNativePush(devices)
	if !devices.isActive(device.DeviceID) {
		t.Fatal("fixture expired before startup reconciliation")
	}
	timer := time.NewTimer(time.Until(device.ExpiresAt) + 150*time.Millisecond)
	defer timer.Stop()
	<-timer.C
	if devices.isActive(device.DeviceID) {
		t.Fatal("fixture has not expired")
	}
	if _, ok := store.Get(device.DeviceID); ok {
		t.Fatal("RED: device expired after restart, but its secret/handle remain indefinitely (no restored expiry timer)")
	}
}

func TestReviewRevokeReportsNativePersistenceFailure(t *testing.T) {
	f := reviewLifecycleFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"ok":true}`) }))
	device := pairedDevice(t, f.handler, f.owner, "disk failure")
	f.register(t, device, strings.Repeat("D", 80))
	parent := filepath.Dir(f.nativePath)
	before, err := os.ReadFile(f.nativePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	rec := pairingRequest(f.handler, http.MethodPost, "/api/pulse/devices/"+device.DeviceID+"/revoke", `{}`, f.owner, "")
	after, err := os.ReadFile(f.nativePath)
	if err != nil {
		t.Fatal(err)
	}
	if f.devices.isActive(device.DeviceID) {
		t.Fatal("durable credential should still revoke despite cleanup failure")
	}
	if !strings.Contains(string(after), strings.Repeat("D", 80)) || string(after) != string(before) {
		t.Fatal("fixture did not actually fail native persistence")
	}
	if rec.Code >= 200 && rec.Code < 300 {
		t.Fatalf("RED: revoke returned %d while native.Remove failed; retained secret/handle on disk and in memory", rec.Code)
	}
}

type reviewPausedReader struct {
	body            io.Reader
	started, resume chan struct{}
	once            sync.Once
}

func (r *reviewPausedReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.started); <-r.resume })
	return r.body.Read(p)
}

func TestReviewRegistrationCannotResurrectAfterRevocation(t *testing.T) {
	f := newNativeFixture(t, newMockProvider(simpleResponseHandler("ok")))
	device := pairedDevice(t, f.handler, f.owner, "slow register")
	started, resume, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	reader := &reviewPausedReader{body: strings.NewReader(f.registration(f.relayURL)), started: started, resume: resume}
	req := httptest.NewRequest(http.MethodPost, "/api/push/native", reader)
	req.Host = "localhost"
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Moa-Request", "1")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", deviceAuthorizationScheme+" "+device.Credential)
	rec := httptest.NewRecorder()
	go func() { defer close(finished); f.handler.ServeHTTP(rec, req) }()
	reviewAwait(t, started)
	revoke := pairingRequest(f.handler, http.MethodPost, "/api/pulse/devices/"+device.DeviceID+"/revoke", `{}`, f.owner, "")
	close(resume)
	reviewAwait(t, finished)
	if revoke.Code != http.StatusNoContent || rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoke=%d registration=%d", revoke.Code, rec.Code)
	}
	if len(f.mgr.pushNative.Store().All()) != 0 {
		t.Fatal("registration resurrected revoked device")
	}
}

func TestReviewNativeAuthFailsClosedWithoutOwnerToken(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	store, err := push.NewNativeStore(filepath.Join(t.TempDir(), "push_native.json"))
	if err != nil {
		t.Fatal(err)
	}
	mgr.pushNative = push.NewNativeSender(store, push.DefaultRelayURL)
	h := NewServer(mgr, WithDeviceStorePath(filepath.Join(t.TempDir(), "devices.json")))
	device := pairedDevice(t, h, nil, "network pairing")
	cookie := deviceBrowserSession(t, h, device.Credential)
	for _, tc := range []struct {
		name       string
		cookie     *http.Cookie
		credential string
		want       int
	}{
		{"network owner", nil, "", http.StatusForbidden},
		{"webview cookie", cookie, "", http.StatusForbidden},
		{"bad credential", nil, "invalid", http.StatusUnauthorized},
		{"native credential", nil, device.Credential, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := pairingRequest(h, http.MethodGet, "/api/push/native/status", "", tc.cookie, tc.credential)
			if got.Code != tc.want {
				t.Fatalf("status=%d, want %d", got.Code, tc.want)
			}
			if got.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("status cached")
			}
		})
	}
}

func TestReviewNewlyClaimedDeviceExpiryRemovesNativeSecrets(t *testing.T) {
	f := reviewLifecycleFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"ok":true}`) }))
	pair, err := f.devices.createPairing("token", 500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	device, err := f.devices.claim("127.0.0.1", pair.PairingID, pairingPayloadSecret(t, pair), "fresh expiry")
	if err != nil {
		t.Fatal(err)
	}
	f.register(t, device, strings.Repeat("F", 80))
	deadline := time.NewTimer(time.Until(device.ExpiresAt) + 150*time.Millisecond)
	defer deadline.Stop()
	<-deadline.C
	if _, ok := f.mgr.pushNative.Store().Get(device.DeviceID); ok {
		t.Fatal("freshly claimed device failed native expiry cleanup")
	}
}

func TestReviewManagerConfigFansQuestionToBothTransports(t *testing.T) {
	if !deviceStoreLockSupported() {
		t.Skip("device advisory locks unavailable")
	}
	webHolder := &Manager{}
	webHits := withPushEndpoint(t, webHolder)
	t.Cleanup(webHolder.pushPolicy.Close)
	secret := []byte(strings.Repeat("s", 32))
	keys, err := push.DeriveDeviceKeys(secret)
	if err != nil {
		t.Fatal(err)
	}
	relay := &nativeRelay{t: t, keys: keys}
	srv := httptest.NewServer(relay)
	t.Cleanup(srv.Close)
	store, err := push.NewNativeStore(filepath.Join(t.TempDir(), "push_native.json"))
	if err != nil {
		t.Fatal(err)
	}
	native := push.NewNativeSender(store, srv.URL)
	provider := newMockProvider(toolCallHandlerFor("ask", "ask_user", map[string]any{
		"questions": []any{map[string]any{"question": "Continue?", "options": []any{"yes", "no"}}},
	}))
	cfg := core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"}
	mgr := NewManager(context.Background(), ManagerConfig{
		ProviderFactory: func(core.Model) (core.Provider, error) { return provider, nil },
		DefaultModel:    core.Model{ID: "test", Provider: "test"},
		WorkspaceRoot:   t.TempDir(),
		SessionBaseDir:  t.TempDir(),
		SchedulePath:    filepath.Join(t.TempDir(), "schedules.json"),
		MoaCfg:          cfg,
		ConfigLoader:    isolatedTestConfigLoader(t, cfg),
		PushDispatcher:  webHolder.pushDispatcher,
		PushNative:      native,
	})
	t.Cleanup(mgr.Shutdown)
	h := NewServer(mgr, WithAuthToken("owner", false), WithDeviceStorePath(filepath.Join(t.TempDir(), "devices.json")))
	device := pairedDevice(t, h, &http.Cookie{Name: authCookieName, Value: "owner"}, "dual transport")
	body := fmt.Sprintf(`{"relay_url":%q,"handle":%q,"expires_at":%d,"secret":%q,"env":"sandbox"}`,
		srv.URL, strings.Repeat("H", 80), time.Now().Add(time.Hour).Unix(), base64.RawURLEncoding.EncodeToString(secret))
	if rec := pairingRequest(h, http.MethodPost, "/api/push/native", body, nil, device.Credential); rec.Code != http.StatusNoContent {
		t.Fatalf("register=%d", rec.Code)
	}
	sess, err := mgr.CreateSession(CreateOpts{Title: "dual question"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(sess.ID, "ask", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 5*time.Second, "both transports and sender completion", func() bool {
		got, _ := relay.delivered()
		last, _ := native.LastResult(device.DeviceID)
		return len(got) == 1 && webHits.Load() == 1 && last.Result == "ok"
	})
	got, _ := relay.delivered()
	if got[0].Sess != sess.ID || got[0].Kind != push.KindAsk || got[0].Level != push.LevelUrgent {
		t.Fatalf("native content=%+v", got[0])
	}
}
