package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

func kaSettingsReq(t *testing.T, srv *httptest.Server, method, body string, wantStatus int) keepAliveSettings {
	t.Helper()
	resp := apiReq(t, srv, method, "/api/cache-keepalive", body)
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s: status = %d, want %d", method, body, resp.StatusCode, wantStatus)
	}
	var s keepAliveSettings
	if wantStatus == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestKeepAliveSettings_RoundTripAndPersistence(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	defer srv.Close()

	got := kaSettingsReq(t, srv, http.MethodGet, "", http.StatusOK)
	if got.Owner || got.Session || got.Subagent || got.SpentTodayUSD != 0 || got.SavedTodayUSD != 0 {
		t.Errorf("defaults = %+v, want all off, nothing spent or saved", got)
	}

	// A hand-written horizon override must survive a save from Settings.
	if err := core.SaveGlobalConfig(func(c *core.MoaConfig) {
		c.CacheKeepAlive = &core.CacheKeepAliveConfig{PingEvery1h: "50m", Owner: &core.KeepAliveHorizons{Idle: "2h"}}
	}); err != nil {
		t.Fatal(err)
	}

	got = kaSettingsReq(t, srv, http.MethodPatch, `{"owner":true}`, http.StatusOK)
	if !got.Owner || got.Session || got.Subagent {
		t.Errorf("after patch = %+v", got)
	}
	// A partial patch leaves the other fields alone.
	got = kaSettingsReq(t, srv, http.MethodPatch, `{"session":true}`, http.StatusOK)
	if !got.Owner || !got.Session {
		t.Errorf("partial patch changed other fields: %+v", got)
	}
	if again := kaSettingsReq(t, srv, http.MethodGet, "", http.StatusOK); again != got {
		t.Errorf("GET %+v != PATCH response %+v", again, got)
	}

	saved := core.LoadGlobalConfig().CacheKeepAlive
	if saved == nil || saved.Owner == nil || !saved.Owner.Enabled || saved.Session == nil || !saved.Session.Enabled || saved.Subagent != nil {
		t.Fatalf("persisted = %+v", saved)
	}
	if saved.PingEvery1h != "50m" || saved.Owner.Idle != "2h" {
		t.Errorf("persisted lost values: %+v owner=%+v", saved, saved.Owner)
	}
	// Written to the global file, and nowhere else.
	raw, _ := os.ReadFile(os.Getenv("MOA_CONFIG_DIR") + "/config.json")
	if !strings.Contains(string(raw), `"cache_keepalive"`) {
		t.Errorf("global config file lacks cache_keepalive: %s", raw)
	}

	got = kaSettingsReq(t, srv, http.MethodPatch, `{"owner":false,"session":false}`, http.StatusOK)
	if got.Owner || got.Session {
		t.Errorf("switching off: %+v", got)
	}
	if mgr.keepAlive.policy().RoleEnabled(core.KeepAliveRoleOwner) {
		t.Error("owner still enabled in the live policy")
	}
}

func TestKeepAliveSettings_Validation(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	srv, _, cancel := newTestServer(t)
	defer cancel()
	defer srv.Close()

	for _, body := range []string{
		`{}`, `not json`, `{"owner":"yes"}`, `{"daily_budget_usd":5}`,
	} {
		kaSettingsReq(t, srv, http.MethodPatch, body, http.StatusBadRequest)
	}
	if c := core.LoadGlobalConfig().CacheKeepAlive; c != nil {
		t.Errorf("a rejected request wrote %+v", c)
	}
}

func TestKeepAliveSettings_AppliesToLiveSessionWithoutRestart(t *testing.T) {
	h := newKAServe(t, nil)
	srv := httptest.NewServer(NewServer(h.mgr))
	defer srv.Close()

	h.advance(2 * time.Hour)
	if n := h.prov.pingCount(); n != 0 {
		t.Fatalf("pings while off = %d", n)
	}
	kaSettingsReq(t, srv, http.MethodPatch, `{"session":true}`, http.StatusOK)
	sendAndWaitIdle(t, h.mgr, h.sess, "again", "")
	h.advance(56 * time.Minute)
	pollUntil(t, 2*time.Second, "ping after the switch", func() bool { return h.prov.pingCount() == 1 })
	pollUntil(t, 2*time.Second, "spent today shown", func() bool {
		return kaSettingsReq(t, srv, http.MethodGet, "", http.StatusOK).SpentTodayUSD > 0
	})
}
