package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/auth"
)

func vpSeedPlanAndKey(t *testing.T, f *providerFixture, provider string) (auth.CredentialSnapshot, string, string) {
	t.Helper()
	plan := auth.Credential{Type: "oauth", Access: "VOICE-PLAN-SENTINEL", Refresh: "VOICE-REFRESH-SENTINEL", Expires: time.Now().Add(time.Hour).UnixMilli()}
	if provider == "openai" {
		plan.Access = providerTestJWT("voice-account", "VOICE-PLAN-SENTINEL")
		plan.AccountID = "voice-account"
	}
	f.seed(provider, plan)
	// Seed the additive format independently of SaveAPIKey so a destructive
	// save cannot prevent the removal/CAS tests from reaching their own route.
	slot := "__voice_" + provider + "_api_v1"
	keyGen := f.seed(slot, auth.Credential{Type: "api_key", Key: "VOICE-KEY-SENTINEL"})
	snap, err := f.store.ResolveSnapshot(context.Background(), provider)
	if err != nil || snap.Kind != "oauth" || snap.Token != plan.Access {
		t.Fatalf("seeded plan was not selected for text: kind=%q error=%v", snap.Kind, err)
	}
	if rec := f.do(http.MethodGet, "/api/providers/status", "", withCookie(ownerCookie)); rec.Code != http.StatusOK {
		t.Fatalf("authenticated Providers control = %d, want 200", rec.Code)
	}
	return snap, slot, keyGen
}

func TestVoiceCredentials_RemoveAPIKeyPreservesOAuth(t *testing.T) {
	for _, provider := range []string{"openai", "xai"} {
		t.Run(provider, func(t *testing.T) {
			f := newProviderFixture(t, true)
			before, slot, keyGen := vpSeedPlanAndKey(t, f, provider)
			rec := f.do(http.MethodPost, "/api/providers/"+provider+"/api-key/remove", beginBody(keyGen), withCookie(ownerCookie))
			if rec.Code != http.StatusOK {
				t.Fatalf("remove only the voice API key = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			reopened := auth.NewStore(f.path)
			after, err := reopened.ResolveSnapshot(context.Background(), provider)
			if err != nil || after.Kind != "oauth" || after.Token != before.Token || after.AccountID != before.AccountID || after.Generation != before.Generation || after.Credential.Refresh != before.Credential.Refresh {
				t.Errorf("removing the key altered the OAuth/text credential: kind=%q error=%v", after.Kind, err)
			}
			if _, exists := reopened.Get(slot); exists {
				t.Error("removed secondary key still exists after reopening the real Store")
			}
			if f.up.callCount() != 0 {
				t.Error("removing a key refreshed or called a provider")
			}
		})
	}
}

func TestVoiceCredentials_StaleKeyRemovalPreservesBothCredentials(t *testing.T) {
	for _, provider := range []string{"openai", "xai"} {
		t.Run(provider, func(t *testing.T) {
			f := newProviderFixture(t, true)
			_, slot, oldGen := vpSeedPlanAndKey(t, f, provider)
			if _, err := f.store.CommitLogin(slot, oldGen, auth.Credential{Type: "api_key", Key: "VOICE-NEW-KEY-SENTINEL"}); err != nil {
				t.Fatal(err)
			}
			before := f.fileBytes()
			rec := f.do(http.MethodPost, "/api/providers/"+provider+"/api-key/remove", fmt.Sprintf(`{"expected_generation":%q}`, oldGen), withCookie(ownerCookie))
			if rec.Code != http.StatusConflict {
				t.Errorf("stale secondary generation = %d, want 409: %s", rec.Code, rec.Body.String())
			}
			if !bytes.Equal(before, f.fileBytes()) {
				t.Error("stale removal rewrote or removed a credential")
			}
			if f.up.callCount() != 0 {
				t.Error("stale removal called a provider")
			}
		})
	}
}

func TestVoiceRelay_Second401StopsAfterOneStoreRefresh(t *testing.T) {
	for _, provider := range []string{"grok-plan", "chatgpt-plan"} {
		t.Run(provider, func(t *testing.T) {
			f := vpNewFixture(t, provider)
			f.up.mu.Lock()
			f.up.reject = http.StatusUnauthorized
			f.up.mu.Unlock()
			raw := f.mustRequest(http.MethodPost, "/api/voice/calls", map[string]any{"session_id": f.sess.ID, "provider": provider}, f.owner(), http.StatusServiceUnavailable)
			var failure struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(raw, &failure); err != nil || failure.Error != "plan_unavailable" {
				t.Fatalf("second 401 was not a classified plan failure: %s, error=%v", raw, err)
			}
			requests, peers, refreshes := f.up.snapshot()
			attempts := 0
			for _, request := range requests {
				if (request.origin == "api.x.ai" && request.path == "/v1/realtime") || (request.origin == "chatgpt.com" && request.path == "/backend-api/wham/realtime/calls") {
					attempts++
				}
			}
			if attempts != 2 || refreshes != 1 || len(peers) != 0 {
				t.Fatalf("rejected OAuth attempts=%d rotations=%d open peers=%d, want 2/1/0", attempts, refreshes, len(peers))
			}
		})
	}
}

func TestVoiceCall_PaidCredentialDoesNotAuthorizeImplicitCall(t *testing.T) {
	for _, provider := range []string{"xai-api", "openai-live-api"} {
		t.Run(provider, func(t *testing.T) {
			f := vpNewFixture(t, provider)
			raw := f.mustRequest(http.MethodPost, "/api/voice/calls", map[string]any{"session_id": f.sess.ID}, f.owner(), http.StatusServiceUnavailable)
			var failure struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(raw, &failure); err != nil || failure.Error != "plan_unavailable" {
				t.Fatalf("implicit paid call did not fail closed: %s, error=%v", raw, err)
			}
			requests, peers, refreshes := f.up.snapshot()
			if len(requests) != 0 || len(peers) != 0 || refreshes != 0 {
				t.Fatalf("missing per-call consent opened upstream: requests=%d peers=%d rotations=%d", len(requests), len(peers), refreshes)
			}
		})
	}
}
