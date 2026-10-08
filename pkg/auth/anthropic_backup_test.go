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
	st, err := s.SaveAnthropicBackup(backupStatus(t, s).Revision, "sk-ant-api03-offline-backup-only")
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestAnthropicBackupDualReopenAndReplaceRemove(t *testing.T) {
	s, path, primary := backupFixture(t)
	st := saveBackup(t, s)
	if !st.Configured || st.Enabled {
		t.Fatalf("save must store OFF: %+v", st)
	}
	other := NewStore(path)
	if got, _ := other.Get("anthropic"); !sameCredential(got, primary) {
		t.Fatal("save replaced OAuth")
	}
	if snap, err := other.ResolveSnapshot(context.Background(), "anthropic"); err != nil || snap.Kind != "oauth" {
		t.Fatalf("primary = %s, %v", snap.Kind, err)
	}
	st, err := other.SetAnthropicBackupEnabled(st.Revision, true)
	if err != nil || !st.Enabled {
		t.Fatalf("enable: %+v %v", st, err)
	}
	st, err = s.SaveAnthropicBackup(st.Revision, "sk-ant-api03-offline-replaced")
	if err != nil || st.Enabled {
		t.Fatalf("replace must store OFF: %+v %v", st, err)
	}
	st, err = other.RemoveAnthropicBackup(st.Revision)
	if err != nil || st.Configured || st.Enabled {
		t.Fatalf("remove: %+v %v", st, err)
	}
	if got, _ := NewStore(path).Get("anthropic"); !sameCredential(got, primary) {
		t.Fatal("replace/remove changed OAuth")
	}
}

func TestAnthropicBackupCASAndDisableRevoke(t *testing.T) {
	s, path, _ := backupFixture(t)
	st := saveBackup(t, s)
	st, err := s.SetAnthropicBackupEnabled(st.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	old := st.Revision
	other := NewStore(path)
	st, err = other.SetAnthropicBackupEnabled(old, false)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if _, err := s.SetAnthropicBackupEnabled(old, true); err == nil {
		t.Fatal("stale CAS enabled")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("stale CAS rewrote winner")
	}
	calls := 0
	if err := s.AdmitAnthropicBackup(context.Background(), old, func(AnthropicBackupSelection) error { calls++; return nil }); err == nil {
		t.Fatal("disabled backup admitted")
	}
	st, err = s.SetAnthropicBackupEnabled(st.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	old = st.Revision
	if _, err := other.RemoveAnthropicBackup(old); err != nil {
		t.Fatal(err)
	}
	if err := s.AdmitAnthropicBackup(context.Background(), old, func(AnthropicBackupSelection) error { calls++; return nil }); err == nil {
		t.Fatal("removed backup admitted")
	}
	if calls != 0 {
		t.Fatalf("revoked dispatches=%d", calls)
	}
}

func TestAnthropicBackupRefreshPreservesNewLoginInvalidates(t *testing.T) {
	s, path, primary := backupFixture(t)
	st := saveBackup(t, s)
	st, err := s.SetAnthropicBackupEnabled(st.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	s.refresh = func(context.Context, string, string) (*OAuthCredentials, error) {
		return &OAuthCredentials{Access: "rotated-fake", Refresh: "rotated-refresh", Expires: time.Now().Add(time.Hour).UnixMilli()}, nil
	}
	snap, _ := s.ResolveSnapshot(context.Background(), "anthropic")
	if _, err := s.RefreshOAuthIfGeneration(context.Background(), snap, primary.Access); err != nil {
		t.Fatal(err)
	}
	if got := backupStatus(t, NewStore(path)); !got.Enabled || got.Revision != st.Revision {
		t.Fatal("same-grant rotation lost backup")
	}
	if _, err := s.CommitLogin("anthropic", primary.Generation, primary); err != nil {
		t.Fatal(err)
	}
	if got := backupStatus(t, NewStore(path)); got.Enabled || !got.Configured {
		t.Fatalf("new login reauthorized old backup: %+v", got)
	}
	if err := s.AdmitAnthropicBackup(context.Background(), st.Revision, func(AnthropicBackupSelection) error { t.Fatal("old primary admitted"); return nil }); err == nil {
		t.Fatal("new primary not fenced")
	}
}

func TestAnthropicBackupDurableReopenDispatchAndStop(t *testing.T) {
	s, path, _ := backupFixture(t)
	st := saveBackup(t, s)
	writer := s.writeFile
	s.writeFile = func(path string, data []byte) error {
		if err := writer(path, data); err != nil {
			return err
		}
		return errors.New("synthetic failure after real rename")
	}
	if _, err := s.SetAnthropicBackupEnabled(st.Revision, true); err == nil {
		t.Fatal("post-rename failure reported success")
	}
	other := NewStore(path)
	st = backupStatus(t, other)
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

func TestAnthropicBackupLegacyCodecRoundTripAndEnv(t *testing.T) {
	s, path, primary := backupFixture(t)
	st := saveBackup(t, s)
	st, err := s.SetAnthropicBackupEnabled(st.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	// The unchanged c995 codec treats policy as extra and the reserved slot as
	// another provider; a legacy general rewrite must preserve both verbatim.
	disk := readAuthFile(t, path)
	if err := s.Set("unknown-future-provider", Credential{Type: "future", extra: map[string]json.RawMessage{"future": json.RawMessage(`{"nested":true}`)}}); err != nil {
		t.Fatal(err)
	}
	got := readAuthFile(t, path)
	if !sameCredential(got[anthropicBackupSlot], disk[anthropicBackupSlot]) || string(got[anthropicBackupSlot].extra["backup_policy"]) != string(disk[anthropicBackupSlot].extra["backup_policy"]) {
		t.Fatal("legacy codec dropped slot/policy")
	}
	if !sameCredential(got["anthropic"], primary) {
		t.Fatal("legacy rewrite changed OAuth")
	}
	t.Setenv("ANTHROPIC_API_KEY", "env-primary-not-backup")
	if snap, err := s.ResolveSnapshot(context.Background(), "anthropic"); err != nil || snap.Token != "env-primary-not-backup" {
		t.Fatal("env legacy priority changed")
	}
	if err := s.AdmitAnthropicBackup(context.Background(), st.Revision, func(AnthropicBackupSelection) error { t.Fatal("env used as backup"); return nil }); err == nil {
		t.Fatal("env admitted backup")
	}
}
