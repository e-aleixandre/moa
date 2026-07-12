package serve

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
)

func TestDeviceStoreLockHelper(t *testing.T) {
	if os.Getenv("MOA_DEVICE_LOCK_HELPER") != "1" {
		return
	}
	store, err := openDeviceStore(os.Getenv("MOA_DEVICE_LOCK_PATH"))
	if err != nil {
		os.Exit(2)
	}
	defer store.Close()
	_, err = store.claim(
		"198.51.100.10",
		os.Getenv("MOA_DEVICE_LOCK_PAIRING_ID"),
		os.Getenv("MOA_DEVICE_LOCK_PAIRING_SECRET"),
		"second process",
	)
	if err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

func TestDeviceStoreHasExclusiveProcessOwnership(t *testing.T) {
	if !deviceStoreLockSupported() {
		t.Skip("device store auth fails closed where advisory process locks are unavailable")
	}
	path := filepath.Join(t.TempDir(), "devices.json")
	store, err := openDeviceStore(path)
	if err != nil {
		t.Fatal(err)
	}
	pairing, err := store.createPairing("token", deviceCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	secret := pairingPayloadSecret(t, pairing)

	runClaimProcess := func() error {
		cmd := exec.Command(os.Args[0], "-test.run=^TestDeviceStoreLockHelper$")
		cmd.Env = append(os.Environ(),
			"MOA_DEVICE_LOCK_HELPER=1",
			"MOA_DEVICE_LOCK_PATH="+path,
			"MOA_DEVICE_LOCK_PAIRING_ID="+pairing.PairingID,
			"MOA_DEVICE_LOCK_PAIRING_SECRET="+secret,
		)
		return cmd.Run()
	}

	if err := runClaimProcess(); err == nil {
		_ = store.Close()
		t.Fatal("second process claimed while device store lock was held")
	} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 {
		_ = store.Close()
		t.Fatalf("second process lock result = %v, want exit 2", err)
	}
	mgr := newTestManager(t, context.Background(), newMockProvider())
	secondServer := NewServer(mgr, WithAuthToken("owner", false), WithDeviceStorePath(path))
	if response := pairingRequest(secondServer, "POST", "/api/pulse/pairings", `{}`, &http.Cookie{Name: authCookieName, Value: "owner"}, ""); response.Code != http.StatusServiceUnavailable {
		_ = store.Close()
		t.Fatalf("second server did not fail closed: %d: %s", response.Code, response.Body.String())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := runClaimProcess(); err != nil {
		t.Fatalf("claim after lock release = %v", err)
	}

	verify, err := openDeviceStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer verify.Close()
	if len(verify.state.Devices) != 1 || verify.state.Devices[0].Label != "second process" {
		t.Fatalf("second process claim state = %#v", verify.state.Devices)
	}
}

func TestLegacyTokenAuthStillWorksWithDeviceStore(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	handler := NewServer(mgr, WithAuthToken("owner", false), WithDeviceStorePath(filepath.Join(t.TempDir(), "devices.json")))

	query := pairingRequest(handler, "GET", "/api/sessions?token=owner", "", nil, "")
	if query.Code != 302 || query.Header().Get("Location") != "/api/sessions" {
		t.Fatalf("legacy query token = %d location=%q", query.Code, query.Header().Get("Location"))
	}
	var cookie *http.Cookie
	for _, candidate := range query.Result().Cookies() {
		if candidate.Name == authCookieName {
			cookie = candidate
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.Value != "owner" {
		t.Fatalf("legacy auth cookie = %#v", cookie)
	}
	if response := pairingRequest(handler, "GET", "/api/sessions", "", cookie, ""); response.Code != 200 {
		t.Fatalf("legacy cookie auth = %d: %s", response.Code, response.Body.String())
	}
}

func TestRemovedOpsBriefingDoesNotInvokeProvider(t *testing.T) {
	calls := 0
	provider := newMockProvider(func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
		calls++
		return nil, fmt.Errorf("unexpected provider call")
	})
	mgr := newTestManager(t, context.Background(), provider)
	handler := NewServer(mgr, WithAuthToken("owner", false), WithDeviceStorePath(filepath.Join(t.TempDir(), "devices.json")))
	response := pairingRequest(handler, "POST", "/api/briefings/ops", `{}`, &http.Cookie{Name: authCookieName, Value: "owner"}, "")
	if (response.Code != http.StatusNotFound && response.Code != http.StatusMethodNotAllowed) || calls != 0 {
		t.Fatalf("removed briefing status=%d provider_calls=%d body=%s", response.Code, calls, response.Body.String())
	}
}
