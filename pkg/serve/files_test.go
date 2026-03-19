package serve

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/ealeixandre/moa/pkg/files"
)

func TestHandleListFiles_OK(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "main.go"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "readme.md"), []byte("x"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "pkg"), 0o755)

	srv, _, cancel := newTestServerWithRoot(t, dir)
	defer cancel()

	// Create a session with CWD=dir
	resp := apiReq(t, srv, "POST", "/api/sessions", `{"title":"test","cwd":"`+dir+`"}`)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("create session: status %d", resp.StatusCode)
	}
	var created map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	resp.Body.Close() //nolint:errcheck
	sessionID := created["id"].(string)

	// List files
	resp = apiReq(t, srv, "GET", "/api/sessions/"+sessionID+"/files", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list files: status %d", resp.StatusCode)
	}
	var entries []files.Entry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		t.Fatalf("decode entries: %v", err)
	}
	resp.Body.Close() //nolint:errcheck

	if len(entries) == 0 {
		t.Fatal("expected at least one entry")
	}

	// Verify the entries contain our files
	paths := make(map[string]bool)
	for _, e := range entries {
		paths[e.Path] = true
	}
	if !paths["main.go"] {
		t.Error("expected main.go")
	}
	if !paths["readme.md"] {
		t.Error("expected readme.md")
	}
	if !paths["pkg"] {
		t.Error("expected pkg directory")
	}
}

func TestHandleListFiles_Query(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "main.go"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "readme.md"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "other.txt"), []byte("x"), 0o644)

	srv, _, cancel := newTestServerWithRoot(t, dir)
	defer cancel()

	resp := apiReq(t, srv, "POST", "/api/sessions", `{"title":"test","cwd":"`+dir+`"}`)
	var created map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	resp.Body.Close() //nolint:errcheck
	sessionID := created["id"].(string)

	resp = apiReq(t, srv, "GET", "/api/sessions/"+sessionID+"/files?q=main", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var entries []files.Entry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		t.Fatalf("decode entries: %v", err)
	}
	resp.Body.Close() //nolint:errcheck

	if len(entries) != 1 {
		t.Fatalf("expected 1 result for 'main', got %d", len(entries))
	}
	if entries[0].Path != "main.go" {
		t.Errorf("expected main.go, got %s", entries[0].Path)
	}
}

func TestHandleListFiles_NotFound(t *testing.T) {
	srv, _, cancel := newTestServer(t)
	defer cancel()

	resp := apiReq(t, srv, "GET", "/api/sessions/nonexistent/files", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	resp.Body.Close() //nolint:errcheck
}

func TestHandleListFiles_LimitBounds(t *testing.T) {
	dir := t.TempDir()
	// Create 20 files
	for i := 0; i < 20; i++ {
		name := filepath.Join(dir, "file"+string(rune('a'+i))+".go")
		_ = os.WriteFile(name, []byte("x"), 0o644)
	}

	srv, _, cancel := newTestServerWithRoot(t, dir)
	defer cancel()

	resp := apiReq(t, srv, "POST", "/api/sessions", `{"title":"test","cwd":"`+dir+`"}`)
	var created map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	resp.Body.Close() //nolint:errcheck
	sessionID := created["id"].(string)

	// Limit to 5
	resp = apiReq(t, srv, "GET", "/api/sessions/"+sessionID+"/files?limit=5", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var entries []files.Entry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		t.Fatalf("decode entries: %v", err)
	}
	resp.Body.Close() //nolint:errcheck

	if len(entries) > 5 {
		t.Errorf("expected at most 5 entries, got %d", len(entries))
	}

	// Invalid limit should use default
	resp = apiReq(t, srv, "GET", "/api/sessions/"+sessionID+"/files?limit=-1", "")
	var entries2 []files.Entry
	if err := json.NewDecoder(resp.Body).Decode(&entries2); err != nil {
		t.Fatalf("decode entries2: %v", err)
	}
	resp.Body.Close() //nolint:errcheck
	if len(entries2) == 0 {
		t.Error("expected entries with invalid limit (should use default)")
	}
}
