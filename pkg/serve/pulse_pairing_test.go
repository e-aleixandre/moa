package serve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"        //nolint:staticcheck // device WS authentication coverage
	"nhooyr.io/websocket/wsjson" //nolint:staticcheck // device WS authentication coverage
)

func pairingRequest(handler http.Handler, method, path, body string, cookie *http.Cookie, credential string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = "localhost"
	req.RemoteAddr = "127.0.0.1:12345"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("X-Moa-Request", "1")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if credential != "" {
		req.Header.Set("Authorization", deviceAuthorizationScheme+" "+credential)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func pairingPayloadSecret(t *testing.T, pairing pairingResult) string {
	t.Helper()
	parts := strings.Split(pairing.Payload, ":")
	if len(parts) != 3 || parts[0] != "moa-pair-v1" || parts[1] != pairing.PairingID || parts[2] == "" {
		t.Fatalf("invalid pairing payload: %#v", pairing)
	}
	return parts[2]
}

func pairedDevice(t *testing.T, handler http.Handler, owner *http.Cookie, label string) deviceCredentialResult {
	t.Helper()
	pair := pairingRequest(handler, http.MethodPost, "/api/pulse/pairings", `{}`, owner, "")
	if pair.Code != http.StatusCreated {
		t.Fatalf("pair device = %d: %s", pair.Code, pair.Body.String())
	}
	var pairing pairingResult
	if err := json.NewDecoder(pair.Body).Decode(&pairing); err != nil {
		t.Fatal(err)
	}
	claimBody := `{"pairing_id":"` + pairing.PairingID + `","pairing_secret":"` + pairingPayloadSecret(t, pairing) + `","device_label":"` + label + `"}`
	claim := pairingRequest(handler, http.MethodPost, "/api/pulse/pairings/claim", claimBody, nil, "")
	if claim.Code != http.StatusCreated {
		t.Fatalf("claim device = %d: %s", claim.Code, claim.Body.String())
	}
	var device deviceCredentialResult
	if err := json.NewDecoder(claim.Body).Decode(&device); err != nil {
		t.Fatal(err)
	}
	return device
}

func TestPulsePairingDeviceAuthAndRevocation(t *testing.T) {
	if !deviceStoreLockSupported() {
		t.Skip("device auth fails closed where advisory process locks are unavailable")
	}
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	sess, err := mgr.CreateSession(CreateOpts{Title: "paired"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "devices.json")
	handler := NewServer(mgr, WithAuthToken("owner", false), WithDeviceStorePath(path))
	owner := &http.Cookie{Name: authCookieName, Value: "owner"}

	noCSRF := httptest.NewRequest(http.MethodPost, "/api/pulse/pairings", strings.NewReader(`{}`))
	noCSRF.Host = "localhost"
	noCSRF.RemoteAddr = "127.0.0.1:12345"
	noCSRF.Header.Set("Content-Type", "application/json")
	noCSRF.AddCookie(owner)
	noCSRFRec := httptest.NewRecorder()
	handler.ServeHTTP(noCSRFRec, noCSRF)
	if noCSRFRec.Code != http.StatusForbidden {
		t.Fatalf("pairing without CSRF = %d", noCSRFRec.Code)
	}
	if got := noCSRFRec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("pairing Cache-Control = %q, want no-store", got)
	}
	if malformed := pairingRequest(handler, http.MethodPost, "/api/pulse/pairings", `{"unexpected":true}`, owner, ""); malformed.Code != http.StatusBadRequest {
		t.Fatalf("pairing unknown JSON field = %d: %s", malformed.Code, malformed.Body.String())
	}

	pairRec := pairingRequest(handler, http.MethodPost, "/api/pulse/pairings", `{}`, owner, "")
	if pairRec.Code != http.StatusCreated {
		t.Fatalf("pairing = %d: %s", pairRec.Code, pairRec.Body.String())
	}
	if got := pairRec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("pairing Cache-Control = %q, want no-store", got)
	}
	var pairing pairingResult
	if err := json.NewDecoder(pairRec.Body).Decode(&pairing); err != nil {
		t.Fatal(err)
	}
	secret := pairingPayloadSecret(t, pairing)
	if pairing.PairingID == "" || !pairing.ExpiresAt.After(time.Now()) || strings.Contains(pairRec.Body.String(), "pairing_secret") {
		t.Fatalf("bad pairing result: %#v", pairing)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), secret) || strings.Contains(string(contents), `"owner"`) {
		t.Fatal("pairing secret or shared auth token persisted raw")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("device store permissions = %v, err=%v", info.Mode().Perm(), err)
	}

	claimBody := `{"pairing_id":"` + pairing.PairingID + `","pairing_secret":"` + secret + `","device_label":"Moa phone"}`
	claimWithoutCSRF := httptest.NewRequest(http.MethodPost, "/api/pulse/pairings/claim", strings.NewReader(claimBody))
	claimWithoutCSRF.Host = "localhost"
	claimWithoutCSRF.RemoteAddr = "127.0.0.1:12345"
	claimWithoutCSRF.Header.Set("Content-Type", "application/json")
	claimWithoutCSRFRec := httptest.NewRecorder()
	handler.ServeHTTP(claimWithoutCSRFRec, claimWithoutCSRF)
	if claimWithoutCSRFRec.Code != http.StatusForbidden {
		t.Fatalf("claim without CSRF = %d", claimWithoutCSRFRec.Code)
	}
	if got := claimWithoutCSRFRec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("pairing claim Cache-Control = %q, want no-store", got)
	}
	claimRec := pairingRequest(handler, http.MethodPost, "/api/pulse/pairings/claim", claimBody, nil, "")
	if claimRec.Code != http.StatusCreated {
		t.Fatalf("claim = %d: %s", claimRec.Code, claimRec.Body.String())
	}
	if got := claimRec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("pairing claim Cache-Control = %q, want no-store", got)
	}
	var credential deviceCredentialResult
	if err := json.NewDecoder(claimRec.Body).Decode(&credential); err != nil {
		t.Fatal(err)
	}
	if credential.DeviceID == "" || credential.Credential == "" || !strings.HasPrefix(credential.Credential, credential.DeviceID+".") {
		t.Fatalf("bad credential result: %#v", credential)
	}
	if location := claimRec.Header().Get("Location"); location != "" && strings.Contains(location, credential.Credential) {
		t.Fatalf("claim credential appeared in redirect URL: %q", location)
	}
	contents, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), credential.Credential) || strings.Contains(string(contents), secret) {
		t.Fatal("raw pairing or device credential persisted")
	}
	if replayClaim := pairingRequest(handler, http.MethodPost, "/api/pulse/pairings/claim", claimBody, nil, ""); replayClaim.Code != http.StatusUnauthorized {
		t.Fatalf("used pairing claim = %d", replayClaim.Code)
	}

	if got := pairingRequest(handler, http.MethodGet, "/api/sessions", "", nil, credential.Credential); got.Code != http.StatusOK {
		t.Fatalf("device sessions auth = %d, want 200: %s", got.Code, got.Body.String())
	}
	for _, path := range []string{
		"/api/attention",
		"/api/usage",
		"/api/sessions/" + sess.ID + "/messages",
	} {
		if got := pairingRequest(handler, http.MethodGet, path, "", nil, credential.Credential); got.Code != http.StatusOK {
			t.Fatalf("device generic read %s = %d, want 200: %s", path, got.Code, got.Body.String())
		}
	}
	if got := pairingRequest(handler, http.MethodPost, "/api/sessions/"+sess.ID+"/archive", `{"archived":true}`, nil, credential.Credential); got.Code != http.StatusOK {
		t.Fatalf("device generic archive = %d, want 200: %s", got.Code, got.Body.String())
	}
	if got := pairingRequest(handler, http.MethodPost, "/api/pulse/pairings", `{}`, nil, credential.Credential); got.Code != http.StatusForbidden {
		t.Fatalf("device pairing administration = %d, want 403: %s", got.Code, got.Body.String())
	}
	if got := pairingRequest(handler, http.MethodGet, "/api/pulse/devices", "", nil, credential.Credential); got.Code != http.StatusForbidden {
		t.Fatalf("device list auth = %d, want 403: %s", got.Code, got.Body.String())
	}
	devices := pairingRequest(handler, http.MethodGet, "/api/pulse/devices", "", owner, "")
	if devices.Code != http.StatusOK || !strings.Contains(devices.Body.String(), credential.DeviceID) || strings.Contains(devices.Body.String(), credential.Credential) || strings.Contains(devices.Body.String(), "verifier") {
		t.Fatalf("device list = %d: %s", devices.Code, devices.Body.String())
	}

	server := httptest.NewServer(handler)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, server.URL+"/api/sessions/"+sess.ID+"/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{deviceAuthorizationScheme + " " + credential.Credential}}}) //nolint:staticcheck
	if err != nil {
		t.Fatalf("device session WS auth: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck,staticcheck
	var event Event
	if err := wsjson.Read(ctx, conn, &event); err != nil { //nolint:staticcheck
		t.Fatal(err)
	}
	if event.Type != "init" {
		t.Fatalf("device session WS event = %q", event.Type)
	}

	revokeRec := pairingRequest(handler, http.MethodPost, "/api/pulse/devices/"+credential.DeviceID+"/revoke", `{}`, owner, "")
	if revokeRec.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d: %s", revokeRec.Code, revokeRec.Body.String())
	}
	if got := pairingRequest(handler, http.MethodGet, "/api/sessions", "", nil, credential.Credential); got.Code != http.StatusUnauthorized {
		t.Fatalf("revoked credential = %d", got.Code)
	}
}

