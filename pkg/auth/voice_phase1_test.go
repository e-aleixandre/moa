package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func voicePhase1Key(provider string) string {
	return "offline-" + provider + "-voice-api-key-0123456789"
}

func assertVoicePhase1KeyPersisted(t *testing.T, path, key string) {
	t.Helper()
	reopened := NewStore(path)
	if err := reopened.LoadError(); err != nil {
		t.Fatalf("reopen credential store: %v", err)
	}
	matches := 0
	// The secondary record name is intentionally not part of this contract.
	for slot, cred := range readAuthFile(t, path) {
		if cred.Type != "api_key" || cred.Key != key {
			continue
		}
		matches++
		if got, ok := reopened.Get(slot); !ok || !sameCredential(got, cred) || got.Generation == "" {
			t.Error("saved API key is not readable with its durable identity from a fresh Store")
		}
	}
	if matches != 1 {
		t.Errorf("durable API-key records matching the connected key = %d, want exactly one; connecting the plan must not erase the key", matches)
	}
}

func assertVoicePhase1TextSelection(t *testing.T, store *Store, provider string, want Credential) {
	t.Helper()
	wantToken := want.Key
	if want.Type == "oauth" {
		wantToken = want.Access
	}
	snap, err := store.ResolveSnapshot(context.Background(), provider)
	if err != nil {
		t.Errorf("ordinary text resolution failed: %v", err)
	} else if snap.Source != core.CredentialSourceStore || snap.Kind != want.Type || snap.Token != wantToken || snap.Generation != want.Generation || snap.AccountID != want.AccountID {
		t.Errorf("ordinary text selection changed: source=%q kind=%q generation_preserved=%v token_preserved=%v account_preserved=%v; want the stored %s credential", snap.Source, snap.Kind, snap.Generation == want.Generation, snap.Token == wantToken, snap.AccountID == want.AccountID, want.Type)
	}
	key, oauth, err := store.GetAPIKey(provider)
	if err != nil || key != wantToken || oauth != (want.Type == "oauth") {
		t.Errorf("legacy GetAPIKey changed text selection: oauth=%v token_preserved=%v error=%v", oauth, key == wantToken, err)
	}
	if kind := store.CredentialKind(provider); kind != want.Type {
		t.Errorf("text transport credential kind = %q, want %q", kind, want.Type)
	}
}

func TestVoiceCredentials_SaveAPIKeyKeepsOAuthPlan(t *testing.T) {
	for _, provider := range []string{"openai", "xai", "anthropic"} {
		t.Run(provider, func(t *testing.T) {
			f := newLoginFixture(t)
			plan := Credential{Type: "oauth", Access: "offline-" + provider + "-plan-access", Refresh: "offline-" + provider + "-plan-refresh", Expires: fresh()}
			if provider == "openai" {
				plan.Access, plan.AccountID = testOpenAIJWT("offline-plan-account", 0), "offline-plan-account"
			}
			gen, err := f.store.CommitLogin(provider, f.gen(t, provider), plan)
			if err != nil {
				t.Fatal(err)
			}
			plan.Generation = gen
			assertVoicePhase1TextSelection(t, NewStore(f.path), provider, plan)
			key := voicePhase1Key(provider)
			keyGen, err := f.m.SaveAPIKey(provider, gen, " \t"+key+"\n")
			if err != nil || keyGen == "" {
				t.Fatalf("real SaveAPIKey rejected a syntactically valid fake key: generation_present=%v error=%v", keyGen != "", err)
			}
			if got := readAuthFile(t, f.path)[provider]; !sameCredential(got, plan) {
				t.Errorf("SaveAPIKey replaced the OAuth plan: primary kind=%q, want the unchanged OAuth access/refresh/account/expiry/generation", got.Type)
			}
			assertVoicePhase1KeyPersisted(t, f.path, key)
			assertVoicePhase1TextSelection(t, NewStore(f.path), provider, plan)
		})
	}
}

