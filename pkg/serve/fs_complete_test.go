package serve

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func doFSComplete(t *testing.T, path string) fsCompleteResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/fs/complete?path="+url.QueryEscape(path), nil)
	rec := httptest.NewRecorder()
	handleFSComplete()(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("path %q: status %d", path, rec.Code)
	}
	var resp fsCompleteResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("path %q: decode: %v", path, err)
	}
	return resp
}

func TestHandleFSComplete_ListsSubdirs(t *testing.T) {
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, "alpha"))
	mustMkdir(t, filepath.Join(dir, "beta"))
	_ = os.WriteFile(filepath.Join(dir, "notadir.txt"), []byte("x"), 0o644)

	resp := doFSComplete(t, dir+"/")
	if !resp.Exists || !resp.IsDir {
		t.Fatalf("expected exists+isDir true, got %+v", resp)
	}
	if !containsAll(resp.Entries, "alpha", "beta") {
		t.Fatalf("expected alpha+beta in entries, got %v", resp.Entries)
	}
	for _, e := range resp.Entries {
		if e == "notadir.txt" {
			t.Fatalf("entries should not include files: %v", resp.Entries)
		}
	}
}

func TestHandleFSComplete_PrefixFilterCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, "Alpha"))
	mustMkdir(t, filepath.Join(dir, "AlphaBeta"))
	mustMkdir(t, filepath.Join(dir, "beta"))

	resp := doFSComplete(t, filepath.Join(dir, "al"))
	if !containsAll(resp.Entries, "Alpha", "AlphaBeta") {
		t.Fatalf("expected Alpha+AlphaBeta, got %v", resp.Entries)
	}
	if containsAny(resp.Entries, "beta") {
		t.Fatalf("did not expect beta in entries, got %v", resp.Entries)
	}
}

func TestHandleFSComplete_NonexistentPath(t *testing.T) {
	dir := t.TempDir()
	resp := doFSComplete(t, filepath.Join(dir, "does-not-exist-xyz"))
	if resp.Exists || resp.IsDir {
		t.Fatalf("expected exists=false isDir=false, got %+v", resp)
	}
	if len(resp.Entries) != 0 {
		t.Fatalf("expected no entries, got %v", resp.Entries)
	}
}

func TestHandleFSComplete_FileNotDir(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "afile.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp := doFSComplete(t, file)
	if !resp.Exists {
		t.Fatalf("expected exists=true, got %+v", resp)
	}
	if resp.IsDir {
		t.Fatalf("expected isDir=false, got %+v", resp)
	}
}

func TestHandleFSComplete_TildeExpansion(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory available")
	}
	resp := doFSComplete(t, "~")
	if !resp.Exists || !resp.IsDir {
		t.Fatalf("expected exists+isDir true for ~, got %+v", resp)
	}
	if resp.Path != home {
		t.Fatalf("expected canonical path %q, got %q", home, resp.Path)
	}
}

func TestHandleFSComplete_HidesDotDirsUnlessQueried(t *testing.T) {
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, ".hidden"))
	mustMkdir(t, filepath.Join(dir, "visible"))

	resp := doFSComplete(t, dir+"/")
	if containsAny(resp.Entries, ".hidden") {
		t.Fatalf("did not expect .hidden in entries, got %v", resp.Entries)
	}
	if !containsAll(resp.Entries, "visible") {
		t.Fatalf("expected visible in entries, got %v", resp.Entries)
	}

	resp = doFSComplete(t, dir+"/.hi")
	if !containsAll(resp.Entries, ".hidden") {
		t.Fatalf("expected .hidden when base starts with '.', got %v", resp.Entries)
	}
}

func TestHandleFSComplete_CapsAt50(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 60; i++ {
		mustMkdir(t, filepath.Join(dir, fmt.Sprintf("d%02d", i)))
	}

	resp := doFSComplete(t, dir+"/")
	if len(resp.Entries) != 50 {
		t.Fatalf("expected 50 entries, got %d", len(resp.Entries))
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func containsAll(haystack []string, want ...string) bool {
	for _, w := range want {
		if !containsAny(haystack, w) {
			return false
		}
	}
	return true
}

func containsAny(haystack []string, want string) bool {
	for _, h := range haystack {
		if h == want {
			return true
		}
	}
	return false
}
