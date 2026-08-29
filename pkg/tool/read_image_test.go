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

// Anthropic limits the base64 payload, not the file on disk. A 9.9 MB image
// expands past its 10 MB limit and must not be allowed into history.
func TestRead_ImageExceedingBase64PayloadLimit(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "large.png")
	if err := os.WriteFile(path, make([]byte, 9_902_807), 0o644); err != nil {
		t.Fatal(err)
	}

	read := NewRead(ToolConfig{WorkspaceRoot: tmp})
	result, err := read.Execute(context.Background(), map[string]any{"path": "large.png"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected an error result for a 9,902,807-byte image, got an image block")
	}
	if got, want := result.Content[0].Text, "Error: image too large after base64 encoding (12 MB, max 10 MB)"; got != want {
		t.Fatalf("error: got %q, want %q", got, want)
	}
}

func TestRead_ImageWithinBase64PayloadLimit(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "normal.jpg")
	writeJPEG(t, path, 100, 200)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(make([]byte, 2*1024*1024)); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	read := NewRead(ToolConfig{WorkspaceRoot: tmp})
	result, err := read.Execute(context.Background(), map[string]any{"path": "normal.jpg"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Content) != 1 || result.Content[0].Type != "image" {
		t.Fatalf("expected an image block, got %+v", result)
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

// The image type is derived from the extension, which lies often enough (a GIF
// saved as .png). Anthropic 400s on the mismatch and history is replayed every
// turn, so the recorded type must come from the bytes.
func TestRead_ImageMimeFollowsBytesNotExtension(t *testing.T) {
	tmp := t.TempDir()
	gif := append([]byte("GIF89a"), make([]byte, 128)...)
	if err := os.WriteFile(filepath.Join(tmp, "actually.png"), gif, 0o644); err != nil {
		t.Fatal(err)
	}

	read := NewRead(ToolConfig{WorkspaceRoot: tmp})
	result, err := read.Execute(context.Background(), map[string]any{"path": "actually.png"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %+v", result.Content)
	}
	if got := result.Content[0].MimeType; got != "image/gif" {
		t.Fatalf("mime: got %q, want image/gif", got)
	}
}
