package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

var invalidAuthFiles = []struct{ name, data string }{
	{"truncated", `{"anthropic": {"type": "oauth", "access": "a"`},
	{"trailing", `{"xai": {"type": "api_key", "key": "k"}} {}`},
	{"empty", ``},
	{"null root", `null`},
	{"array root", `[{"type": "api_key"}]`},
	{"scalar root", `42`},
	{"null provider", `{"anthropic": null}`},
	{"array provider", `{"anthropic": ["oauth"]}`},
	{"wrong field type", `{"anthropic": {"type": "oauth", "access": "a", "refresh": "r", "expires": "soon"}}`},
	{"null known field", `{"anthropic": {"type": "oauth", "access": null, "refresh": "r"}}`},
}

type storeWriter struct {
	name string
	run  func(*Store) error
}

var storeWriters = []storeWriter{
	{"Set", func(s *Store) error { return s.Set("xai", Credential{Type: "api_key", Key: "new-key"}) }},
	{"Remove", func(s *Store) error { return s.Remove("xai") }},
	{"CommitLogin", func(s *Store) error {
		_, err := s.CommitLogin("xai", "", Credential{Type: "api_key", Key: "new-key"})
		return err
	}},
	{"date refresh", func(s *Store) error { _, _, err := s.GetAPIKey("anthropic"); return err }},
	{"ResolveSnapshot refresh", func(s *Store) error {
		_, err := s.ResolveSnapshot(context.Background(), "anthropic")
		return err
	}},
	{"reactive refresh", func(s *Store) error { _, err := refreshOAuthIfCurrent(s, "anthropic", "access-0"); return err }},
	{"RefreshOAuthIfGeneration", func(s *Store) error {
		_, err := s.RefreshOAuthIfGeneration(context.Background(), CredentialSnapshot{Provider: "anthropic", Source: "store", Kind: "oauth", Token: "access-0"}, "access-0")
		return err
	}},
	{"RetrySave", func(s *Store) error { return s.RetrySave("anthropic") }},
}

func validSeed() map[string]Credential {
	return map[string]Credential{
		"anthropic": {Type: "oauth", Access: "access-0", Refresh: "refresh-0", Expires: expired()},
		"xai":       {Type: "api_key", Key: "old-key"},
	}
}

// TestFailClosed_WritersLeaveInvalidFileUntouched pins R08: once the shared
// auth.json is invalid, no writer may "repair" it from memory and no rotation
// request may leave from that state.
func TestFailClosed_WritersLeaveInvalidFileUntouched(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("XAI_API_KEY", "")
	for _, mode := range []string{"loaded then corrupted", "corrupt at load"} {
		for _, bad := range invalidAuthFiles {
			for _, w := range storeWriters {
				t.Run(mode+"/"+bad.name+"/"+w.name, func(t *testing.T) {
					path := filepath.Join(t.TempDir(), "auth.json")
					ts := newRotatingTokenServer(t, "refresh-0")
					if mode == "loaded then corrupted" {
						seedAuthFile(t, path, validSeed())
					} else {
						writeAuthFile(t, path, bad.data)
					}
					store := newServedStore(t, path, ts)
					writeAuthFile(t, path, bad.data)

					err := w.run(store)
					wantCredClass(t, err, core.CredentialStoreUnavailable)
					if got := fileBytes(t, path); !bytes.Equal(got, []byte(bad.data)) {
						t.Fatalf("invalid auth.json was rewritten:\n%s", got)
					}
					if n := ts.callCount(); n != 0 {
						t.Fatalf("%d rotation requests sent from an invalid store", n)
					}
				})
			}
		}
	}
}

func TestFailClosed_UnreadableFile(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("XAI_API_KEY", "")
	for _, w := range storeWriters {
		t.Run(w.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "auth.json")
			ts := newRotatingTokenServer(t, "refresh-0")
			seedAuthFile(t, path, validSeed())
			before := fileBytes(t, path)
			store := newServedStore(t, path, ts)
			if err := os.Chmod(path, 0); err != nil {
				t.Fatal(err)
			}
			err := w.run(store)
			_ = os.Chmod(path, 0600)
			wantCredClass(t, err, core.CredentialStoreUnavailable)
			if got := fileBytes(t, path); !bytes.Equal(got, before) {
				t.Fatalf("unreadable auth.json was replaced:\n%s", got)
			}
			if n := ts.callCount(); n != 0 {
				t.Fatalf("%d rotation requests sent from an unreadable store", n)
			}
		})
	}
}

