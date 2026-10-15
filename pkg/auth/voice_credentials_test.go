package auth

import (
	"bytes"
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// voiceKeyFixture is a real Store with a plan sign-in for provider and a
// login manager to save keys through the production path.
type voiceKeyFixture struct {
	path  string
	store *Store
	m     *ProviderLoginManager
	plan  Credential
}

func newVoiceKeyFixture(t *testing.T, provider string, ts *rotatingTokenServer) *voiceKeyFixture {
	t.Helper()
	clearProviderEnv(t)
	f := &voiceKeyFixture{path: filepath.Join(t.TempDir(), "auth.json")}
	f.store = NewStore(f.path)
	if ts != nil {
		f.store.refresh = ts.endpoints().refresh
	}
	f.m = NewProviderLoginManager(context.Background(), f.store)
	t.Cleanup(f.m.Close)
	plan := Credential{Type: "oauth", Access: "offline-voice-plan-access", Refresh: "refresh-0", Expires: fresh()}
	if provider == "openai" {
		plan.Access, plan.AccountID = testOpenAIJWT("acct", 0), "acct"
	}
	gen, err := f.store.CommitLogin(provider, "", plan)
	if err != nil {
		t.Fatal(err)
	}
	plan.Generation = gen
	f.plan = plan
	return f
}

func (f *voiceKeyFixture) saveKey(t *testing.T, provider, key string) string {
	t.Helper()
	gen, err := f.m.SaveAPIKey(provider, f.plan.Generation, key)
	if err != nil {
		t.Fatalf("SaveAPIKey: %v", err)
	}
	return gen
}

func TestVoiceKey_RemovingEitherCredentialKeepsTheOther(t *testing.T) {
	for _, provider := range []string{"openai", "xai"} {
		t.Run(provider, func(t *testing.T) {
			f := newVoiceKeyFixture(t, provider, nil)
			key := voicePhase1Key(provider)
			keyGen := f.saveKey(t, provider, key)

			if err := NewStore(f.path).RemoveProviderAPIKey(provider, keyGen); err != nil {
				t.Fatalf("remove key: %v", err)
			}
			store := NewStore(f.path)
			if got := readAuthFile(t, f.path)[provider]; !sameCredential(got, f.plan) {
				t.Error("removing the key changed the plan sign-in")
			}
			if _, err := store.ResolveSnapshot(context.Background(), provider, SelectVoiceAPIKey); err == nil {
				t.Error("voice API key still selected after removal")
			} else {
				wantCredClass(t, err, core.CredentialMissing)
			}
			if snap, err := store.ResolveSnapshot(context.Background(), provider, SelectPlanOAuth); err != nil || snap.Generation != f.plan.Generation {
				t.Errorf("plan not selectable after key removal: %v", err)
			}

			keyGen = f.saveKey(t, provider, key)
			if err := NewStore(f.path).Remove(provider); err != nil {
				t.Fatal(err)
			}
			store = NewStore(f.path)
			snap, err := store.ResolveSnapshot(context.Background(), provider, SelectVoiceAPIKey)
			if err != nil || snap.Kind != "api_key" || snap.Token != key || snap.Generation != keyGen || snap.Source != core.CredentialSourceStore {
				t.Errorf("key not kept after removing the plan: kind=%q gen_ok=%v err=%v", snap.Kind, snap.Generation == keyGen, err)
			}
			_, err = store.ResolveSnapshot(context.Background(), provider, SelectPlanOAuth)
			wantCredClass(t, err, core.CredentialMissing)
			// Without the plan the kept key is the only key, so text uses it.
			assertVoicePhase1TextSelection(t, store, provider, Credential{Type: "api_key", Key: key, Generation: keyGen})
		})
	}
}

func TestVoiceKey_StaleGenerationNeverRemovesNewerKeyAcrossStores(t *testing.T) {
	for _, provider := range []string{"openai", "xai"} {
		t.Run(provider, func(t *testing.T) {
			f := newVoiceKeyFixture(t, provider, nil)
			oldGen := f.saveKey(t, provider, voicePhase1Key(provider))

			other := NewStore(f.path)
			otherManager := NewProviderLoginManager(context.Background(), other)
			t.Cleanup(otherManager.Close)
			const newer = "offline-newer-voice-api-key-0123456789"
			newGen, err := otherManager.SaveAPIKey(provider, f.plan.Generation, newer)
			if err != nil {
				t.Fatal(err)
			}
			before := fileBytes(t, f.path)
			err = f.store.RemoveProviderAPIKey(provider, oldGen)
			wantCredClass(t, err, core.CredentialChanged)
			if !bytes.Equal(before, fileBytes(t, f.path)) {
				t.Error("stale removal rewrote auth.json")
			}
			snap, err := f.store.ResolveSnapshot(context.Background(), provider, SelectVoiceAPIKey)
			if err != nil || snap.Token != newer || snap.Generation != newGen {
				t.Errorf("newer key lost: err=%v", err)
			}
			if got := readAuthFile(t, f.path)[provider]; !sameCredential(got, f.plan) {
				t.Error("key replacement changed the plan sign-in")
			}
			// A save against a stale primary generation is refused too.
			if _, err := f.m.SaveAPIKey(provider, "stale-primary", newer); err == nil {
				t.Error("SaveAPIKey accepted a stale primary generation")
			}
		})
	}
}

func TestVoiceKey_PrimaryKeyRemovedWhenOnlyKey(t *testing.T) {
	clearProviderEnv(t)
	path := filepath.Join(t.TempDir(), "auth.json")
	store := NewStore(path)
	gen, err := store.CommitLogin("xai", "", Credential{Type: "api_key", Key: voicePhase1Key("xai")})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := store.ResolveSnapshot(context.Background(), "xai", SelectVoiceAPIKey)
	if err != nil || snap.Generation != gen {
		t.Fatalf("legacy primary key not selectable for voice: %v", err)
	}
	if err := store.RemoveProviderAPIKey("xai", "wrong"); err == nil {
		t.Fatal("stale generation removed the primary key")
	}
	if err := store.RemoveProviderAPIKey("xai", gen); err != nil {
		t.Fatal(err)
	}
	if _, ok := NewStore(path).Get("xai"); ok {
		t.Error("primary key survived removal")
	}
}

func TestVoiceKey_KeySaveAfterPlanRemovalReplacesTheKeptKey(t *testing.T) {
	f := newVoiceKeyFixture(t, "openai", nil)
	kept := f.saveKey(t, "openai", voicePhase1Key("openai"))
	if err := f.store.Remove("openai"); err != nil {
		t.Fatal(err)
	}
	// The kept key is now the primary: a page that saw no key is stale.
	const replacement = "offline-replacement-api-key-0123456789"
	_, err := f.m.SaveAPIKey("openai", "", replacement)
	wantCredClass(t, err, core.CredentialChanged)
	gen, err := f.m.SaveAPIKey("openai", kept, replacement)
	if err != nil {
		t.Fatal(err)
	}
	if keys := apiKeyRecords(t, f.path); len(keys) != 1 {
		t.Errorf("API key records = %d, want the replacement only", len(keys))
	}
	assertVoicePhase1TextSelection(t, NewStore(f.path), "openai", Credential{Type: "api_key", Key: replacement, Generation: gen})
	snap, err := NewStore(f.path).ResolveSnapshot(context.Background(), "openai", SelectVoiceAPIKey)
	if err != nil || snap.Token != replacement {
		t.Errorf("voice did not select the only key: %v", err)
	}
}

func TestVoiceKey_PlanLoginKeepsTheMostRecentOfTwoKeys(t *testing.T) {
	clearProviderEnv(t)
	path := filepath.Join(t.TempDir(), "auth.json")
	store := NewStore(path)
	if err := store.Set("__voice_xai_api_v1", Credential{Type: "api_key", Key: "offline-existing-secondary-key-0123"}); err != nil {
		t.Fatal(err)
	}
	gen, err := store.CommitLogin("xai", "", Credential{Type: "api_key", Key: "offline-primary-key-0123456789"})
	if err != nil {
		t.Fatal(err)
	}
	// Re-seed the older key beside it as a writer that set it directly.
	if err := store.Set("__voice_xai_api_v1", Credential{Type: "api_key", Key: "offline-existing-secondary-key-0123"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitLogin("xai", gen, Credential{Type: "oauth", Access: "a", Refresh: "r", Expires: fresh()}); err != nil {
		t.Fatal(err)
	}
	if keys := apiKeyRecords(t, path); len(keys) != 1 || keys["__voice_xai_api_v1"].Key != "offline-primary-key-0123456789" {
		t.Errorf("plan login kept %d keys or not the primary one", len(keys))
	}
}

func TestVoiceSelection_PlanAndKeyDespiteEnvironment(t *testing.T) {
	for _, provider := range []string{"openai", "xai"} {
		t.Run(provider, func(t *testing.T) {
			f := newVoiceKeyFixture(t, provider, nil)
			keyGen := f.saveKey(t, provider, voicePhase1Key(provider))
			const envKey = "offline-environment-api-key-0123456789"
			t.Setenv(envKeyForProvider(provider), envKey)
			store := NewStore(f.path)
			before := fileBytes(t, f.path)

			text, err := store.ResolveSnapshot(context.Background(), provider)
			if err != nil || text.Source != core.CredentialSourceEnv || text.Token != envKey {
				t.Errorf("text selection changed with environment set: source=%q err=%v", text.Source, err)
			}
			for name, resolve := range map[string]func(SnapshotSelection) (CredentialSnapshot, error){
				"resolve": func(sel SnapshotSelection) (CredentialSnapshot, error) {
					return store.ResolveSnapshot(context.Background(), provider, sel)
				},
				"peek": func(sel SnapshotSelection) (CredentialSnapshot, error) { return store.PeekSnapshot(provider, sel) },
			} {
				plan, err := resolve(SelectPlanOAuth)
				if err != nil || plan.Source != core.CredentialSourceStore || plan.Kind != "oauth" || plan.Token != f.plan.Access || plan.Generation != f.plan.Generation || plan.AccountID != f.plan.AccountID {
					t.Errorf("%s: plan not selected despite env key: source=%q kind=%q err=%v", name, plan.Source, plan.Kind, err)
				}
				key, err := resolve(SelectVoiceAPIKey)
				if err != nil || key.Source != core.CredentialSourceEnv || key.Token != envKey {
					t.Errorf("%s: voice key ignores the environment key policy: source=%q err=%v", name, key.Source, err)
				}
			}
			t.Setenv(envKeyForProvider(provider), "")
			key, err := store.ResolveSnapshot(context.Background(), provider, SelectVoiceAPIKey)
			if err != nil || key.Source != core.CredentialSourceStore || key.Kind != "api_key" || key.Generation != keyGen {
				t.Errorf("stored voice key not selected: err=%v", err)
			}
			if !bytes.Equal(before, fileBytes(t, f.path)) {
				t.Error("resolving selections rewrote auth.json")
			}
		})
	}
}

func TestVoiceSelection_OpenAIEnvironmentOAuthIsNotAVoiceKey(t *testing.T) {
	f := newVoiceKeyFixture(t, "openai", nil)
	keyGen := f.saveKey(t, "openai", voicePhase1Key("openai"))
	t.Setenv("OPENAI_API_KEY", testOpenAIJWT("environment-account", 1))
	snap, err := NewStore(f.path).ResolveSnapshot(context.Background(), "openai", SelectVoiceAPIKey)
	if err != nil || snap.Kind != "api_key" || snap.Source != core.CredentialSourceStore || snap.Generation != keyGen {
		t.Errorf("voice key selected an OAuth environment token: kind=%q source=%q err=%v", snap.Kind, snap.Source, err)
	}
}

func TestVoiceSelection_PlanRefreshWithEnvironmentKeyRotatesOnce(t *testing.T) {
	ts := newRotatingTokenServer(t, "refresh-0")
	ts.account = "acct"
	f := newVoiceKeyFixture(t, "openai", ts)
	t.Setenv("OPENAI_API_KEY", "offline-environment-api-key-0123456789")
	snap, err := f.store.ResolveSnapshot(context.Background(), "openai", SelectPlanOAuth)
	if err != nil {
		t.Fatal(err)
	}
	stores := []*Store{f.store, newServedStore(t, f.path, ts), newServedStore(t, f.path, ts)}
	results := make([]CredentialSnapshot, 6)
	errs := make([]error, len(results))
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = stores[i%len(stores)].RefreshOAuthIfGeneration(context.Background(), snap, snap.Token)
		}(i)
	}
	wg.Wait()
	want := testOpenAIJWT("acct", 1)
	for i := range results {
		if errs[i] != nil || results[i].Token != want || results[i].Generation != snap.Generation || results[i].Kind != "oauth" {
			t.Fatalf("worker %d: token_ok=%v err=%v", i, results[i].Token == want, errs[i])
		}
	}
	if n := ts.callCount(); n != 1 {
		t.Fatalf("refresh calls = %d, want 1", n)
	}
	// The refreshed snapshot keeps the plan selection: it can be admitted
	// although the environment key still wins for text.
	if err := f.store.AdmitSnapshot(context.Background(), results[0], func(got CredentialSnapshot) error {
		if got.Token != want {
			t.Error("admitted another token")
		}
		return nil
	}); err != nil {
		t.Errorf("plan snapshot not admitted with environment key set: %v", err)
	}
	if text, _ := f.store.ResolveSnapshot(context.Background(), "openai"); text.Source != core.CredentialSourceEnv {
		t.Error("text stopped using the environment key")
	}
	// A default (text) snapshot of the stored plan still refuses to refresh
	// under an environment key, as before.
	textSnap := storeSnapshot("openai", readAuthFile(t, f.path)["openai"])
	_, err = f.store.RefreshOAuthIfGeneration(context.Background(), textSnap, textSnap.Token)
	wantCredClass(t, err, core.CredentialChanged)
	if n := ts.callCount(); n != 1 {
		t.Fatalf("text refresh under env made a network call: %d", n)
	}
}

func TestVoiceSelection_TextAndVoiceShareOneRotation(t *testing.T) {
	ts := newRotatingTokenServer(t, "refresh-0")
	ts.account = "acct"
	f := newVoiceKeyFixture(t, "openai", ts)
	text, err := f.store.ResolveSnapshot(context.Background(), "openai")
	if err != nil {
		t.Fatal(err)
	}
	voice, err := newServedStore(t, f.path, ts).ResolveSnapshot(context.Background(), "openai", SelectPlanOAuth)
	if err != nil {
		t.Fatal(err)
	}
	snaps := []CredentialSnapshot{text, voice}
	stores := []*Store{f.store, newServedStore(t, f.path, ts)}
	errs := make([]error, 4)
	tokens := make([]string, 4)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := stores[i%2].RefreshOAuthIfGeneration(context.Background(), snaps[i/2], snaps[i/2].Token)
			tokens[i], errs[i] = got.Token, err
		}(i)
	}
	wg.Wait()
	for i := range errs {
		if errs[i] != nil || tokens[i] != testOpenAIJWT("acct", 1) {
			t.Fatalf("worker %d: err=%v", i, errs[i])
		}
	}
	if n := ts.callCount(); n != 1 {
		t.Fatalf("refresh calls = %d, want one shared rotation", n)
	}
}

