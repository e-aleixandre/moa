package serve

import (
	"encoding/json"
	"net/http"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

// compactAtPolicy is the wire shape of the global compaction threshold.
// CompactAt is in tokens, 0 meaning automatic (compact at the model window).
// Min travels with it because the engine floors any lower threshold: a client
// that offered a smaller number would be promising a compaction point that will
// not happen. Same contract as the per-session compact_at/compact_at_min pair.
type compactAtPolicy struct {
	CompactAt int `json:"compact_at"`
	Min       int `json:"compact_at_min"`
}

func handleCompactAt(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, currentCompactAtPolicy(core.GetCompactAt(core.LoadGlobalConfig())))
		case http.MethodPatch:
			handleCompactAtPatch(w, r, mgr)
		}
	}
}

func handleCompactAtPatch(w http.ResponseWriter, r *http.Request, mgr *Manager) {
	limitBody(w, r, maxJSONBodySize)
	// Pointer: 0 is a real value here ("automatic"), so only nil can mean the
	// request isn't changing the threshold.
	var body struct {
		CompactAt *int `json:"compact_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if body.CompactAt == nil {
		http.Error(w, "compact_at required", http.StatusBadRequest)
		return
	}
	if *body.CompactAt < 0 {
		http.Error(w, "compact_at must be >= 0", http.StatusBadRequest)
		return
	}

	// Raised to the floor rather than rejected, and the stored value is what
	// comes back: the client then displays the threshold the engine will really
	// use instead of the smaller one that was asked for and silently ignored.
	tokens := *body.CompactAt
	if floor := core.DefaultCompactionSettings.MinCompactAt(); tokens > 0 && tokens < floor {
		tokens = floor
	}

	mgr.configMutationMu.Lock()
	defer mgr.configMutationMu.Unlock()

	if err := core.SaveGlobalConfig(func(cfg *core.MoaConfig) {
		cfg.CompactAt = tokens
	}); err != nil {
		http.Error(w, "failed to save compaction threshold", http.StatusInternalServerError)
		return
	}
	// The setting is global and live: it applies to every conversation, open or
	// not, old or new. Saving the file alone only covers sessions built later —
	// a resident agent captured the previous value when it was constructed and
	// would keep using it for as long as it stays loaded, which on this server
	// is days.
	mgr.applyDefaultCompactAt(tokens)
	writeJSON(w, http.StatusOK, currentCompactAtPolicy(tokens))
}

func currentCompactAtPolicy(tokens int) compactAtPolicy {
	return compactAtPolicy{CompactAt: tokens, Min: core.DefaultCompactionSettings.MinCompactAt()}
}

// applyDefaultCompactAt pushes the global threshold into every session already
// resident in memory. Sessions built after this point read the saved config, so
// only the loaded ones need telling; a session that chose its own compact_at
// keeps it, because the agent stores the two separately and its own value wins.
func (m *Manager) applyDefaultCompactAt(tokens int) {
	m.mu.RLock()
	loaded := make([]*ManagedSession, 0, len(m.sessions))
	for _, sess := range m.sessions {
		loaded = append(loaded, sess)
	}
	m.mu.RUnlock()

	for _, sess := range loaded {
		if sess.runtime == nil || sess.runtime.Bus == nil {
			continue
		}
		// Best effort: one unreachable session must not stop the rest.
		_ = sess.runtime.Bus.Execute(bus.SetDefaultCompactAt{SessionID: sess.ID, Tokens: tokens})
	}
}
