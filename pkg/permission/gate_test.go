package permission

import (
	"context"
	"testing"
	"time"
)

func TestYolo_ApprovesEverything(t *testing.T) {
	g := New(ModeYolo, Config{})

	for _, tool := range []string{"bash", "write", "edit", "read", "ls"} {
		if d := g.Check(context.Background(), tool, nil); d != nil {
			t.Errorf("yolo mode should approve %s, got block: %s", tool, d.Reason)
		}
	}
}

func TestAsk_ApprovesReadOnly(t *testing.T) {
	g := New(ModeAsk, Config{})

	for _, tool := range []string{"read", "ls", "grep", "find"} {
		if d := g.Check(context.Background(), tool, nil); d != nil {
			t.Errorf("ask mode should auto-approve %s", tool)
		}
	}
}

func TestAsk_BlocksWriteTools(t *testing.T) {
	g := New(ModeAsk, Config{})

	for _, tool := range []string{"bash", "write", "edit"} {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)

		go func() {
			select {
			case req := <-g.Requests():
				req.Response <- Response{Approved: false}
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
	g := New(ModeAsk, Config{})

	go func() {
		req := <-g.Requests()
		req.Response <- Response{Approved: true}
	}()

	d := g.Check(context.Background(), "bash", map[string]any{"command": "ls"})
	if d != nil {
		t.Errorf("should approve when user says yes, got: %s", d.Reason)
	}
}

func TestAsk_ContextCancellation(t *testing.T) {
	g := New(ModeAsk, Config{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	d := g.Check(ctx, "bash", nil)
	if d == nil || !d.Block {
		t.Error("should block on cancelled context")
	}
}

func TestAsk_RequestCarriesToolInfo(t *testing.T) {
	g := New(ModeAsk, Config{})
	args := map[string]any{"command": "rm -rf /"}

	go func() {
		req := <-g.Requests()
		if req.ToolName != "bash" {
			t.Errorf("expected tool name bash, got %s", req.ToolName)
		}
		if req.Args["command"] != "rm -rf /" {
			t.Errorf("expected command in args")
		}
		req.Response <- Response{Approved: false}
	}()

	g.Check(context.Background(), "bash", args)
}

func TestAsk_DenialWithFeedback(t *testing.T) {
	g := New(ModeAsk, Config{})

	go func() {
		req := <-g.Requests()
		req.Response <- Response{Approved: false, Feedback: "use a different filename"}
	}()

	d := g.Check(context.Background(), "write", map[string]any{"path": "test.txt"})
	if d == nil || !d.Block {
		t.Fatal("should block")
	}
	if d.Reason != "use a different filename" {
		t.Errorf("expected feedback as reason, got: %s", d.Reason)
	}
}

func TestAsk_AllowViaResponse(t *testing.T) {
	g := New(ModeAsk, Config{})

	go func() {
		req := <-g.Requests()
		req.Response <- Response{Approved: true, Allow: "Bash(git:*)"}
	}()

	d := g.Check(context.Background(), "bash", map[string]any{"command": "git status"})
	if d != nil {
		t.Error("should approve")
	}

	// The pattern should now be in the allow list
	d = g.Check(context.Background(), "bash", map[string]any{"command": "git log"})
	if d != nil {
		t.Error("git should be auto-approved after allow was added")
	}
}

func TestAsk_AllowListAutoApproves(t *testing.T) {
	g := New(ModeAsk, Config{Allow: []string{"Bash(git:*)", "Bash(npm:*)"}})

	// Allowed by pattern — no user prompt needed
	d := g.Check(context.Background(), "bash", map[string]any{"command": "git status"})
	if d != nil {
		t.Error("git should be auto-approved by allow list")
	}

	d = g.Check(context.Background(), "bash", map[string]any{"command": "npm test"})
	if d != nil {
		t.Error("npm should be auto-approved by allow list")
	}
}

func TestAsk_DenyListBlocksBeforeAllow(t *testing.T) {
	g := New(ModeAsk, Config{
		Allow: []string{"Bash(rm:*)"},  // would allow...
		Deny:  []string{"Bash(rm:*)"}, // ...but deny takes priority
	})

	d := g.Check(context.Background(), "bash", map[string]any{"command": "rm -rf /"})
	if d == nil || !d.Block {
		t.Error("deny should override allow")
	}
}

func TestAsk_AddAllowAtRuntime(t *testing.T) {
	g := New(ModeAsk, Config{})

	// Initially asks
	go func() {
		req := <-g.Requests()
		req.Response <- Response{Approved: true}
	}()
	g.Check(context.Background(), "bash", map[string]any{"command": "go test ./..."})

	// After adding allow, auto-approves
	g.AddAllow("Bash(go:*)")
	d := g.Check(context.Background(), "bash", map[string]any{"command": "go test ./..."})
	if d != nil {
		t.Error("should auto-approve after AddAllow")
	}
}

func TestAuto_FallsBackToAsk(t *testing.T) {
	g := New(ModeAuto, Config{Rules: []string{"allow npm scripts"}})

	go func() {
		req := <-g.Requests()
		req.Response <- Response{Approved: true}
	}()

	d := g.Check(context.Background(), "write", map[string]any{"path": "test.txt"})
	if d != nil {
		t.Error("should approve when user approves in auto fallback")
	}
}

func TestAuto_AllowListStillWorks(t *testing.T) {
	g := New(ModeAuto, Config{Allow: []string{"Bash(git:*)"}})

	d := g.Check(context.Background(), "bash", map[string]any{"command": "git log"})
	if d != nil {
		t.Error("allow list should work in auto mode too")
	}
}
