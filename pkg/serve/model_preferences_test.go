package serve

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestModelPreferencesEndpoint(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	srv, _, cancel := newTestServer(t)
	defer cancel()
	defer srv.Close()

	models := core.ListModels()
	if len(models) < 2 {
		t.Fatal("expected at least two known models")
	}
	firstID := models[0].Model.ID
	secondID := models[1].Model.ID

	assertModelPreferences(t, apiReq(t, srv, "GET", "/api/model-preferences", ""), nil)
	assertModelPreferences(t, apiReq(t, srv, "PATCH", "/api/model-preferences", `{"model_id":"retired-model","pinned":false}`), nil)
	assertModelPreferences(t, apiReq(t, srv, "PATCH", "/api/model-preferences", `{"model_id":"`+secondID+`","pinned":true}`), []string{secondID})
	assertModelPreferences(t, apiReq(t, srv, "PATCH", "/api/model-preferences", `{"model_id":"`+firstID+`","pinned":true}`), []string{secondID, firstID})
	assertModelPreferences(t, apiReq(t, srv, "PATCH", "/api/model-preferences", `{"model_id":"`+secondID+`","pinned":true}`), []string{secondID, firstID})
	assertModelPreferences(t, apiReq(t, srv, "PATCH", "/api/model-preferences", `{"model_id":"`+secondID+`","pinned":false}`), []string{firstID})
	assertModelPreferences(t, apiReq(t, srv, "GET", "/api/model-preferences", ""), []string{firstID})

	if got := core.LoadGlobalConfig().PinnedModels; len(got) != 1 || got[0] != firstID {
		t.Fatalf("persisted pinned models = %v, want [%s]", got, firstID)
	}
}

func TestModelPreferencesEndpointValidation(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	srv, _, cancel := newTestServer(t)
	defer cancel()
	defer srv.Close()

	knownID := core.ListModels()[0].Model.ID
	for _, body := range []string{
		`{`,
		`{"pinned":true}`,
		`{"model_id":"` + knownID + `"}`,
		`{"model_id":"not-a-model","pinned":true}`,
	} {
		resp := apiReq(t, srv, "PATCH", "/api/model-preferences", body)
		resp.Body.Close() //nolint:errcheck // test cleanup
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("PATCH body %s: status = %d, want 400", body, resp.StatusCode)
		}
	}
}

func assertModelPreferences(t *testing.T, resp *http.Response, want []string) {
	t.Helper()
	defer resp.Body.Close() //nolint:errcheck // test cleanup
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got modelPreferences
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.PinnedModels == nil {
		t.Fatal("pinned_models should be an empty array, not null")
	}
	if len(got.PinnedModels) != len(want) {
		t.Fatalf("pinned_models = %v, want %v", got.PinnedModels, want)
	}
	for i := range want {
		if got.PinnedModels[i] != want[i] {
			t.Fatalf("pinned_models = %v, want %v", got.PinnedModels, want)
		}
	}
}
