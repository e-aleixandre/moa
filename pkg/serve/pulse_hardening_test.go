package serve

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPairedDeviceRouteClampdownKeepsOwnerLegacySurface(t *testing.T) {
	if !deviceStoreLockSupported() {
		t.Skip("device auth fails closed where advisory process locks are unavailable")
	}
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	sess, err := mgr.CreateSession(CreateOpts{Title: "route clamp"})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(mgr, WithAuthToken("owner", false), WithDeviceStorePath(filepath.Join(t.TempDir(), "devices.json")))
	owner := &http.Cookie{Name: authCookieName, Value: "owner"}
	device := pulseOperationDevice(t, handler, owner, "clamped phone")

	for _, path := range []string{
		"/api/sessions",
		"/api/ops/overview",
		"/api/sessions/" + sess.ID,
		"/api/sessions/" + sess.ID + "/messages",
		"/api/sessions/" + sess.ID + "/subagents",
	} {
		if got := pairingRequest(handler, http.MethodGet, path, "", nil, device.Credential); got.Code != http.StatusOK {
			t.Fatalf("device safe read %s = %d: %s", path, got.Code, got.Body.String())
		}
	}

	for _, request := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/api/sessions", `{"title":"blocked"}`},
		{http.MethodPost, "/api/sessions/" + sess.ID + "/send", `{"text":"blocked"}`},
		{http.MethodPost, "/api/sessions/" + sess.ID + "/instruction", `{"text":"blocked","request_id":"blocked"}`},
		{http.MethodPost, "/api/sessions/" + sess.ID + "/permission", `{}`},
		{http.MethodPost, "/api/sessions/" + sess.ID + "/ask", `{}`},
		{http.MethodPost, "/api/sessions/" + sess.ID + "/cancel", `{}`},
		{http.MethodPatch, "/api/sessions/" + sess.ID + "/config", `{}`},
		{http.MethodPost, "/api/sessions/" + sess.ID + "/command", `{"command":"/clear"}`},
		{http.MethodPost, "/api/sessions/" + sess.ID + "/shell", `{"command":"true"}`},
		{http.MethodPost, "/api/sessions/" + sess.ID + "/branch", `{"entry_id":"x"}`},
		{http.MethodPost, "/api/ops/instruction", `{"target":"route clamp","text":"blocked","request_id":"blocked"}`},
		{http.MethodPost, "/api/ops/ask", `{}`},
		{http.MethodPost, "/api/push/subscribe", `{}`},
		{http.MethodPost, "/api/pulse/pairings", `{}`},
		{http.MethodPost, "/api/pulse/pairings/claim", `{}`},
		{http.MethodGet, "/api/pulse/devices", ""},
		{http.MethodPost, "/api/pulse/devices/" + device.DeviceID + "/revoke", `{}`},
	} {
		got := pairingRequest(handler, request.method, request.path, request.body, nil, device.Credential)
		if got.Code != http.StatusForbidden {
			t.Fatalf("device legacy/admin route %s %s = %d, want 403: %s", request.method, request.path, got.Code, got.Body.String())
		}
	}

	ownerInstruction := pairingRequest(handler, http.MethodPost, "/api/sessions/"+sess.ID+"/instruction", `{"text":"owner legacy remains available","request_id":"owner-legacy"}`, owner, "")
	if ownerInstruction.Code != http.StatusAccepted {
		t.Fatalf("owner legacy instruction = %d: %s", ownerInstruction.Code, ownerInstruction.Body.String())
	}
}

