package serve

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/auth"
)

func TestNativeProvidersExpiredDeviceCookieAndHeaderDenied(t *testing.T) {
	clearProviderTestEnv(t)
	path := filepath.Join(t.TempDir(), "devices.json")
	ds, err := openDeviceStore(path)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := ds.createPairing("token", deviceCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	d, err := ds.claim("127.0.0.1", pair.PairingID, pairingPayloadSecret(t, pair), "fake expiring app")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ds.authenticate(d.Credential)
	if err != nil {
		t.Fatal(err)
	}
	value, _, err := ds.createBrowserSession(identity)
	if err != nil {
		t.Fatal(err)
	}
	ds.mu.Lock()
	ds.state.Devices[0].ExpiresAt = time.Now().Add(-time.Second)
	err = ds.saveLocked()
	ds.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := ds.Close(); err != nil {
		t.Fatal(err)
	}
	mgr := newTestManager(t, context.Background(), newMockProvider())
	s := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	logins := auth.NewProviderLoginManager(context.Background(), s)
	defer logins.Close()
	h := NewServer(mgr, WithAuthToken("owner-token", true), WithDeviceStorePath(path), WithProviderCredentials(s, logins))
	for _, route := range []string{"/api/providers", "/api/providers/status", "/api/pulse/devices"} {
		for _, header := range []bool{true, false} {
			cookie := &http.Cookie{Name: deviceSessionCookieName, Value: value}
			credential := ""
			if header {
				cookie = nil
				credential = d.Credential
			}
			if r := pairingRequest(h, "GET", route, "", cookie, credential); r.Code != 401 {
				t.Fatalf("expired device header=%v %s=%d", header, route, r.Code)
			}
		}
	}
}
