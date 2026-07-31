package tool

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func writeJPEG(t *testing.T, path string, w, h int) {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h)), &jpeg.Options{Quality: 1}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRead_Image(t *testing.T) {
	tmp := t.TempDir()
	writeJPEG(t, filepath.Join(tmp, "small.jpg"), 100, 200)

	read := NewRead(ToolConfig{WorkspaceRoot: tmp})
	result, err := read.Execute(context.Background(), map[string]any{"path": "small.jpg"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || result.Content[0].Type != "image" {
		t.Fatalf("expected an image block, got %+v", result.Content[0])
	}
}

// A tall screenshot beyond the provider's per-side limit must be refused at
// read time: once such a block enters history, every later request 400s.
func TestRead_ImageExceedingMaxDimension(t *testing.T) {
	tmp := t.TempDir()
	writeJPEG(t, filepath.Join(tmp, "tall.jpg"), 100, core.MaxImageDimension+1)

	read := NewRead(ToolConfig{WorkspaceRoot: tmp})
	result, err := read.Execute(context.Background(), map[string]any{"path": "tall.jpg"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected an error result for an oversized image")
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "8000") || !strings.Contains(text, "8001") {
		t.Fatalf("error should name the size and the limit: %q", text)
	}
	for _, c := range result.Content {
		if c.Type == "image" {
			t.Fatal("oversized image must not be returned as an image block")
		}
	}
}
