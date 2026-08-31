package serve

import (
	"encoding/json"
	"net/http"

	"github.com/e-aleixandre/moa/pkg/core"
)

// compactStrategyPolicy is the wire shape of the global pre-compaction strategy.
// Options travels with it so a client renders the choices the server actually
// accepts instead of a hardcoded list that can drift.
type compactStrategyPolicy struct {
	Strategy string   `json:"compact_strategy"`
	Options  []string `json:"compact_strategy_options"`
}

func handleCompactStrategy(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, currentCompactStrategy(core.GetCompactStrategy(core.LoadGlobalConfig())))
		case http.MethodPatch:
			handleCompactStrategyPatch(w, r, mgr)
		}
	}
}

func handleCompactStrategyPatch(w http.ResponseWriter, r *http.Request, mgr *Manager) {
	limitBody(w, r, maxJSONBodySize)
	var body struct {
		Strategy string `json:"compact_strategy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	switch body.Strategy {
	case core.CompactPlain, core.CompactNotify, core.CompactPrepare:
	default:
		http.Error(w, "compact_strategy must be plain, notify or prepare", http.StatusBadRequest)
		return
	}

	mgr.configMutationMu.Lock()
	defer mgr.configMutationMu.Unlock()

	if err := core.SaveGlobalConfig(func(cfg *core.MoaConfig) {
		cfg.CompactStrategy = body.Strategy
	}); err != nil {
		http.Error(w, "failed to save compaction strategy", http.StatusInternalServerError)
		return
	}
	// Global and live, like the threshold it sits next to: a resident agent
	// captured the previous value when it was built and would keep using it for
	// as long as it stays loaded, which on this server is days.
	mgr.applyCompactStrategy(body.Strategy)
	writeJSON(w, http.StatusOK, currentCompactStrategy(body.Strategy))
}

func currentCompactStrategy(strategy string) compactStrategyPolicy {
	return compactStrategyPolicy{
		Strategy: strategy,
		Options:  []string{core.CompactPlain, core.CompactNotify, core.CompactPrepare},
	}
}

// applyCompactStrategy pushes the strategy into every session already resident
// in memory. Sessions built later read the saved config.
func (m *Manager) applyCompactStrategy(strategy string) {
	m.mu.RLock()
	loaded := make([]*ManagedSession, 0, len(m.sessions))
	for _, sess := range m.sessions {
		loaded = append(loaded, sess)
	}
	m.mu.RUnlock()

	for _, sess := range loaded {
		if sess == nil || sess.runtime == nil {
			continue
		}
		if agent := sess.runtime.Context().Agent; agent != nil {
			agent.SetCompactStrategy(strategy)
		}
	}
}
