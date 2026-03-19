package serve

import (
	"net/http"
	"strconv"

	"github.com/ealeixandre/moa/pkg/files"
)

// handleListFiles returns file entries for a session's CWD, filtered by query.
// GET /api/sessions/{id}/files?q=...&limit=...
func handleListFiles(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := mgr.Get(r.PathValue("id"))
		if !ok {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}

		q := r.URL.Query().Get("q")
		limit := 50
		if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 && l <= 200 {
			limit = l
		}

		all := mgr.FileScanner().Scan(sess.CWD)
		results := files.Filter(all, q, limit)

		// Return empty array instead of null.
		if results == nil {
			results = []files.Entry{}
		}

		writeJSON(w, http.StatusOK, results)
	}
}
