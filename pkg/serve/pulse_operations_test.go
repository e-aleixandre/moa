package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/core"
)

func pulseOperationDevice(t *testing.T, handler http.Handler, owner *http.Cookie, label string) deviceCredentialResult {
	t.Helper()
	pair := pairingRequest(handler, http.MethodPost, "/api/pulse/pairings", `{}`, owner, "")
	if pair.Code != http.StatusCreated {
		t.Fatalf("create pairing = %d: %s", pair.Code, pair.Body.String())
	}
	var pairing pairingResult
	if err := json.NewDecoder(pair.Body).Decode(&pairing); err != nil {
		t.Fatal(err)
	}
	claim := pairingRequest(handler, http.MethodPost, "/api/pulse/pairings/claim", `{"pairing_id":"`+pairing.PairingID+`","pairing_secret":"`+pairingPayloadSecret(t, pairing)+`","device_label":"`+label+`"}`, nil, "")
	if claim.Code != http.StatusCreated {
		t.Fatalf("claim pairing = %d: %s", claim.Code, claim.Body.String())
	}
	var credential deviceCredentialResult
	if err := json.NewDecoder(claim.Body).Decode(&credential); err != nil {
		t.Fatal(err)
	}
	return credential
}

func pulseOperationRequest(handler http.Handler, method, path, body, credential string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = "localhost"
	req.RemoteAddr = "127.0.0.1:12345"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("X-Moa-Request", "1")
	}
	req.Header.Set("Authorization", deviceAuthorizationScheme+" "+credential)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func decodePulseOperation(t *testing.T, rec *httptest.ResponseRecorder) pulseOperationResponse {
	t.Helper()
	var response pulseOperationResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode Pulse operation response: %v; body=%s", err, rec.Body.String())
	}
	return response
}

