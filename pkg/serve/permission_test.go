package serve

import (
	"context"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/permission"
)

func TestPermissionBridge_ApproveUnblocks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	gate := permission.New(permission.ModeAsk, permission.Config{})

	sess := &ManagedSession{
		ID:    "test",
		State: StateIdle,
		gate:  gate,
	}
	go sess.permissionBridge(ctx)

	// Simulate a permission request (like the agent would via gate.Check).
	respCh := make(chan permission.Response, 1)
	go func() {
		gate.Requests() // drain is not needed since we send directly
	}()

	// Send request directly to the gate's request channel.
	// We use askUser path: gate sends Request to its channel.
	// For testing, we simulate what happens when gate.Check calls askUser:
	// it sends a Request on the channel, which permissionBridge reads.
	done := make(chan struct{})
	go func() {
		defer close(done)
		// This blocks until permissionBridge reads it and we resolve.
		decision := gate.Check(ctx, "bash", map[string]any{"command": "rm -rf"})
		if decision != nil {
			t.Errorf("expected approval (nil decision), got: %+v", decision)
		}
	}()

	// Wait for the permission bridge to pick up the request.
	pollUntil(t, 2*time.Second, "pending permission set", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sess.pending != nil
	})

	// Verify state changed to permission.
	sess.mu.Lock()
	state := sess.State
	permID := sess.pending.ID
	sess.mu.Unlock()
	if state != StatePermission {
		t.Fatalf("expected permission state, got %s", state)
	}

	// Approve.
	err := sess.ResolvePermission(permID, true, "")
	if err != nil {
		t.Fatal(err)
	}

	// Wait for gate.Check to return.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("gate.Check didn't unblock after approval")
	}

	_ = respCh // not needed
}

func TestPermissionBridge_DenyUnblocks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	gate := permission.New(permission.ModeAsk, permission.Config{})

	sess := &ManagedSession{
		ID:    "test",
		State: StateIdle,
		gate:  gate,
	}
	go sess.permissionBridge(ctx)

	done := make(chan struct{})
	var decision *core.ToolCallDecision
	go func() {
		defer close(done)
		decision = gate.Check(ctx, "bash", map[string]any{"command": "rm -rf"})
	}()

	pollUntil(t, 2*time.Second, "pending permission set", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sess.pending != nil
	})

	sess.mu.Lock()
	permID := sess.pending.ID
	sess.mu.Unlock()

	err := sess.ResolvePermission(permID, false, "too dangerous")
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("gate.Check didn't unblock after denial")
	}

	if decision == nil || !decision.Block {
		t.Fatal("expected blocking decision after deny")
	}
	if decision.Reason != "too dangerous" {
		t.Fatalf("expected reason 'too dangerous', got %q", decision.Reason)
	}
}

func TestPermissionBridge_StaleID(t *testing.T) {
	sess := &ManagedSession{
		ID:    "test",
		State: StatePermission,
		pending: &pendingPermission{
			ID:       "perm_42",
			ToolName: "bash",
			response: make(chan<- permission.Response, 1),
		},
	}

	err := sess.ResolvePermission("perm_99", true, "")
	if err == nil {
		t.Fatal("expected error for stale permission ID")
	}
	if sess.pending == nil {
		t.Fatal("pending should not be cleared on stale ID")
	}
}

func TestPermissionBridge_DuplicateApprove(t *testing.T) {
	respCh := make(chan permission.Response, 1)
	sess := &ManagedSession{
		ID:    "test",
		State: StatePermission,
		pending: &pendingPermission{
			ID:       "perm_1",
			ToolName: "bash",
			response: respCh,
		},
	}

	// First approve.
	err := sess.ResolvePermission("perm_1", true, "")
	if err != nil {
		t.Fatal(err)
	}

	// Second approve — should be idempotent (returns nil, not error).
	err = sess.ResolvePermission("perm_1", true, "")
	if err != nil {
		t.Fatalf("expected idempotent success, got error: %v", err)
	}
	// Should not hang or panic.
}

func TestPermissionBridge_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	gate := permission.New(permission.ModeAsk, permission.Config{})

	sess := &ManagedSession{
		ID:    "test",
		State: StateIdle,
		gate:  gate,
	}

	done := make(chan struct{})
	go func() {
		sess.permissionBridge(ctx)
		close(done)
	}()

	// Cancel context — bridge goroutine should exit.
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("permissionBridge didn't exit after context cancel")
	}
}
