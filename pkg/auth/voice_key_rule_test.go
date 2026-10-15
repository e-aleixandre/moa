package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// One plan and one API key per provider: these tests pin that a key is never
// lost or duplicated by plan or key changes.

func apiKeyRecords(t *testing.T, path string) map[string]Credential {
	t.Helper()
	out := map[string]Credential{}
	for name, cred := range readAuthFile(t, path) {
		if cred.Type == "api_key" {
			out[name] = cred
		}
	}
	return out
}

func TestVoiceKey_StaleKeySaveRejectedAcrossStores(t *testing.T) {
	for _, provider := range []string{"openai", "xai"} {
		t.Run(provider, func(t *testing.T) {
			f := newVoiceKeyFixture(t, provider, nil)
			seen := f.saveKey(t, provider, voicePhase1Key(provider))
			other := NewProviderLoginManager(context.Background(), NewStore(f.path))
			t.Cleanup(other.Close)
			const newer = "offline-newer-voice-api-key-0123456789"
			winner, err := other.SaveAPIKeyExpecting(provider, f.plan.Generation, seen, newer)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.m.SaveAPIKeyExpecting(provider, f.plan.Generation, seen, "offline-stale-page-api-key-0123456789")
			wantCredClass(t, err, core.CredentialChanged)
			slot, _ := voiceKeySlot(provider)
			if got := readAuthFile(t, f.path)[slot]; got.Key != newer || got.Generation != winner {
				t.Error("a stale page replaced the newer key")
			}
			// A page that saw no key cannot add one over a saved key either.
			_, err = f.m.SaveAPIKeyExpecting(provider, f.plan.Generation, "", "offline-stale-page-api-key-0123456789")
			wantCredClass(t, err, core.CredentialChanged)
			if _, err := f.m.SaveAPIKeyExpecting(provider, f.plan.Generation, winner, "offline-current-page-api-key-0123456789"); err != nil {
				t.Errorf("a current page could not replace the key: %v", err)
			}
		})
	}
}

func TestVoiceKey_MigratedKeyRespectsFailedLoginFence(t *testing.T) {
	for _, provider := range []string{"openai", "xai"} {
		t.Run(provider, func(t *testing.T) {
			clearProviderEnv(t)
			path := filepath.Join(t.TempDir(), "auth.json")
			s := NewStore(path)
			key := voicePhase1Key(provider)
			gen, err := s.CommitLogin(provider, "", Credential{Type: "api_key", Key: key})
			if err != nil {
				t.Fatal(err)
			}
			s.writeFile = func(p string, data []byte) error {
				if err := writeCredentialFile(p, data); err != nil {
					return err
				}
				return errors.New("injected failure after the atomic write")
			}
			_, err = s.CommitLogin(provider, gen, Credential{Type: "oauth", Access: "offline-access", Refresh: "offline-refresh", Expires: fresh(), AccountID: "account"})
			wantCredClass(t, err, core.CredentialPersistenceFailed)
			if !s.hasPending(provider) {
				t.Fatal("fixture did not reach the login fence")
			}
			for name, resolve := range map[string]func() (CredentialSnapshot, error){
				"resolve": func() (CredentialSnapshot, error) {
					return s.ResolveSnapshot(context.Background(), provider, SelectVoiceAPIKey)
				},
				"peek": func() (CredentialSnapshot, error) { return s.PeekSnapshot(provider, SelectVoiceAPIKey) },
			} {
				if snap, err := resolve(); err == nil || snap.Token != "" {
					t.Errorf("%s: voice selected the migrated key through a failed login fence", name)
				}
			}
			s.writeFile = writeCredentialFile
			snap, err := s.ResolveSnapshot(context.Background(), provider, SelectVoiceAPIKey)
			if err != nil || snap.Token != key {
				t.Errorf("migrated key not selectable once the write is durable: %v", err)
			}
		})
	}
}

