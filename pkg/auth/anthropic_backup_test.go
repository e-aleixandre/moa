package auth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func backupFixture(t *testing.T) (*Store, string, Credential) {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "")
	path := filepath.Join(t.TempDir(), "auth.json")
	s := NewStore(path)
	if err := s.Set("anthropic", Credential{Type: "oauth", Access: "fake-primary-access", Refresh: "fake-primary-refresh", Expires: time.Now().Add(time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Get("anthropic")
	return s, path, c
}

func backupStatus(t *testing.T, s *Store) AnthropicBackupStatus {
	t.Helper()
	st, err := s.AnthropicBackupStatus()
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func saveBackup(t *testing.T, s *Store) AnthropicBackupStatus {
	t.Helper()
	gen, err := s.StoredGeneration("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveAnthropicAPIKey(gen, "sk-ant-api03-offline-backup-only"); err != nil {
		t.Fatal(err)
	}
	return backupStatus(t, s)
}

func TestAnthropicBackupReopenReplaceRemove(t *testing.T) {
	s, path, primary := backupFixture(t)
	st := saveBackup(t, s)
	if !st.Configured || !st.Active || st.State != "active" {
		t.Fatalf("a saved key is in use: %+v", st)
	}
	other := NewStore(path)
	if got, _ := other.Get("anthropic"); !sameCredential(got, primary) {
		t.Fatal("save replaced OAuth")
	}
	if _, err := other.SaveAnthropicAPIKey(st.Revision.Primary, "sk-ant-api03-offline-replaced"); err != nil {
		t.Fatal(err)
	}
	st = backupStatus(t, s)
	if !st.Active || !admitsKey(t, s, "sk-ant-api03-offline-replaced") {
		t.Fatalf("replace: %+v", st)
	}
	if err := other.RemoveAnthropicAPIKey(st.Revision.Key); err != nil {
		t.Fatal(err)
	}
	if st = backupStatus(t, s); st.Configured || st.Active || st.State != "not_configured" {
		t.Fatalf("remove: %+v", st)
	}
	if got, _ := NewStore(path).Get("anthropic"); !sameCredential(got, primary) {
		t.Fatal("replace/remove changed OAuth")
	}
}

func TestAnthropicBackupStaleRemoveAndRevoke(t *testing.T) {
	s, path, _ := backupFixture(t)
	st := saveBackup(t, s)
	old := st.Revision
	other := NewStore(path)
	if _, err := other.SaveAnthropicAPIKey(old.Primary, "sk-ant-api03-offline-newer"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if err := s.RemoveAnthropicAPIKey(old.Key); err == nil {
		t.Fatal("stale remove deleted a newer key")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) || s.backupBlocked {
		t.Fatal("stale remove rewrote the winner or fenced it")
	}
	calls := 0
	if err := s.AdmitAnthropicBackup(context.Background(), old, func(AnthropicBackupSelection) error { calls++; return nil }); err == nil {
		t.Fatal("replaced key admitted")
	}
	cur := backupStatus(t, s)
	if err := other.RemoveAnthropicAPIKey(cur.Revision.Key); err != nil {
		t.Fatal(err)
	}
	if err := s.AdmitAnthropicBackup(context.Background(), cur.Revision, func(AnthropicBackupSelection) error { calls++; return nil }); err == nil {
		t.Fatal("removed key admitted")
	}
	if calls != 0 {
		t.Fatalf("revoked dispatches=%d", calls)
	}
}

func TestAnthropicBackupRefreshPreservesKey(t *testing.T) {
	s, path, primary := backupFixture(t)
	st := saveBackup(t, s)
	s.refresh = func(context.Context, string, string) (*OAuthCredentials, error) {
		return &OAuthCredentials{Access: "rotated-fake", Refresh: "rotated-refresh", Expires: time.Now().Add(time.Hour).UnixMilli()}, nil
	}
	snap, _ := s.ResolveSnapshot(context.Background(), "anthropic")
	if _, err := s.RefreshOAuthIfGeneration(context.Background(), snap, primary.Access); err != nil {
		t.Fatal(err)
	}
	if got := backupStatus(t, NewStore(path)); !got.Active || got.Revision != st.Revision {
		t.Fatal("same-grant rotation lost the key")
	}
}

func TestAnthropicBackupDurableReopenDispatchAndStop(t *testing.T) {
	s, path, primary := backupFixture(t)
	writer := s.writeFile
	s.writeFile = func(path string, data []byte) error {
		if err := writer(path, data); err != nil {
			return err
		}
		return errors.New("synthetic failure after real rename")
	}
	if _, err := s.SaveAnthropicAPIKey(primary.Generation, "sk-ant-api03-offline-backup-only"); err == nil {
		t.Fatal("post-rename failure reported success")
	}
	other := NewStore(path)
	st := backupStatus(t, NewStore(path))
	calls, syncs := 0, 0
	other.writeFile = func(string, []byte) error { syncs++; return errors.New("sync unavailable") }
	admit := func(AnthropicBackupSelection) error { calls++; return nil }
	if err := other.AdmitAnthropicBackup(context.Background(), st.Revision, admit); err == nil || calls != 0 || syncs != 1 {
		t.Fatalf("reopen is not durable proof: calls=%d syncs=%d err=%v", calls, syncs, err)
	}
	other.writeFile = func(path string, data []byte) error { syncs++; return writer(path, data) }
	if err := other.AdmitAnthropicBackup(context.Background(), st.Revision, admit); err != nil || calls != 1 || syncs != 2 {
		t.Fatalf("reconciled dispatch: calls=%d syncs=%d err=%v", calls, syncs, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := other.AdmitAnthropicBackup(ctx, st.Revision, admit); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatal("Stop admitted new request")
	}
}

func TestAnthropicBackupLegacyCodecRoundTrip(t *testing.T) {
	s, path, primary := backupFixture(t)
	saveBackup(t, s)
	// The codec treats the reserved slot as another provider; a general
	// rewrite must preserve it verbatim.
	disk := readAuthFile(t, path)
	if err := s.Set("unknown-future-provider", Credential{Type: "future", extra: map[string]json.RawMessage{"future": json.RawMessage(`{"nested":true}`)}}); err != nil {
		t.Fatal(err)
	}
	got := readAuthFile(t, path)
	if !sameCredential(got[anthropicBackupSlot], disk[anthropicBackupSlot]) {
		t.Fatal("codec dropped the key slot")
	}
	if !sameCredential(got["anthropic"], primary) {
		t.Fatal("rewrite changed OAuth")
	}
}
