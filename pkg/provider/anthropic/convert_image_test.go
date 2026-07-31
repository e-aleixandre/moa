package anthropic

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/jpeg"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func jpegB64(t *testing.T, w, h int) string {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h)), &jpeg.Options{Quality: 1}); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestConvertContentBlocks_Image(t *testing.T) {
	blocks := convertContentBlocks([]core.Content{
		core.ImageContent(jpegB64(t, 100, 200), "image/jpeg"),
	})
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	block := blocks[0].(map[string]any)
	if block["type"] != "image" {
		t.Fatalf("type: got %v, want image", block["type"])
	}
}

// History is replayed on every turn, so an image already recorded above the
// per-side limit would 400 the session forever. It must be swapped for a note.
func TestConvertContentBlocks_OversizedImageReplacedByNote(t *testing.T) {
	blocks := convertContentBlocks([]core.Content{
		core.TextContent("before"),
		core.ImageContent(jpegB64(t, 100, core.MaxImageDimension+1), "image/jpeg"),
	})
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	block := blocks[1].(map[string]any)
	if block["type"] != "text" {
		t.Fatalf("oversized image should become text, got %v", block["type"])
	}
	text, _ := block["text"].(string)
	if !strings.Contains(text, "8001") || !strings.Contains(text, "8000") {
		t.Fatalf("note should name the size and limit: %q", text)
	}
}

// An unreadable/unknown payload must be passed through untouched rather than
// silently dropped: only images we can prove are oversized get replaced.
func TestConvertContentBlocks_UndecodableImagePassedThrough(t *testing.T) {
	blocks := convertContentBlocks([]core.Content{
		core.ImageContent("ZGF0YQ==", "image/png"),
	})
	block := blocks[0].(map[string]any)
	if block["type"] != "image" {
		t.Fatalf("undecodable image must pass through, got %v", block["type"])
	}
}
