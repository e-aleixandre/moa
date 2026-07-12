package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"nhooyr.io/websocket"        //nolint:staticcheck // device websocket lifecycle coverage
	"nhooyr.io/websocket/wsjson" //nolint:staticcheck // device websocket lifecycle coverage

	"github.com/ealeixandre/moa/pkg/bus"
)

type storedDeviceCredential struct {
	deviceCredentialResult
}

func createStoredDeviceCredential(t *testing.T, path string, expiresAt time.Time) storedDeviceCredential {
	t.Helper()
	store, err := openDeviceStore(path)
	if err != nil {
		t.Fatal(err)
	}
	pairing, err := store.createPairing("token", deviceCredentialTTL)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	credential, err := store.claim("127.0.0.1", pairing.PairingID, pairingPayloadSecret(t, pairing), "websocket device")
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if !expiresAt.IsZero() {
		store.mu.Lock()
		store.state.Devices[len(store.state.Devices)-1].ExpiresAt = expiresAt
		err = store.saveLocked()
		store.mu.Unlock()
		if err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return storedDeviceCredential{credential}
}

type deviceWSSet struct {
	companion *websocket.Conn
	ops       *websocket.Conn
}

func dialDeviceWebSockets(t *testing.T, server *httptest.Server, sessionID, credential string) deviceWSSet {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dial := func(path string) *websocket.Conn {
		conn, _, err := websocket.Dial(ctx, server.URL+path, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{deviceAuthorizationScheme + " " + credential}}}) //nolint:staticcheck
		if err != nil {
			t.Fatalf("dial %s: %v", path, err)
		}
		return conn
	}
	set := deviceWSSet{
		companion: dial("/api/sessions/" + sessionID + "/companion-ws"),
		ops:       dial("/api/ops/ws"),
	}
	var companion CompanionWireEvent
	if err := wsjson.Read(ctx, set.companion, &companion); err != nil { //nolint:staticcheck
		t.Fatal(err)
	}
	if companion.Type != "init" {
		t.Fatalf("companion init = %q", companion.Type)
	}
	var opsEvent opsWireEvent
	if err := wsjson.Read(ctx, set.ops, &opsEvent); err != nil { //nolint:staticcheck
		t.Fatal(err)
	}
	if opsEvent.Type != "init" {
		t.Fatalf("ops init = %q", opsEvent.Type)
	}
	return set
}

func (set deviceWSSet) close() {
	_ = set.companion.Close(websocket.StatusNormalClosure, "") //nolint:errcheck,staticcheck
	_ = set.ops.Close(websocket.StatusNormalClosure, "")       //nolint:errcheck,staticcheck
}

func expectDeviceWSClose(t *testing.T, conn *websocket.Conn, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var value json.RawMessage
	err := wsjson.Read(ctx, conn, &value) //nolint:staticcheck
	if err == nil {
		t.Fatalf("%s received data after device access ended: %s", name, value)
	}
	if status := websocket.CloseStatus(err); status != -1 && status != websocket.StatusPolicyViolation {
		t.Fatalf("%s close status = %v, want policy violation or transport closure; err=%v", name, status, err)
	}
}

func TestDeviceRevokeClosesEveryWebSocketAndLeavesTokenSocketAlive(t *testing.T) {
	if !deviceStoreLockSupported() {
		t.Skip("device auth fails closed where advisory process locks are unavailable")
	}
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{Title: "ws revoke"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "devices.json")
	credential := createStoredDeviceCredential(t, path, time.Time{})
	handler := NewServer(mgr, WithAuthToken("owner", false), WithDeviceStorePath(path))
	server := httptest.NewServer(handler)
	defer server.Close()
	deviceSockets := dialDeviceWebSockets(t, server, sess.ID, credential.Credential)
	defer deviceSockets.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tokenConn, _, err := websocket.Dial(ctx, server.URL+"/api/sessions/"+sess.ID+"/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Cookie": []string{authCookieName + "=owner"}}}) //nolint:staticcheck
	if err != nil {
		t.Fatal(err)
	}
	defer tokenConn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck,staticcheck
	var init Event
	if err := wsjson.Read(ctx, tokenConn, &init); err != nil { //nolint:staticcheck
		t.Fatal(err)
	}

	revoked := pairingRequest(handler, http.MethodPost, "/api/pulse/devices/"+credential.DeviceID+"/revoke", `{}`, &http.Cookie{Name: authCookieName, Value: "owner"}, "")
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d: %s", revoked.Code, revoked.Body.String())
	}
	sess.runtime.Bus.Publish(bus.TextDelta{SessionID: sess.ID, RunGen: 1, Delta: "after revoke"})

	expectDeviceWSClose(t, deviceSockets.companion, "companion")
	expectDeviceWSClose(t, deviceSockets.ops, "ops")

	tokenReadCtx, cancelTokenRead := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelTokenRead()
	var tokenEvent Event
	if err := wsjson.Read(tokenReadCtx, tokenConn, &tokenEvent); err != nil { //nolint:staticcheck
		t.Fatalf("token-authenticated websocket closed on device revoke: %v", err)
	}
	if tokenEvent.Type != "text_delta" {
		t.Fatalf("token websocket event = %#v", tokenEvent)
	}
}

func TestDeviceCredentialExpiryClosesEveryWebSocketWhileIdle(t *testing.T) {
	if !deviceStoreLockSupported() {
		t.Skip("device auth fails closed where advisory process locks are unavailable")
	}
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{Title: "ws expiry"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "devices.json")
	credential := createStoredDeviceCredential(t, path, time.Now().Add(150*time.Millisecond))
	handler := NewServer(mgr, WithAuthToken("owner", false), WithDeviceStorePath(path))
	server := httptest.NewServer(handler)
	defer server.Close()
	deviceSockets := dialDeviceWebSockets(t, server, sess.ID, credential.Credential)
	defer deviceSockets.close()

	expectDeviceWSClose(t, deviceSockets.companion, "companion expiry")
	expectDeviceWSClose(t, deviceSockets.ops, "ops expiry")
}
