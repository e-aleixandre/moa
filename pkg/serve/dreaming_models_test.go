package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func dreamingModelsCall(t *testing.T, srv *httptest.Server, method, body string, want int) dreamingModelsPolicy {
	t.Helper()
	resp := apiReq(t, srv, method, "/api/dreaming-models", body)
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != want {
		t.Fatalf("%s %s: status = %d, want %d", method, body, resp.StatusCode, want)
	}
	var p dreamingModelsPolicy
	if want == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// The settings persist in the real global config file, next to the ones that
// were already there, and read back through the endpoint.
func TestDreamingModelsEndpoint(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	if err := core.SaveGlobalConfig(func(c *core.MoaConfig) { c.CompactModel = "terra" }); err != nil {
		t.Fatal(err)
	}
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	defer srv.Close()
	mgr.providerCredentialAvailable = func(p string) bool { return p == "anthropic" }

	got := dreamingModelsCall(t, srv, http.MethodGet, "", http.StatusOK)
	if got.Reader != "" || got.Organizer != "" {
		t.Errorf("unset = %q / %q, want both empty (the default)", got.Reader, got.Organizer)
	}
	if got.Defaults.Reader != "claude-haiku-5-5" || got.Defaults.Organizer != "sonnet" {
		t.Errorf("defaults with only Anthropic = %+v", got.Defaults)
	}
	for _, c := range append(append([]compactModelChoice{}, got.ReaderChoices...), got.OrganizerChoices...) {
		if c.Provider != "anthropic" {
			t.Errorf("offered %q from %q, which has no credential", c.Spec, c.Provider)
		}
	}
	has := func(list []compactModelChoice, spec string) bool {
		for _, c := range list {
			if c.Spec == spec {
				return true
			}
		}
		return false
	}
	if !has(got.ReaderChoices, "claude-haiku-5-5") || !has(got.OrganizerChoices, "sonnet") {
		t.Errorf("the default pair is not offered: %+v / %+v", got.ReaderChoices, got.OrganizerChoices)
	}

	got = dreamingModelsCall(t, srv, http.MethodPatch, `{"reader":"claude-haiku-5-5"}`, http.StatusOK)
	if got.Reader != "claude-haiku-5-5" || got.Organizer != "" {
		t.Errorf("after reader patch = %q / %q", got.Reader, got.Organizer)
	}
	// Patching one role leaves the other alone.
	dreamingModelsCall(t, srv, http.MethodPatch, `{"organizer":"opus"}`, http.StatusOK)
	cfg := core.LoadGlobalConfig()
	if cfg.DreamingReaderModel != "claude-haiku-5-5" || cfg.DreamingOrganizerModel != "opus" {
		t.Errorf("persisted = %q / %q", cfg.DreamingReaderModel, cfg.DreamingOrganizerModel)
	}
	if cfg.CompactModel != "terra" {
		t.Errorf("an existing setting was lost: compact_model = %q", cfg.CompactModel)
	}

	// A model whose reservation does not fit the night is refused by the API too.
	// With a smaller night (as an owner's budget_usd could make it) the dear
	// models no longer fit: Astra reserves about $1.3-2 a call, GPT-5.5 and 5.6
	// about $1-1.5, the defaults well under $0.6.
	mgr.providerCredentialAvailable = func(string) bool { return true }
	defer func(v float64) { dreamingSelectorBudget = v }(dreamingSelectorBudget)
	dreamingSelectorBudget = 0.9
	for _, spec := range []string{"astra", "gpt5", "gpt-5.6"} {
		dreamingModelsCall(t, srv, http.MethodPatch, `{"organizer":"`+spec+`"}`, http.StatusUnprocessableEntity)
	}
	if got := core.LoadGlobalConfig().DreamingOrganizerModel; got != "opus" {
		t.Errorf("a refused model changed the setting to %q", got)
	}
	all := dreamingModelsCall(t, srv, http.MethodGet, "", http.StatusOK)
	for _, c := range append(append([]compactModelChoice{}, all.ReaderChoices...), all.OrganizerChoices...) {
		if c.Spec == "astra" || c.Spec == "gpt5" || c.Spec == "gpt-5.6" {
			t.Errorf("%s is offered", c.Spec)
		}
	}
	if !has(all.ReaderChoices, "sonnet") {
		t.Errorf("a smaller budget dropped a default: %+v", all.ReaderChoices)
	}
	dreamingSelectorBudget = 3
	mgr.providerCredentialAvailable = func(p string) bool { return p == "anthropic" }

	// Unknown spec and empty body are refused and change nothing.
	dreamingModelsCall(t, srv, http.MethodPatch, `{"reader":"no-such-model"}`, http.StatusBadRequest)
	dreamingModelsCall(t, srv, http.MethodPatch, `{}`, http.StatusBadRequest)
	if got := core.LoadGlobalConfig().DreamingReaderModel; got != "claude-haiku-5-5" {
		t.Errorf("a rejected patch changed the setting to %q", got)
	}

	// Empty goes back to the default.
	got = dreamingModelsCall(t, srv, http.MethodPatch, `{"reader":"","organizer":""}`, http.StatusOK)
	if got.Reader != "" || got.Organizer != "" || core.LoadGlobalConfig().DreamingOrganizerModel != "" {
		t.Errorf("reset = %+v", got)
	}
}

// A config file written before these settings existed reads as "default".
func TestDreamingModelsAbsentFromOldConfig(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	if err := core.SaveGlobalConfig(func(c *core.MoaConfig) { c.CompactAt = 1000 }); err != nil {
		t.Fatal(err)
	}
	cfg := core.LoadGlobalConfig()
	if cfg.DreamingReaderModel != "" || cfg.DreamingOrganizerModel != "" || cfg.CompactAt != 1000 {
		t.Fatalf("old config = %+v", cfg)
	}
}