func TestFailClosed_InitialLoadErrorIsRetained(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	writeAuthFile(t, path, `null`)
	store := NewStore(path)
	wantCredClass(t, store.LoadError(), core.CredentialStoreUnavailable)

	missing := NewStore(filepath.Join(t.TempDir(), "auth.json"))
	if err := missing.LoadError(); err != nil {
		t.Fatalf("missing file must load as an empty store, got %v", err)
	}
}

// A disappearing file after a valid read is not permission to rewrite the
// whole store from a cached copy.
func TestFailClosed_DisappearedFileIsNotEmpty(t *testing.T) {
	t.Setenv("XAI_API_KEY", "")
	path := filepath.Join(t.TempDir(), "auth.json")
	seedAuthFile(t, path, validSeed())
	store := NewStore(path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	err := store.Set("xai", Credential{Type: "api_key", Key: "new-key"})
	wantCredClass(t, err, core.CredentialStoreUnavailable)
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("auth.json recreated from stale memory (stat err %v)", statErr)
	}
	if _, _, err := store.GetAPIKey("xai"); err == nil {
		t.Fatal("cached key served after auth.json disappeared")
	}
}

func TestFailClosed_MissingFileInitializes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "auth.json")
	store := NewStore(path)
	if err := store.Set("xai", Credential{Type: "api_key", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if got := readAuthFile(t, path)["xai"].Key; got != "k" {
		t.Fatalf("key = %q", got)
	}
}

// Effective reads must not keep serving a cached credential once the disk is
// invalid; environment credentials stay usable.
func TestFailClosed_EffectiveReadsDoNotUseStaleCache(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	path := filepath.Join(t.TempDir(), "auth.json")
	seedAuthFile(t, path, map[string]Credential{
		"anthropic": {Type: "oauth", Access: "cached-access", Refresh: "r", Expires: fresh()},
	})
	store := NewStore(path)
	writeAuthFile(t, path, `{"anthropic": `)

	if key, _, err := store.GetAPIKey("anthropic"); err == nil {
		t.Fatalf("GetAPIKey served cached %q from an invalid store", key)
	} else {
		wantCredClass(t, err, core.CredentialStoreUnavailable)
	}
	if snap, err := store.ResolveSnapshot(context.Background(), "anthropic"); err == nil {
		t.Fatalf("ResolveSnapshot served cached %q from an invalid store", snap.Token)
	}
	if _, err := store.PeekSnapshot("anthropic"); err == nil {
		t.Fatal("PeekSnapshot served the cache from an invalid store")
	}

	t.Setenv("ANTHROPIC_API_KEY", "sk-"+"ant-api03-env")
	snap, err := store.ResolveSnapshot(context.Background(), "anthropic")
	if err != nil || snap.Source != core.CredentialSourceEnv || snap.Token != "sk-"+"ant-api03-env" {
		t.Fatalf("env snapshot = %+v, %v", snap, err)
	}
}

// Unknown fields and providers written by a newer binary survive rewrites.
func TestStore_PreservesUnknownFieldsAndProviders(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	path := filepath.Join(t.TempDir(), "auth.json")
	writeAuthFile(t, path, `{
  "anthropic": {"type": "oauth", "access": "access-0", "refresh": "refresh-0", "expires": 1, "future": {"x": 1}},
  "newprov": {"type": "weird", "blob": [1, 2]}
}`)
	ts := newRotatingTokenServer(t, "refresh-0")
	store := newServedStore(t, path, ts)
	if err := store.Set("xai", Credential{Type: "api_key", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetAPIKey("anthropic"); err != nil { // rotation rewrite
		t.Fatal(err)
	}
	var raw map[string]map[string]json.RawMessage
	if err := json.Unmarshal(fileBytes(t, path), &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["newprov"]["blob"]) == "" || string(raw["newprov"]["type"]) != `"weird"` {
		t.Fatalf("unknown provider not preserved: %s", fileBytes(t, path))
	}
	if string(raw["anthropic"]["future"]) == "" || string(raw["anthropic"]["refresh"]) != `"refresh-1"` {
		t.Fatalf("unknown field lost on rotation: %s", fileBytes(t, path))
	}
}
