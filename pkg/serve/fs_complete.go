package serve

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ealeixandre/moa/pkg/core"
)

// fsCompleteResponse is the JSON body returned by handleFSComplete.
type fsCompleteResponse struct {
	Path    string   `json:"path"`
	Exists  bool     `json:"exists"`
	IsDir   bool     `json:"isDir"`
	Entries []string `json:"entries"`
}

// handleFSComplete lists subdirectories under a partial filesystem path, and
// reports whether the exact path exists and is a directory. Used by the web
// palette to autocomplete + validate a session cwd before creating it.
// GET /api/fs/complete?path=...
func handleFSComplete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Query().Get("path")
		resp := fsCompleteResponse{Entries: []string{}}

		if raw == "" || (!strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "~")) {
			writeJSON(w, http.StatusOK, resp)
			return
		}

		expanded := expandHome(raw)

		canonical, err := core.CanonicalizePath(expanded)
		if err != nil {
			writeJSON(w, http.StatusOK, resp)
			return
		}
		resp.Path = canonical

		if info, statErr := os.Stat(canonical); statErr == nil {
			resp.Exists = true
			resp.IsDir = info.IsDir()
		}

		// Split into (dir, base): a trailing separator means "list everything
		// in this dir"; otherwise base is the prefix to filter subdirs by.
		var dir, base string
		if strings.HasSuffix(expanded, "/") {
			dir, base = canonical, ""
		} else {
			dir, base = filepath.Dir(canonical), filepath.Base(canonical)
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			writeJSON(w, http.StatusOK, resp)
			return
		}

		baseLower := strings.ToLower(base)
		showDot := strings.HasPrefix(base, ".")
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if !showDot && strings.HasPrefix(name, ".") {
				continue
			}
			if base != "" && !strings.HasPrefix(strings.ToLower(name), baseLower) {
				continue
			}
			names = append(names, name)
		}
		sort.Strings(names)
		if len(names) > 50 {
			names = names[:50]
		}
		resp.Entries = names

		writeJSON(w, http.StatusOK, resp)
	}
}

// expandHome expands a leading "~" or "~/" in path to the current user's home
// directory. Paths that don't start with "~" are returned unchanged.
func expandHome(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
