package tool

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/memory"
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
		"action":          "write",
		"name":            "uses-docker",
		"description":     "builds run in docker",
		"scope":           "project",
		"content":         "Always build with docker compose.",
		"invalidate_when": "when issue #84 tracking the Docker migration is closed",
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
	if !strings.Contains(resultText(r), "docker compose") || !strings.Contains(resultText(r), "Invalidate when: when issue #84 tracking the Docker migration is closed") {
		t.Errorf("read missing body: %q", resultText(r))
	}

	// List shows it with its description, never the expiry condition.
	r = runMem(t, tool, map[string]any{"action": "list"})
	if !strings.Contains(resultText(r), "project/uses-docker") || !strings.Contains(resultText(r), "builds run in docker") || strings.Contains(resultText(r), "Docker migration") {
		t.Errorf("list missing entry: %q", resultText(r))
	}
}

func TestMemory_WriteRoutesGlobal(t *testing.T) {
	cfg, store := newMemoryTool(t)
	tool := NewMemory(cfg)
	runMem(t, tool, map[string]any{
		"action": "write", "name": "prefers-tabs", "description": "tabs",
		"scope": "global", "content": "The user prefers tabs.", "durable": true,
	})
	// scope: global → global directory.
	if _, ok, _ := store.Read("global/prefers-tabs"); !ok {
		t.Error("global fact should be readable at global scope")
	}
}

