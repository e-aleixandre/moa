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
	store := memory.New(t.TempDir(), "/test/project")
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

func runMem(t *testing.T, tool core.Tool, params map[string]any) core.Result {
	t.Helper()
	r, err := tool.Execute(context.Background(), params, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestMemory_ListEmpty(t *testing.T) {
	cfg, _ := newMemoryTool(t)
	tool := NewMemory(cfg)
	r := runMem(t, tool, map[string]any{"action": "list"})
	if !strings.Contains(resultText(r), "No memories") {
		t.Errorf("expected empty message, got %q", resultText(r))
	}
}

func TestMemory_WriteReadList(t *testing.T) {
	cfg, _ := newMemoryTool(t)
	tool := NewMemory(cfg)

	r := runMem(t, tool, map[string]any{
		"action":      "write",
		"name":        "uses-docker",
		"description": "builds run in docker",
		"type":        "project",
		"content":     "Always build with docker compose.",
	})
	if r.IsError {
		t.Fatalf("write failed: %q", resultText(r))
	}
	// Confirmation should surface the canonical id for later reads.
	if !strings.Contains(resultText(r), "project/uses-docker") {
		t.Errorf("write confirmation missing id: %q", resultText(r))
	}

	// Read it back by canonical id.
	r = runMem(t, tool, map[string]any{"action": "read", "id": "project/uses-docker"})
	if !strings.Contains(resultText(r), "docker compose") {
		t.Errorf("read missing body: %q", resultText(r))
	}

	// List shows it with type and description.
	r = runMem(t, tool, map[string]any{"action": "list"})
	if !strings.Contains(resultText(r), "project/uses-docker") || !strings.Contains(resultText(r), "(project)") {
		t.Errorf("list missing entry: %q", resultText(r))
	}
}

func TestMemory_WriteRoutesGlobal(t *testing.T) {
	cfg, store := newMemoryTool(t)
	tool := NewMemory(cfg)
	runMem(t, tool, map[string]any{
		"action": "write", "name": "prefers-tabs", "description": "tabs",
		"type": "user", "content": "The user prefers tabs.",
	})
	// user type → global scope.
	if _, ok, _ := store.Read("global/prefers-tabs"); !ok {
		t.Error("user fact should be readable at global scope")
	}
}

func TestMemory_WriteInvalidType(t *testing.T) {
	cfg, _ := newMemoryTool(t)
	tool := NewMemory(cfg)
	r := runMem(t, tool, map[string]any{
		"action": "write", "name": "foo", "description": "d",
		"type": "bogus", "content": "b",
	})
	if !r.IsError {
		t.Error("invalid type should be a hard error")
	}
}

func TestMemory_ReadMissing(t *testing.T) {
	cfg, _ := newMemoryTool(t)
	tool := NewMemory(cfg)
	r := runMem(t, tool, map[string]any{"action": "read", "id": "project/nope"})
	if !r.IsError {
		t.Error("reading a missing fact should error")
	}
}

func TestMemory_Delete(t *testing.T) {
	cfg, _ := newMemoryTool(t)
	tool := NewMemory(cfg)
	runMem(t, tool, map[string]any{
		"action": "write", "name": "temp", "description": "d",
		"type": "project", "content": "b",
	})
	r := runMem(t, tool, map[string]any{"action": "delete", "id": "project/temp"})
	if r.IsError {
		t.Fatalf("delete failed: %q", resultText(r))
	}
	r = runMem(t, tool, map[string]any{"action": "read", "id": "project/temp"})
	if !r.IsError {
		t.Error("fact should be gone after delete")
	}
}

func TestMemory_InvalidAction(t *testing.T) {
	cfg, _ := newMemoryTool(t)
	tool := NewMemory(cfg)
	r := runMem(t, tool, map[string]any{"action": "frobnicate"})
	if !r.IsError {
		t.Error("expected error for invalid action")
	}
}

func TestMemory_LockKeyStable(t *testing.T) {
	cfg, _ := newMemoryTool(t)
	tool := NewMemory(cfg)
	if tool.LockKey(nil) != tool.LockKey(map[string]any{"action": "write"}) {
		t.Error("lock key should be stable across calls")
	}
	if tool.LockKey(nil) == "" {
		t.Error("lock key should be non-empty")
	}
}
