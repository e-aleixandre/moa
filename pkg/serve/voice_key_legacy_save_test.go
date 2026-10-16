package serve

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/e-aleixandre/moa/pkg/auth"
)

// An already-loaded old page sends no expected_key_generation. Its save must
// still lose to a key saved after it loaded (409, winner intact).
func TestVoiceKeyLegacySaveCannotOverwriteNewKey(t *testing.T) {
	for _, provider := range []string{"openai", "xai"} {
		t.Run(provider, func(t *testing.T) {
			f := newProviderFixture(t, true)
			_, slot, seen := vpSeedPlanAndKey(t, f, provider)
			planGen := f.generation(provider)
			current := f.do(http.MethodPost, "/api/providers/"+provider+"/api-key",
				fmt.Sprintf(`{"key":%q,"expected_generation":%q,"expected_key_generation":%q}`, "sk-newer-KEY-SENTINEL-0123456789", planGen, seen), withCookie(ownerCookie))
			if current.Code != http.StatusOK {
				t.Fatalf("current-page save = %d", current.Code)
			}
			winner, _ := auth.NewStore(f.path).Get(slot)
			stale := f.do(http.MethodPost, "/api/providers/"+provider+"/api-key",
				fmt.Sprintf(`{"key":%q,"expected_generation":%q}`, "sk-stale-KEY-SENTINEL-0123456789", planGen), withCookie(ownerCookie))
			got, _ := auth.NewStore(f.path).Get(slot)
			if stale.Code != http.StatusConflict || got.Key != winner.Key || got.Generation != winner.Generation {
				t.Errorf("legacy save = %d, winner preserved = %v", stale.Code, got.Key == winner.Key)
			}
		})
	}
}

// Without a plan the primary-generation CAS already covers a legacy save.
func TestVoiceKeyLegacySaveWithoutPlanUsesPrimaryCAS(t *testing.T) {
	for _, provider := range []string{"openai", "xai"} {
		t.Run(provider, func(t *testing.T) {
			f := newProviderFixture(t, true)
			f.seed(provider, auth.Credential{Type: "api_key", Key: "sk-first-KEY-SENTINEL-0123456789"})
			oldGen := f.generation(provider)
			save := func(key, gen string) *httptest.ResponseRecorder {
				return f.do(http.MethodPost, "/api/providers/"+provider+"/api-key",
					fmt.Sprintf(`{"key":%q,"expected_generation":%q}`, key, gen), withCookie(ownerCookie))
			}
			if rec := save("sk-second-KEY-SENTINEL-0123456789", oldGen); rec.Code != http.StatusOK {
				t.Fatalf("first save = %d", rec.Code)
			}
			winner, _ := auth.NewStore(f.path).Get(provider)
			if rec := save("sk-stale-KEY-SENTINEL-0123456789", oldGen); rec.Code != http.StatusConflict {
				t.Fatalf("stale save = %d, want 409", rec.Code)
			}
			if got, _ := auth.NewStore(f.path).Get(provider); got.Key != winner.Key {
				t.Error("winner overwritten")
			}
		})
	}
}
