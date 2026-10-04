package auth

import (
	"context"
	"encoding/hex"
	"path/filepath"
	"sync"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func wantGeneration(t *testing.T, gen string) {
	t.Helper()
	if b, err := hex.DecodeString(gen); err != nil || len(b) != 16 {
		t.Fatalf("generation %q is not 16 random bytes in hex", gen)
	}
}

// TestCommitLogin_GuardsGeneration pins R11: each commit gets a fresh random
// generation and a commit prepared against an older one is refused.
func TestCommitLogin_GuardsGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	store := NewStore(path)
	gen0, err := store.CommitLogin("xai", "", Credential{Type: "api_key", Key: "key-a"})
	if err != nil {
		t.Fatal(err)
	}
	wantGeneration(t, gen0)
	gen1, err := NewStore(path).CommitLogin("xai", gen0, Credential{Type: "api_key", Key: "key-b"})
	if err != nil {
		t.Fatal(err)
	}
	wantGeneration(t, gen1)
	if gen1 == gen0 {
		t.Fatal("replacement reused the generation")
	}

	_, err = store.CommitLogin("xai", gen0, Credential{Type: "api_key", Key: "key-stale"})
	wantCredClass(t, err, core.CredentialChanged)
	if got := readAuthFile(t, path)["xai"]; got.Key != "key-b" || got.Generation != gen1 {
		t.Fatalf("stale commit overwrote the newer login: %+v", got)
	}
	if got, err := store.StoredGeneration("xai"); err != nil || got != gen1 {
		t.Fatalf("StoredGeneration = %q, %v", got, err)
	}
}

func TestRotation_PreservesGeneration(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	path := filepath.Join(t.TempDir(), "auth.json")
	ts := newRotatingTokenServer(t, "refresh-0")
	store := newServedStore(t, path, ts)
	gen, err := store.CommitLogin("anthropic", "", Credential{Type: "oauth", Access: "access-0", Refresh: "refresh-0", Expires: expired()})
	if err != nil {
		t.Fatal(err)
	}
	wantGeneration(t, gen)
	snap, err := store.ResolveSnapshot(context.Background(), "anthropic")
	if err != nil || snap.Token != "access-1" || snap.Generation != gen || snap.Source != core.CredentialSourceStore || snap.Kind != "oauth" {
		t.Fatalf("snapshot = %+v, %v", snap, err)
	}
	if got := readAuthFile(t, path)["anthropic"].Generation; got != gen {
		t.Fatalf("rotation changed generation %q -> %q", gen, got)
	}
}

// Refresh must not move an OpenAI login to another account within the same
// generation: that needs a new login.
func TestRefresh_RejectsOpenAIAccountChange(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	path := filepath.Join(t.TempDir(), "auth.json")
	ts := newRotatingTokenServer(t, "refresh-0")
	ts.account = "acct-B"
	store := newServedStore(t, path, ts)
	if _, err := store.CommitLogin("openai", "", Credential{Type: "oauth", Access: testOpenAIJWT("acct-A", 0), Refresh: "refresh-0", Expires: expired(), AccountID: "acct-A"}); err != nil {
		t.Fatal(err)
	}
	before := fileBytes(t, path)
	key, _, err := store.GetAPIKey("openai")
	if key != "" {
		t.Fatalf("token for another account returned: err %v", err)
	}
	wantCredClass(t, err, core.CredentialReconnect)
	if got := fileBytes(t, path); string(got) != string(before) {
		t.Fatalf("account change persisted:\n%s", got)
	}
}