func TestVoiceCredentials_OAuthLoginKeepsExistingAPIKey(t *testing.T) {
	for _, provider := range []string{"openai", "xai", "anthropic"} {
		t.Run(provider, func(t *testing.T) {
			var f *loginFixture
			var device *xaiFixture
			if provider == "xai" {
				device = newXAIFixture(t, nil)
				f = device.loginFixture
				device.srv.script["DEVICE-CODE-SENTINEL-1"] = []string{"success"}
			} else {
				f = newLoginFixture(t)
			}
			key := voicePhase1Key(provider)
			keyGen, err := f.m.SaveAPIKey(provider, f.gen(t, provider), key)
			if err != nil {
				t.Fatal(err)
			}
			assertVoicePhase1TextSelection(t, NewStore(f.path), provider, Credential{Type: "api_key", Key: key, Generation: keyGen})
			assertVoicePhase1KeyPersisted(t, f.path, key)
			view := f.begin(t, provider, browserA)
			if device != nil {
				device.nextWait(t)
				device.release(t)
				device.waitSettled(t, "xai:saved")
				if calls := device.srv.pollCount("DEVICE-CODE-SENTINEL-1"); calls != 1 {
					t.Fatalf("local device token requests = %d, want one real exchange", calls)
				}
			} else {
				if err := f.m.Complete(context.Background(), provider, view.AttemptID, browserA, pasteFor(t, view)); err != nil {
					t.Fatalf("complete login through local token endpoint: %v", err)
				}
				if calls := f.ts.callCount(); calls != 1 || f.ts.lastRequest()["code"] != codeSentinel {
					t.Fatalf("local code token requests = %d, want one exchange of the fixture code", calls)
				}
			}
			progress, err := f.m.Progress(provider, view.AttemptID, browserA)
			if err != nil || progress.State != attemptSaved {
				t.Fatalf("OAuth login was not saved: state=%q error=%v", progress.State, err)
			}
			plan := readAuthFile(t, f.path)[provider]
			if plan.Type != "oauth" || plan.Access == "" || plan.Refresh != refreshSentinel || plan.Generation == "" || plan.Generation == keyGen {
				t.Fatal("local OAuth exchange did not durably establish the new plan credential")
			}
			if provider == "openai" && plan.AccountID != "acct-new" {
				t.Errorf("plan account = %q, want the local token endpoint's account", plan.AccountID)
			}
			assertVoicePhase1TextSelection(t, NewStore(f.path), provider, plan)
			assertVoicePhase1KeyPersisted(t, f.path, key)
		})
	}
}

func TestVoiceCredentials_LegacyTextEnvironmentPrecedence(t *testing.T) {
	for _, tt := range []struct {
		name, provider, env, kind, account string
	}{
		{name: "openai_api_key", provider: "openai", env: "offline-openai-environment-api-key", kind: "api_key"},
		{name: "openai_oauth_account", provider: "openai", env: testOpenAIJWT("environment-account", 1), kind: "oauth", account: "environment-account"},
		{name: "xai_api_key", provider: "xai", env: "offline-xai-environment-api-key", kind: "api_key"},
		{name: "xai_jwt_stays_api_key", provider: "xai", env: testOpenAIJWT("not-an-xai-account", 1), kind: "api_key"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clearProviderEnv(t)
			path := filepath.Join(t.TempDir(), "auth.json")
			plan := Credential{Type: "oauth", Access: "offline-legacy-plan-access", Refresh: "offline-legacy-plan-refresh", Expires: fresh()}
			if tt.provider == "openai" {
				plan.Access, plan.AccountID = testOpenAIJWT("stored-account", 0), "stored-account"
			}
			// No generation: this is a legacy auth.json, not a new-format fixture.
			seedAuthFile(t, path, map[string]Credential{tt.provider: plan})
			before := fileBytes(t, path)
			assertVoicePhase1TextSelection(t, NewStore(path), tt.provider, plan)
			t.Setenv(envKeyForProvider(tt.provider), tt.env)
			store := NewStore(path)
			snap, err := store.ResolveSnapshot(context.Background(), tt.provider)
			if err != nil || snap.Source != core.CredentialSourceEnv || snap.Kind != tt.kind || snap.Token != tt.env || snap.AccountID != tt.account || snap.Generation != "" {
				t.Errorf("legacy environment selection changed: source=%q kind=%q account=%q token_preserved=%v generation_empty=%v error=%v", snap.Source, snap.Kind, snap.AccountID, snap.Token == tt.env, snap.Generation == "", err)
			}
			key, oauth, err := store.GetAPIKey(tt.provider)
			if err != nil || key != tt.env || oauth != (tt.kind == "oauth") || store.GetAccountID(tt.provider) != tt.account {
				t.Errorf("legacy text credential helpers changed environment precedence: oauth=%v token_preserved=%v error=%v", oauth, key == tt.env, err)
			}
			// CredentialKind's legacy environment shortcut always returns
			// api_key, even when ResolveSnapshot recognises OpenAI OAuth.
			if kind := store.CredentialKind(tt.provider); kind != "api_key" {
				t.Errorf("legacy CredentialKind environment shortcut = %q, want api_key", kind)
			}
			t.Setenv(envKeyForProvider(tt.provider), "")
			assertVoicePhase1TextSelection(t, NewStore(path), tt.provider, plan)
			if !bytes.Equal(fileBytes(t, path), before) {
				t.Error("reading legacy text credentials rewrote auth.json")
			}
		})
	}
}

