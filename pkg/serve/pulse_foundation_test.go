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
	"sync/atomic"
	"testing"
	"time"

	"nhooyr.io/websocket"        //nolint:staticcheck // test coverage for device WS auth
	"nhooyr.io/websocket/wsjson" //nolint:staticcheck // test coverage for device WS auth

	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/permission"
)

func pulseTestRequest(handler http.Handler, method, path, body string, cookie *http.Cookie, credential string) *httptest.ResponseRecorder {
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

	pairRec := pulseTestRequest(handler, http.MethodPost, "/api/pulse/pairings", `{}`, owner, "")
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
	claimRec := pulseTestRequest(handler, http.MethodPost, "/api/pulse/pairings/claim", claimBody, nil, "")
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
	if replayClaim := pulseTestRequest(handler, http.MethodPost, "/api/pulse/pairings/claim", claimBody, nil, ""); replayClaim.Code != http.StatusUnauthorized {
		t.Fatalf("used pairing claim = %d", replayClaim.Code)
	}

	if got := pulseTestRequest(handler, http.MethodGet, "/api/sessions", "", nil, credential.Credential); got.Code != http.StatusOK {
		t.Fatalf("device REST auth = %d: %s", got.Code, got.Body.String())
	}
	devices := pulseTestRequest(handler, http.MethodGet, "/api/pulse/devices", "", owner, "")
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

	revokeRec := pulseTestRequest(handler, http.MethodPost, "/api/pulse/devices/"+credential.DeviceID+"/revoke", `{}`, owner, "")
	if revokeRec.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d: %s", revokeRec.Code, revokeRec.Body.String())
	}
	if got := pulseTestRequest(handler, http.MethodGet, "/api/sessions", "", nil, credential.Credential); got.Code != http.StatusUnauthorized {
		t.Fatalf("revoked credential = %d", got.Code)
	}
}

func TestPulsePairingExpiryHostAndTLSBoundary(t *testing.T) {
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
}

