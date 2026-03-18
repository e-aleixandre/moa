package tool

import (
	"context"
	"strings"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/memory"
)

func newMemoryTool(t *testing.T) (ToolConfig, *memory.Store) {
	t.Helper()
	store := memory.New(t.TempDir())
	cfg := ToolConfig{
		WorkspaceRoot: "/test/project",
		MemoryStore:   store,
	}
	return cfg, store
}

// resultText extracts the text from the first content block of a Result.
func resultText(r core.Result) string {
	if len(r.Content) == 0 {
		return ""
	}
	return r.Content[0].Text
}

func TestMemory_ReadEmpty(t *testing.T) {
	cfg, _ := newMemoryTool(t)
	tool := NewMemory(cfg)

	result, err := tool.Execute(context.Background(), map[string]any{"action": "read"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resultText(result), "No memory saved") {
		t.Errorf("expected no-memory message, got %q", resultText(result))
	}
}

func TestMemory_ReadWithContent(t *testing.T) {
	cfg, store := newMemoryTool(t)
	if err := store.Save("/test/project", "# Memory\n- Use Docker"); err != nil {
		t.Fatal(err)
	}

	tool := NewMemory(cfg)
	result, err := tool.Execute(context.Background(), map[string]any{"action": "read"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resultText(result), "Use Docker") {
		t.Errorf("expected memory content, got %q", resultText(result))
	}
}

func TestMemory_Update(t *testing.T) {
	cfg, store := newMemoryTool(t)
	tool := NewMemory(cfg)

	content := "# Memory\n- Prefer tabs over spaces\n- Use Go 1.22\n"
	result, err := tool.Execute(context.Background(), map[string]any{
		"action":  "update",
		"content": content,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(result)
	if !strings.Contains(text, "Memory updated") {
		t.Errorf("expected confirmation, got %q", text)
	}

	// Verify persisted.
	got, err := store.Load("/test/project")
	if err != nil {
		t.Fatal(err)
	}
	if got != content {
		t.Errorf("got %q, want %q", got, content)
	}
}

func TestMemory_UpdateNoContent(t *testing.T) {
	cfg, _ := newMemoryTool(t)
	tool := NewMemory(cfg)

	result, err := tool.Execute(context.Background(), map[string]any{
		"action": "update",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error for update without content")
	}
}

func TestMemory_UpdateTooLarge(t *testing.T) {
	cfg, _ := newMemoryTool(t)
	tool := NewMemory(cfg)

	big := strings.Repeat("x", memory.MaxSize+1)
	result, err := tool.Execute(context.Background(), map[string]any{
		"action":  "update",
		"content": big,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error for oversized content")
	}
}

func TestMemory_InvalidAction(t *testing.T) {
	cfg, _ := newMemoryTool(t)
	tool := NewMemory(cfg)

	result, err := tool.Execute(context.Background(), map[string]any{
		"action": "delete",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error for invalid action")
	}
}

func TestMemory_LockKey(t *testing.T) {
	cfg, store := newMemoryTool(t)
	tool := NewMemory(cfg)

	key := tool.LockKey(nil)
	expected := store.FilePath("/test/project")
	if key != expected {
		t.Errorf("LockKey = %q, want %q", key, expected)
	}
}
