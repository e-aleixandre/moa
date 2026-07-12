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

func TestPulsePairingDeviceAuthAndRevocation(t *testing.T) {
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
	if malformed := pairingRequest(handler, http.MethodPost, "/api/pulse/pairings", `{"unexpected":true}`, owner, ""); malformed.Code != http.StatusBadRequest {
		t.Fatalf("pairing unknown JSON field = %d: %s", malformed.Code, malformed.Body.String())
	}

	pairRec := pairingRequest(handler, http.MethodPost, "/api/pulse/pairings", `{}`, owner, "")
	if pairRec.Code != http.StatusCreated {
		t.Fatalf("pairing = %d: %s", pairRec.Code, pairRec.Body.String())
	}
	var pairing pairingResult
	if err := json.NewDecoder(pairRec.Body).Decode(&pairing); err != nil {
		t.Fatal(err)
	}
	if pairing.PairingID == "" || pairing.Secret == "" || !strings.Contains(pairing.Payload, pairing.Secret) || !pairing.ExpiresAt.After(time.Now()) {
		t.Fatalf("bad pairing result: %#v", pairing)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), pairing.Secret) {
		t.Fatal("pairing secret persisted raw")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("device store permissions = %v, err=%v", info.Mode().Perm(), err)
	}

	claimBody := `{"pairing_id":"` + pairing.PairingID + `","pairing_secret":"` + pairing.Secret + `","device_label":"Moa phone"}`
	claimRec := pairingRequest(handler, http.MethodPost, "/api/pulse/pairings/claim", claimBody, nil, "")
	if claimRec.Code != http.StatusCreated {
		t.Fatalf("claim = %d: %s", claimRec.Code, claimRec.Body.String())
	}
	var credential deviceCredentialResult
	if err := json.NewDecoder(claimRec.Body).Decode(&credential); err != nil {
		t.Fatal(err)
	}
	if credential.DeviceID == "" || credential.Credential == "" || !strings.HasPrefix(credential.Credential, credential.DeviceID+".") {
		t.Fatalf("bad credential result: %#v", credential)
	}
	contents, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), credential.Credential) || strings.Contains(string(contents), pairing.Secret) {
		t.Fatal("raw pairing or device credential persisted")
	}
	if replayClaim := pairingRequest(handler, http.MethodPost, "/api/pulse/pairings/claim", claimBody, nil, ""); replayClaim.Code != http.StatusUnauthorized {
		t.Fatalf("used pairing claim = %d", replayClaim.Code)
	}

	if got := pairingRequest(handler, http.MethodGet, "/api/sessions", "", nil, credential.Credential); got.Code != http.StatusOK {
		t.Fatalf("device REST auth = %d: %s", got.Code, got.Body.String())
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
		t.Fatalf("device WS auth: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck,staticcheck
	var event Event
	if err := wsjson.Read(ctx, conn, &event); err != nil { //nolint:staticcheck
		t.Fatal(err)
	}
	if event.Type != "init" {
		t.Fatalf("device WS event = %q", event.Type)
	}

	revokeRec := pairingRequest(handler, http.MethodPost, "/api/pulse/devices/"+credential.DeviceID+"/revoke", `{}`, owner, "")
	if revokeRec.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d: %s", revokeRec.Code, revokeRec.Body.String())
	}
	if got := pairingRequest(handler, http.MethodGet, "/api/sessions", "", nil, credential.Credential); got.Code != http.StatusUnauthorized {
		t.Fatalf("revoked credential = %d", got.Code)
	}
}

func TestPulsePairingExpiryRateLimitsHostAndTLSBoundary(t *testing.T) {
	store, err := openDeviceStore(filepath.Join(t.TempDir(), "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	store.now = func() time.Time { return now }
	pairing, err := store.createPairing("token", deviceCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return now.Add(devicePairingTTL + time.Second) }
	if _, err := store.claim(pairing.PairingID, pairing.Secret, "expired phone"); !errors.Is(err, errInvalidPairing) {
		t.Fatalf("expired pairing error = %v", err)
	}
	store.now = func() time.Time { return now }
	livePairing, err := store.createPairing("token", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	liveCredential, err := store.claim(livePairing.PairingID, livePairing.Secret, "short lived")
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
	locked, err := store.createPairing("token", deviceCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < devicePairingAttempts; i++ {
		if _, err := store.claim(locked.PairingID, "wrong-secret", "phone"); !errors.Is(err, errInvalidPairing) {
			t.Fatalf("failed claim %d error = %v", i, err)
		}
	}
	if _, err := store.claim(locked.PairingID, locked.Secret, "phone"); !errors.Is(err, errInvalidPairing) {
		t.Fatalf("locked pairing error = %v", err)
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
