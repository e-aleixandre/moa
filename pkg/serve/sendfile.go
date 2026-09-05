package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tool"
)

// newSendFileTool creates the send_file tool for a session. cfg resolves paths
// against the same workspace/PathPolicy as the built-in file tools; sessionID
// builds the download URL and store is the session's durable artifact catalog.
//
// A successful call has already persisted its reference: there is no in-memory
// allowlist and no snapshot of the bytes, so the artifact keeps pointing at the
// live file across restarts and shows whatever that path holds when it is read.
func newSendFileTool(cfg tool.ToolConfig, sessionID string, store *session.ArtifactStore) core.Tool {
	return core.Tool{
		Name:  "send_file",
		Label: "Send file",
		Description: "Deliver a file to the user: it appears in the web chat as a download card " +
			"(on mobile it opens the native share sheet) and is added to the conversation's " +
			"Artifacts, where the user can reopen it later. Use it to hand over any result you " +
			"produced or were asked for — writing a file is not delivering it. The reference " +
			"points at the file itself, so keep the source where it is (do not delete or move it) " +
			"and later edits are what the user sees. One file per call — call it once per file; " +
			"to send many files, zip them first with bash. Read-only: does not modify the file.",
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
				},
				"title": {
					"type": "string",
					"description": "Optional short title for the artifact card (default: the filename)."
				},
				"description": {
					"type": "string",
					"description": "Optional one-line description of what the file contains."
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
			if store == nil {
				return core.ErrorResult("artifact storage is unavailable for this session"), nil
			}
			resolved, err := tool.SafePath(cfg, path)
			if err != nil {
				return core.ErrorResult(err.Error()), nil
			}
			// Resolve any allowed symlink to the destination that will actually
			// be published, then re-check THAT path against the policy: a
			// symlink flipped between the two steps must not turn an allowed
			// input into a publication outside the boundary.
			canonical, err := canonicalArtifactPath(cfg, resolved)
			if err != nil {
				return core.ErrorResult(fmt.Sprintf("cannot access %s: %v", path, err)), nil
			}

			file, info, err := openArtifactPath(canonical)
			if err != nil {
				return core.ErrorResult(fmt.Sprintf("cannot send %s: %v", path, err)), nil
			}
			defer file.Close() //nolint:errcheck

			name, err := artifactText(params, "name")
			if err != nil {
				return core.ErrorResult("name: " + err.Error()), nil
			}
			if name == "" {
				name = filepath.Base(canonical)
			}
			name = safeBase(name) // don't let a custom name inject path separators
			if name == "" {
				name = "file"
			}
			title, err := artifactText(params, "title")
			if err != nil {
				return core.ErrorResult("title: " + err.Error()), nil
			}
			description, err := artifactText(params, "description")
			if err != nil {
				return core.ErrorResult("description: " + err.Error()), nil
			}

			mimeType := detectMimeFromFile(file, name)
			size := info.Size()

			// Persist BEFORE reporting success: a card the user can click must
			// correspond to a reference that survived the process.
			artifact, err := store.Upsert(canonical, session.ArtifactMeta{
				Name: name, Title: title, Description: description, Mime: mimeType, Size: size,
			})
			if err != nil {
				return core.ErrorResult(fmt.Sprintf("cannot publish %s: %v", path, err)), nil
			}

			url := fmt.Sprintf("/api/sessions/%s/files/%s", sessionID, artifact.ID)
			// Result = one human-readable line for the model, then a JSON line
			// the frontend parses to render the download card (see
			// FileCard.jsx). The legacy fields keep their shape for installed
			// clients; title/description are additive.
			card := map[string]any{
				"file_id": artifact.ID,
				"name":    artifact.Name,
				"size":    artifact.Size,
				"mime":    artifact.Mime,
				"url":     url,
			}
			if artifact.Title != "" {
				card["title"] = artifact.Title
			}
			if artifact.Description != "" {
				card["description"] = artifact.Description
			}
			encoded, _ := json.Marshal(card)
			text := fmt.Sprintf("Sent %q (%s) to the user.\n%s", artifact.Name, humanSize(artifact.Size), encoded)
			return core.TextResult(text), nil
		},
	}
}

// canonicalArtifactPath turns an allowed input path into the existing
// destination that will be stored, re-validating it against the path policy.
func canonicalArtifactPath(cfg tool.ToolConfig, resolved string) (string, error) {
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	checked, err := tool.SafePath(cfg, canonical)
	if err != nil {
		return "", err
	}
	return checked, nil
}

// artifactText reads an optional string parameter, trimming outer whitespace
// and rejecting NUL bytes or invalid UTF-8 (which would corrupt the catalog
// and the headers built from it).
func artifactText(params map[string]any, key string) (string, error) {
	raw, ok := params[key]
	if !ok || raw == nil {
		return "", nil
	}
	s, ok := raw.(string)
	if !ok {
		return "", errors.New("must be a string")
	}
	s = strings.TrimSpace(s)
	if strings.ContainsRune(s, 0) || !utf8.ValidString(s) {
		return "", errors.New("must be valid UTF-8 text")
	}
	return s, nil
}

// detectMimeFromFile determines a file's MIME type from its name extension,
// falling back to sniffing at most the first 512 bytes of the ALREADY VALIDATED
// descriptor. The path is never reopened: what is sniffed is what is served.
func detectMimeFromFile(f *os.File, name string) string {
	if t := mime.TypeByExtension(filepath.Ext(name)); t != "" {
		return t
	}
	if head := artifactHead(f); len(head) > 0 {
		return http.DetectContentType(head)
	}
	return "application/octet-stream"
}

