package serve

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestCompactAtEndpoint(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	srv, _, cancel := newTestServer(t)
	defer cancel()
	defer srv.Close()

	floor := core.DefaultCompactionSettings.MinCompactAt()

	// Nothing configured means automatic: compaction waits for the model window.
	assertCompactAt(t, apiReq(t, srv, "GET", "/api/compact-at", ""), 0, floor)

	assertCompactAt(t, apiReq(t, srv, "PATCH", "/api/compact-at", `{"compact_at":120000}`), 120_000, floor)
	assertCompactAt(t, apiReq(t, srv, "GET", "/api/compact-at", ""), 120_000, floor)
	if got := core.LoadGlobalConfig().CompactAt; got != 120_000 {
		t.Fatalf("persisted compact_at = %d, want 120000", got)
	}

	// Below the floor the engine raises the threshold anyway, so the stored and
	// returned value is the floor — the client must not display a number the
	// engine will not honor.
	assertCompactAt(t, apiReq(t, srv, "PATCH", "/api/compact-at", `{"compact_at":1000}`), floor, floor)
	if got := core.LoadGlobalConfig().CompactAt; got != floor {
		t.Fatalf("persisted compact_at = %d, want the floor %d", got, floor)
	}

	// 0 restores automatic.
	assertCompactAt(t, apiReq(t, srv, "PATCH", "/api/compact-at", `{"compact_at":0}`), 0, floor)
	if got := core.LoadGlobalConfig().CompactAt; got != 0 {
		t.Fatalf("persisted compact_at = %d, want 0", got)
	}
}

func TestCompactAtEndpointValidation(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	srv, _, cancel := newTestServer(t)
	defer cancel()
	defer srv.Close()

	for _, body := range []string{`{`, `{}`, `{"compact_at":-1}`} {
		resp := apiReq(t, srv, "PATCH", "/api/compact-at", body)
		resp.Body.Close() //nolint:errcheck // test cleanup
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("PATCH body %s: status = %d, want 400", body, resp.StatusCode)
		}
	}
	if got := core.LoadGlobalConfig().CompactAt; got != 0 {
		t.Fatalf("a rejected request still wrote %d", got)
	}
}

func assertCompactAt(t *testing.T, resp *http.Response, want, wantMin int) {
	t.Helper()
	defer resp.Body.Close() //nolint:errcheck // test cleanup
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got compactAtPolicy
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.CompactAt != want {
		t.Fatalf("compact_at = %d, want %d", got.CompactAt, want)
	}
	if got.Min != wantMin {
		t.Fatalf("compact_at_min = %d, want %d", got.Min, wantMin)
	}
}
