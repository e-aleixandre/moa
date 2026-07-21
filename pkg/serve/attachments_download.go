package serve

import (
	"bytes"
	"io"
	"mime"
	"net/http"
)

var inlineAttachmentMIMEs = map[string]bool{
	"image/gif":  true,
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
}

// handleGetAttachment serves a durable attachment when its session-owned
// occurrence is still present. The store index is the authorization boundary,
// so this deliberately does not require a live session runtime.
func handleGetAttachment(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mgr.attachStore == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		sessionID, attID := r.PathValue("id"), r.PathValue("attID")
		if _, ok := mgr.attachStore.Lookup(sessionID, attID); !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		reader, descriptor, err := mgr.attachStore.Open(sessionID, attID)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		defer reader.Close() //nolint:errcheck

		name := safeBase(descriptor.Name)
		if name == "" {
			name = "attachment"
		}
		disposition := "attachment"
		if inlineAttachmentMIMEs[descriptor.Mime] {
			disposition = "inline"
		}
		w.Header().Set("Content-Type", descriptor.Mime)
		w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "sandbox")
		w.Header().Set("ETag", `"sha256-`+descriptor.SHA256+`"`)
		w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")

		if seeker, ok := reader.(io.ReadSeeker); ok {
			http.ServeContent(w, r, name, descriptor.CreatedAt, seeker)
			return
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			http.Error(w, "attachment unavailable", http.StatusInternalServerError)
			return
		}
		http.ServeContent(w, r, name, descriptor.CreatedAt, bytes.NewReader(data))
	}
}