// TestRefreshOAuthIfGeneration_ChangedSelection: a late 401 from login A must
// not refresh or return login B's token.
func TestRefreshOAuthIfGeneration_ChangedSelection(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	path := filepath.Join(t.TempDir(), "auth.json")
	ts := newRotatingTokenServer(t, "refresh-a", "refresh-b")
	store := newServedStore(t, path, ts)
	gen, err := store.CommitLogin("anthropic", "", Credential{Type: "oauth", Access: "access-a", Refresh: "refresh-a", Expires: fresh()})
	if err != nil {
		t.Fatal(err)
	}
	snapA, err := store.ResolveSnapshot(context.Background(), "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(path).CommitLogin("anthropic", gen, Credential{Type: "oauth", Access: "access-b", Refresh: "refresh-b", Expires: fresh()}); err != nil {
		t.Fatal(err)
	}
	snap, err := store.RefreshOAuthIfGeneration(context.Background(), snapA, "access-a")
	if snap.Token != "" {
		t.Fatalf("late 401 from A received B's token %q", snap.Token)
	}
	wantCredClass(t, err, core.CredentialChanged)
	if n := ts.callCount(); n != 0 {
		t.Fatalf("refresh calls = %d, want 0", n)
	}
}

// Two Stores on one file get the same rejected token at once: exactly one
// refresh, both reuse it, and other providers survive.
func TestRefreshOAuthIfGeneration_CrossStoreSingleFlight(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	path := filepath.Join(t.TempDir(), "auth.json")
	ts := newRotatingTokenServer(t, "refresh-0")
	seed := newServedStore(t, path, ts)
	if _, err := seed.CommitLogin("anthropic", "", Credential{Type: "oauth", Access: "access-0", Refresh: "refresh-0", Expires: fresh()}); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.CommitLogin("xai", "", Credential{Type: "api_key", Key: "xai-" + "key"}); err != nil {
		t.Fatal(err)
	}
	snap, err := seed.ResolveSnapshot(context.Background(), "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	stores := []*Store{newServedStore(t, path, ts), newServedStore(t, path, ts), seed}
	var wg sync.WaitGroup
	results := make([]CredentialSnapshot, len(stores)*2)
	errs := make([]error, len(results))
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = stores[i%len(stores)].RefreshOAuthIfGeneration(context.Background(), snap, "access-0")
		}(i)
	}
	wg.Wait()
	for i := range results {
		if errs[i] != nil || results[i].Token != "access-1" || results[i].Generation != snap.Generation {
			t.Fatalf("worker %d = %+v, %v", i, results[i], errs[i])
		}
	}
	if n := ts.callCount(); n != 1 {
		t.Fatalf("refresh calls = %d, want 1", n)
	}
	disk := readAuthFile(t, path)
	if disk["xai"].Key != "xai-"+"key" || disk["anthropic"].Refresh != "refresh-1" {
		t.Fatalf("disk = %+v", disk)
	}
}

// Concurrent logins and rotations from two Stores never lose a write.
func TestStore_ConcurrentCommitAndRotation(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	path := filepath.Join(t.TempDir(), "auth.json")
	ts := newRotatingTokenServer(t, "refresh-0")
	a, b := newServedStore(t, path, ts), newServedStore(t, path, ts)
	if _, err := a.CommitLogin("anthropic", "", Credential{Type: "oauth", Access: "access-0", Refresh: "refresh-0", Expires: expired()}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	providers := []string{"p1", "p2", "p3", "p4"}
	for i, p := range providers {
		wg.Add(2)
		go func(s *Store, p string) {
			defer wg.Done()
			if _, err := s.CommitLogin(p, "", Credential{Type: "api_key", Key: p}); err != nil {
				t.Error(err)
			}
		}([]*Store{a, b}[i%2], p)
		go func(s *Store) {
			defer wg.Done()
			if _, err := s.ResolveSnapshot(context.Background(), "anthropic"); err != nil {
				t.Error(err)
			}
		}([]*Store{a, b}[i%2])
	}
	wg.Wait()
	disk := readAuthFile(t, path)
	for _, p := range providers {
		if disk[p].Key != p {
			t.Fatalf("lost %s: %+v", p, disk)
		}
	}
	if disk["anthropic"].Refresh != "refresh-1" || ts.callCount() != 1 {
		t.Fatalf("anthropic = %+v, calls %d", disk["anthropic"], ts.callCount())
	}
}