func pendingPulsePermissionOperation(t *testing.T, command string) (*Manager, *ManagedSession, http.Handler, deviceCredentialResult) {
	t.Helper()
	toolCall := func(_ context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 2)
		message := core.Message{
			Role:       "assistant",
			Content:    []core.Content{core.ToolCallContent("permission-call", "bash", map[string]any{"command": command})},
			StopReason: "tool_use",
			Timestamp:  time.Now().Unix(),
		}
		ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &message}
		ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &message}
		close(ch)
		return ch, nil
	}
	mgr := newTestManagerWithConfig(t, context.Background(), newMockProvider(toolCall, simpleResponseHandler("done")), t.TempDir(), core.MoaConfig{
		DisableSandbox: true,
		Permissions:    core.PermissionsConfig{Mode: "ask"},
	})
	sess, err := mgr.CreateSession(CreateOpts{Title: "permission target"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := mgr.Send(sess.ID, "request permission", nil, ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 3*time.Second, "pending permission", func() bool {
		_, err := bus.QueryTyped[bus.GetPermissionDecisionSnapshot, bus.PermissionDecisionSnapshot](sess.runtime.Bus, bus.GetPermissionDecisionSnapshot{SessionID: sess.ID})
		return err == nil
	})
	handler := NewServer(mgr, WithAuthToken("owner", false), WithDeviceStorePath(filepath.Join(t.TempDir(), "devices.json")))
	credential := pulseOperationDevice(t, handler, &http.Cookie{Name: authCookieName, Value: "owner"}, "permission phone")
	return mgr, sess, handler, credential
}

func TestPulsePermissionDecisionTypedReviewAndReceipt(t *testing.T) {
	if !deviceStoreLockSupported() {
		t.Skip("device auth fails closed where advisory process locks are unavailable")
	}
	secret := "permission_raw_secret_123"
	mgr, sess, handler, credential := pendingPulsePermissionOperation(t, "echo 'Authorization: Bearer "+secret+"'")

	owner := &http.Cookie{Name: authCookieName, Value: "owner"}
	if got := pairingRequest(handler, http.MethodPost, "/api/pulse/operations/prepare", `{"kind":"permission_decision","target":"`+sess.ID+`","decision":"deny"}`, owner, ""); got.Code != http.StatusForbidden {
		t.Fatalf("owner permission operation = %d, want 403", got.Code)
	}
	for _, body := range []string{
		`{"kind":"permission_decision","target":"` + sess.ID + `","decision":"allow"}`,
		`{"kind":"permission_decision","target":"` + sess.ID + `","decision":"approve_once","allow":"Bash(*)"}`,
		`{"kind":"permission_decision","target":"` + sess.ID + `","decision":"approve_once","action":"add_rule"}`,
		`{"kind":"permission_decision","target":"` + sess.ID + `","decision":"approve_once","text":"free-form"}`,
		`{"kind":"permission_decision","target":"` + sess.ID + `","decision":"approve_once","text":""}`,
		`{"kind":"permission_decision","target":"` + sess.ID + `","decision":"approve_once","feedback":"please tell the agent this benign instruction"}`,
	} {
		got := pulseOperationRequest(handler, http.MethodPost, "/api/pulse/operations/prepare", body, credential.Credential)
		if got.Code != http.StatusBadRequest {
			t.Fatalf("permission schema %s = %d, want 400: %s", body, got.Code, got.Body.String())
		}
		if strings.Contains(body, `"feedback"`) && !strings.Contains(got.Body.String(), "feedback is not accepted") {
			t.Fatalf("feedback rejection was not explicit: %s", got.Body.String())
		}
	}
	withoutPermission, err := mgr.CreateSession(CreateOpts{Title: sess.title()})
	if err != nil {
		t.Fatal(err)
	}
	if ambiguous := pulseOperationRequest(handler, http.MethodPost, "/api/pulse/operations/prepare", `{"kind":"permission_decision","target":"permission target","decision":"deny"}`, credential.Credential); ambiguous.Code != http.StatusConflict {
		t.Fatalf("ambiguous permission target = %d, want 409: %s", ambiguous.Code, ambiguous.Body.String())
	}
	if noPending := pulseOperationRequest(handler, http.MethodPost, "/api/pulse/operations/prepare", `{"kind":"permission_decision","target":"`+withoutPermission.ID+`","decision":"deny"}`, credential.Credential); noPending.Code != http.StatusConflict {
		t.Fatalf("permission prepare without pending request = %d, want 409: %s", noPending.Code, noPending.Body.String())
	}

	prepared := pulseOperationRequest(handler, http.MethodPost, "/api/pulse/operations/prepare", `{"kind":"permission_decision","target":"`+sess.ID+`","decision":"deny"}`, credential.Credential)
	if prepared.Code != http.StatusCreated {
		t.Fatalf("prepare permission = %d: %s", prepared.Code, prepared.Body.String())
	}
	operation := decodePulseOperation(t, prepared)
	remaining := operation.ExpiresAt.Sub(time.Now())
	if operation.Kind != pulseOperationPermissionDecision || operation.Review == nil || operation.Review.Target.ID != sess.ID || operation.Review.Decision != "deny" || operation.Review.Action != "deny" || operation.Review.Tool != "bash" || operation.Review.Scope != "one-time permission request" || remaining > pulsePermissionOperationTTL || remaining < pulsePermissionOperationTTL-time.Second {
		t.Fatalf("unsafe or incomplete permission review: %#v", operation)
	}
	if strings.Contains(strings.ToLower(prepared.Body.String()), strings.ToLower(secret)) || strings.Contains(prepared.Body.String(), "Authorization") || strings.Contains(prepared.Body.String(), "echo '") {
		t.Fatalf("permission review exposed raw arguments: %s", prepared.Body.String())
	}
	contents, err := os.ReadFile(mgr.pulseOperations.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(contents, []byte(secret)) || bytes.Contains(contents, []byte("Authorization")) || bytes.Contains(contents, []byte("echo '")) || bytes.Contains(contents, []byte("permission_feedback")) || bytes.Contains(contents, []byte("please tell the agent")) {
		t.Fatalf("permission operation store retained raw arguments: %s", contents)
	}
	if pending := pulseOperationRequest(handler, http.MethodGet, "/api/pulse/operations/"+operation.OperationID, "", credential.Credential); pending.Code != http.StatusOK || decodePulseOperation(t, pending).Review == nil {
		t.Fatalf("pending permission query = %d: %s", pending.Code, pending.Body.String())
	}

	if mutable := pulseOperationRequest(handler, http.MethodPost, "/api/pulse/operations/"+operation.OperationID+"/confirm", `{"approved":true}`, credential.Credential); mutable.Code != http.StatusBadRequest {
		t.Fatalf("permission mutable confirm = %d, want 400", mutable.Code)
	}
	resolved := make(chan bus.PermissionResolved, 2)
	sess.runtime.Bus.Subscribe(func(event bus.PermissionResolved) { resolved <- event })
	confirmed := pulseOperationRequest(handler, http.MethodPost, "/api/pulse/operations/"+operation.OperationID+"/confirm", `{}`, credential.Credential)
	if confirmed.Code != http.StatusOK {
		t.Fatalf("confirm permission = %d: %s", confirmed.Code, confirmed.Body.String())
	}
	receipt := decodePulseOperation(t, confirmed).Receipt
	if receipt == nil || receipt.Status != "rejected" || receipt.Action != "deny" || receipt.Delivery != "not_applicable" || receipt.Observation != "permission_resolved" || receipt.Completion != "" {
		t.Fatalf("permission receipt = %#v", receipt)
	}
	if strings.Contains(confirmed.Body.String(), "completion") {
		t.Fatalf("permission receipt must not claim completion: %s", confirmed.Body.String())
	}
	sess.runtime.Bus.Drain(time.Second)
	select {
	case event := <-resolved:
		if event.ID == "" {
			t.Fatal("permission resolution event omitted id")
		}
	case <-time.After(time.Second):
		t.Fatal("canonical permission resolver was not invoked")
	}
	replay := pulseOperationRequest(handler, http.MethodPost, "/api/pulse/operations/"+operation.OperationID+"/confirm", `{}`, credential.Credential)
	if replay.Code != http.StatusOK || decodePulseOperation(t, replay).Receipt == nil {
		t.Fatalf("permission replay = %d: %s", replay.Code, replay.Body.String())
	}
	sess.runtime.Bus.Drain(time.Second)
	select {
	case event := <-resolved:
		t.Fatalf("permission resolver replayed: %#v", event)
	default:
	}
}

func TestPulsePermissionDecisionApproveOnceConcurrentConfirmIsCanonicalOnce(t *testing.T) {
	if !deviceStoreLockSupported() {
		t.Skip("device auth fails closed where advisory process locks are unavailable")
	}
	_, sess, handler, credential := pendingPulsePermissionOperation(t, "true")
	prepared := pulseOperationRequest(handler, http.MethodPost, "/api/pulse/operations/prepare", `{"kind":"permission_decision","target":"`+sess.ID+`","decision":"approve_once"}`, credential.Credential)
	if prepared.Code != http.StatusCreated {
		t.Fatalf("prepare approve_once = %d: %s", prepared.Code, prepared.Body.String())
	}
	operation := decodePulseOperation(t, prepared)
	resolved := make(chan bus.PermissionResolved, 2)
	sess.runtime.Bus.Subscribe(func(event bus.PermissionResolved) { resolved <- event })

	const confirms = 8
	results := make(chan pulseOperationReceipt, confirms)
	errs := make(chan string, confirms)
	var wg sync.WaitGroup
	for range confirms {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response := pulseOperationRequest(handler, http.MethodPost, "/api/pulse/operations/"+operation.OperationID+"/confirm", `{}`, credential.Credential)
			if response.Code != http.StatusOK {
				errs <- response.Body.String()
				return
			}
			receipt := decodePulseOperation(t, response).Receipt
			if receipt == nil {
				errs <- "missing receipt"
				return
			}
			results <- *receipt
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent permission confirm failed: %s", err)
	}
	for receipt := range results {
		if receipt.Status != "accepted" || receipt.Action != "approve_once" || receipt.Observation != "permission_resolved" || receipt.Completion != "" {
			t.Fatalf("concurrent permission receipt = %#v", receipt)
		}
	}
	sess.runtime.Bus.Drain(time.Second)
	select {
	case <-resolved:
	case <-time.After(time.Second):
		t.Fatal("approve_once did not invoke the canonical resolver")
	}
	select {
	case event := <-resolved:
		t.Fatalf("approve_once invoked canonical resolver more than once: %#v", event)
	default:
	}
}

func TestPulsePermissionDecisionExpiryAndDeviceRevokeDoNotResolve(t *testing.T) {
	if !deviceStoreLockSupported() {
		t.Skip("device auth fails closed where advisory process locks are unavailable")
	}

	expiryMgr, expirySession, expiryHandler, expiryDevice := pendingPulsePermissionOperation(t, "true")
	expiring := pulseOperationRequest(expiryHandler, http.MethodPost, "/api/pulse/operations/prepare", `{"kind":"permission_decision","target":"`+expirySession.ID+`","decision":"approve_once"}`, expiryDevice.Credential)
	if expiring.Code != http.StatusCreated {
		t.Fatalf("prepare expiring permission = %d: %s", expiring.Code, expiring.Body.String())
	}
	expiringOperation := decodePulseOperation(t, expiring)
	// The store's injectable clock is the same authoritative expiry boundary
	// used by beginConfirm; a stale review must not resolve the still-pending
	// canonical permission.
	expiryMgr.pulseOperations.now = func() time.Time { return expiringOperation.ExpiresAt.Add(time.Second) }
	expired := pulseOperationRequest(expiryHandler, http.MethodPost, "/api/pulse/operations/"+expiringOperation.OperationID+"/confirm", `{}`, expiryDevice.Credential)
	if expired.Code != http.StatusOK {
		t.Fatalf("confirm expired permission = %d: %s", expired.Code, expired.Body.String())
	}
	receipt := decodePulseOperation(t, expired).Receipt
	if receipt == nil || receipt.Status != "rejected" || receipt.Reason != "review_expired" {
		t.Fatalf("expired permission receipt = %#v", receipt)
	}
	if _, err := bus.QueryTyped[bus.GetPermissionDecisionSnapshot, bus.PermissionDecisionSnapshot](expirySession.runtime.Bus, bus.GetPermissionDecisionSnapshot{SessionID: expirySession.ID}); err != nil {
		t.Fatalf("expiry resolved the permission: %v", err)
	}

	owner := &http.Cookie{Name: authCookieName, Value: "owner"}
	revokeDevice := pulseOperationDevice(t, expiryHandler, owner, "revoked permission phone")
	prepared := pulseOperationRequest(expiryHandler, http.MethodPost, "/api/pulse/operations/prepare", `{"kind":"permission_decision","target":"`+expirySession.ID+`","decision":"approve_once"}`, revokeDevice.Credential)
	if prepared.Code != http.StatusCreated {
		t.Fatalf("prepare revoked permission = %d: %s", prepared.Code, prepared.Body.String())
	}
	operation := decodePulseOperation(t, prepared)
	if revoked := pairingRequest(expiryHandler, http.MethodPost, "/api/pulse/devices/"+revokeDevice.DeviceID+"/revoke", `{}`, owner, ""); revoked.Code != http.StatusNoContent {
		t.Fatalf("revoke device = %d: %s", revoked.Code, revoked.Body.String())
	}
	stored, err := expiryMgr.pulseOperations.get(operation.OperationID, revokeDevice.DeviceID)
	if err != nil || stored.Receipt == nil || stored.Receipt.Status != "rejected" || stored.Receipt.Reason != "device_inactive" {
		t.Fatalf("revoked permission operation = %#v, %v", stored, err)
	}
	if _, err := bus.QueryTyped[bus.GetPermissionDecisionSnapshot, bus.PermissionDecisionSnapshot](expirySession.runtime.Bus, bus.GetPermissionDecisionSnapshot{SessionID: expirySession.ID}); err != nil {
		t.Fatalf("device revoke resolved the permission: %v", err)
	}
}

func TestPulsePermissionReviewRedactsSensitiveValuesAndControls(t *testing.T) {
	input := "tool\x00 password hunter2 authorization: Bearer secret-token key=private-value"
	got := redactPulseReviewText(input, 80)
	for _, secret := range []string{"hunter2", "secret-token", "private-value", "\x00"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redaction retained %q in %q", secret, got)
		}
	}
	if !strings.Contains(got, "[redacted]") || utf8.RuneCountInString(got) > 81 {
		t.Fatalf("unsafe redaction result %q", got)
	}
}

func TestPulseOperationDeviceOnlyStrictInstructionReceiptAndReplay(t *testing.T) {
	if !deviceStoreLockSupported() {
		t.Skip("device auth fails closed where advisory process locks are unavailable")
	}
	provider := newMockProvider(simpleResponseHandler("ok"))
	mgr := newTestManager(t, context.Background(), provider)
	sess, err := mgr.CreateSession(CreateOpts{Title: "release API"})
	if err != nil {
		t.Fatal(err)
	}
	devicePath := filepath.Join(t.TempDir(), "devices.json")
	handler := NewServer(mgr, WithAuthToken("owner", false), WithDeviceStorePath(devicePath))
	owner := &http.Cookie{Name: authCookieName, Value: "owner"}
	credential := pulseOperationDevice(t, handler, owner, "first phone")

	legacy := pairingRequest(handler, http.MethodPost, "/api/pulse/operations/prepare", `{"kind":"directed_instruction","target":"release API","text":"legacy"}`, owner, "")
	if legacy.Code != http.StatusForbidden {
		t.Fatalf("legacy owner operation auth = %d, want 403", legacy.Code)
	}
	withoutCSRF := httptest.NewRequest(http.MethodPost, "/api/pulse/operations/prepare", strings.NewReader(`{"kind":"directed_instruction","target":"release API","text":"missing csrf"}`))
	withoutCSRF.Host = "localhost"
	withoutCSRF.RemoteAddr = "127.0.0.1:12345"
	withoutCSRF.Header.Set("Content-Type", "application/json")
	withoutCSRF.Header.Set("Authorization", deviceAuthorizationScheme+" "+credential.Credential)
	withoutCSRFRec := httptest.NewRecorder()
	handler.ServeHTTP(withoutCSRFRec, withoutCSRF)
	if withoutCSRFRec.Code != http.StatusForbidden {
		t.Fatalf("operation without CSRF = %d, want 403", withoutCSRFRec.Code)
	}
	insecure := httptest.NewRequest(http.MethodPost, "/api/pulse/operations/prepare", strings.NewReader(`{"kind":"directed_instruction","target":"release API","text":"insecure"}`))
	insecure.Host = "localhost"
	insecure.RemoteAddr = "192.0.2.10:12345"
	insecure.Header.Set("Content-Type", "application/json")
	insecure.Header.Set("X-Moa-Request", "1")
	insecure.Header.Set("Authorization", deviceAuthorizationScheme+" "+credential.Credential)
	insecureRec := httptest.NewRecorder()
	handler.ServeHTTP(insecureRec, insecure)
	if insecureRec.Code != http.StatusUpgradeRequired {
		t.Fatalf("non-loopback non-TLS operation = %d, want 426", insecureRec.Code)
	}
	badHost := httptest.NewRequest(http.MethodPost, "/api/pulse/operations/prepare", strings.NewReader(`{"kind":"directed_instruction","target":"release API","text":"bad host"}`))
	badHost.Host = "attacker.example"
	badHost.RemoteAddr = "127.0.0.1:12345"
	badHost.Header.Set("Content-Type", "application/json")
	badHost.Header.Set("X-Moa-Request", "1")
	badHost.Header.Set("Authorization", deviceAuthorizationScheme+" "+credential.Credential)
	badHostRec := httptest.NewRecorder()
	handler.ServeHTTP(badHostRec, badHost)
	if badHostRec.Code != http.StatusForbidden {
		t.Fatalf("operation host check = %d, want 403", badHostRec.Code)
	}
	if badType := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/pulse/operations/prepare", strings.NewReader(`{}`))
		req.Host, req.RemoteAddr = "localhost", "127.0.0.1:12345"
		req.Header.Set("Content-Type", "text/plain")
		req.Header.Set("X-Moa-Request", "1")
		req.Header.Set("Authorization", deviceAuthorizationScheme+" "+credential.Credential)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}(); badType.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("operation non-JSON = %d, want 415", badType.Code)
	}
	if strict := pulseOperationRequest(handler, http.MethodPost, "/api/pulse/operations/prepare", `{"kind":"directed_instruction","target":"release API","text":"x","confirmed":true}`, credential.Credential); strict.Code != http.StatusBadRequest {
		t.Fatalf("operation unknown/confirmed field = %d, want 400", strict.Code)
	}
	if unsupported := pulseOperationRequest(handler, http.MethodPost, "/api/pulse/operations/prepare", `{"kind":"permission_decision","session_id":"anything"}`, credential.Credential); unsupported.Code != http.StatusBadRequest {
		t.Fatalf("unsupported operation kind = %d, want 400", unsupported.Code)
	}
	if query := pulseOperationRequest(handler, http.MethodPost, "/api/pulse/operations/prepare?unexpected=true", `{"kind":"directed_instruction","target":"release API","text":"x"}`, credential.Credential); query.Code != http.StatusBadRequest {
		t.Fatalf("operation query parameters = %d, want 400", query.Code)
	}

	text := "directive-private-after-confirm"
	prepared := pulseOperationRequest(handler, http.MethodPost, "/api/pulse/operations/prepare", `{"kind":"directed_instruction","target":" release API ","text":"`+text+`"}`, credential.Credential)
	if prepared.Code != http.StatusCreated {
		t.Fatalf("prepare = %d: %s", prepared.Code, prepared.Body.String())
	}
	review := decodePulseOperation(t, prepared)
	if review.Kind != pulseOperationDirectedInstruction || review.Status != "pending_confirmation" || review.Review == nil || review.Review.Target.ID != sess.ID || review.Review.Target.Title != "release API" || review.Review.Text != text || review.Review.Action != "send" || review.ExpiresAt.IsZero() {
		t.Fatalf("unsafe or incomplete instruction review: %#v", review)
	}
	if provider.calls.Load() != 0 {
		t.Fatalf("prepare invoked provider %d times", provider.calls.Load())
	}
	status := pulseOperationRequest(handler, http.MethodGet, "/api/pulse/operations/"+review.OperationID, "", credential.Credential)
	if status.Code != http.StatusOK || decodePulseOperation(t, status).Review == nil {
		t.Fatalf("pending status = %d: %s", status.Code, status.Body.String())
	}
	secondDevice := pulseOperationDevice(t, handler, owner, "second phone")
	if mismatch := pulseOperationRequest(handler, http.MethodPost, "/api/pulse/operations/"+review.OperationID+"/confirm", `{}`, secondDevice.Credential); mismatch.Code != http.StatusForbidden {
		t.Fatalf("device mismatch confirm = %d, want 403", mismatch.Code)
	}
	if mutable := pulseOperationRequest(handler, http.MethodPost, "/api/pulse/operations/"+review.OperationID+"/confirm", `{"confirmed":true}`, credential.Credential); mutable.Code != http.StatusBadRequest {
		t.Fatalf("confirm mutable gesture = %d, want 400", mutable.Code)
	}

	confirmed := pulseOperationRequest(handler, http.MethodPost, "/api/pulse/operations/"+review.OperationID+"/confirm", `{}`, credential.Credential)
	if confirmed.Code != http.StatusOK {
		t.Fatalf("confirm = %d: %s", confirmed.Code, confirmed.Body.String())
	}
	receiptResponse := decodePulseOperation(t, confirmed)
	if receiptResponse.Receipt == nil || receiptResponse.Receipt.Status != "accepted" || receiptResponse.Receipt.Action != "send" || receiptResponse.Receipt.Delivery != "delivered_to_agent" || receiptResponse.Receipt.Observation != "not_observed" || receiptResponse.Receipt.Completion != "not_observed" {
		t.Fatalf("instruction receipt claimed wrong semantics: %#v", receiptResponse.Receipt)
	}
	if strings.Contains(strings.ToLower(confirmed.Body.String()), "done") {
		t.Fatalf("receipt must not claim completion: %s", confirmed.Body.String())
	}
	replay := pulseOperationRequest(handler, http.MethodPost, "/api/pulse/operations/"+review.OperationID+"/confirm", `{}`, credential.Credential)
	if replay.Code != http.StatusOK {
		t.Fatalf("confirm replay = %d: %s", replay.Code, replay.Body.String())
	}
	replayReceipt := decodePulseOperation(t, replay).Receipt
	if replayReceipt == nil || *replayReceipt != *receiptResponse.Receipt {
		t.Fatalf("replay receipt = %#v, want %#v", replayReceipt, receiptResponse.Receipt)
	}
	storedReceipt := pulseOperationRequest(handler, http.MethodGet, "/api/pulse/operations/"+review.OperationID, "", credential.Credential)
	if storedReceipt.Code != http.StatusOK || decodePulseOperation(t, storedReceipt).Receipt == nil {
		t.Fatalf("receipt status = %d: %s", storedReceipt.Code, storedReceipt.Body.String())
	}
	contents, err := os.ReadFile(mgr.pulseOperations.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(contents, []byte(text)) || bytes.Contains(contents, []byte("transcript")) {
		t.Fatalf("final Pulse operation store retained private instruction: %s", contents)
	}
}

