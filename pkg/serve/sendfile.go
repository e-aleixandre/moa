package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/tool"
)

// sharedFile is a file the agent has explicitly shared via send_file.
type sharedFile struct {
	Path string // canonical absolute path on disk
	Name string // download filename (basename or override)
	Mime string
	Size int64
}

// sharedFiles is the per-session allowlist: only entries registered here can
// be served by handleDownloadFile. In-memory only — cleared when the session
// is deleted or the server restarts, at which point old download links 404.
type sharedFiles struct {
	mu sync.Mutex
	m  map[string]sharedFile // fileID -> sharedFile
}

func newSharedFiles() *sharedFiles {
	return &sharedFiles{m: make(map[string]sharedFile)}
}

// add registers f under a new random fileID and returns it.
func (s *sharedFiles) add(f sharedFile) string {
	id := newID()
	s.mu.Lock()
	s.m[id] = f
	s.mu.Unlock()
	return id
}

// get looks up a registered file by fileID.
func (s *sharedFiles) get(id string) (sharedFile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.m[id]
	return f, ok
}

// newSendFileTool creates the send_file tool for a session. cfg resolves
// paths against the same workspace/PathPolicy as the built-in file tools;
// sessionID and reg build the download URL and allowlist entry.
func newSendFileTool(cfg tool.ToolConfig, sessionID string, reg *sharedFiles) core.Tool {
	return core.Tool{
		Name:  "send_file",
		Label: "Send file",
		Description: "Send a file to the user: it appears in the web chat as a download card " +
			"(on mobile it opens the native share sheet). Use when the user asks you to " +
			"send/share/give them a file. One file per call — call it once per file; to send " +
			"many files, zip them first with bash. Read-only: does not modify the file.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {
					"type": "string",
					"description": "Path to the file to send (absolute, or relative to the working directory)."
				},
				"name": {
					"type": "string",
					"description": "Download filename shown to the user (default: the file's basename)."
				}
			},
			"required": ["path"]
		}`),
		Effect: core.EffectReadOnly,
		Execute: func(_ context.Context, params map[string]any, _ func(core.Result)) (core.Result, error) {
			path, _ := params["path"].(string)
			if path == "" {
				return core.ErrorResult("path is required"), nil
			}
			resolved, err := tool.SafePath(cfg, path)
			if err != nil {
				return core.ErrorResult(err.Error()), nil
			}
			info, err := os.Stat(resolved)
			if err != nil {
				return core.ErrorResult(fmt.Sprintf("cannot access %s: %v", path, err)), nil
			}
			if !info.Mode().IsRegular() {
				return core.ErrorResult(fmt.Sprintf("%s is not a regular file", path)), nil
			}

			name, _ := params["name"].(string)
			if name == "" {
				name = filepath.Base(resolved)
			} else {
				name = filepath.Base(name) // don't let a custom name inject path separators
			}

			mimeType := detectMime(resolved, name)

			id := reg.add(sharedFile{Path: resolved, Name: name, Mime: mimeType, Size: info.Size()})
			url := fmt.Sprintf("/api/sessions/%s/files/%s", sessionID, id)

			// Result = one human-readable line for the model, then a JSON line the
			// frontend parses to render the download card (see FileCard.jsx).
			card, _ := json.Marshal(map[string]any{
				"file_id": id,
				"name":    name,
				"size":    info.Size(),
				"mime":    mimeType,
				"url":     url,
			})
			text := fmt.Sprintf("Sent %q (%s) to the user.\n%s", name, humanSize(info.Size()), card)
			return core.TextResult(text), nil
		},
	}
}

// detectMime determines a file's MIME type from its extension, falling back to
// content sniffing and finally application/octet-stream.
func detectMime(path, name string) string {
	if t := mime.TypeByExtension(filepath.Ext(name)); t != "" {
		return t
	}
	if f, err := os.Open(path); err == nil {
		defer f.Close() //nolint:errcheck
		buf := make([]byte, 512)
		n, _ := f.Read(buf)
		if n > 0 {
			return http.DetectContentType(buf[:n])
		}
	}
	return "application/octet-stream"
}

// humanSize formats a byte count as a human-readable string (e.g. "2.4 MB").
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// handleDownloadFile serves a file previously registered via send_file. Only
// fileIDs present in the session's allowlist are served — the path never comes
// from the request, so there is no path-traversal surface. A re-stat with
// IsRegular right before serving closes the TOCTOU window between registration
// and download (e.g. the path was replaced by a symlink or directory).
func handleDownloadFile(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := mgr.Get(r.PathValue("id"))
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		f, ok := sess.sharedFiles.get(r.PathValue("fileID"))
		if !ok {
			http.Error(w, "file not shared (or the session was restarted) — ask the agent to send it again", http.StatusNotFound)
			return
		}

		file, err := os.Open(f.Path)
		if err != nil {
			http.Error(w, "file no longer exists on disk", http.StatusNotFound)
			return
		}
		defer file.Close() //nolint:errcheck

		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			http.Error(w, "file no longer exists on disk", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", f.Mime)
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": f.Name}))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, f.Name, info.ModTime(), file)
	}
}
