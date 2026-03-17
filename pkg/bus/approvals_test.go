package bus

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/permission"
)

func newTestApprovalManager(t *testing.T) (*ApprovalManager, EventBus) {
	t.Helper()
	b := NewLocalBus()
	t.Cleanup(func() { b.Close() })
	sm := NewStateMachine(b, "test")
	sm.ForceState(StateRunning) // permission transitions require running state
	am := NewApprovalManager(b, sm, "test")
	return am, b
}

func TestApprovalManager_PermissionBridge(t *testing.T) {
	am, b := newTestApprovalManager(t)

	// Create a gate and start bridge.
	gate := permission.New(permission.ModeAsk, permission.Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	am.StartPermissionBridge(ctx, gate)

	gotPerm := make(chan PermissionRequested, 1)
	b.Subscribe(func(e PermissionRequested) { gotPerm <- e })

	// Simulate a gate request (in a goroutine because Check blocks).
	respCh := make(chan permission.Response, 1)
	go func() {
		gate.Requests() // drain is not how it works — Check sends to reqCh internally
	}()

	// Directly push to gate's request channel via Check in a goroutine.
	go func() {
		gate.Check(ctx, "write", map[string]any{"path": "foo.go"})
	}()

	// Wait for PermissionRequested event.
	b.Drain(time.Second)
	select {
	case e := <-gotPerm:
		if e.ToolName != "write" {
			t.Fatalf("ToolName = %q, want write", e.ToolName)
		}
		if e.AllowPattern == "" {
			t.Fatal("AllowPattern should not be empty")
		}
		// Clean up: resolve to unblock the Check goroutine.
		_ = am.ResolvePermission(e.ID, true, "", "")
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for PermissionRequested")
	}
	_ = respCh // prevent unused
}

func TestApprovalManager_ResolvePermission(t *testing.T) {
	am, b := newTestApprovalManager(t)

	respCh := make(chan permission.Response, 1)

	// Manually register a pending permission (bypass bridge for unit test).
	am.mu.Lock()
	am.perms["p1"] = &PendingPermission{
		ID:           "p1",
		ToolName:     "write",
		Args:         map[string]any{"path": "test.go"},
		AllowPattern: "write(test.go)",
		response:     respCh,
	}
	am.mu.Unlock()

	// Force state to permission.
	am.state.ForceState(StatePermission)

	gotResolved := make(chan PermissionResolved, 1)
	b.Subscribe(func(e PermissionResolved) { gotResolved <- e })

	err := am.ResolvePermission("p1", true, "ok", "write(*)")
	if err != nil {
		t.Fatal(err)
	}

	// Check response was sent.
	select {
	case resp := <-respCh:
		if !resp.Approved {
			t.Fatal("expected approved")
		}
		if resp.Allow != "write(*)" {
			t.Fatalf("Allow = %q", resp.Allow)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for response")
	}

	// Check PermissionResolved event.
	b.Drain(time.Second)
	select {
	case e := <-gotResolved:
		if e.ID != "p1" {
			t.Fatalf("ID = %q", e.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for PermissionResolved")
	}

	// State should be running.
	if am.state.Current() != StateRunning {
		t.Fatalf("state = %q, want running", am.state.Current())
	}
}

func TestApprovalManager_ResolvePermission_Idempotent(t *testing.T) {
	am, _ := newTestApprovalManager(t)

	respCh := make(chan permission.Response, 1)
	am.mu.Lock()
	am.perms["p1"] = &PendingPermission{
		ID: "p1", ToolName: "write", response: respCh,
	}
	am.mu.Unlock()

	am.state.ForceState(StatePermission)

	// First resolve.
	if err := am.ResolvePermission("p1", true, "", ""); err != nil {
		t.Fatal(err)
	}
	// Second resolve — should be idempotent (p1 is already removed).
	err := am.ResolvePermission("p1", false, "", "")
	if err == nil {
		t.Fatal("expected error for unknown ID after resolution")
	}
}

func TestApprovalManager_ResolvePermission_UnknownID(t *testing.T) {
	am, _ := newTestApprovalManager(t)

	err := am.ResolvePermission("nonexistent", true, "", "")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestApprovalManager_StopBridge_AutoDenies(t *testing.T) {
	am, _ := newTestApprovalManager(t)

	respCh := make(chan permission.Response, 1)
	am.mu.Lock()
	am.perms["p1"] = &PendingPermission{
		ID: "p1", ToolName: "write", response: respCh,
	}
	am.mu.Unlock()

	am.state.ForceState(StatePermission)

	am.StopPermissionBridge()

	// Response should be auto-denied.
	select {
	case resp := <-respCh:
		if resp.Approved {
			t.Fatal("expected denied")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout — agent blocked forever")
	}

	// State should transition back to running.
	if am.state.Current() != StateRunning {
		t.Fatalf("state = %q, want running", am.state.Current())
	}
}

func TestApprovalManager_ResolveAskUser(t *testing.T) {
	am, b := newTestApprovalManager(t)

	respCh := make(chan []string, 1)
	am.mu.Lock()
	am.asks["a1"] = &PendingAsk{
		ID:        "a1",
		Questions: []AskQuestion{{Text: "Name?"}},
		response:  respCh,
	}
	am.mu.Unlock()

	gotResolved := make(chan AskUserResolved, 1)
	b.Subscribe(func(e AskUserResolved) { gotResolved <- e })

	err := am.ResolveAskUser("a1", []string{"Bob"})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case answers := <-respCh:
		if len(answers) != 1 || answers[0] != "Bob" {
			t.Fatalf("answers = %v", answers)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}

	b.Drain(time.Second)
	select {
	case e := <-gotResolved:
		if e.ID != "a1" {
			t.Fatalf("ID = %q", e.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout for AskUserResolved")
	}
}

func TestApprovalManager_ResolveAskUser_WrongAnswerCount(t *testing.T) {
	am, _ := newTestApprovalManager(t)

	respCh := make(chan []string, 1)
	am.mu.Lock()
	am.asks["a1"] = &PendingAsk{
		ID:        "a1",
		Questions: []AskQuestion{{Text: "Q1"}, {Text: "Q2"}},
		response:  respCh,
	}
	am.mu.Unlock()

	err := am.ResolveAskUser("a1", []string{"only one"})
	if err == nil {
		t.Fatal("expected error for wrong answer count")
	}
}

func TestApprovalManager_PendingInfo(t *testing.T) {
	am, _ := newTestApprovalManager(t)

	// Empty.
	info := am.PendingInfo()
	if info.Permission != nil || info.Ask != nil {
		t.Fatal("expected empty info")
	}

	// Add pending.
	respPerm := make(chan permission.Response, 1)
	respAsk := make(chan []string, 1)
	am.mu.Lock()
	am.perms["p1"] = &PendingPermission{
		ID: "p1", ToolName: "write", AllowPattern: "write(*)", response: respPerm,
	}
	am.asks["a1"] = &PendingAsk{
		ID: "a1", Questions: []AskQuestion{{Text: "Q?"}}, response: respAsk,
	}
	am.mu.Unlock()

	info = am.PendingInfo()
	if info.Permission == nil {
		t.Fatal("expected permission info")
	}
	if info.Permission.ID != "p1" || info.Permission.AllowPattern != "write(*)" {
		t.Fatalf("permission = %+v", info.Permission)
	}
	if info.Ask == nil || info.Ask.ID != "a1" {
		t.Fatal("expected ask info")
	}
}

func TestApprovalManager_ValidatePending(t *testing.T) {
	am, _ := newTestApprovalManager(t)

	respCh := make(chan permission.Response, 1)
	am.mu.Lock()
	am.perms["p1"] = &PendingPermission{
		ID: "p1", ToolName: "write", response: respCh,
	}
	am.mu.Unlock()

	if err := am.ValidatePending("p1"); err != nil {
		t.Fatalf("ValidatePending(p1) = %v", err)
	}
	if err := am.ValidatePending("nonexistent"); err == nil {
		t.Fatal("expected error for nonexistent")
	}
}

func TestApprovalManager_Stop_Idempotent(t *testing.T) {
	am, _ := newTestApprovalManager(t)
	am.Stop()
	am.Stop() // should not panic
}

func TestApprovalManager_ConcurrentResolve(t *testing.T) {
	am, _ := newTestApprovalManager(t)

	respCh := make(chan permission.Response, 1)
	am.mu.Lock()
	am.perms["p1"] = &PendingPermission{
		ID: "p1", ToolName: "write", response: respCh,
	}
	am.mu.Unlock()

	am.state.ForceState(StatePermission)

	var wg sync.WaitGroup
	errs := make([]error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = am.ResolvePermission("p1", true, "", "")
		}(i)
	}
	wg.Wait()

	// Exactly one should succeed, rest should get "unknown" error.
	successCount := 0
	for _, err := range errs {
		if err == nil {
			successCount++
		}
	}
	if successCount != 1 {
		t.Fatalf("expected exactly 1 success, got %d", successCount)
	}
}