func TestPulsePairingUsesServeNetworkBoundaryWhenTokenIsDisabled(t *testing.T) {
	if !deviceStoreLockSupported() {
		t.Skip("device auth fails closed where advisory process locks are unavailable")
	}
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	handler := NewServer(mgr, WithDeviceStorePath(filepath.Join(t.TempDir(), "devices.json")))

	pairRec := pairingRequest(handler, http.MethodPost, "/api/pulse/pairings", `{}`, nil, "")
	if pairRec.Code != http.StatusCreated {
		t.Fatalf("tokenless network pairing = %d: %s", pairRec.Code, pairRec.Body.String())
	}
	var pairing pairingResult
	if err := json.NewDecoder(pairRec.Body).Decode(&pairing); err != nil {
		t.Fatal(err)
	}
	claimRec := pairingRequest(handler, http.MethodPost, "/api/pulse/pairings/claim", `{"pairing_id":"`+pairing.PairingID+`","pairing_secret":"`+pairingPayloadSecret(t, pairing)+`","device_label":"network phone"}`, nil, "")
	if claimRec.Code != http.StatusCreated {
		t.Fatalf("tokenless network claim = %d: %s", claimRec.Code, claimRec.Body.String())
	}
	var device deviceCredentialResult
	if err := json.NewDecoder(claimRec.Body).Decode(&device); err != nil {
		t.Fatal(err)
	}
	if got := pairingRequest(handler, http.MethodGet, "/api/sessions", "", nil, device.Credential); got.Code != http.StatusOK {
		t.Fatalf("tokenless device sessions auth = %d, want 200: %s", got.Code, got.Body.String())
	}
}