func TestOperationStoreAdmissionNeverEvictsYoungRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.json")
	store, err := openOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	store.now = func() time.Time { return now }
	store.maxRecords = 5
	store.maxPending = 2
	store.maxPendingPerDevice = 1

	create := func(device string) string {
		t.Helper()
		id, err := newPulseOperationID()
		if err != nil {
			t.Fatal(err)
		}
		if err := store.create(durableOperation{ID: id, DeviceID: device, Kind: pulseOperationDirectedInstruction, PayloadDigest: "digest", Text: "private"}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	finish := func(id, device string) {
		t.Helper()
		start, err := store.beginConfirm(id, device)
		if err != nil || !start.Execute {
			t.Fatalf("begin %s = %#v, %v", id, start, err)
		}
		receipt := acceptedOperationReceipt(start.Operation, "send", now)
		if _, err := store.finishConfirm(id, receipt); err != nil {
			t.Fatal(err)
		}
	}

	first := create("device-a")
	if id, err := newPulseOperationID(); err != nil {
		t.Fatal(err)
	} else if err := store.create(durableOperation{ID: id, DeviceID: "device-a", Kind: pulseOperationDirectedInstruction}); !errors.Is(err, errOperationAdmission) {
		t.Fatalf("per-device pending admission = %v, want limit", err)
	}
	second := create("device-b")
	if id, err := newPulseOperationID(); err != nil {
		t.Fatal(err)
	} else if err := store.create(durableOperation{ID: id, DeviceID: "device-c", Kind: pulseOperationDirectedInstruction}); !errors.Is(err, errOperationAdmission) {
		t.Fatalf("global pending admission = %v, want limit", err)
	}
	for _, item := range []struct{ id, device string }{{first, "device-a"}, {second, "device-b"}} {
		if operation, err := store.get(item.id, item.device); err != nil || operation.State != "pending" {
			t.Fatalf("admission lost pending %s: %#v, %v", item.id, operation, err)
		}
		finish(item.id, item.device)
	}

	records := []struct{ id, device string }{{first, "device-a"}, {second, "device-b"}}
	for len(records) < store.maxRecords {
		device := "device-a"
		if len(records)%2 == 0 {
			device = "device-b"
		}
		id := create(device)
		finish(id, device)
		records = append(records, struct{ id, device string }{id, device})
	}
	if id, err := newPulseOperationID(); err != nil {
		t.Fatal(err)
	} else if err := store.create(durableOperation{ID: id, DeviceID: "device-c", Kind: pulseOperationDirectedInstruction}); !errors.Is(err, errOperationAdmission) {
		t.Fatalf("young receipt admission = %v, want limit", err)
	}
	for _, item := range records {
		operation, err := store.get(item.id, item.device)
		if err != nil || operation.Receipt == nil || operation.Receipt.Status != "accepted" {
			t.Fatalf("young receipt was lost: %#v, %v", operation, err)
		}
	}

	now = now.Add(pulseOperationReceiptTTL + time.Second)
	if id, err := newPulseOperationID(); err != nil {
		t.Fatal(err)
	} else if err := store.create(durableOperation{ID: id, DeviceID: "device-c", Kind: pulseOperationDirectedInstruction}); err != nil {
		t.Fatalf("admission after receipt TTL = %v", err)
	}
}

func TestPulseOperationAdmissionReturns429WithoutDroppingReview(t *testing.T) {
	if !deviceStoreLockSupported() {
		t.Skip("device auth fails closed where advisory process locks are unavailable")
	}
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	if _, err := mgr.CreateSession(CreateOpts{Title: "admission review"}); err != nil {
		t.Fatal(err)
	}
	mgr.pulseOperations.maxRecords = 1
	handler := NewServer(mgr, WithAuthToken("owner", false), WithDeviceStorePath(filepath.Join(t.TempDir(), "devices.json")))
	credential := pulseOperationDevice(t, handler, &http.Cookie{Name: authCookieName, Value: "owner"}, "admission phone")
	first := pulseOperationRequest(handler, http.MethodPost, "/api/pulse/operations/prepare", `{"kind":"directed_instruction","target":"admission review","text":"first"}`, credential.Credential)
	if first.Code != http.StatusCreated {
		t.Fatalf("first prepare = %d: %s", first.Code, first.Body.String())
	}
	operation := decodePulseOperation(t, first)
	second := pulseOperationRequest(handler, http.MethodPost, "/api/pulse/operations/prepare", `{"kind":"directed_instruction","target":"admission review","text":"second"}`, credential.Credential)
	if second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") == "" {
		t.Fatalf("capacity prepare = %d, Retry-After=%q: %s", second.Code, second.Header().Get("Retry-After"), second.Body.String())
	}
	status := pulseOperationRequest(handler, http.MethodGet, "/api/pulse/operations/"+operation.OperationID, "", credential.Credential)
	if status.Code != http.StatusOK || decodePulseOperation(t, status).Review == nil {
		t.Fatalf("admission dropped first review: %d %s", status.Code, status.Body.String())
	}
}

