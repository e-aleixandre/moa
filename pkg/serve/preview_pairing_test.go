package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The Live Preview target is a generic owner surface: a paired device — a
// phone or another browser holding its own device credential, not the owner
// cookie — can read and drive it exactly like the owner's own browser.
// Pairing administration itself stays owner-only, and its rejection must
// describe the route generically rather than leaking the word "pairing" onto
// every admin surface.
func TestPreviewTargetIsAGenericOwnerSurfaceForPairedDevices(t *testing.T) {
	if !deviceStoreLockSupported() {
		t.Skip("device auth fails closed where advisory process locks are unavailable")
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<head></head>")
	}))
	defer upstream.Close()

	// A stale entry as an older Moa (or a previous, since-closed browser
	// session) might have left it: a loopback address nobody should be
	// proposed again, and that this run must not touch.
	store := &memoryPreviewStore{settings: PreviewSettings{PublicURL: "http://localhost:5173", Port: 9999}}
	preview := NewPreviewController(0, nil, store)
	t.Cleanup(preview.Close)

	mgr := newTestManager(t, context.Background(), newMockProvider())
	handler := NewServer(mgr, WithAuthToken("owner", false), WithDeviceStorePath(filepath.Join(t.TempDir(), "devices.json")), WithPreviewController(preview))
	owner := &http.Cookie{Name: authCookieName, Value: "owner"}
	device := pairedDevice(t, handler, owner, "phone")

	// GET as a paired device: no owner cookie anywhere in the request.
	idle := pairingRequest(handler, http.MethodGet, "/api/preview/target", "", nil, device.Credential)
	if idle.Code != http.StatusOK {
		t.Fatalf("paired device GET /api/preview/target = %d: %s", idle.Code, idle.Body.String())
	}
	var idleStatus map[string]any
	if err := json.Unmarshal(idle.Body.Bytes(), &idleStatus); err != nil {
		t.Fatal(err)
	}
	if idleStatus["enabled"] != false || idleStatus["supported"] != true {
		t.Fatalf("idle status for a paired device = %v", idleStatus)
	}
	if idleStatus["public_url"] != "http://localhost:5173" {
		t.Fatalf("the legacy configured address was not surfaced: %v", idleStatus)
	}

	// PUT as the paired device, with the address that device derived for
	// itself from its own host — not the stale one above, and not something
	// the server invents.
	port := freePort(t)
	deviceOrigin := "http://192.168.1.50:" + strconv.Itoa(port)
	body := fmt.Sprintf(`{"url":%q,"public_url":%q,"port":%d,"parent_origin":"http://device.test"}`, upstream.URL, deviceOrigin, port)
	activated := pairingRequest(handler, http.MethodPut, "/api/preview/target", body, nil, device.Credential)
	if activated.Code != http.StatusOK {
		t.Fatalf("paired device PUT /api/preview/target = %d: %s", activated.Code, activated.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(activated.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["enabled"] != true || result["public_url"] != deviceOrigin {
		t.Fatalf("activation did not honor the device's own origin: %v", result)
	}
	if !listening(port) {
		t.Fatal("the paired device's activation did not bind the listener")
	}

	// The device's own address is isolated to this activation: the store's
	// stale legacy value must still be exactly what it was before, untouched
	// by what this device derived for itself.
	store.mu.Lock()
	settingsAfterActivate := store.settings
	store.mu.Unlock()
	if settingsAfterActivate.PublicURL != "http://localhost:5173" {
		t.Fatalf("the device's activation leaked into the persisted settings: %+v", settingsAfterActivate)
	}
	if settingsAfterActivate.Port != port {
		t.Fatalf("the activated port was not remembered: %+v", settingsAfterActivate)
	}

	// GET again, still as the paired device: the real active state, not the
	// stale saved default.
	afterActivateStatus := pairingRequest(handler, http.MethodGet, "/api/preview/target", "", nil, device.Credential)
	var live map[string]any
	if err := json.Unmarshal(afterActivateStatus.Body.Bytes(), &live); err != nil {
		t.Fatal(err)
	}
	if live["enabled"] != true || live["public_url"] != deviceOrigin || live["port"] != float64(port) {
		t.Fatalf("live status for the paired device = %v", live)
	}

	// The owner can deactivate it just the same — this is a shared listener,
	// not a per-device one — and the PUT contract stays the generic one.
	off := pairingRequest(handler, http.MethodPut, "/api/preview/target", `{"enabled":false}`, owner, "")
	if off.Code != http.StatusOK {
		t.Fatalf("owner deactivation = %d: %s", off.Code, off.Body.String())
	}
	if listening(port) {
		t.Fatal("deactivation left the listener open")
	}

	// Pairing administration is not part of that generic surface. The paired
	// device is refused, and the refusal must not describe the route as
	// pairing-specific — that language is reserved for the pairing routes
	// that actually are one (claim).
	forbidden := pairingRequest(handler, http.MethodGet, "/api/pulse/devices", "", nil, device.Credential)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("paired device GET /api/pulse/devices = %d: %s", forbidden.Code, forbidden.Body.String())
	}
	if msg := strings.ToLower(strings.TrimSpace(forbidden.Body.String())); strings.Contains(msg, "pairing") {
		t.Fatalf("owner-admin rejection leaked pairing-specific wording: %q", msg)
	}

	createPairing := pairingRequest(handler, http.MethodPost, "/api/pulse/pairings", `{}`, nil, device.Credential)
	if createPairing.Code != http.StatusForbidden {
		t.Fatalf("paired device POST /api/pulse/pairings = %d: %s", createPairing.Code, createPairing.Body.String())
	}
	if msg := strings.ToLower(strings.TrimSpace(createPairing.Body.String())); strings.Contains(msg, "pairing") {
		t.Fatalf("owner-admin rejection leaked pairing-specific wording: %q", msg)
	}

	// The owner keeps full access to that admin surface.
	ownerDevices := pairingRequest(handler, http.MethodGet, "/api/pulse/devices", "", owner, "")
	if ownerDevices.Code != http.StatusOK {
		t.Fatalf("owner GET /api/pulse/devices = %d: %s", ownerDevices.Code, ownerDevices.Body.String())
	}
}