func TestPulseDirectedOperationPrepareConfirmReplayAndStateChecks(t *testing.T) {
	var calls atomic.Int32
	provider := newMockProvider(func(_ context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		calls.Add(1)
		return simpleResponse("done"), nil
	})
	mgr := newTestManager(t, context.Background(), provider)
	sess, err := mgr.CreateSession(CreateOpts{Title: "release"})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(mgr, WithAuthToken("owner", false), WithDeviceStorePath(filepath.Join(t.TempDir(), "devices.json")))
	owner := &http.Cookie{Name: authCookieName, Value: "owner"}
	body := `{"target":"` + sess.ID + `","text":"run the release check","request_id":"instruction-1"}`
	prepare := pulseTestRequest(handler, http.MethodPost, "/api/pulse/operations/directed-instruction/prepare", body, owner, "")
	if prepare.Code != http.StatusCreated {
		t.Fatalf("prepare = %d: %s", prepare.Code, prepare.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatalf("prepare invoked provider %d times", calls.Load())
	}
	if info, err := os.Stat(mgr.operationStore.path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("operation store permissions = %v, err=%v", info.Mode().Perm(), err)
	}
	var view pulseOperationView
	if err := json.NewDecoder(prepare.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	if view.Target.ID != sess.ID || view.CurrentState != "idle" || view.Scope == "" || view.Risk != "medium" || view.Review == "" || view.ExpiresAt.IsZero() {
		t.Fatalf("unsafe operation binding: %#v", view)
	}
	retryPrepare := pulseTestRequest(handler, http.MethodPost, "/api/pulse/operations/directed-instruction/prepare", body, owner, "")
	var retryView pulseOperationView
	if retryPrepare.Code != http.StatusCreated || json.NewDecoder(retryPrepare.Body).Decode(&retryView) != nil || retryView.OperationID != view.OperationID {
		t.Fatalf("prepare retry = %d, %#v", retryPrepare.Code, retryView)
	}
	conflictingPrepare := pulseTestRequest(handler, http.MethodPost, "/api/pulse/operations/directed-instruction/prepare", `{"target":"`+sess.ID+`","text":"different scope","request_id":"instruction-1"}`, owner, "")
	if conflictingPrepare.Code != http.StatusConflict {
		t.Fatalf("prepare scope tamper = %d: %s", conflictingPrepare.Code, conflictingPrepare.Body.String())
	}

	tampered := pulseTestRequest(handler, http.MethodPost, "/api/pulse/operations/"+view.OperationID+"/confirm", `{"confirmed":true}`, owner, "")
	if tampered.Code != http.StatusBadRequest {
		t.Fatalf("client confirmation assertion = %d", tampered.Code)
	}
	confirmed := pulseTestRequest(handler, http.MethodPost, "/api/pulse/operations/"+view.OperationID+"/confirm", `{}`, owner, "")
	if confirmed.Code != http.StatusOK {
		t.Fatalf("confirm = %d: %s", confirmed.Code, confirmed.Body.String())
	}
	if err := json.NewDecoder(confirmed.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	if view.Status != "accepted" || view.Receipt == nil || view.Receipt.Status != "accepted" || !strings.Contains(view.Receipt.DeliveryStatus, "delivered") || view.Receipt.RequestIdentity != "token" {
		t.Fatalf("receipt = %#v", view)
	}
	replay := pulseTestRequest(handler, http.MethodPost, "/api/pulse/operations/"+view.OperationID+"/confirm", `{}`, owner, "")
	if replay.Code != http.StatusOK {
		t.Fatalf("confirm replay = %d", replay.Code)
	}
	var replayView pulseOperationView
	if err := json.NewDecoder(replay.Body).Decode(&replayView); err != nil || replayView.Receipt == nil || replayView.Receipt.Timestamp != view.Receipt.Timestamp {
		t.Fatalf("replay receipt=%#v err=%v", replayView, err)
	}
	operationContents, err := os.ReadFile(mgr.operationStore.path)
	if err != nil || strings.Contains(string(operationContents), "run the release check") {
		t.Fatalf("completed operation retained raw instruction: err=%v contents=%s", err, operationContents)
	}
	restarted := NewManager(context.Background(), ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return provider, nil },
		DefaultModel:    core.Model{ID: "test-model", Provider: "mock"},
		WorkspaceRoot:   t.TempDir(),
		MoaCfg:          core.MoaConfig{DisableSandbox: true},
		SessionBaseDir:  t.TempDir(),
		SchedulePath:    filepath.Join(t.TempDir(), "schedules.json"),
		OperationPath:   mgr.operationStore.path,
	})
	defer restarted.Shutdown()
	restored, err := restarted.pulseOperation(view.OperationID)
	if err != nil || restored.Receipt == nil || restored.Receipt.Status != "accepted" {
		t.Fatalf("durable receipt after restart = %#v, err=%v", restored, err)
	}

	other, err := mgr.CreateSession(CreateOpts{Title: "state change"})
	if err != nil {
		t.Fatal(err)
	}
	stateBody := `{"target":"` + other.ID + `","text":"check status","request_id":"instruction-state"}`
	statePrepare := pulseTestRequest(handler, http.MethodPost, "/api/pulse/operations/directed-instruction/prepare", stateBody, owner, "")
	if statePrepare.Code != http.StatusCreated {
		t.Fatalf("state prepare = %d: %s", statePrepare.Code, statePrepare.Body.String())
	}
	var stateView pulseOperationView
	_ = json.NewDecoder(statePrepare.Body).Decode(&stateView)
	other.runtime.State.ForceState(bus.StateRunning)
	stateConfirm := pulseTestRequest(handler, http.MethodPost, "/api/pulse/operations/"+stateView.OperationID+"/confirm", `{}`, owner, "")
	if stateConfirm.Code != http.StatusOK {
		t.Fatalf("state confirm = %d", stateConfirm.Code)
	}
	_ = json.NewDecoder(stateConfirm.Body).Decode(&stateView)
	if stateView.Status != "rejected" || stateView.Receipt == nil || stateView.Receipt.Status != "rejected" {
		t.Fatalf("state change receipt=%#v", stateView)
	}

	expiring, err := mgr.CreateSession(CreateOpts{Title: "expiry"})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now().UTC()
	mgr.operationNow = func() time.Time { return clock }
	expiryPrepare := pulseTestRequest(handler, http.MethodPost, "/api/pulse/operations/directed-instruction/prepare", `{"target":"`+expiring.ID+`","text":"wait","request_id":"instruction-expiry"}`, owner, "")
	if expiryPrepare.Code != http.StatusCreated {
		t.Fatalf("expiry prepare = %d: %s", expiryPrepare.Code, expiryPrepare.Body.String())
	}
	var expiryView pulseOperationView
	_ = json.NewDecoder(expiryPrepare.Body).Decode(&expiryView)
	clock = clock.Add(pulseOperationTTL + time.Second)
	expiryConfirm := pulseTestRequest(handler, http.MethodPost, "/api/pulse/operations/"+expiryView.OperationID+"/confirm", `{}`, owner, "")
	if expiryConfirm.Code != http.StatusOK {
		t.Fatalf("expiry confirm = %d: %s", expiryConfirm.Code, expiryConfirm.Body.String())
	}
	_ = json.NewDecoder(expiryConfirm.Body).Decode(&expiryView)
	if expiryView.Status != "expired" || expiryView.Receipt == nil || expiryView.Receipt.Status != "expired" {
		t.Fatalf("expiry receipt=%#v", expiryView)
	}
	mgr.operationNow = time.Now

	ambiguous, err := mgr.CreateSession(CreateOpts{Title: "release"})
	if err != nil {
		t.Fatal(err)
	}
	_ = ambiguous
	ambiguousPrepare := pulseTestRequest(handler, http.MethodPost, "/api/pulse/operations/directed-instruction/prepare", `{"target":"release","text":"check","request_id":"ambiguous"}`, owner, "")
	if ambiguousPrepare.Code != http.StatusConflict || !strings.Contains(ambiguousPrepare.Body.String(), "candidates") {
		t.Fatalf("ambiguous target = %d: %s", ambiguousPrepare.Code, ambiguousPrepare.Body.String())
	}
}

func TestPulsePermissionOperationIsOneOffAndRedacted(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{Title: "permission target"})
	if err != nil {
		t.Fatal(err)
	}
	gate := permission.New(permission.ModeAsk, permission.Config{})
	sess.runtime.State.ForceState(bus.StateRunning)
	sess.runtime.Context().Approvals.StartPermissionBridge(context.Background(), gate)
	decision := make(chan *core.ToolCallDecision, 1)
	go func() {
		decision <- gate.Check(context.Background(), "bash", map[string]any{"command": "deploy --token=super-secret"})
	}()
	var pending bus.PendingApprovalInfo
	pollUntil(t, time.Second, "pending permission", func() bool {
		pending, _ = bus.QueryTyped[bus.GetPendingApproval, bus.PendingApprovalInfo](sess.runtime.Bus, bus.GetPendingApproval{})
		return pending.Permission != nil
	})

	handler := NewServer(mgr, WithAuthToken("owner", false), WithDeviceStorePath(filepath.Join(t.TempDir(), "devices.json")))
	owner := &http.Cookie{Name: authCookieName, Value: "owner"}
	body := `{"session_id":"` + sess.ID + `","permission_id":"` + pending.Permission.ID + `","decision":"approve","request_id":"permission-one"}`
	prepare := pulseTestRequest(handler, http.MethodPost, "/api/pulse/operations/permission/prepare", body, owner, "")
	if prepare.Code != http.StatusCreated {
		t.Fatalf("permission prepare = %d: %s", prepare.Code, prepare.Body.String())
	}
	if strings.Contains(prepare.Body.String(), "super-secret") || strings.Contains(prepare.Body.String(), "command") || strings.Contains(prepare.Body.String(), "allow_pattern") {
		t.Fatalf("permission prepare leaked raw data: %s", prepare.Body.String())
	}
	var view pulseOperationView
	_ = json.NewDecoder(prepare.Body).Decode(&view)
	if !strings.Contains(view.Scope, "no allow pattern") || !strings.Contains(view.Review, "does not add") {
		t.Fatalf("permission scope = %#v", view)
	}
	confirm := pulseTestRequest(handler, http.MethodPost, "/api/pulse/operations/"+view.OperationID+"/confirm", `{}`, owner, "")
	if confirm.Code != http.StatusOK {
		t.Fatalf("permission confirm = %d: %s", confirm.Code, confirm.Body.String())
	}
	_ = json.NewDecoder(confirm.Body).Decode(&view)
	if view.Status != "accepted" || view.Receipt == nil || view.Receipt.Status != "accepted" {
		t.Fatalf("permission receipt=%#v", view)
	}
	select {
	case result := <-decision:
		if result != nil {
			t.Fatalf("approved permission decision = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("permission decision was not delivered")
	}
	if got := gate.AllowPatterns(); len(got) != 0 {
		t.Fatalf("one-off Pulse decision added allow patterns: %v", got)
	}

	sess.runtime.State.ForceState(bus.StateRunning)
	denied := make(chan *core.ToolCallDecision, 1)
	go func() {
		denied <- gate.Check(context.Background(), "bash", map[string]any{"command": "remove protected data"})
	}()
	pollUntil(t, time.Second, "second pending permission", func() bool {
		pending, _ = bus.QueryTyped[bus.GetPendingApproval, bus.PendingApprovalInfo](sess.runtime.Bus, bus.GetPendingApproval{})
		return pending.Permission != nil
	})
	denyBody := `{"session_id":"` + sess.ID + `","permission_id":"` + pending.Permission.ID + `","decision":"deny","request_id":"permission-deny"}`
	denyPrepare := pulseTestRequest(handler, http.MethodPost, "/api/pulse/operations/permission/prepare", denyBody, owner, "")
	if denyPrepare.Code != http.StatusCreated {
		t.Fatalf("deny prepare = %d: %s", denyPrepare.Code, denyPrepare.Body.String())
	}
	var denyView pulseOperationView
	_ = json.NewDecoder(denyPrepare.Body).Decode(&denyView)
	denyConfirm := pulseTestRequest(handler, http.MethodPost, "/api/pulse/operations/"+denyView.OperationID+"/confirm", `{}`, owner, "")
	if denyConfirm.Code != http.StatusOK {
		t.Fatalf("deny confirm = %d: %s", denyConfirm.Code, denyConfirm.Body.String())
	}
	select {
	case result := <-denied:
		if result == nil || !result.Block {
			t.Fatalf("denied permission decision = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("denial was not delivered")
	}
}