func TestOperationStoreFinalPersistenceFailureRetainsKnownReceipt(t *testing.T) {
	dir := t.TempDir()
	store, err := openOperationStore(filepath.Join(dir, "operations.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	id, err := newPulseOperationID()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.create(durableOperation{ID: id, DeviceID: "device", Kind: pulseOperationDirectedInstruction, PayloadDigest: "digest"}); err != nil {
		t.Fatal(err)
	}
	start, err := store.beginConfirm(id, "device")
	if err != nil || !start.Execute {
		t.Fatalf("begin = %#v, %v", start, err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	receipt := acceptedOperationReceipt(start.Operation, "send", time.Now().UTC())
	got, err := store.finishConfirm(id, receipt)
	if err == nil || got != receipt {
		t.Fatalf("final persistence failure receipt = %#v, %v", got, err)
	}
	if replay, err := store.finalizedReceipt(id, "device"); err != nil || replay != receipt {
		t.Fatalf("known in-process receipt was discarded: %#v, %v", replay, err)
	}
	if err := store.create(durableOperation{ID: "another", DeviceID: "device", Kind: pulseOperationDirectedInstruction}); !errors.Is(err, errOperationStoreUnavailable) {
		t.Fatalf("degraded store accepted a prepare: %v", err)
	}
}

func TestPulseRecoveryUsesCanonicalLedgerWithoutDuplicateDelivery(t *testing.T) {
	provider := newMockProvider(simpleResponseHandler("ok"))
	mgr := newTestManager(t, context.Background(), provider)
	sess, err := mgr.CreateSession(CreateOpts{Title: "recover once"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := newPulseOperationID()
	if err != nil {
		t.Fatal(err)
	}
	op := durableOperation{
		ID: id, DeviceID: "device", Kind: pulseOperationDirectedInstruction, PayloadDigest: "digest",
		Target: opsInstructionTarget{ID: sess.ID, Title: "recover once", Project: sess.CWD}, Text: "deliver once", ExpectedAction: "send",
	}
	if err := mgr.pulseOperations.create(op); err != nil {
		t.Fatal(err)
	}
	if start, err := mgr.pulseOperations.beginConfirm(id, "device"); err != nil || !start.Execute {
		t.Fatalf("begin confirmation = %#v, %v", start, err)
	}
	if _, err := mgr.voiceInstructionExpected(sess.ID, "deliver once", "pulse."+id, "send"); err != nil {
		t.Fatalf("canonical delivery = %v", err)
	}
	pollUntil(t, time.Second, "one canonical provider call", func() bool { return provider.calls.Load() == 1 })

	path := mgr.pulseOperations.path
	if err := mgr.pulseOperations.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := openOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	mgr.pulseOperations = store
	mgr.recoverPulseConfirmations()
	response, err := mgr.pulseOperationStatus("device", id)
	if err != nil || response.Receipt == nil || response.Receipt.Status != "accepted" || response.Receipt.Action != "send" {
		t.Fatalf("recovered receipt = %#v, %v", response, err)
	}
	time.Sleep(100 * time.Millisecond)
	if calls := provider.calls.Load(); calls != 1 {
		t.Fatalf("recovery delivered a duplicate instruction: provider calls=%d", calls)
	}
}

func TestPulseRecoveryDoesNotRetryUnknownConfirmation(t *testing.T) {
	provider := newMockProvider(simpleResponseHandler("ok"))
	mgr := newTestManager(t, context.Background(), provider)
	sess, err := mgr.CreateSession(CreateOpts{Title: "unknown recovery"})
	if err != nil {
		t.Fatal(err)
	}
	newConfirming := func(expired bool) string {
		t.Helper()
		id, err := newPulseOperationID()
		if err != nil {
			t.Fatal(err)
		}
		op := durableOperation{
			ID: id, DeviceID: "device", Kind: pulseOperationDirectedInstruction, PayloadDigest: "digest",
			Target: opsInstructionTarget{ID: sess.ID, Title: "unknown recovery", Project: sess.CWD}, Text: "must not retry", ExpectedAction: "send",
		}
		if err := mgr.pulseOperations.create(op); err != nil {
			t.Fatal(err)
		}
		if start, err := mgr.pulseOperations.beginConfirm(id, "device"); err != nil || !start.Execute {
			t.Fatalf("begin unknown confirmation = %#v, %v", start, err)
		}
		if expired {
			mgr.pulseOperations.mu.Lock()
			index, stored, ok := mgr.pulseOperations.findWithIndexLocked(id)
			if !ok {
				mgr.pulseOperations.mu.Unlock()
				t.Fatal("new operation disappeared")
			}
			stored.ExpiresAt = time.Now().UTC().Add(-time.Second)
			mgr.pulseOperations.state.Operations[index] = stored
			err := mgr.pulseOperations.saveLocked()
			mgr.pulseOperations.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	validID := newConfirming(false)
	expiredID := newConfirming(true)

	path := mgr.pulseOperations.path
	if err := mgr.pulseOperations.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := openOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	mgr.pulseOperations = store
	mgr.recoverPulseConfirmations()
	valid, err := mgr.pulseOperationStatus("device", validID)
	if err != nil || valid.Status != "confirming" || valid.Receipt != nil {
		t.Fatalf("valid unknown recovery should wait for explicit confirm: %#v, %v", valid, err)
	}
	expired, err := mgr.pulseOperationStatus("device", expiredID)
	if err != nil || expired.Receipt == nil || expired.Receipt.Status != "indeterminate" || expired.Receipt.Delivery != "indeterminate" {
		t.Fatalf("expired unknown recovery = %#v, %v", expired, err)
	}
	if calls := provider.calls.Load(); calls != 0 {
		t.Fatalf("recovery retried an unknown delivery: provider calls=%d", calls)
	}
}

func TestPulsePermissionRecoveryAfterAttemptIsIndeterminateAndNeverReruns(t *testing.T) {
	provider := newMockProvider(simpleResponseHandler("unexpected"))
	mgr := newTestManager(t, context.Background(), provider)
	sess, err := mgr.CreateSession(CreateOpts{Title: "permission crash"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := newPulseOperationID()
	if err != nil {
		t.Fatal(err)
	}
	op := durableOperation{
		ID:                           id,
		DeviceID:                     "device",
		Kind:                         pulseOperationPermissionDecision,
		PayloadDigest:                "private-binding",
		Target:                       opsInstructionTarget{ID: sess.ID, Title: "permission crash"},
		PermissionID:                 "runtime_only_permission",
		PermissionRunGen:             4,
		PermissionTool:               "bash",
		PermissionAllowPatternDigest: "private-scope-digest",
		PermissionArgsDigest:         "private-args-digest",
		PermissionDecision:           "approve_once",
	}
	if err := mgr.pulseOperations.create(op); err != nil {
		t.Fatal(err)
	}
	if start, err := mgr.pulseOperations.beginConfirm(id, "device"); err != nil || !start.Execute {
		t.Fatalf("begin permission confirmation = %#v, %v", start, err)
	}
	if err := mgr.pulseOperations.markAttempt(id); err != nil {
		t.Fatal(err)
	}

	path := mgr.pulseOperations.path
	if err := mgr.pulseOperations.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := openOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	mgr.pulseOperations = store
	mgr.recoverPulseConfirmations()
	response, err := mgr.pulseOperationStatus("device", id)
	if err != nil || response.Receipt == nil || response.Receipt.Status != "indeterminate" || response.Receipt.Delivery != "indeterminate" || response.Receipt.Completion != "" {
		t.Fatalf("permission crash receipt = %#v, %v", response, err)
	}
	if calls := provider.calls.Load(); calls != 0 {
		t.Fatalf("permission recovery invoked provider %d times", calls)
	}
}

func TestDeviceRevokeAndExpiryInvalidateOperationsAtExecutionBoundary(t *testing.T) {
	if !deviceStoreLockSupported() {
		t.Skip("device auth fails closed where advisory process locks are unavailable")
	}
	operations, err := openOperationStore(filepath.Join(t.TempDir(), "operations.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer operations.Close()
	devices, err := openDeviceStore(filepath.Join(t.TempDir(), "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer devices.Close()
	devices.setDeactivationHook(operations.invalidateDevice)
	pairing, err := devices.createPairing("token", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := devices.claim("127.0.0.1", pairing.PairingID, pairingPayloadSecret(t, pairing), "race phone")
	if err != nil {
		t.Fatal(err)
	}
	createPending := func() string {
		id, err := newPulseOperationID()
		if err != nil {
			t.Fatal(err)
		}
		if err := operations.create(durableOperation{ID: id, DeviceID: credential.DeviceID, Kind: pulseOperationDirectedInstruction, PayloadDigest: "digest"}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	pending := createPending()

	entered := make(chan struct{})
	release := make(chan struct{})
	var executions atomic.Int32
	executed := make(chan error, 1)
	go func() {
		executed <- devices.withActiveDevice(credential.DeviceID, func() error {
			close(entered)
			<-release
			executions.Add(1)
			return nil
		})
	}()
	<-entered
	revoked := make(chan error, 1)
	go func() { revoked <- devices.revoke(credential.DeviceID, "token") }()
	select {
	case err := <-revoked:
		t.Fatalf("revoke returned before protected execution boundary released: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-executed; err != nil {
		t.Fatalf("pre-revoke protected execution = %v", err)
	}
	if err := <-revoked; err != nil {
		t.Fatal(err)
	}
	before := executions.Load()
	if err := devices.withActiveDevice(credential.DeviceID, func() error { executions.Add(1); return nil }); !errors.Is(err, errInvalidDeviceCredential) {
		t.Fatalf("post-revoke execution guard = %v", err)
	}
	if executions.Load() != before {
		t.Fatalf("execution started after revoke returned")
	}
	if operation, err := operations.get(pending, credential.DeviceID); err != nil || operation.Receipt == nil || operation.Receipt.Status != "rejected" {
		t.Fatalf("revoke did not synchronously invalidate pending operation: %#v, %v", operation, err)
	}

	expiringPair, err := devices.createPairing("token", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	expiring, err := devices.claim("127.0.0.1", expiringPair.PairingID, pairingPayloadSecret(t, expiringPair), "expiry phone")
	if err != nil {
		t.Fatal(err)
	}
	expiringOperation := createPendingForDevice(t, operations, expiring.DeviceID)
	devices.mu.Lock()
	for i := range devices.state.Devices {
		if devices.state.Devices[i].ID == expiring.DeviceID {
			expires := time.Now().UTC().Add(-time.Second)
			devices.state.Devices[i].ExpiresAt = expires
		}
	}
	devices.mu.Unlock()
	if err := devices.withActiveDevice(expiring.DeviceID, func() error { t.Fatal("expired device executed"); return nil }); !errors.Is(err, errInvalidDeviceCredential) {
		t.Fatalf("expired execution guard = %v", err)
	}
	if operation, err := operations.get(expiringOperation, expiring.DeviceID); err != nil || operation.Receipt == nil || operation.Receipt.Status != "rejected" {
		t.Fatalf("expiry did not invalidate pending operation: %#v, %v", operation, err)
	}
}

func TestPulseConfirmRaceWithDeviceRevoke(t *testing.T) {
	if !deviceStoreLockSupported() {
		t.Skip("device auth fails closed where advisory process locks are unavailable")
	}
	provider := newMockProvider(simpleResponseHandler("ok"))
	mgr := newTestManager(t, context.Background(), provider)
	if _, err := mgr.CreateSession(CreateOpts{Title: "confirm revoke race"}); err != nil {
		t.Fatal(err)
	}
	devices, err := openDeviceStore(filepath.Join(t.TempDir(), "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer devices.Close()
	devices.setDeactivationHook(mgr.invalidatePulseDeviceOperations)
	pairing, err := devices.createPairing("token", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := devices.claim("127.0.0.1", pairing.PairingID, pairingPayloadSecret(t, pairing), "confirm race")
	if err != nil {
		t.Fatal(err)
	}
	prepared, _, err := mgr.preparePulseOperation(credential.DeviceID, pulseOperationPrepareBody{
		Kind: pulseOperationDirectedInstruction, Target: "confirm revoke race", Text: "deliver at most once",
	})
	if err != nil {
		t.Fatal(err)
	}

	const confirms = 12
	start := make(chan struct{})
	results := make(chan pulseOperationReceipt, confirms)
	errs := make(chan error, confirms)
	var wg sync.WaitGroup
	for range confirms {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			receipt, err := mgr.confirmPulseOperation(context.Background(), devices, credential.DeviceID, prepared.OperationID)
			if err != nil {
				errs <- err
				return
			}
			results <- receipt
		}()
	}
	close(start)
	if err := devices.revoke(credential.DeviceID, "token"); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("confirm/revoke race returned an error: %v", err)
	}
	for receipt := range results {
		switch receipt.Status {
		case "accepted", "rejected", "indeterminate":
		default:
			t.Fatalf("unexpected confirm/revoke receipt: %#v", receipt)
		}
	}
	if replay, err := mgr.confirmPulseOperation(context.Background(), devices, credential.DeviceID, prepared.OperationID); err != nil {
		t.Fatalf("post-revoke replay lost final receipt: %v", err)
	} else if replay.Status != "accepted" && replay.Status != "rejected" && replay.Status != "indeterminate" {
		t.Fatalf("post-revoke replay = %#v", replay)
	}
	if calls := provider.calls.Load(); calls > 1 {
		t.Fatalf("confirm/revoke race duplicated canonical delivery: provider calls=%d", calls)
	}
}

func createPendingForDevice(t *testing.T, operations *operationStore, deviceID string) string {
	t.Helper()
	id, err := newPulseOperationID()
	if err != nil {
		t.Fatal(err)
	}
	if err := operations.create(durableOperation{ID: id, DeviceID: deviceID, Kind: pulseOperationDirectedInstruction, PayloadDigest: "digest"}); err != nil {
		t.Fatal(err)
	}
	return id
}
