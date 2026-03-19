package files

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func setupTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Create a directory tree:
	// dir/
	//   main.go
	//   README.md
	//   pkg/
	//     server.go
	//     handler.go
	//   cmd/
	//     app.go
	//   .git/          (should be skipped)
	//     config
	//   node_modules/  (should be skipped)
	//     foo.js

	for _, d := range []string{"pkg", "cmd", ".git", "node_modules"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{
		"main.go", "README.md",
		"pkg/server.go", "pkg/handler.go",
		"cmd/app.go",
		".git/config",
		"node_modules/foo.js",
	} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	return dir
}

func TestScanner_Scan(t *testing.T) {
	dir := setupTestDir(t)
	s := NewScanner()

	entries := s.Scan(dir)

	// Should have: cmd/, pkg/ (dirs) + cmd/app.go, main.go, README.md, pkg/handler.go, pkg/server.go (files)
	// .git/ and node_modules/ are skipped.
	paths := make(map[string]bool)
	for _, e := range entries {
		paths[e.Path] = true
	}

	// Directories should be present
	if !paths["cmd"] {
		t.Error("expected cmd/ directory")
	}
	if !paths["pkg"] {
		t.Error("expected pkg/ directory")
	}

	// Files should be present
	for _, f := range []string{"main.go", "README.md", "cmd/app.go", "pkg/server.go", "pkg/handler.go"} {
		if !paths[f] {
			t.Errorf("expected file %s", f)
		}
	}

	// Skipped dirs
	if paths[".git"] || paths[".git/config"] {
		t.Error(".git should be skipped")
	}
	if paths["node_modules"] || paths["node_modules/foo.js"] {
		t.Error("node_modules should be skipped")
	}
}

func TestScanner_Cache(t *testing.T) {
	dir := setupTestDir(t)
	s := NewScanner()

	entries1 := s.Scan(dir)
	// Add a new file
	if err := os.WriteFile(filepath.Join(dir, "new.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries2 := s.Scan(dir)

	// Should be the same (cached)
	if len(entries1) != len(entries2) {
		t.Error("second scan should use cache and have same count")
	}

	// Invalidate and rescan
	s.Invalidate(dir)
	entries3 := s.Scan(dir)
	if len(entries3) != len(entries1)+1 {
		t.Errorf("after invalidate, expected %d entries, got %d", len(entries1)+1, len(entries3))
	}
}

func TestScanner_CacheTTL(t *testing.T) {
	dir := setupTestDir(t)
	s := &Scanner{
		cache:  make(map[string]cachedScan),
		maxAge: 1 * time.Millisecond, // very short TTL
	}

	entries1 := s.Scan(dir)
	// Add a new file
	if err := os.WriteFile(filepath.Join(dir, "ttl.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	time.Sleep(5 * time.Millisecond)
	entries2 := s.Scan(dir)

	if len(entries2) != len(entries1)+1 {
		t.Errorf("after TTL expiry expected %d entries, got %d", len(entries1)+1, len(entries2))
	}
}

func TestScanner_ConcurrentAccess(t *testing.T) {
	dir := setupTestDir(t)
	s := NewScanner()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entries := s.Scan(dir)
			if len(entries) == 0 {
				t.Error("expected non-empty entries")
			}
		}()
	}
	wg.Wait()
}

func TestScanner_SkipDirs(t *testing.T) {
	dir := setupTestDir(t)
	s := NewScanner()
	entries := s.Scan(dir)

	for _, e := range entries {
		base := filepath.Base(e.Path)
		if SkipDirs[base] {
			t.Errorf("expected %s to be skipped", e.Path)
		}
		if base == "config" && filepath.Dir(e.Path) == ".git" {
			t.Errorf("expected .git/config to be skipped")
		}
	}
}

func TestScanner_EmptyWorkDir(t *testing.T) {
	s := NewScanner()
	entries := s.Scan("")
	if entries != nil {
		t.Error("expected nil for empty workDir")
	}
}

func TestFilter_Ranking(t *testing.T) {
	entries := []Entry{
		{Path: "cmd/app.go"},
		{Path: "pkg/handler.go"},
		{Path: "main.go"},
		{Path: "pkg/main_test.go"},
		{Path: "internal/main_helper.go"},
	}

	results := Filter(entries, "main", 50)
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	// "main.go" should be exact match (first)
	if results[0].Path != "main.go" {
		t.Errorf("expected main.go first, got %s", results[0].Path)
	}
}

func TestFilter_Prefix(t *testing.T) {
	entries := []Entry{
		{Path: "cmd/app.go"},
		{Path: "pkg/server.go"},
		{Path: "pkg/server_test.go"},
		{Path: "main.go"},
	}

	results := Filter(entries, "pkg/", 50)
	if len(results) != 2 {
		t.Errorf("expected 2 results for prefix 'pkg/', got %d", len(results))
	}
}

func TestFilter_Contains(t *testing.T) {
	entries := []Entry{
		{Path: "cmd/app.go"},
		{Path: "pkg/server_handler.go"},
	}

	results := Filter(entries, "handler", 50)
	if len(results) != 1 || results[0].Path != "pkg/server_handler.go" {
		t.Errorf("expected pkg/server_handler.go, got %v", results)
	}
}

func TestFilter_Limit(t *testing.T) {
	entries := make([]Entry, 100)
	for i := range entries {
		entries[i] = Entry{Path: "file" + string(rune('a'+i%26)) + ".go"}
	}

	results := Filter(entries, "", 10)
	if len(results) != 10 {
		t.Errorf("expected 10 results, got %d", len(results))
	}
}

func TestFilter_Empty(t *testing.T) {
	entries := []Entry{{Path: "main.go"}, {Path: "cmd/app.go"}}
	results := Filter(entries, "", 50)
	if len(results) != 2 {
		t.Errorf("expected 2 results for empty filter, got %d", len(results))
	}
}

func TestFilter_CaseInsensitive(t *testing.T) {
	entries := []Entry{{Path: "README.md"}, {Path: "main.go"}}
	results := Filter(entries, "readme", 50)
	if len(results) != 1 || results[0].Path != "README.md" {
		t.Errorf("expected README.md, got %v", results)
	}
}

func TestScan_SortOrder(t *testing.T) {
	dir := setupTestDir(t)
	s := NewScanner()
	entries := s.Scan(dir)

	// Directories should come before files.
	sawFile := false
	for _, e := range entries {
		if !e.IsDir {
			sawFile = true
		}
		if e.IsDir && sawFile {
			t.Errorf("directory %s appeared after files", e.Path)
		}
	}
}