func TestVoiceCredentials_LegacyKeyOnlyAndUnknownRecordsRemainCompatible(t *testing.T) {
	for _, provider := range []string{"openai", "xai"} {
		t.Run(provider, func(t *testing.T) {
			clearProviderEnv(t)
			path := filepath.Join(t.TempDir(), "auth.json")
			const oldKey = "offline-legacy-text-api-key-0123456789"
			writeAuthFile(t, path, fmt.Sprintf(`{
				%q: {"type":"api_key","key":%q},
				"unrelated-future-provider": {"type":"future","opaque":{"nested":["keep",7]}}
			}`, provider, oldKey))
			var before map[string]json.RawMessage
			if err := json.Unmarshal(fileBytes(t, path), &before); err != nil {
				t.Fatal(err)
			}
			store := NewStore(path)
			assertVoicePhase1TextSelection(t, store, provider, Credential{Type: "api_key", Key: oldKey})
			manager := NewProviderLoginManager(context.Background(), store)
			t.Cleanup(manager.Close)
			key := voicePhase1Key(provider)
			gen, err := manager.SaveAPIKey(provider, "", key)
			if err != nil || gen == "" {
				t.Fatalf("legacy key-only reconnect failed: generation_present=%v error=%v", gen != "", err)
			}
			assertVoicePhase1TextSelection(t, NewStore(path), provider, Credential{Type: "api_key", Key: key, Generation: gen})
			assertVoicePhase1KeyPersisted(t, path, key)
			var after map[string]json.RawMessage
			if err := json.Unmarshal(fileBytes(t, path), &after); err != nil {
				t.Fatal(err)
			}
			var wantUnknown, gotUnknown any
			if err := json.Unmarshal(before["unrelated-future-provider"], &wantUnknown); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(after["unrelated-future-provider"], &gotUnknown); err != nil {
				t.Fatal(err)
			}
			wantJSON, err := json.Marshal(wantUnknown)
			if err != nil {
				t.Fatal(err)
			}
			gotJSON, err := json.Marshal(gotUnknown)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(gotJSON, wantJSON) {
				t.Error("saving the credential rewrote or removed an unrelated future auth.json record")
			}
		})
	}
}

type voicePhase1SecondaryFixture struct {
	path, slot               string
	plan, secondary, unknown Credential
}

func newVoicePhase1SecondaryFixture(t *testing.T, provider string) voicePhase1SecondaryFixture {
	t.Helper()
	clearProviderEnv(t)
	// These names are the agreed F1 codec schema, not public model providers.
	slot := map[string]string{"openai": "__voice_openai_api_v1", "xai": "__voice_xai_api_v1"}[provider]
	path := filepath.Join(t.TempDir(), "auth.json")
	store := NewStore(path)
	var unknown Credential
	if err := json.Unmarshal([]byte(`{"type":"future","opaque":{"nested":["keep",7]}}`), &unknown); err != nil {
		t.Fatal(err)
	}
	if err := store.Set("unrelated-future-provider", unknown); err != nil {
		t.Fatal(err)
	}
	unknown = readAuthFile(t, path)["unrelated-future-provider"]
	plan := Credential{Type: "oauth", Access: "offline-secondary-fixture-plan", Refresh: "offline-secondary-fixture-refresh", Expires: fresh()}
	if provider == "openai" {
		plan.Access, plan.AccountID = testOpenAIJWT("stored-plan-account", 0), "stored-plan-account"
	}
	if err := store.Set(provider, plan); err != nil {
		t.Fatal(err)
	}
	plan = readAuthFile(t, path)[provider]
	if err := store.Set(slot, Credential{Type: "api_key", Key: voicePhase1Key(provider)}); err != nil {
		t.Fatal(err)
	}
	secondary := readAuthFile(t, path)[slot]
	if slot == "" || plan.Generation == "" || secondary.Generation == "" || unknown.Generation == "" {
		t.Fatal("Store.Set did not establish the fixture's durable credential identities")
	}
	return voicePhase1SecondaryFixture{path: path, slot: slot, plan: plan, secondary: secondary, unknown: unknown}
}