func TestVoiceSelection_KeySaveKeepsRotationReloginInvalidatesIt(t *testing.T) {
	ts := newRotatingTokenServer(t, "refresh-0")
	ts.account = "acct"
	f := newVoiceKeyFixture(t, "openai", ts)
	snap, err := f.store.ResolveSnapshot(context.Background(), "openai", SelectPlanOAuth)
	if err != nil {
		t.Fatal(err)
	}
	f.saveKey(t, "openai", voicePhase1Key("openai"))
	next, err := newServedStore(t, f.path, ts).RefreshOAuthIfGeneration(context.Background(), snap, snap.Token)
	if err != nil || next.Token != testOpenAIJWT("acct", 1) {
		t.Fatalf("saving the voice key invalidated the plan rotation: %v", err)
	}
	if n := ts.callCount(); n != 1 {
		t.Fatalf("refresh calls = %d, want 1", n)
	}
	if _, err := f.store.CommitLogin("openai", f.plan.Generation, Credential{Type: "oauth", Access: testOpenAIJWT("acct", 9), Refresh: "refresh-new", Expires: fresh(), AccountID: "acct"}); err != nil {
		t.Fatal(err)
	}
	_, err = f.store.RefreshOAuthIfGeneration(context.Background(), next, next.Token)
	wantCredClass(t, err, core.CredentialChanged)
	if n := ts.callCount(); n != 1 {
		t.Fatalf("refresh after relogin made a network call: %d", n)
	}
	if got := readAuthFile(t, f.path)["__voice_openai_api_v1"]; got.Key != voicePhase1Key("openai") {
		t.Error("relogin dropped the key beside the plan")
	}
}