func TestPulsePairingExpiryRateLimitsHostAndTLSBoundary(t *testing.T) {
	if !deviceStoreLockSupported() {
		t.Skip("device auth fails closed where advisory process locks are unavailable")
	}
	store, err := openDeviceStore(filepath.Join(t.TempDir(), "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close() //nolint:errcheck // test cleanup
	now := time.Now().UTC()
	store.now = func() time.Time { return now }
	pairing, err := store.createPairing("token", deviceCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return now.Add(devicePairingTTL + time.Second) }
	if _, err := store.claim("192.0.2.1", pairing.PairingID, pairingPayloadSecret(t, pairing), "expired phone"); !errors.Is(err, errInvalidPairing) {
		t.Fatalf("expired pairing error = %v", err)
	}
	store.now = func() time.Time { return now }
	livePairing, err := store.createPairing("token", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	liveCredential, err := store.claim("192.0.2.1", livePairing.PairingID, pairingPayloadSecret(t, livePairing), "short lived")
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return now.Add(time.Hour + time.Second) }
	if _, err := store.authenticate(liveCredential.Credential); !errors.Is(err, errInvalidDeviceCredential) {
		t.Fatalf("expired device credential error = %v", err)
	}

	store, err = openDeviceStore(filepath.Join(t.TempDir(), "limited-devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close() //nolint:errcheck // test cleanup
	for i := 0; i < devicePairingRate; i++ {
		if _, err := store.createPairing("token", deviceCredentialTTL); err != nil {
			t.Fatalf("pairing %d: %v", i, err)
		}
	}
	if _, err := store.createPairing("token", deviceCredentialTTL); !errors.Is(err, errDeviceRateLimit) {
		t.Fatalf("pairing rate error = %v", err)
	}

	store, err = openDeviceStore(filepath.Join(t.TempDir(), "locked-devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close() //nolint:errcheck // test cleanup
	locked, err := store.createPairing("token", deviceCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < devicePairingAttempts; i++ {
		if _, err := store.claim("192.0.2.1", locked.PairingID, "wrong-secret", "phone"); !errors.Is(err, errInvalidPairing) {
			t.Fatalf("failed claim %d error = %v", i, err)
		}
	}
	if _, err := store.claim("192.0.2.1", locked.PairingID, pairingPayloadSecret(t, locked), "phone"); !errors.Is(err, errInvalidPairing) {
		t.Fatalf("locked pairing error = %v", err)
	}

	store, err = openDeviceStore(filepath.Join(t.TempDir(), "per-source-devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close() //nolint:errcheck // test cleanup
	attacked, err := store.createPairing("token", deviceCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.createPairing("token", deviceCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < deviceClaimRate; i++ {
		if _, err := store.claim("192.0.2.10", attacked.PairingID, "wrong-secret", "phone"); !errors.Is(err, errInvalidPairing) {
			t.Fatalf("source-limited claim %d error = %v", i, err)
		}
	}
	if _, err := store.claim("192.0.2.10", attacked.PairingID, "wrong-secret", "phone"); !errors.Is(err, errDeviceRateLimit) {
		t.Fatalf("source rate error = %v", err)
	}
	if _, err := store.claim("192.0.2.11", other.PairingID, pairingPayloadSecret(t, other), "other phone"); err != nil {
		t.Fatalf("one source throttled another source: %v", err)
	}

	mgr := newTestManager(t, context.Background(), newMockProvider())
	handler := NewServer(mgr, WithAuthToken("owner", false), WithDeviceStorePath(filepath.Join(t.TempDir(), "devices.json")))
	req := httptest.NewRequest(http.MethodPost, "/api/pulse/pairings", strings.NewReader(`{}`))
	req.Host = "100.64.0.5"
	req.RemoteAddr = "100.64.0.5:12345"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Moa-Request", "1")
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: "owner"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUpgradeRequired {
		t.Fatalf("non-TLS remote pairing = %d: %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/pulse/pairings", strings.NewReader(`{}`))
	req.Host = "localhost"
	req.RemoteAddr = "100.64.0.5:12345"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Moa-Request", "1")
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: "owner"})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUpgradeRequired {
		t.Fatalf("spoofed loopback host pairing = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/pulse/pairings", strings.NewReader(`{}`))
	req.Host = "evil.example"
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Moa-Request", "1")
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: "owner"})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("rebinding host pairing = %d", rec.Code)
	}
}

func TestDeviceClaimSourceUsesDirectPeerNotForwardedHeaders(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/pulse/pairings/claim", nil)
	request.RemoteAddr = "198.51.100.4:12345"
	request.Header.Set("X-Forwarded-For", "203.0.113.9")
	if got := deviceClaimSource(request); got != "198.51.100.4" {
		t.Fatalf("claim source = %q", got)
	}
}