func TestPulseOperationConcurrentConfirmsDeliverOneCanonicalInstruction(t *testing.T) {
	if !deviceStoreLockSupported() {
		t.Skip("device auth fails closed where advisory process locks are unavailable")
	}
	provider := newMockProvider(simpleResponseHandler("ok"))
	mgr := newTestManager(t, context.Background(), provider)
	sess, err := mgr.CreateSession(CreateOpts{Title: "concurrent"})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(mgr, WithAuthToken("owner", false), WithDeviceStorePath(filepath.Join(t.TempDir(), "devices.json")))
	credential := pulseOperationDevice(t, handler, &http.Cookie{Name: authCookieName, Value: "owner"}, "phone")
	prepared := pulseOperationRequest(handler, http.MethodPost, "/api/pulse/operations/prepare", `{"kind":"directed_instruction","target":"concurrent","text":"deliver exactly once"}`, credential.Credential)
	if prepared.Code != http.StatusCreated {
		t.Fatalf("prepare concurrent operation = %d: %s", prepared.Code, prepared.Body.String())
	}
	operation := decodePulseOperation(t, prepared)

	const confirms = 12
	results := make(chan pulseOperationReceipt, confirms)
	errs := make(chan string, confirms)
	var wg sync.WaitGroup
	for range confirms {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := pulseOperationRequest(handler, http.MethodPost, "/api/pulse/operations/"+operation.OperationID+"/confirm", `{}`, credential.Credential)
			if rec.Code != http.StatusOK {
				errs <- rec.Body.String()
				return
			}
			var response pulseOperationResponse
			if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
				errs <- "invalid receipt JSON: " + err.Error()
				return
			}
			if response.Receipt == nil {
				errs <- "missing receipt"
				return
			}
			results <- *response.Receipt
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent confirm failed: %s", err)
	}
	var first pulseOperationReceipt
	count := 0
	for receipt := range results {
		count++
		if receipt.Status != "accepted" || receipt.Action != "send" || receipt.Delivery != "delivered_to_agent" {
			t.Fatalf("concurrent receipt = %#v", receipt)
		}
		if first.OperationID == "" {
			first = receipt
		} else if receipt != first {
			t.Fatalf("concurrent receipt changed: %#v, want %#v", receipt, first)
		}
	}
	if count != confirms {
		t.Fatalf("concurrent receipt count = %d, want %d", count, confirms)
	}
	mgr.instructionMu.Lock()
	records := append([]instructionRequest(nil), mgr.instructionRequests[sess.ID]...)
	mgr.instructionMu.Unlock()
	if len(records) != 1 || records[0].id != "pulse."+operation.OperationID || records[0].action != "send" {
		t.Fatalf("canonical instruction records = %#v", records)
	}
	pollUntil(t, time.Second, "one provider invocation", func() bool { return provider.calls.Load() == 1 })
}