func TestVoiceSelection_APIKeyNeverRefreshes(t *testing.T) {
	ts := newRotatingTokenServer(t, "refresh-0")
	ts.account = "acct"
	f := newVoiceKeyFixture(t, "openai", ts)
	f.saveKey(t, "openai", voicePhase1Key("openai"))
	before := fileBytes(t, f.path)
	stored, err := f.store.ResolveSnapshot(context.Background(), "openai", SelectVoiceAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.store.RefreshOAuthIfGeneration(context.Background(), stored, stored.Token)
	wantCredClass(t, err, core.CredentialKeyRejected)
	t.Setenv("OPENAI_API_KEY", "offline-environment-api-key-0123456789")
	env, err := f.store.ResolveSnapshot(context.Background(), "openai", SelectVoiceAPIKey)
	if err != nil || env.Source != core.CredentialSourceEnv {
		t.Fatal(err)
	}
	_, err = f.store.RefreshOAuthIfGeneration(context.Background(), env, env.Token)
	wantCredClass(t, err, core.CredentialKeyRejected)
	if n := ts.callCount(); n != 0 {
		t.Fatalf("API-key refresh made %d network calls, want 0", n)
	}
	if !bytes.Equal(before, fileBytes(t, f.path)) {
		t.Error("API-key refresh rewrote auth.json")
	}
}

func TestVoiceSelection_AdmitRevalidatesKeySelection(t *testing.T) {
	f := newVoiceKeyFixture(t, "xai", nil)
	f.saveKey(t, "xai", voicePhase1Key("xai"))
	snap, err := f.store.ResolveSnapshot(context.Background(), "xai", SelectVoiceAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	admitted := 0
	admit := func(got CredentialSnapshot) error {
		admitted++
		if got.Token != snap.Token || got.Kind != "api_key" {
			t.Error("admitted another credential")
		}
		return nil
	}
	if err := f.store.AdmitSnapshot(context.Background(), snap, admit); err != nil || admitted != 1 {
		t.Fatalf("current key not admitted: %v", err)
	}
	other := NewProviderLoginManager(context.Background(), NewStore(f.path))
	t.Cleanup(other.Close)
	if _, err := other.SaveAPIKey("xai", f.plan.Generation, "offline-replaced-voice-key-0123456789"); err != nil {
		t.Fatal(err)
	}
	err = f.store.AdmitSnapshot(context.Background(), snap, admit)
	wantCredClass(t, err, core.CredentialChanged)
	if admitted != 1 {
		t.Error("a replaced key was admitted")
	}
}

func TestVoiceKey_PersistenceFailureLeavesFileIntact(t *testing.T) {
	for _, provider := range []string{"openai", "xai"} {
		t.Run(provider, func(t *testing.T) {
			f := newVoiceKeyFixture(t, provider, nil)
			keyGen := f.saveKey(t, provider, voicePhase1Key(provider))
			before := fileBytes(t, f.path)
			restore := makeReadOnlyDir(t, filepath.Dir(f.path))
			if _, err := f.m.SaveAPIKey(provider, f.plan.Generation, "offline-unsaved-voice-key-0123456789"); err == nil {
				t.Error("save into a read-only directory reported success")
			} else {
				wantCredClass(t, err, core.CredentialPersistenceFailed)
			}
			err := f.store.RemoveProviderAPIKey(provider, keyGen)
			wantCredClass(t, err, core.CredentialPersistenceFailed)
			restore()
			if !bytes.Equal(before, fileBytes(t, f.path)) {
				t.Fatal("failed writes changed auth.json")
			}
			snap, err := f.store.ResolveSnapshot(context.Background(), provider, SelectVoiceAPIKey)
			if err != nil || snap.Generation != keyGen {
				t.Errorf("the saved key is not selected after the failed writes: %v", err)
			}
			if err := f.store.RemoveProviderAPIKey(provider, keyGen); err != nil {
				t.Errorf("remove after restoring the directory: %v", err)
			}
		})
	}
}
