package permission

import (
	"context"
	"testing"
	"time"
)

func TestYolo_ApprovesEverything(t *testing.T) {
	g := New(ModeYolo, nil)

	for _, tool := range []string{"bash", "write", "edit", "read", "ls"} {
		if d := g.Check(context.Background(), tool, nil); d != nil {
			t.Errorf("yolo mode should approve %s, got block: %s", tool, d.Reason)
		}
	}
}

func TestAsk_ApprovesReadOnly(t *testing.T) {
	g := New(ModeAsk, nil)

	for _, tool := range []string{"read", "ls", "grep", "find"} {
		if d := g.Check(context.Background(), tool, nil); d != nil {
			t.Errorf("ask mode should auto-approve %s", tool)
		}
	}
}

func TestAsk_BlocksWriteTools(t *testing.T) {
	g := New(ModeAsk, nil)

	for _, tool := range []string{"bash", "write", "edit"} {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)

		// Respond with denial from a goroutine
		go func() {
			select {
			case req := <-g.Requests():
				req.Response <- false
			case <-ctx.Done():
			}
		}()

		d := g.Check(ctx, tool, map[string]any{"command": "ls"})
		cancel()
		if d == nil || !d.Block {
			t.Errorf("ask mode should block %s when denied", tool)
		}
	}
}

func TestAsk_ApprovesWhenUserSaysYes(t *testing.T) {
	g := New(ModeAsk, nil)

	go func() {
		req := <-g.Requests()
		req.Response <- true
	}()

	d := g.Check(context.Background(), "bash", map[string]any{"command": "ls"})
	if d != nil {
		t.Errorf("should approve when user says yes, got: %s", d.Reason)
	}
}

func TestAsk_ContextCancellation(t *testing.T) {
	g := New(ModeAsk, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	d := g.Check(ctx, "bash", nil)
	if d == nil || !d.Block {
		t.Error("should block on cancelled context")
	}
}

func TestAsk_RequestCarriesToolInfo(t *testing.T) {
	g := New(ModeAsk, nil)
	args := map[string]any{"command": "rm -rf /"}

	go func() {
		req := <-g.Requests()
		if req.ToolName != "bash" {
			t.Errorf("expected tool name bash, got %s", req.ToolName)
		}
		if req.Args["command"] != "rm -rf /" {
			t.Errorf("expected command in args")
		}
		req.Response <- false
	}()

	g.Check(context.Background(), "bash", args)
}

func TestAuto_FallsBackToAsk(t *testing.T) {
	g := New(ModeAuto, []string{"allow npm scripts"})

	// Auto mode without evaluator should still ask for write tools
	go func() {
		req := <-g.Requests()
		req.Response <- true
	}()

	d := g.Check(context.Background(), "write", map[string]any{"path": "test.txt"})
	if d != nil {
		t.Error("should approve when user approves in auto fallback")
	}
}
