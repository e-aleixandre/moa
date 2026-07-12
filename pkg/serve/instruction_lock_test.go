package serve

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/core"
)

func TestInstructionStoreLockHelper(t *testing.T) {
	if os.Getenv("MOA_INSTRUCTION_LOCK_HELPER") != "1" {
		return
	}
	store, _, err := openInstructionStore(os.Getenv("MOA_INSTRUCTION_LOCK_PATH"))
	if err != nil {
		os.Exit(2)
	}
	if err := store.Close(); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

func runInstructionLockHelper(path string) error {
	cmd := exec.Command(os.Args[0], "-test.run=^TestInstructionStoreLockHelper$")
	cmd.Env = append(os.Environ(),
		"MOA_INSTRUCTION_LOCK_HELPER=1",
		"MOA_INSTRUCTION_LOCK_PATH="+path,
	)
	return cmd.Run()
}

func TestInstructionStoreHasLifetimeExclusiveProcessOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instruction-idempotency.json")
	if !instructionStoreLockSupported() {
		if _, _, err := openInstructionStore(path); !errors.Is(err, errInstructionStoreInUse) {
			t.Fatalf("unsupported-platform instruction lock = %v, want fail closed", err)
		}
		return
	}

	store, _, err := openInstructionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path + ".lock"); err != nil || info.Mode().Perm() != 0o600 {
		_ = store.Close()
		t.Fatalf("instruction lock permissions = %v, err=%v", info.Mode().Perm(), err)
	}
	if second, _, err := openInstructionStore(path); !errors.Is(err, errInstructionStoreInUse) {
		if second != nil {
			_ = second.Close()
		}
		_ = store.Close()
		t.Fatalf("second in-process instruction store = %v, want exclusive lock", err)
	}

	if err := runInstructionLockHelper(path); err == nil {
		_ = store.Close()
		t.Fatal("second process opened canonical instruction ledger while lock was held")
	} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 {
		_ = store.Close()
		t.Fatalf("second process lock result = %v, want exit 2", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := runInstructionLockHelper(path); err != nil {
		t.Fatalf("second process did not reacquire released instruction lock: %v", err)
	}
}

func instructionLockManager(t *testing.T, root, instructionPath, operationPath, sessionBase string, provider core.Provider) *Manager {
	t.Helper()
	cfg := core.MoaConfig{DisableSandbox: true}
	mgr := NewManager(context.Background(), ManagerConfig{
		ProviderFactory:    func(core.Model) (core.Provider, error) { return provider, nil },
		DefaultModel:       core.Model{ID: "test", Provider: "mock"},
		WorkspaceRoot:      root,
		MoaCfg:             cfg,
		ConfigLoader:       isolatedTestConfigLoader(t, cfg),
		SessionBaseDir:     sessionBase,
		SchedulePath:       filepath.Join(sessionBase, "schedules.json"),
		OpsPath:            filepath.Join(sessionBase, "ops.json"),
		InstructionPath:    instructionPath,
		PulseOperationPath: operationPath,
	})
	t.Cleanup(mgr.Shutdown)
	return mgr
}

func TestInstructionLedgerLockFailsClosedWithoutStalePulseWALOverwrite(t *testing.T) {
	if !instructionStoreLockSupported() {
		t.Skip("canonical instruction ledger fails closed where advisory process locks are unavailable")
	}
	root := t.TempDir()
	instructionPath := filepath.Join(root, "instruction-idempotency.json")
	operationPath := filepath.Join(root, "pulse-operations.json")
	sessionBase := filepath.Join(root, "sessions")
	provider := newMockProvider(simpleResponseHandler("ok"))

	first := instructionLockManager(t, root, instructionPath, operationPath, sessionBase, provider)
	sess, err := first.CreateSession(CreateOpts{Title: "exclusive recovery"})
	if err != nil {
		t.Fatal(err)
	}
	prepared, _, err := first.preparePulseOperation("device-a", pulseOperationPrepareBody{
		Kind:   pulseOperationDirectedInstruction,
		Target: "exclusive recovery",
		Text:   "persist the canonical WAL once",
	})
	if err != nil {
		t.Fatal(err)
	}
	if start, err := first.pulseOperations.beginConfirm(prepared.OperationID, "device-a"); err != nil || !start.Execute {
		t.Fatalf("begin confirmation = %#v, %v", start, err)
	}
	if action, err := first.voiceInstructionExpected(sess.ID, "persist the canonical WAL once", "pulse."+prepared.OperationID, "send"); err != nil || action != "send" {
		t.Fatalf("canonical Pulse WAL delivery = %q, %v", action, err)
	}
	pollUntil(t, time.Second, "one canonical delivery", func() bool { return provider.calls.Load() == 1 })
	ledgerBeforeBlockedManager, err := os.ReadFile(instructionPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := runInstructionLockHelper(instructionPath); err == nil {
		t.Fatal("second process opened the Pulse WAL ledger while its owner was live")
	} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 {
		t.Fatalf("cross-process Pulse WAL lock result = %v, want exit 2", err)
	}

	// This manager has its own session and operation ledgers, so its only
	// contention is the shared canonical instruction ledger. Before the lock,
	// it could load a stale snapshot and later overwrite the Pulse WAL.
	second := instructionLockManager(t, root, instructionPath, filepath.Join(root, "second-operations.json"), filepath.Join(root, "second-sessions"), provider)
	secondSession, err := second.CreateSession(CreateOpts{Title: "blocked writer"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.VoiceInstruction(secondSession.ID, "must not send", "blocked-legacy"); !errors.Is(err, errInstructionLedgerUnavailable) {
		t.Fatalf("second manager legacy instruction = %v, want ledger unavailable", err)
	}
	if state := secondSession.runtime.State.Current(); state != "idle" {
		t.Fatalf("blocked legacy instruction changed state to %s", state)
	}
	if _, _, err := second.preparePulseOperation("device-b", pulseOperationPrepareBody{
		Kind:   pulseOperationDirectedInstruction,
		Target: "blocked writer",
		Text:   "must not prepare",
	}); !errors.Is(err, errInstructionLedgerUnavailable) {
		t.Fatalf("second manager Pulse prepare = %v, want ledger unavailable", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/sessions/"+secondSession.ID+"/instruction", strings.NewReader(`{"text":"blocked endpoint","request_id":"blocked-http"}`))
	request.SetPathValue("id", secondSession.ID)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handleInstruction(second).ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("locked legacy instruction endpoint = %d: %s", response.Code, response.Body.String())
	}
	opsRequest := httptest.NewRequest(http.MethodPost, "/api/ops/instruction", strings.NewReader(`{"target":"blocked writer","text":"blocked ops endpoint","request_id":"blocked-ops"}`))
	opsRequest.Header.Set("Content-Type", "application/json")
	opsResponse := httptest.NewRecorder()
	handleOpsInstruction(second).ServeHTTP(opsResponse, opsRequest)
	if opsResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("locked Ops instruction endpoint = %d: %s", opsResponse.Code, opsResponse.Body.String())
	}
	ledgerAfterBlockedManager, err := os.ReadFile(instructionPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ledgerBeforeBlockedManager, ledgerAfterBlockedManager) {
		t.Fatal("blocked manager overwrote the canonical Pulse WAL with a stale snapshot")
	}
	if calls := provider.calls.Load(); calls != 1 {
		t.Fatalf("blocked manager delivered an instruction: provider calls=%d", calls)
	}

	// Simulate the original process exiting after canonical acceptance but
	// before operation receipt persistence. The blocked manager never acquired
	// the ledger and cannot become a later stale writer.
	first.Shutdown()
	second.Shutdown()

	third := instructionLockManager(t, root, instructionPath, operationPath, sessionBase, provider)
	status, err := third.pulseOperationStatus("device-a", prepared.OperationID)
	if err != nil || status.Receipt == nil || status.Receipt.Status != "accepted" || status.Receipt.Action != "send" {
		t.Fatalf("Pulse recovery after instruction-lock contention = %#v, %v", status, err)
	}
	if _, err := third.ResumeSession(sess.ID); err != nil {
		t.Fatalf("resume recovered session = %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if calls := provider.calls.Load(); calls != 1 {
		t.Fatalf("recovery retried a durable Pulse WAL delivery: provider calls=%d", calls)
	}
	third.Shutdown()

	store, _, err := openInstructionStore(instructionPath)
	if err != nil {
		t.Fatalf("instruction ledger was not released by Manager.Shutdown: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAttemptedPulseRecoveryWithUnavailableLedgerIsIndeterminate(t *testing.T) {
	if !instructionStoreLockSupported() {
		t.Skip("canonical instruction ledger fails closed where advisory process locks are unavailable")
	}
	root := t.TempDir()
	instructionPath := filepath.Join(root, "instruction-idempotency.json")
	operationPath := filepath.Join(root, "pulse-operations.json")
	provider := newMockProvider(simpleResponseHandler("unexpected"))

	first := instructionLockManager(t, root, instructionPath, operationPath, filepath.Join(root, "first-sessions"), provider)
	if _, err := first.CreateSession(CreateOpts{Title: "attempted recovery"}); err != nil {
		t.Fatal(err)
	}
	prepared, _, err := first.preparePulseOperation("device-attempted", pulseOperationPrepareBody{
		Kind:   pulseOperationDirectedInstruction,
		Target: "attempted recovery",
		Text:   "must not be retried without the ledger",
	})
	if err != nil {
		t.Fatal(err)
	}
	if start, err := first.pulseOperations.beginConfirm(prepared.OperationID, "device-attempted"); err != nil || !start.Execute {
		t.Fatalf("begin attempted confirmation = %#v, %v", start, err)
	}
	if err := first.pulseOperations.markAttempt(prepared.OperationID); err != nil {
		t.Fatalf("durably mark attempted = %v", err)
	}
	// Keep the first Manager's instruction ledger lock while releasing only the
	// operation store, modelling a restarted process whose canonical ledger is
	// currently locked/unavailable to the recovery process.
	if err := first.pulseOperations.Close(); err != nil {
		t.Fatal(err)
	}

	second := instructionLockManager(t, root, instructionPath, operationPath, filepath.Join(root, "second-sessions"), provider)
	if second.instructionLedgerAvailable() {
		t.Fatal("recovery manager unexpectedly acquired the locked canonical ledger")
	}
	status, err := second.pulseOperationStatus("device-attempted", prepared.OperationID)
	if err != nil || status.Receipt == nil {
		t.Fatalf("attempted recovery did not preserve a queryable receipt: %#v, %v", status, err)
	}
	receipt := *status.Receipt
	if receipt.Status != "indeterminate" || receipt.Delivery != "indeterminate" || receipt.Reason != "canonical_ledger_unavailable" {
		t.Fatalf("attempted recovery receipt = %#v, want canonical-ledger indeterminate", receipt)
	}
	if receipt.Delivery == "not_delivered" || receipt.Status == "rejected" {
		t.Fatalf("attempted recovery falsely rejected a possibly delivered action: %#v", receipt)
	}
	// Both query and retry return exactly the terminal receipt; neither may
	// invoke canonical delivery while the ledger remains unavailable.
	retry, err := second.confirmPulseOperation(context.Background(), &deviceStore{}, "device-attempted", prepared.OperationID)
	if err != nil || retry != receipt {
		t.Fatalf("attempted recovery retry = %#v, %v; want %#v", retry, err, receipt)
	}
	again, err := second.pulseOperationStatus("device-attempted", prepared.OperationID)
	if err != nil || again.Receipt == nil || *again.Receipt != receipt {
		t.Fatalf("attempted recovery query changed = %#v, %v", again, err)
	}
	time.Sleep(100 * time.Millisecond)
	if calls := provider.calls.Load(); calls != 0 {
		t.Fatalf("unavailable-ledger recovery retried SendPrompt/steer: provider calls=%d", calls)
	}

	// A genuinely new, non-attempted operation remains a truthful rejection
	// when delivery cannot start; only an existing durable Attempted marker is
	// elevated to indeterminate.
	newID, err := newPulseOperationID()
	if err != nil {
		t.Fatal(err)
	}
	if err := second.pulseOperations.create(durableOperation{ID: newID, DeviceID: "device-new", Kind: pulseOperationDirectedInstruction, PayloadDigest: "digest"}); err != nil {
		t.Fatal(err)
	}
	newReceipt, err := second.confirmPulseOperation(context.Background(), &deviceStore{}, "device-new", newID)
	if err != nil || newReceipt.Status != "rejected" || newReceipt.Delivery != "not_delivered" || newReceipt.Reason != "delivery_unavailable" {
		t.Fatalf("new non-attempted unavailable delivery = %#v, %v", newReceipt, err)
	}

	second.Shutdown()
	first.Shutdown()
	third := instructionLockManager(t, root, instructionPath, operationPath, filepath.Join(root, "third-sessions"), provider)
	stable, err := third.pulseOperationStatus("device-attempted", prepared.OperationID)
	if err != nil || stable.Receipt == nil || *stable.Receipt != receipt {
		t.Fatalf("attempted indeterminate receipt did not survive recovery restart: %#v, %v", stable, err)
	}
	if calls := provider.calls.Load(); calls != 0 {
		t.Fatalf("post-lock recovery retried SendPrompt/steer: provider calls=%d", calls)
	}
}

func TestNewNonAttemptedPulsePolicyRejectionRemainsRejected(t *testing.T) {
	provider := newMockProvider(simpleResponseHandler("unexpected"))
	mgr := newTestManager(t, context.Background(), provider)
	sess, err := mgr.CreateSession(CreateOpts{Title: "new policy rejection"})
	if err != nil {
		t.Fatal(err)
	}
	prepared, _, err := mgr.preparePulseOperation("device-new", pulseOperationPrepareBody{
		Kind:   pulseOperationDirectedInstruction,
		Target: "new policy rejection",
		Text:   "reviewed as send",
	})
	if err != nil {
		t.Fatal(err)
	}
	start, err := mgr.pulseOperations.beginConfirm(prepared.OperationID, "device-new")
	if err != nil || !start.Execute || start.Operation.Attempted {
		t.Fatalf("new non-attempted confirmation = %#v, %v", start, err)
	}
	// The reviewed send cannot silently become a steer. This ordinary new
	// policy rejection is still rejected/not_delivered, unlike recovery of an
	// already durable Attempted marker.
	sess.runtime.State.ForceState(bus.StateRunning)
	receipt := mgr.executePulseOperation(start.Operation)
	if receipt.Status != "rejected" || receipt.Delivery != "not_delivered" || receipt.Reason != "review_expired" {
		t.Fatalf("new policy rejection receipt = %#v", receipt)
	}
	time.Sleep(100 * time.Millisecond)
	if calls := provider.calls.Load(); calls != 0 {
		t.Fatalf("new policy rejection delivered a prompt: provider calls=%d", calls)
	}
}