// artifactHead reads at most the first 512 bytes of the validated descriptor
// without disturbing its offset (ServeContent seeks it itself afterwards).
func artifactHead(f *os.File) []byte {
	buf := make([]byte, 512)
	n, err := f.ReadAt(buf, 0)
	if n > 0 && (err == nil || err == io.EOF) {
		return buf[:n]
	}
	return nil
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

// artifactDTO is the wire shape of one artifact. path never leaves the server.
type artifactDTO struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Title       string    `json:"title,omitempty"`
	Description string    `json:"description,omitempty"`
	Mime        string    `json:"mime"`
	Size        int64     `json:"size"`
	URL         string    `json:"url"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Available   bool      `json:"available"`
}

// artifactStoreFor resolves the artifact catalog of a session, live or not.
//
// Both paths require the session's own "<id>.json" to exist with a matching
// header: an orphaned sidecar left by a crash is never served, and a delete
// that already removed the main JSON stops answering. The second return value
// is the HTTP status to use when err != nil.
func artifactStoreFor(mgr *Manager, id string) (*session.ArtifactStore, int, error) {
	if sess, ok := mgr.Get(id); ok {
		store := sess.artifactStore
		if store == nil {
			return nil, http.StatusInternalServerError, errors.New("session has no artifact store")
		}
		if _, err := session.FindSessionStoreReadOnly(mgr.sessionBaseDir, id); err != nil {
			if errors.Is(err, session.ErrNotFound) {
				return nil, http.StatusNotFound, err
			}
			return nil, http.StatusInternalServerError, err
		}
		return store, http.StatusOK, nil
	}
	store, err := session.FindSessionStoreReadOnly(mgr.sessionBaseDir, id)
	if err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return nil, http.StatusNotFound, err
		}
		return nil, http.StatusInternalServerError, err
	}
	// Read-only view of the same sidecar: the atomic rename means a reader
	// always sees a whole catalog, so no extra writer is created.
	return session.NewArtifactStore(store.Dir(), id), http.StatusOK, nil
}

// handleListArtifacts serves a session's artifact collection, newest update
// first. Each entry is opened through the same safe helper the download path
// uses, so size/MIME reflect the file right now; an entry that cannot be opened
// as a regular file keeps its last known metadata and reports available:false
// instead of failing the whole list.
func handleListArtifacts(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		store, status, err := artifactStoreFor(mgr, id)
		if err != nil {
			http.Error(w, http.StatusText(status), status)
			return
		}
		artifacts, err := store.List()
		if err != nil {
			http.Error(w, "artifact catalog is unreadable", http.StatusInternalServerError)
			return
		}

		out := make([]artifactDTO, 0, len(artifacts))
		for _, a := range artifacts {
			dto := artifactDTO{
				ID: a.ID, Name: a.Name, Title: a.Title, Description: a.Description,
				Mime: a.Mime, Size: a.Size,
				URL:       fmt.Sprintf("/api/sessions/%s/files/%s", id, a.ID),
				CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
			}
			if f, info, openErr := openArtifactPath(a.Path); openErr == nil {
				dto.Available = true
				dto.Size = info.Size()
				dto.Mime = detectMimeFromFile(f, a.Name)
				_ = f.Close()
			}
			out = append(out, dto)
		}
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
				return out[i].ID < out[j].ID
			}
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		})
		writeJSON(w, http.StatusOK, map[string]any{"artifacts": out})
	}
}

// handleDownloadFile serves the current content of an artifact. The path never
// comes from the request — it is read from the session's own catalog — and the
// descriptor is opened without following symlinks in any component, so a path
// or parent swapped for a link to somewhere else is refused rather than served.
// A legitimate atomic replacement at the same location simply returns the new
// bytes.
func handleDownloadFile(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		store, status, err := artifactStoreFor(mgr, id)
		if err != nil {
			http.Error(w, http.StatusText(status), status)
			return
		}
		artifact, ok, err := store.Get(r.PathValue("fileID"))
		if err != nil {
			http.Error(w, "artifact catalog is unreadable", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "artifact not found", http.StatusNotFound)
			return
		}

		file, info, err := openArtifactPath(artifact.Path)
		if err != nil {
			// Known reference, unreachable source: distinct from 404 so the UI
			// can offer recovery instead of claiming the artifact never existed.
			// The path is not revealed.
			http.Error(w, "artifact source is unavailable", http.StatusGone)
			return
		}
		defer file.Close() //nolint:errcheck

		name := safeBase(artifact.Name)
		if name == "" {
			name = "file"
		}
		mimeType := detectMimeFromFile(file, name)
		disposition := "attachment"
		// Inline only for an image type whose BYTES agree: a text/HTML payload
		// named .png must not be rendered in the browsing context.
		if inlineAttachmentMIMEs[mimeType] && bytesLookLikeImage(artifactHead(file), mimeType) {
			disposition = "inline"
		}
		w.Header().Set("Content-Type", mimeType)
		w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "sandbox")
		w.Header().Set("Cache-Control", "private, no-store")
		// ServeContent streams from the descriptor (HEAD and ranges included):
		// the file is never copied into memory, so size is not capped here.
		http.ServeContent(w, r, name, info.ModTime(), file)
	}
}
