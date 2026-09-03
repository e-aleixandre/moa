package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestCompactModelEndpoint(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	defer srv.Close()

	// Only Anthropic has a credential in this server.
	mgr.providerCredentialAvailable = func(provider string) bool { return provider == "anthropic" }

	// Nothing configured: compaction runs on the session's own model, which is
	// how it behaved before the setting existed.
	got := getCompactModel(t, srv)
	if got.Model != core.CompactModelSession {
		t.Errorf("unset compact_model = %q, want %q", got.Model, core.CompactModelSession)
	}
	if len(got.Choices) == 0 {
		t.Fatal("expected the credentialed provider's models to be offered")
	}
	// A selector must not offer a model whose provider has no credential: the
	// choice would silently degrade to the session's model hours later, mid
	// compaction, with nobody looking at Settings.
	for _, c := range got.Choices {
		if c.Provider != "anthropic" {
			t.Errorf("offered %q from %q, which has no credential", c.Spec, c.Provider)
		}
	}

	// A real spec is stored and comes back.
	got = patchCompactModel(t, srv, `{"compact_model":"terra"}`, http.StatusOK)
	if got.Model != "terra" {
		t.Errorf("compact_model = %q, want terra", got.Model)
	}
	if persisted := core.LoadGlobalConfig().CompactModel; persisted != "terra" {
		t.Errorf("persisted compact_model = %q, want terra", persisted)
	}

	// An unknown spec is refused up front. Accepting it would only surface as a
	// fallback notice on some later compaction, far from the mistake.
	patchCompactModel(t, srv, `{"compact_model":"definitely-not-a-model"}`, http.StatusBadRequest)
	if persisted := core.LoadGlobalConfig().CompactModel; persisted != "terra" {
		t.Errorf("a rejected spec must not overwrite the setting, got %q", persisted)
	}

	// Empty means "the session's model", stored as the keyword so the config
	// file says what it means.
	got = patchCompactModel(t, srv, `{"compact_model":""}`, http.StatusOK)
	if got.Model != core.CompactModelSession {
		t.Errorf("empty compact_model = %q, want %q", got.Model, core.CompactModelSession)
	}
	if persisted := core.LoadGlobalConfig().CompactModel; persisted != core.CompactModelSession {
		t.Errorf("persisted compact_model = %q, want %q", persisted, core.CompactModelSession)
	}
}

// Without a credential probe the server cannot tell which choices are real, so
// it offers none rather than listing models that would not work.
func TestCompactModelEndpoint_NoCredentialProbeOffersNothing(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	defer srv.Close()

	mgr.providerCredentialAvailable = nil
	if got := getCompactModel(t, srv); len(got.Choices) != 0 {
		t.Errorf("expected no choices without a credential probe, got %d", len(got.Choices))
	}
}

func getCompactModel(t *testing.T, srv *httptest.Server) compactModelPolicy {
	t.Helper()
	return decodeCompactModel(t, apiReq(t, srv, http.MethodGet, "/api/compact-model", ""), http.StatusOK)
}

func patchCompactModel(t *testing.T, srv *httptest.Server, body string, wantStatus int) compactModelPolicy {
	t.Helper()
	return decodeCompactModel(t, apiReq(t, srv, http.MethodPatch, "/api/compact-model", body), wantStatus)
}

func decodeCompactModel(t *testing.T, resp *http.Response, wantStatus int) compactModelPolicy {
	t.Helper()
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != wantStatus {
		t.Fatalf("status = %d, want %d", resp.StatusCode, wantStatus)
	}
	var policy compactModelPolicy
	if wantStatus == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&policy); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return policy
}
