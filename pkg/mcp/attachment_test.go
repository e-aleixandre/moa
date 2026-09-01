package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/e-aleixandre/moa/pkg/attachment"
	"github.com/e-aleixandre/moa/pkg/core"
)

const mcpTestSession = "0123456789abcdef01234567"

func testJPEG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewGray(image.Rect(0, 0, 8, 8)), &jpeg.Options{Quality: 1}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestMCPImageServerHelper is a helper MCP server whose only tool returns an
// image, so the wrapper's image path can be exercised end to end.
func TestMCPImageServerHelper(t *testing.T) {
	if os.Getenv("GO_WANT_MCP_IMAGE_HELPER") != "1" {
		return
	}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "image-helper", Version: "0.1"}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "shot",
		Description: "Returns a picture",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, input any) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.ImageContent{Data: testJPEG(t), MIMEType: "image/jpeg"}},
		}, nil, nil
	})
	if err := server.Run(context.Background(), &sdkmcp.StdioTransport{}); err != nil {
		t.Fatal(err)
	}
}

func imageHelperConfig() core.MCPServer {
	return core.MCPServer{
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestMCPImageServerHelper$", "--"},
		Env:     map[string]string{"GO_WANT_MCP_IMAGE_HELPER": "1"},
	}
}

func newTestScope(t *testing.T, baseDir string) *attachment.Scope {
	t.Helper()
	store, err := attachment.New(baseDir)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := attachment.NewScope(store, mcpTestSession)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func mcpIndexIDs(t *testing.T, baseDir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(baseDir, "sessions", mcpTestSession+".json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var index map[string]json.RawMessage
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(index))
	for id := range index {
		ids = append(ids, id)
	}
	return ids
}

func imageTool(t *testing.T, mgr *Manager) core.Tool {
	t.Helper()
	for _, tl := range mgr.Tools() {
		if tl.Label == "pics/shot" {
			return tl
		}
	}
	t.Fatal("image tool missing")
	return core.Tool{}
}

// MCP test (a): ONE wrapper value, two invocations. With the capability in the
// context it externalizes; without it, inline. Nothing may be captured when the
// manager or the wrapper is built — MCP tools are shared across agents.
func TestMCPWrapperObeysTheInvocationContext(t *testing.T) {
	storeDir := t.TempDir()
	scope := newTestScope(t, storeDir)

	mgr := NewManager(nil, "")
	startWait(t, mgr, map[string]core.MCPServer{"pics": imageHelperConfig()}, nil)
	defer mgr.Close()

	shared := imageTool(t, mgr)

	withCap, err := shared.Execute(attachment.WithScope(context.Background(), scope), map[string]any{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := withCap.Content[0]; got.AttachmentID == "" {
		t.Fatalf("with capability the MCP image was not externalized: %+v", got)
	} else if got.Data != "" {
		t.Error("externalized MCP image still carries inline base64")
	}

	withoutCap, err := shared.Execute(context.Background(), map[string]any{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := withoutCap.Content[0]; got.AttachmentID != "" {
		t.Errorf("the SAME wrapper externalized without a capability in ctx (%q): it captured one", got.AttachmentID)
	} else if got.Data == "" {
		t.Error("without capability the MCP image should be inline base64")
	}

	if ids := mcpIndexIDs(t, storeDir); len(ids) != 1 {
		t.Errorf("session index = %v, want exactly the one reference from the capable invocation", ids)
	}
}

// MCP test (b): after a reload (session_lifecycle.go builds a brand new
// Manager and re-registers its tools), the NEW wrapper must still resolve the
// capability from the invocation context. Nothing is injected into a manager,
// so a reload cannot silently drop or freeze the behavior.
func TestMCPWrapperStillObeysContextAfterReload(t *testing.T) {
	storeDir := t.TempDir()
	scope := newTestScope(t, storeDir)

	oldMgr := NewManager(nil, "")
	startWait(t, oldMgr, map[string]core.MCPServer{"pics": imageHelperConfig()}, nil)
	_ = imageTool(t, oldMgr)
	oldMgr.Close()

	// Exactly what reloadMCP does: a fresh manager, fresh wrappers.
	newMgr := NewManager(nil, "")
	startWait(t, newMgr, map[string]core.MCPServer{"pics": imageHelperConfig()}, nil)
	defer newMgr.Close()

	reloaded := imageTool(t, newMgr)

	withCap, err := reloaded.Execute(attachment.WithScope(context.Background(), scope), map[string]any{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if withCap.Content[0].AttachmentID == "" {
		t.Errorf("the reloaded wrapper ignored the capability in ctx: %+v", withCap.Content[0])
	}

	withoutCap, err := reloaded.Execute(context.Background(), map[string]any{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := withoutCap.Content[0]; got.AttachmentID != "" {
		t.Errorf("the reloaded wrapper externalized without a capability (%q)", got.AttachmentID)
	} else if got.Data == "" {
		t.Error("the reloaded wrapper produced neither a descriptor nor inline bytes")
	}
}

// A storage failure in the MCP path degrades to inline, never to an error and
// never to a descriptor with no blob.
func TestMCPImageFallsBackToInlineWhenStoringFails(t *testing.T) {
	storeDir := t.TempDir()
	scope := newTestScope(t, storeDir)
	stagingDir := filepath.Join(storeDir, "staging")
	if err := os.Chmod(stagingDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stagingDir, 0o700) })

	raw := testJPEG(t)
	got := mcpImageContent(scope, &sdkmcp.ImageContent{Data: raw, MIMEType: "image/jpeg"})
	if got.AttachmentID != "" {
		t.Errorf("a failed Put produced a descriptor (%q) with no blob behind it", got.AttachmentID)
	}
	if got.Data != base64.StdEncoding.EncodeToString(raw) {
		t.Error("a failed Put lost the image instead of falling back to inline bytes")
	}
	if ids := mcpIndexIDs(t, storeDir); len(ids) != 0 {
		t.Errorf("a failed Put left references behind: %v", ids)
	}
	entries, err := os.ReadDir(stagingDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a failed Put left %d staging file(s) behind", len(entries))
	}
}

func TestMCPImageExceedingBase64PayloadLimitIsNotReturned(t *testing.T) {
	image := &sdkmcp.ImageContent{Data: make([]byte, 9_902_807), MIMEType: "image/png"}
	got := mcpImageContent(nil, image)
	if got.Type != "text" {
		t.Fatalf("expected an explanatory text block, got %+v", got)
	}
	if want := "MCP image too large after base64 encoding (12 MB, max 10 MB); return a smaller image."; got.Text != want {
		t.Fatalf("text: got %q, want %q", got.Text, want)
	}
}