func TestPulseOperationInstructionAmbiguityScopeChangeExpiryAndSteer(t *testing.T) {
	if !deviceStoreLockSupported() {
		t.Skip("device auth fails closed where advisory process locks are unavailable")
	}
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	first, err := mgr.CreateSession(CreateOpts{Title: "duplicate"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.CreateSession(CreateOpts{Title: "duplicate"}); err != nil {
		t.Fatal(err)
	}
	handler := NewServer(mgr, WithAuthToken("owner", false), WithDeviceStorePath(filepath.Join(t.TempDir(), "devices.json")))
	credential := pulseOperationDevice(t, handler, &http.Cookie{Name: authCookieName, Value: "owner"}, "phone")
	ambiguous := pulseOperationRequest(handler, http.MethodPost, "/api/pulse/operations/prepare", `{"kind":"directed_instruction","target":"duplicate","text":"choose nothing"}`, credential.Credential)
	if ambiguous.Code != http.StatusConflict || !strings.Contains(ambiguous.Body.String(), first.ID) {
		t.Fatalf("ambiguous prepare = %d: %s", ambiguous.Code, ambiguous.Body.String())
	}
	first.runtime.State.ForceState(bus.StatePermission)
	if blocked := pulseOperationRequest(handler, http.MethodPost, "/api/pulse/operations/prepare", `{"kind":"directed_instruction","target":"`+first.ID+`","text":"must not bypass permission"}`, credential.Credential); blocked.Code != http.StatusConflict {
		t.Fatalf("permission-blocked instruction prepare = %d, want 409", blocked.Code)
	}
	first.runtime.State.ForceState(bus.StateIdle)

	prepared := pulseOperationRequest(handler, http.MethodPost, "/api/pulse/operations/prepare", `{"kind":"directed_instruction","target":"`+first.ID+`","text":"reviewed send"}`, credential.Credential)
	if prepared.Code != http.StatusCreated {
		t.Fatalf("prepare send = %d: %s", prepared.Code, prepared.Body.String())
	}
	operation := decodePulseOperation(t, prepared)
	first.runtime.State.ForceState(bus.StateRunning)
	stale := pulseOperationRequest(handler, http.MethodPost, "/api/pulse/operations/"+operation.OperationID+"/confirm", `{}`, credential.Credential)
	if stale.Code != http.StatusOK {
		t.Fatalf("stale confirm = %d: %s", stale.Code, stale.Body.String())
	}
	if got := decodePulseOperation(t, stale).Receipt; got == nil || got.Status != "rejected" || got.Reason != "review_expired" {
		t.Fatalf("stale scope receipt = %#v", got)
	}

	first.runtime.State.ForceState(bus.StateIdle)
	expiring := pulseOperationRequest(handler, http.MethodPost, "/api/pulse/operations/prepare", `{"kind":"directed_instruction","target":"`+first.ID+`","text":"expire"}`, credential.Credential)
	if expiring.Code != http.StatusCreated {
		t.Fatalf("prepare expiry = %d: %s", expiring.Code, expiring.Body.String())
	}
	expiringOp := decodePulseOperation(t, expiring)
	now := time.Now().UTC()
	mgr.pulseOperations.now = func() time.Time { return now.Add(pulseOperationTTL + time.Second) }
	expired := pulseOperationRequest(handler, http.MethodPost, "/api/pulse/operations/"+expiringOp.OperationID+"/confirm", `{}`, credential.Credential)
	if got := decodePulseOperation(t, expired).Receipt; expired.Code != http.StatusOK || got == nil || got.Status != "rejected" || got.Reason != "review_expired" {
		t.Fatalf("expiry receipt = status %d, %#v", expired.Code, got)
	}
	mgr.pulseOperations.now = time.Now

	first.runtime.State.ForceState(bus.StateRunning)
	steer := pulseOperationRequest(handler, http.MethodPost, "/api/pulse/operations/prepare", `{"kind":"directed_instruction","target":"`+first.ID+`","text":"reviewed steer"}`, credential.Credential)
	steerOp := decodePulseOperation(t, steer)
	if steer.Code != http.StatusCreated || steerOp.Review == nil || steerOp.Review.Action != "steer" {
		t.Fatalf("prepare steer = %d: %s", steer.Code, steer.Body.String())
	}
	steered := pulseOperationRequest(handler, http.MethodPost, "/api/pulse/operations/"+steerOp.OperationID+"/confirm", `{}`, credential.Credential)
	if got := decodePulseOperation(t, steered).Receipt; steered.Code != http.StatusOK || got == nil || got.Status != "accepted" || got.Action != "steer" {
		t.Fatalf("steer receipt = status %d, %#v", steered.Code, got)
	}
}

func TestPulseOperationStoreDurabilityAndConcurrentConfirm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "operations.json")
	store, err := openOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if second, err := openOperationStore(path); err == nil {
		second.Close()
		t.Fatal("second process opened Pulse operation store")
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("operation directory permissions = %v, err=%v", info.Mode().Perm(), err)
	}
	id, err := newPulseOperationID()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.create(durableOperation{ID: id, DeviceID: "device-a", Kind: pulseOperationDirectedInstruction, PayloadDigest: "digest", Text: "private pending directive"}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("operation file permissions = %v, err=%v", info.Mode().Perm(), err)
	}
	if info, err := os.Stat(path + ".lock"); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("operation lock permissions = %v, err=%v", info.Mode().Perm(), err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = openOperationStore(path)
	if err != nil {
		t.Fatalf("restart store = %v", err)
	}
	defer store.Close()
	if operation, err := store.get(id, "device-a"); err != nil || operation.Text != "private pending directive" || operation.State != "pending" {
		t.Fatalf("durable pending operation = %#v, %v", operation, err)
	}

	concurrentID, err := newPulseOperationID()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.create(durableOperation{ID: concurrentID, DeviceID: "device-a", Kind: pulseOperationDirectedInstruction, PayloadDigest: "digest", Text: "private concurrent directive"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	starts := make(chan operationConfirmStart, 12)
	errs := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start, err := store.beginConfirm(concurrentID, "device-a")
			if err == nil {
				starts <- start
			} else {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(starts)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent begin = %v", err)
	}
	var executor int
	var waiters []<-chan struct{}
	for start := range starts {
		if start.Execute {
			executor++
		} else if start.Wait != nil {
			waiters = append(waiters, start.Wait)
		} else {
			t.Fatalf("unexpected concurrent start: %#v", start)
		}
	}
	if executor != 1 || len(waiters) != 11 {
		t.Fatalf("concurrent confirms executors=%d waiters=%d", executor, len(waiters))
	}
	receipt := pulseOperationReceipt{OperationID: concurrentID, Kind: pulseOperationDirectedInstruction, Status: "accepted", Delivery: "delivered_to_agent", Observation: "not_observed", Completion: "not_observed", At: time.Now().UTC()}
	if _, err := store.finishConfirm(concurrentID, receipt); err != nil {
		t.Fatal(err)
	}
	for _, waiter := range waiters {
		select {
		case <-waiter:
		case <-time.After(time.Second):
			t.Fatal("concurrent confirm waiter was not released")
		}
	}
	if got, err := store.finalizedReceipt(concurrentID, "device-a"); err != nil || got != receipt {
		t.Fatalf("concurrent receipt = %#v, %v", got, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = openOperationStore(path)
	if err != nil {
		t.Fatalf("restart receipt store = %v", err)
	}
	defer store.Close()
	if got, err := store.finalizedReceipt(concurrentID, "device-a"); err != nil || got != receipt {
		t.Fatalf("restarted receipt = %#v, %v", got, err)
	}

	store.now = func() time.Time { return time.Now().Add(pulseOperationTTL + time.Second) }
	if expired, err := store.get(id, "device-a"); err != nil || expired.Receipt == nil || expired.Receipt.Reason != "review_expired" {
		t.Fatalf("expired durable operation = %#v, %v", expired, err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(contents, []byte("private pending directive")) || bytes.Contains(contents, []byte("private concurrent directive")) {
		t.Fatalf("expired/final store retained directive text: %s", contents)
	}
	if !errors.Is(func() error { _, err := store.get(id, "device-b"); return err }(), errOperationDeviceMismatch) {
		t.Fatal("store did not bind operation to the original device")
	}
}