func assertVoicePhase1UnknownRecordPreserved(t *testing.T, fixture voicePhase1SecondaryFixture) {
	t.Helper()
	got, ok := NewStore(fixture.path).Get("unrelated-future-provider")
	if !ok {
		t.Fatal("unrelated future-provider record disappeared from the reopened Store")
	}
	wantJSON, err := json.Marshal(fixture.unknown)
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Error("credential mutation changed the unrelated record's generation or unknown fields")
	}
}

func TestVoiceCredentials_SecondaryKeyServesTextOnlyAfterOAuthRemoval(t *testing.T) {
	for _, provider := range []string{"openai", "xai"} {
		t.Run(provider, func(t *testing.T) {
			fixture := newVoicePhase1SecondaryFixture(t, provider)
			assertVoicePhase1TextSelection(t, NewStore(fixture.path), provider, fixture.plan)
			assertVoicePhase1KeyPersisted(t, fixture.path, fixture.secondary.Key)
			jwtKind, jwtAccount := "api_key", ""
			if provider == "openai" {
				jwtKind, jwtAccount = "oauth", "environment-account"
			}
			for _, env := range []struct{ token, kind, account string }{
				{token: "offline-" + provider + "-environment-api-key", kind: "api_key"},
				{token: testOpenAIJWT("environment-account", 1), kind: jwtKind, account: jwtAccount},
			} {
				t.Setenv(envKeyForProvider(provider), env.token)
				store := NewStore(fixture.path)
				snap, err := store.ResolveSnapshot(context.Background(), provider)
				if err != nil || snap.Source != core.CredentialSourceEnv || snap.Kind != env.kind || snap.Token != env.token || snap.AccountID != env.account || snap.Generation != "" {
					t.Errorf("secondary key changed environment precedence: source=%q kind=%q token_preserved=%v account_preserved=%v generation_empty=%v error=%v", snap.Source, snap.Kind, snap.Token == env.token, snap.AccountID == env.account, snap.Generation == "", err)
				}
				key, oauth, err := store.GetAPIKey(provider)
				if err != nil || key != env.token || oauth != (env.kind == "oauth") {
					t.Errorf("secondary key changed legacy environment selection: oauth=%v token_preserved=%v error=%v", oauth, key == env.token, err)
				}
			}
			t.Setenv(envKeyForProvider(provider), "")
			assertVoicePhase1TextSelection(t, NewStore(fixture.path), provider, fixture.plan)
			if err := NewStore(fixture.path).Remove(provider); err != nil {
				t.Fatal(err)
			}
			// Without the plan the key, with its generation, is the only
			// credential, so text now selects it.
			reopened := NewStore(fixture.path)
			if _, ok := reopened.Get(fixture.slot); ok {
				t.Error("the key stayed beside a removed plan")
			}
			assertVoicePhase1TextSelection(t, reopened, provider, fixture.secondary)
			assertVoicePhase1KeyPersisted(t, fixture.path, fixture.secondary.Key)
			assertVoicePhase1UnknownRecordPreserved(t, fixture)
		})
	}
}

func TestVoiceCredentials_SecondaryKeyRemovalPreservesOAuth(t *testing.T) {
	for _, provider := range []string{"openai", "xai"} {
		t.Run(provider, func(t *testing.T) {
			fixture := newVoicePhase1SecondaryFixture(t, provider)
			assertVoicePhase1TextSelection(t, NewStore(fixture.path), provider, fixture.plan)
			assertVoicePhase1KeyPersisted(t, fixture.path, fixture.secondary.Key)
			if err := NewStore(fixture.path).Remove(fixture.slot); err != nil {
				t.Fatal(err)
			}
			reopened := NewStore(fixture.path)
			if _, ok := reopened.Get(fixture.slot); ok {
				t.Error("removed secondary key survived reopening")
			}
			if got, ok := reopened.Get(provider); !ok || !sameCredential(got, fixture.plan) {
				t.Error("removing the secondary key changed or deleted the OAuth plan")
			}
			assertVoicePhase1TextSelection(t, reopened, provider, fixture.plan)
			assertVoicePhase1UnknownRecordPreserved(t, fixture)
		})
	}
}
