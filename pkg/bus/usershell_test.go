package bus

import (
	"context"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// RunUserShell — idle append
// ---------------------------------------------------------------------------

func TestRunUserShell_IdleAppends(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	executed := make(chan UserShellExecuted, 1)
	b.Subscribe(func(e UserShellExecuted) { executed <- e })

	res := RunUserShell(context.Background(), sctx, "echo hi", false)

	if res.Delivered != UserShellDeliveryAppend {
		t.Fatalf("Delivered = %q, want %q", res.Delivered, UserShellDeliveryAppend)
	}
	if res.DeliveryErr != nil {
		t.Fatalf("DeliveryErr = %v, want nil", res.DeliveryErr)
	}
	if !strings.Contains(res.Output, "hi") {
		t.Fatalf("Output = %q, want to contain %q", res.Output, "hi")
	}

	msgs := fa.Messages()
	if len(msgs) != 1 {
		t.Fatalf("agent messages = %d, want 1", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Fatalf("appended message role = %q, want %q", msgs[0].Role, "user")
	}
	if fa.getSteered() != "" {
		t.Fatalf("steer should not have been called, got %q", fa.getSteered())
	}

	e := drainChan(executed, b, t)
	if e.Delivered != UserShellDeliveryAppend {
		t.Fatalf("event Delivered = %q, want %q", e.Delivered, UserShellDeliveryAppend)
	}
	if e.SessionID != sctx.SessionID {
		t.Fatalf("event SessionID = %q, want %q", e.SessionID, sctx.SessionID)
	}
}

// TestRunUserShell_IdleSilentAppendsWithShellRole verifies "!!" while idle is
// still delivered (unlike the busy case), tagged with role "shell".
func TestRunUserShell_IdleSilentAppendsWithShellRole(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	res := RunUserShell(context.Background(), sctx, "echo hi", true)

	if res.Delivered != UserShellDeliveryAppend {
		t.Fatalf("Delivered = %q, want %q", res.Delivered, UserShellDeliveryAppend)
	}
	msgs := fa.Messages()
	if len(msgs) != 1 {
		t.Fatalf("agent messages = %d, want 1", len(msgs))
	}
	if msgs[0].Role != "shell" {
		t.Fatalf("appended message role = %q, want %q", msgs[0].Role, "shell")
	}
}

// ---------------------------------------------------------------------------
// RunUserShell — permission/running steer
// ---------------------------------------------------------------------------

func TestRunUserShell_RunningSteers(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	if err := sctx.State.Transition(StateRunning); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	res := RunUserShell(context.Background(), sctx, "echo steer-me", false)

	if res.Delivered != UserShellDeliverySteer {
		t.Fatalf("Delivered = %q, want %q", res.Delivered, UserShellDeliverySteer)
	}
	if res.DeliveryErr != nil {
		t.Fatalf("DeliveryErr = %v, want nil", res.DeliveryErr)
	}
	if !strings.Contains(fa.getSteered(), "steer-me") {
		t.Fatalf("steered = %q, want to contain %q", fa.getSteered(), "steer-me")
	}
	if len(fa.Messages()) != 0 {
		t.Fatalf("agent messages = %d, want 0 (should not append while running)", len(fa.Messages()))
	}
}

// TestRunUserShell_PermissionStateSteers ensures StatePermission (an approval
// prompt in progress) is treated as "busy" like StateRunning, per the doc
// comment on RunUserShell.
func TestRunUserShell_PermissionStateSteers(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	if err := sctx.State.Transition(StateRunning); err != nil {
		t.Fatalf("Transition to running: %v", err)
	}
	if err := sctx.State.Transition(StatePermission); err != nil {
		t.Fatalf("Transition to permission: %v", err)
	}

	res := RunUserShell(context.Background(), sctx, "echo during-permission", false)

	if res.Delivered != UserShellDeliverySteer {
		t.Fatalf("Delivered = %q, want %q", res.Delivered, UserShellDeliverySteer)
	}
	if !strings.Contains(fa.getSteered(), "during-permission") {
		t.Fatalf("steered = %q, want to contain %q", fa.getSteered(), "during-permission")
	}
}

// TestRunUserShell_RunningSilentNotDelivered verifies "!!" while busy is not
// delivered anywhere (neither steer nor append), so it doesn't interrupt a
// live run/approval.
func TestRunUserShell_RunningSilentNotDelivered(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	if err := sctx.State.Transition(StateRunning); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	res := RunUserShell(context.Background(), sctx, "echo silent", true)

	if res.Delivered != UserShellDeliveryNone {
		t.Fatalf("Delivered = %q, want %q", res.Delivered, UserShellDeliveryNone)
	}
	if fa.getSteered() != "" {
		t.Fatalf("steered = %q, want empty", fa.getSteered())
	}
	if len(fa.Messages()) != 0 {
		t.Fatalf("agent messages = %d, want 0", len(fa.Messages()))
	}
}

// ---------------------------------------------------------------------------
// RunUserShell — no State machine (nil State field), matches doc comment
// "sctx.State != nil" guard.
// ---------------------------------------------------------------------------

func TestRunUserShell_NilStateTreatedAsIdle(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa) // no State machine
	RegisterHandlers(sctx)

	res := RunUserShell(context.Background(), sctx, "echo no-state", false)

	if res.Delivered != UserShellDeliveryAppend {
		t.Fatalf("Delivered = %q, want %q", res.Delivered, UserShellDeliveryAppend)
	}
	if len(fa.Messages()) != 1 {
		t.Fatalf("agent messages = %d, want 1", len(fa.Messages()))
	}
}

// ---------------------------------------------------------------------------
// RunUserShell — output cap and timeout
// ---------------------------------------------------------------------------

func TestRunUserShell_OutputCapped(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	// Generate far more output than UserShellMaxOutput (50KB) so RunShell's
	// head+tail cap kicks in.
	res := RunUserShell(context.Background(), sctx, "yes x | head -c 200000", false)

	if len(res.Output) >= 200000 {
		t.Fatalf("Output len = %d, want capped well below 200000", len(res.Output))
	}
	if res.Output == "" {
		t.Fatal("Output empty, want some captured content")
	}
}

func TestRunUserShell_Timeout(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	// RunUserShell hard-codes UserShellTimeout (5 minutes) internally, which
	// is too slow for a unit test. Exercise the timeout path via the
	// underlying tool.RunShell/ShellConfig plumbing instead by using a very
	// short parent-context deadline; RunUserShell's internal timeout only
	// bounds via UserShellTimeout but tool.RunShell still respects ctx
	// cancellation for the exec itself.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	res := RunUserShell(ctx, sctx, "sleep 5", false)

	if !res.TimedOut && res.ExitCode == 0 {
		t.Fatalf("expected timeout or non-zero exit for a killed sleep, got TimedOut=%v ExitCode=%d", res.TimedOut, res.ExitCode)
	}
}