func TestMemory_WriteInvalidScope(t *testing.T) {
	cfg, _ := newMemoryTool(t)
	tool := NewMemory(cfg)
	for _, scope := range []string{"", "bogus"} {
		r := runMem(t, tool, map[string]any{
			"action": "write", "name": "foo", "description": "d",
			"scope": scope, "content": "b", "durable": true,
		})
		if !r.IsError {
			t.Errorf("scope %q should be a hard error", scope)
		}
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
		"scope": "project", "content": "b", "durable": true,
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

func TestMemory_WriteRequiresLifecycle(t *testing.T) {
	cfg, _ := newMemoryTool(t)
	tool := NewMemory(cfg)
	r := runMem(t, tool, map[string]any{
		"action": "write", "name": "short-lived", "description": "d",
		"scope": "project", "content": "b",
	})
	if !r.IsError || !strings.Contains(resultText(r), "invalidate_when") {
		t.Errorf("write without lifecycle should guide the agent, got %q", resultText(r))
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

func TestMemory_Search(t *testing.T) {
	cfg, _ := newMemoryTool(t)
	tool := NewMemory(cfg)
	runMem(t, tool, map[string]any{
		"action": "write", "name": "docker-setup", "description": "how builds run",
		"scope": "project", "content": "Long body about docker compose.", "durable": true,
	})
	runMem(t, tool, map[string]any{
		"action": "write", "name": "prefers-tabs", "description": "indentation",
		"scope": "global", "content": "The user prefers tabs, not docker.", "durable": true,
	})

	r := runMem(t, tool, map[string]any{"action": "search", "query": "docker"})
	out := resultText(r)
	if r.IsError {
		t.Fatalf("search failed: %q", out)
	}
	if !strings.Contains(out, "project/docker-setup") || !strings.Contains(out, "global/prefers-tabs") {
		t.Errorf("search should span both scopes: %q", out)
	}
	if !strings.Contains(out, "2 matching memories") {
		t.Errorf("search should report the total: %q", out)
	}

	// Paging tells the agent how to get the rest.
	r = runMem(t, tool, map[string]any{"action": "search", "query": "docker", "limit": 1})
	if !strings.Contains(resultText(r), "offset=1") {
		t.Errorf("truncated page should explain how to continue: %q", resultText(r))
	}

	if r := runMem(t, tool, map[string]any{"action": "search"}); !r.IsError {
		t.Error("search without a query should error")
	}
	if r := runMem(t, tool, map[string]any{"action": "search", "query": "(", "regex": true}); !r.IsError {
		t.Error("an invalid regex should error")
	}
	if r := runMem(t, tool, map[string]any{"action": "search", "query": "nothing-here"}); strings.Contains(resultText(r), "docker") {
		t.Errorf("no-match search should say so: %q", resultText(r))
	}
}

func TestMemory_SearchOutputIsBounded(t *testing.T) {
	cfg, _ := newMemoryTool(t)
	tool := NewMemory(cfg)
	for i := 0; i < memory.MaxSearchLimit; i++ {
		runMem(t, tool, map[string]any{
			"action": "write", "name": fmt.Sprintf("fact-%02d", i), "description": "a needle in the hook",
			"scope": "project", "content": strings.Repeat("filler ", 500), "durable": true,
		})
	}
	r := runMem(t, tool, map[string]any{"action": "search", "query": "needle", "limit": 100})
	out := resultText(r)
	if len(out) > maxSearchResultBytes {
		t.Errorf("search result is %d bytes, over the %d cap", len(out), maxSearchResultBytes)
	}
	if !utf8.ValidString(out) {
		t.Error("search result must be valid UTF-8")
	}
}

func TestMemory_SearchOutputReservesTruncationMarker(t *testing.T) {
	cfg, _ := newMemoryTool(t)
	tool := NewMemory(cfg)
	longName := strings.Repeat("a", 220)
	for i := 0; i < memory.MaxSearchLimit; i++ {
		name := fmt.Sprintf("%s-%02d", longName, i)
		runMem(t, tool, map[string]any{
			"action": "write", "name": name, "description": "needle",
			"scope": "project", "content": "b", "durable": true,
		})
	}
	r := runMem(t, tool, map[string]any{"action": "search", "query": "needle", "limit": memory.MaxSearchLimit})
	out := resultText(r)
	if !strings.Contains(out, "[results truncated") {
		t.Fatalf("expected truncation marker, got %d-byte result", len(out))
	}
	if len(out) > maxSearchResultBytes {
		t.Errorf("truncated search result is %d bytes, over the %d cap", len(out), maxSearchResultBytes)
	}
}

func TestMemory_WriteReportsIndexBudget(t *testing.T) {
	cfg, _ := newMemoryTool(t)
	tool := NewMemory(cfg)
	r := runMem(t, tool, map[string]any{
		"action": "write", "name": "small", "description": "d",
		"scope": "project", "content": "b", "durable": true,
	})
	if !strings.Contains(resultText(r), "Index:") {
		t.Errorf("write should report index usage: %q", resultText(r))
	}

	// Fill the index past its budget: the write must say facts are invisible.
	for i := 0; i < 120; i++ {
		runMem(t, tool, map[string]any{
			"action": "write", "name": fmt.Sprintf("filler-%03d", i),
			"description": strings.Repeat("d", 150),
			"scope":       "project", "content": "b", "durable": true,
		})
	}
	r = runMem(t, tool, map[string]any{
		"action": "write", "name": "last", "description": "d",
		"scope": "project", "content": "b", "durable": true,
	})
	out := resultText(r)
	if !strings.Contains(out, "do not fit") || !strings.Contains(out, "Consolidate") {
		t.Errorf("an overflowing index should be reported on write: %q", out)
	}
}

func TestMemory_WriteWarnsOnLongDescription(t *testing.T) {
	cfg, _ := newMemoryTool(t)
	tool := NewMemory(cfg)
	r := runMem(t, tool, map[string]any{
		"action": "write", "name": "wordy", "description": strings.Repeat("w", 150),
		"scope": "project", "content": "b", "durable": true,
	})
	if r.IsError {
		t.Fatalf("a long-but-legal description must be saved: %q", resultText(r))
	}
	if !strings.Contains(resultText(r), "Note:") {
		t.Errorf("expected a soft warning: %q", resultText(r))
	}
	r = runMem(t, tool, map[string]any{
		"action": "write", "name": "toolong", "description": strings.Repeat("w", 200),
		"scope": "project", "content": "b", "durable": true,
	})
	if !r.IsError {
		t.Error("a description over the byte limit should be rejected")
	}
}
