package auth

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// These tests use only APIs that also existed in PR #36, so they run against
// that version too: there they fail, which is the point of the change.

const fakeAnthropicKey = "sk-ant-api03-offline-fake-key-0001"

func admitsKey(t *testing.T, s *Store, want string) bool {
	t.Helper()
	st, err := s.AnthropicBackupStatus()
	if err != nil {
		t.Fatal(err)
	}
	got := ""
	err = s.AdmitAnthropicBackup(context.Background(), st.Revision, func(sel AnthropicBackupSelection) error {
		got = sel.Snapshot.Token
		return nil
	})
	return err == nil && got == want
}

func TestAPIKeyFieldBesidePlanNeverReplacesOAuth(t *testing.T) {
	s, path, primary := backupFixture(t)
	m := NewProviderLoginManager(context.Background(), s)
	t.Cleanup(m.Close)
	if _, err := m.SaveAPIKey("anthropic", primary.Generation, fakeAnthropicKey); err != nil {
		t.Fatal(err)
	}
	if got := readAuthFile(t, path)["anthropic"]; !sameCredential(got, primary) {
		t.Fatal("saving the API key replaced the plan sign-in")
	}
	snap, err := NewStore(path).ResolveSnapshot(context.Background(), "anthropic")
	if err != nil || snap.Kind != "oauth" || snap.Generation != primary.Generation {
		t.Fatalf("primary after save: kind=%s err=%v", snap.Kind, err)
	}
	// Saved is used: no separate enable step.
	if !admitsKey(t, NewStore(path), fakeAnthropicKey) {
		t.Fatal("the saved key is not used at the plan limit")
	}
}

func TestAPIKeyFieldWithoutPlanIsThePrimaryKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	path := filepath.Join(t.TempDir(), "auth.json")
	s := NewStore(path)
	m := NewProviderLoginManager(context.Background(), s)
	t.Cleanup(m.Close)
	if _, err := m.SaveAPIKey("anthropic", "", fakeAnthropicKey); err != nil {
		t.Fatal(err)
	}
	snap, err := NewStore(path).ResolveSnapshot(context.Background(), "anthropic")
	if err != nil || snap.Kind != "api_key" || snap.Token != fakeAnthropicKey {
		t.Fatalf("key-only primary: kind=%s err=%v", snap.Kind, err)
	}
	if _, ok := readAuthFile(t, path)[anthropicBackupSlot]; ok {
		t.Fatal("a key-only setup wrote the second slot")
	}
}

// Production state after PR #36: a key in the backup slot with an enabled
// policy. It must keep working with no migration step and without touching
// the plan sign-in, including the policy shapes #36 would have refused.
func TestShippedBackupKeyKeepsWorkingAsTheAPIKey(t *testing.T) {
	for name, policy := range map[string]func(primaryGen string) string{
		"enabled":         func(g string) string { return `{"version":1,"primary_generation":"` + g + `","policy_generation":"p1","enabled":true}` },
		"stored_off":      func(g string) string { return `{"version":1,"primary_generation":"` + g + `","policy_generation":"p1","enabled":false}` },
		"primary_changed": func(string) string { return `{"version":1,"primary_generation":"older-sign-in","policy_generation":"p1","enabled":true}` },
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("ANTHROPIC_API_KEY", "")
			path := filepath.Join(t.TempDir(), "auth.json")
			expires := time.Now().Add(time.Hour).UnixMilli()
			writeAuthFile(t, path, `{
  "anthropic": {"type":"oauth","access":"fake-access","refresh":"fake-refresh","expires":`+strconv.FormatInt(expires, 10)+`,"generation":"plan-gen"},
  "`+anthropicBackupSlot+`": {"type":"api_key","key":"`+fakeAnthropicKey+`","generation":"key-gen","backup_policy":`+policy("plan-gen")+`}
}`)
			before := readAuthFile(t, path)["anthropic"]
			s := NewStore(path)
			if !admitsKey(t, s, fakeAnthropicKey) {
				t.Fatal("the shipped key is not used at the plan limit")
			}
			if got := readAuthFile(t, path)["anthropic"]; !sameCredential(got, before) {
				t.Fatal("the plan sign-in changed")
			}
			snap, err := s.ResolveSnapshot(context.Background(), "anthropic")
			if err != nil || snap.Kind != "oauth" {
				t.Fatalf("primary: kind=%s err=%v", snap.Kind, err)
			}
		})
	}
}

func TestPlanSignInKeepsTheAPIKeyBesideIt(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	path := filepath.Join(t.TempDir(), "auth.json")
	s := NewStore(path)
	m := NewProviderLoginManager(context.Background(), s)
	t.Cleanup(m.Close)
	gen, err := m.SaveAPIKey("anthropic", "", fakeAnthropicKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitLogin("anthropic", gen, Credential{Type: "oauth", Access: "fake-access", Refresh: "fake-refresh", Expires: time.Now().Add(time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if snap, err := s.ResolveSnapshot(context.Background(), "anthropic"); err != nil || snap.Kind != "oauth" {
		t.Fatalf("plan is not first: kind=%s err=%v", snap.Kind, err)
	}
	if !admitsKey(t, NewStore(path), fakeAnthropicKey) {
		t.Fatal("signing in to the plan dropped the API key")
	}
}

func TestNewPlanSignInKeepsTheKeyActive(t *testing.T) {
	s, path, primary := backupFixture(t)
	m := NewProviderLoginManager(context.Background(), s)
	t.Cleanup(m.Close)
	if _, err := m.SaveAPIKey("anthropic", primary.Generation, fakeAnthropicKey); err != nil {
		t.Fatal(err)
	}
	cur, _ := NewStore(path).StoredGeneration("anthropic")
	if _, err := s.CommitLogin("anthropic", cur, Credential{Type: "oauth", Access: "fake-access-2", Refresh: "fake-refresh-2", Expires: time.Now().Add(time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if !admitsKey(t, NewStore(path), fakeAnthropicKey) {
		t.Fatal("a new sign-in switched the key off")
	}
}

func TestEnvAnthropicKeyStaysPrimary(t *testing.T) {
	s, path, primary := backupFixture(t)
	m := NewProviderLoginManager(context.Background(), s)
	t.Cleanup(m.Close)
	if _, err := m.SaveAPIKey("anthropic", primary.Generation, fakeAnthropicKey); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "env-primary-fake-key")
	if snap, err := s.ResolveSnapshot(context.Background(), "anthropic"); err != nil || snap.Token != "env-primary-fake-key" {
		t.Fatal("env priority changed")
	}
	if admitsKey(t, NewStore(path), fakeAnthropicKey) {
		t.Fatal("stored key used while the environment manages Anthropic")
	}
	if _, err := m.SaveAPIKey("anthropic", primary.Generation, fakeAnthropicKey); err == nil {
		t.Fatal("saved a key while the environment manages Anthropic")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
