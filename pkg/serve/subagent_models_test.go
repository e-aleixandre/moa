package serve

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestSubagentModelsEndpoint(t *testing.T) {
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

	// No policy configured means "all models allowed", which is what an empty
	// list encodes; it must never come back as null.
	assertSubagentModels(t, apiReq(t, srv, "GET", "/api/subagent-models", ""), nil)

	assertSubagentModels(t,
		apiReq(t, srv, "PATCH", "/api/subagent-models", `{"allowed_models":["`+firstID+`","`+secondID+`","`+firstID+`"]}`),
		[]string{firstID, secondID})
	assertSubagentModels(t, apiReq(t, srv, "GET", "/api/subagent-models", ""), []string{firstID, secondID})

	if got := core.LoadGlobalConfig().SubagentAllowedModels; !slices.Equal(got, []string{firstID, secondID}) {
		t.Fatalf("persisted allowed models = %v, want [%s %s]", got, firstID, secondID)
	}

	// Clearing the list restores the unrestricted default.
	assertSubagentModels(t, apiReq(t, srv, "PATCH", "/api/subagent-models", `{"allowed_models":[]}`), nil)
	if got := core.LoadGlobalConfig().SubagentAllowedModels; len(got) != 0 {
		t.Fatalf("persisted allowed models = %v, want empty", got)
	}
}

func TestSubagentModelsEndpointValidation(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	srv, _, cancel := newTestServer(t)
	defer cancel()
	defer srv.Close()

	for _, body := range []string{
		`{`,
		`{}`,
		`{"allowed_models":["not-a-model"]}`,
	} {
		resp := apiReq(t, srv, "PATCH", "/api/subagent-models", body)
		resp.Body.Close() //nolint:errcheck // test cleanup
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("PATCH body %s: status = %d, want 400", body, resp.StatusCode)
		}
	}
	if got := core.LoadGlobalConfig().SubagentAllowedModels; len(got) != 0 {
		t.Fatalf("a rejected request still wrote %v", got)
	}
}

func assertSubagentModels(t *testing.T, resp *http.Response, want []string) {
	t.Helper()
	defer resp.Body.Close() //nolint:errcheck // test cleanup
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got subagentModelPolicy
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.AllowedModels == nil {
		t.Fatal("allowed_models should be an empty array, not null")
	}
	if !slices.Equal(got.AllowedModels, want) && (len(got.AllowedModels) != 0 || len(want) != 0) {
		t.Fatalf("allowed_models = %v, want %v", got.AllowedModels, want)
	}
}
