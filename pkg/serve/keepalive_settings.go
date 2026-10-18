package serve

import (
	"encoding/json"
	"net/http"

	"github.com/e-aleixandre/moa/pkg/core"
)

// keepAliveSettings is the wire shape of the cache keep-alive switches in
// Settings. Horizon overrides edited by hand in the config file are not part
// of it and survive a save untouched.
type keepAliveSettings struct {
	Owner    bool `json:"owner"`
	Session  bool `json:"session"`
	Subagent bool `json:"subagent"`
	// Today, across every session: what pings cost, and the estimated cold
	// writes the warm wakes they bought did not pay. Informative only.
	SpentTodayUSD float64 `json:"spent_today_usd"`
	SavedTodayUSD float64 `json:"saved_today_usd"`
}

func (k *keepAliveService) settings() keepAliveSettings {
	c := k.setting.Load()
	var out keepAliveSettings
	out.SpentTodayUSD, out.SavedTodayUSD = k.today()
	if c == nil {
		return out
	}
	out.Owner = c.Owner != nil && c.Owner.Enabled
	out.Session = c.Session != nil && c.Session.Enabled
	out.Subagent = c.Subagent != nil && c.Subagent.Enabled
	return out
}

func handleCacheKeepAlive(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, mgr.keepAlive.settings())
		case http.MethodPatch:
			handleCacheKeepAlivePatch(w, r, mgr)
		}
	}
}

// handleCacheKeepAlivePatch changes only the fields sent.
func handleCacheKeepAlivePatch(w http.ResponseWriter, r *http.Request, mgr *Manager) {
	limitBody(w, r, maxJSONBodySize)
	var body struct {
		Owner    *bool `json:"owner"`
		Session  *bool `json:"session"`
		Subagent *bool `json:"subagent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if body.Owner == nil && body.Session == nil && body.Subagent == nil {
		http.Error(w, "nothing to change", http.StatusBadRequest)
		return
	}

	mgr.configMutationMu.Lock()
	defer mgr.configMutationMu.Unlock()

	var saved *core.CacheKeepAliveConfig
	if err := core.SaveGlobalConfig(func(cfg *core.MoaConfig) {
		c := cfg.CacheKeepAlive
		if c == nil {
			c = &core.CacheKeepAliveConfig{}
		}
		for _, s := range []struct {
			on  *bool
			dst **core.KeepAliveHorizons
		}{{body.Owner, &c.Owner}, {body.Session, &c.Session}, {body.Subagent, &c.Subagent}} {
			if s.on == nil {
				continue
			}
			if *s.dst == nil {
				*s.dst = &core.KeepAliveHorizons{}
			}
			(*s.dst).Enabled = *s.on
		}
		cfg.CacheKeepAlive = c
		saved = c
	}); err != nil {
		http.Error(w, "failed to save cache keep-alive", http.StatusInternalServerError)
		return
	}
	// Live agents read the setting at their next request or ping.
	mgr.keepAlive.set(saved)
	writeJSON(w, http.StatusOK, mgr.keepAlive.settings())
}
