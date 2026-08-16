package tool_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/attachment"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/tool"
)

// TestE2EReadImagePersistsDescriptorNotBase64 is the end-to-end check the owner
// asked for: exercise the real `read` tool against a real store and assert on
// the filesystem that the session payload carries a reference, not base64, and
// that the blob is actually there.
func TestE2EReadImagePersistsDescriptorNotBase64(t *testing.T) {
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "captura.png")
	// 1x1 PNG, real bytes so magic-byte mime detection works.
	raw, err := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(imgPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := attachment.New(filepath.Join(dir, "attachments"))
	if err != nil {
		t.Fatal(err)
	}
	const sessionID = "0123456789abcdef01234567"
	scope, err := attachment.NewScope(store, sessionID)
	if err != nil {
		t.Fatal(err)
	}

	readTool := tool.NewRead(tool.ToolConfig{WorkspaceRoot: dir, DisableSandbox: true})

	// With capability: must produce a reference and no base64.
	ctx := attachment.WithScope(context.Background(), scope)
	res, err := readTool.Execute(ctx, map[string]any{"path": imgPath}, nil)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if len(res.Content) != 1 || res.Content[0].Type != "image" {
		t.Fatalf("expected one image block, got %+v", res.Content)
	}
	got := res.Content[0]
	if got.AttachmentID == "" {
		t.Fatal("no AttachmentID: the image was not externalized")
	}
	if got.Data != "" {
		t.Fatalf("Data is not empty (%d chars): base64 is still inline", len(got.Data))
	}

	// The persisted shape (what lands in the session JSON) must not carry bytes.
	encoded, err := json.Marshal(core.AgentMessage{
		Message: core.Message{Role: "user", Content: []core.Content{got}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), base64.StdEncoding.EncodeToString(raw)) {
		t.Fatal("session payload still contains the base64 of the image")
	}
	t.Logf("persisted payload: %d bytes, image was %d bytes raw", len(encoded), len(raw))

	// The blob must exist and round-trip back to the exact original bytes.
	msgs := []core.Message{{Role: "user", Content: []core.Content{got}}}
	out, err := store.MaterializeMessages(sessionID, msgs)
	if err != nil {
		t.Fatalf("materialize failed: %v", err)
	}
	back, err := base64.StdEncoding.DecodeString(out[0].Content[0].Data)
	if err != nil {
		t.Fatalf("materialized data is not valid base64: %v", err)
	}
	if string(back) != string(raw) {
		t.Fatal("materialized bytes differ from the original image")
	}

	// Without capability: identical call must stay inline (shared tool object).
	res2, err := readTool.Execute(context.Background(), map[string]any{"path": imgPath}, nil)
	if err != nil {
		t.Fatalf("read without scope failed: %v", err)
	}
	if res2.Content[0].AttachmentID != "" {
		t.Fatal("externalized without capability: the scope was captured, not resolved per call")
	}
	if res2.Content[0].Data == "" {
		t.Fatal("no inline data without capability: the image would be invisible")
	}
}
