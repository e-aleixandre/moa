package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestCompactStrategy_DefaultsToNotify(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	mgr := newTestManager(t, t.Context(), nil)

	req := httptest.NewRequest(http.MethodGet, "/api/compact-strategy", nil)
	rec := httptest.NewRecorder()
	handleCompactStrategy(mgr)(rec, req)

	var got compactStrategyPolicy
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Strategy != core.CompactNotify {
		t.Errorf("strategy = %q, want notify: an unconfigured server should warn the agent", got.Strategy)
	}
	// The client renders whatever the server offers, so all three have to be here.
	if len(got.Options) != 3 {
		t.Errorf("options = %v, want plain, notify and prepare", got.Options)
	}
}

func TestCompactStrategy_SavesAndRejects(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	mgr := newTestManager(t, t.Context(), nil)

	patch := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/api/compact-strategy", strings.NewReader(body))
		rec := httptest.NewRecorder()
		handleCompactStrategy(mgr)(rec, req)
		return rec
	}

	if rec := patch(`{"compact_strategy":"prepare"}`); rec.Code != http.StatusOK {
		t.Fatalf("saving prepare: %d %s", rec.Code, rec.Body)
	}
	// It has to survive the request: this is a global setting, not session state.
	if got := core.GetCompactStrategy(core.LoadGlobalConfig()); got != core.CompactPrepare {
		t.Errorf("after saving, strategy = %q, want prepare", got)
	}

	// An unknown value would silently fall back to notify at read time, hiding
	// a client bug behind working behaviour.
	if rec := patch(`{"compact_strategy":"whatever"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown strategy was accepted: %d", rec.Code)
	}
	if got := core.GetCompactStrategy(core.LoadGlobalConfig()); got != core.CompactPrepare {
		t.Errorf("a rejected request changed the setting to %q", got)
	}
}