func TestVoiceKey_PlanDisconnectKeepsKeyAsTheOnlyKey(t *testing.T) {
	for _, provider := range []string{"openai", "xai"} {
		t.Run(provider, func(t *testing.T) {
			f := newVoiceKeyFixture(t, provider, nil)
			key := voicePhase1Key(provider)
			keyGen := f.saveKey(t, provider, key)
			if err := NewStore(f.path).Remove(provider); err != nil {
				t.Fatal(err)
			}
			kept := Credential{Type: "api_key", Key: key, Generation: keyGen}
			assertVoicePhase1TextSelection(t, NewStore(f.path), provider, kept)
			if snap, err := NewStore(f.path).ResolveSnapshot(context.Background(), provider, SelectVoiceAPIKey); err != nil || snap.Token != key {
				t.Errorf("voice lost the key: %v", err)
			}
			if n := len(apiKeyRecords(t, f.path)); n != 1 {
				t.Errorf("API key records = %d, want 1", n)
			}

			const replacement = "offline-replacement-api-key-0123456789"
			gen, err := f.m.SaveAPIKey(provider, keyGen, replacement)
			if err != nil {
				t.Fatal(err)
			}
			assertVoicePhase1TextSelection(t, NewStore(f.path), provider, Credential{Type: "api_key", Key: replacement, Generation: gen})
			if keys := apiKeyRecords(t, f.path); len(keys) != 1 || keys[provider].Key != replacement {
				t.Errorf("saving a key did not replace the only key: %d records", len(keys))
			}
		})
	}
}

func TestVoiceKey_PrimaryKeyLoginLeavesOneKey(t *testing.T) {
	for _, provider := range []string{"openai", "xai"} {
		t.Run(provider, func(t *testing.T) {
			f := newVoiceKeyFixture(t, provider, nil)
			f.saveKey(t, provider, voicePhase1Key(provider))
			// CommitLogin (the CLI) still replaces the primary credential; the
			// new key replaces the one beside the plan too.
			const key = "offline-cli-api-key-0123456789"
			gen, err := f.store.CommitLogin(provider, f.plan.Generation, Credential{Type: "api_key", Key: key})
			if err != nil {
				t.Fatal(err)
			}
			assertVoicePhase1TextSelection(t, NewStore(f.path), provider, Credential{Type: "api_key", Key: key, Generation: gen})
			if keys := apiKeyRecords(t, f.path); len(keys) != 1 || keys[provider].Key != key {
				t.Errorf("API key records = %d, want the new key only", len(keys))
			}
		})
	}
}

// twoKeysFile is an auth.json a slot-unaware writer (an older binary) leaves
// when it saves a primary key while a key beside the plan exists.
func twoKeysFile(t *testing.T, provider string) (path string, primary, slotKey Credential) {
	t.Helper()
	clearProviderEnv(t)
	path = filepath.Join(t.TempDir(), "auth.json")
	slot, _ := voiceKeySlot(provider)
	primary = Credential{Type: "api_key", Key: "offline-newer-primary-key-0123456789"}
	slotKey = Credential{Type: "api_key", Key: "offline-older-slot-key-0123456789", Generation: "slot-gen"}
	seedAuthFile(t, path, map[string]Credential{provider: primary, slot: slotKey})
	return path, primary, slotKey
}

func TestVoiceKey_TwoKeysKeepThePrimaryAsMostRecent(t *testing.T) {
	for _, provider := range []string{"openai", "xai"} {
		t.Run(provider, func(t *testing.T) {
			path, primary, _ := twoKeysFile(t, provider)
			store := NewStore(path)
			assertVoicePhase1TextSelection(t, store, provider, primary)
			if snap, err := store.ResolveSnapshot(context.Background(), provider, SelectVoiceAPIKey); err != nil || snap.Token != primary.Key {
				t.Errorf("voice did not select the most recent key: %v", err)
			}

			// Connecting a plan keeps that key beside it and drops the older one.
			gen, err := store.CommitLogin(provider, "", Credential{Type: "oauth", Access: "offline-access", Refresh: "offline-refresh", Expires: fresh(), AccountID: "account"})
			if err != nil {
				t.Fatal(err)
			}
			slot, _ := voiceKeySlot(provider)
			if keys := apiKeyRecords(t, path); len(keys) != 1 || keys[slot].Key != primary.Key {
				t.Errorf("plan login kept %d keys or the older one", len(keys))
			}
			if got := readAuthFile(t, path)[provider]; got.Type != "oauth" || got.Generation != gen {
				t.Error("plan not connected")
			}
		})
	}
}

func TestVoiceKey_TwoKeysRemoveTheKeyThePageShows(t *testing.T) {
	for _, provider := range []string{"openai", "xai"} {
		t.Run(provider, func(t *testing.T) {
			path, _, _ := twoKeysFile(t, provider)
			store := NewStore(path)
			// The page shows the primary key; its generation is the legacy "".
			if err := store.RemoveProviderAPIKey(provider, ""); err != nil {
				t.Fatalf("remove the shown key: %v", err)
			}
			if keys := apiKeyRecords(t, path); len(keys) != 0 {
				t.Errorf("an older key survived removing the only key shown: %d records", len(keys))
			}
		})
	}
}
