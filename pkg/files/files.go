// Package files provides reusable file scanning and filtering logic.
// It is used by both the TUI file picker and the HTTP /api/sessions/{id}/files endpoint.
package files

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Entry represents a file or directory relative to a workspace root.
type Entry struct {
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
}

// MaxScanEntries is the cap on total entries returned by a single scan.
const MaxScanEntries = 5000

// SkipDirs are directory names that are always skipped during scanning.
var SkipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	".next":        true,
	"dist":         true,
	"build":        true,
	"__pycache__":  true,
	".venv":        true,
	".cache":       true,
	".idea":        true,
	".vscode":      true,
}

// Scanner caches file entries per workDir. Thread-safe.
// Callers create their own instance; there is no package-level global.
type Scanner struct {
	mu     sync.Mutex
	cache  map[string]cachedScan
	maxAge time.Duration
}

type cachedScan struct {
	entries []Entry
	scanned time.Time
}

// NewScanner returns a Scanner with a 30-second TTL.
func NewScanner() *Scanner {
	return &Scanner{
		cache:  make(map[string]cachedScan),
		maxAge: 30 * time.Second,
	}
}

// Scan returns file entries for workDir, using a cached result if fresh.
func (s *Scanner) Scan(workDir string) []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()

	if c, ok := s.cache[workDir]; ok && time.Since(c.scanned) < s.maxAge {
		return c.entries
	}

	entries := scan(workDir)
	s.cache[workDir] = cachedScan{entries: entries, scanned: time.Now()}
	return entries
}

// Invalidate forces a re-scan for the given workDir on the next call to Scan.
func (s *Scanner) Invalidate(workDir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cache, workDir)
}

// scan walks workDir and returns sorted entries (dirs first, then alphabetical).
func scan(workDir string) []Entry {
	if workDir == "" {
		return nil
	}

	var entries []Entry

	_ = filepath.WalkDir(workDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()

		// Skip hidden dirs and known junk dirs (except root).
		if d.IsDir() && path != workDir {
			if SkipDirs[name] || (strings.HasPrefix(name, ".") && name != ".") {
				return filepath.SkipDir
			}
		}

		// Skip root itself.
		if path == workDir {
			return nil
		}

		rel, relErr := filepath.Rel(workDir, path)
		if relErr != nil {
			return nil
		}

		entries = append(entries, Entry{
			Path:  rel,
			IsDir: d.IsDir(),
		})

		if len(entries) >= MaxScanEntries {
			return filepath.SkipAll
		}
		return nil
	})

	// Sort: directories first, then alphabetical.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return entries[i].Path < entries[j].Path
	})

	return entries
}

// Filter returns entries matching query, ranked: exact > prefix > contains.
// Both the full path and the basename are checked. Results are capped to limit.
// All entries are scanned to guarantee correct ranking; truncation is applied
// only to the final composed result.
func Filter(entries []Entry, query string, limit int) []Entry {
	if limit <= 0 {
		limit = 50
	}
	if query == "" {
		if len(entries) > limit {
			return entries[:limit]
		}
		return entries
	}

	lower := strings.ToLower(query)
	var exact, prefix, contains []Entry

	for _, entry := range entries {
		lp := strings.ToLower(entry.Path)
		base := strings.ToLower(filepath.Base(entry.Path))

		if lp == lower {
			exact = append(exact, entry)
		} else if strings.HasPrefix(lp, lower) || strings.HasPrefix(base, lower) {
			prefix = append(prefix, entry)
		} else if strings.Contains(lp, lower) {
			contains = append(contains, entry)
		}
	}

	result := make([]Entry, 0, len(exact)+len(prefix)+len(contains))
	result = append(result, exact...)
	result = append(result, prefix...)
	result = append(result, contains...)
	if len(result) > limit {
		result = result[:limit]
	}
	return result
}
