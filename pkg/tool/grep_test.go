package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrepSupportsRegexAlternation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "events.txt"), []byte("async job\nbackground job\nforeground job\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := NewGrep(ToolConfig{WorkspaceRoot: root}).Execute(context.Background(), map[string]any{
		"pattern": "async|background",
		"include": "*.txt",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "async job") || !strings.Contains(text, "background job") {
		t.Fatalf("grep did not honor regex alternation: %q", text)
	}
	if strings.Contains(text, "foreground job") {
		t.Fatalf("grep matched a non-alternative line: %q", text)
	}
}
