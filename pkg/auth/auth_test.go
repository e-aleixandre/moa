package auth

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStore_SetGetRemove(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "auth.json")
	store := NewStore(path)

	// Initially empty
	_, ok := store.Get("anthropic")
	if ok {
		t.Fatal("expected no credential initially")
	}

	// Set API key
	err := store.Set("anthropic", Credential{Type: "api_key", Key: "sk-ant-api03-test"})
	if err != nil {
		t.Fatal(err)
	}

	cred, ok := store.Get("anthropic")
	if !ok {
		t.Fatal("expected credential after Set")
	}
	if cred.Type != "api_key" || cred.Key != "sk-ant-api03-test" {
		t.Fatalf("unexpected credential: %+v", cred)
	}

	// Persisted to disk
	store2 := NewStore(path)
	cred2, ok := store2.Get("anthropic")
	if !ok || cred2.Key != "sk-ant-api03-test" {
		t.Fatal("credential not persisted to disk")
	}

	// Remove
	err = store.Remove("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	_, ok = store.Get("anthropic")
	if ok {
		t.Fatal("credential should be removed")
	}
}

func TestStore_FilePermissions(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "auth.json")
	store := NewStore(path)

	if err := store.Set("test", Credential{Type: "api_key", Key: "secret"}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("expected 0600 permissions, got %o", perm)
	}
}

func TestStore_GetAPIKey_EnvVar(t *testing.T) {
	tmp := t.TempDir()
	store := NewStore(filepath.Join(tmp, "auth.json"))

	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-api03-from-env")

	key, isOAuth, err := store.GetAPIKey("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if key != "sk-ant-api03-from-env" {
		t.Errorf("expected env key, got %q", key)
	}
	if isOAuth {
		t.Error("env API key should not be OAuth")
	}
}

func TestStore_GetAPIKey_EnvVar_OAuth(t *testing.T) {
	tmp := t.TempDir()
	store := NewStore(filepath.Join(tmp, "auth.json"))

	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-oat-from-env")

	key, isOAuth, err := store.GetAPIKey("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if key != "sk-ant-oat-from-env" {
		t.Errorf("expected env key, got %q", key)
	}
	if !isOAuth {
		t.Error("sk-ant-oat key should be detected as OAuth")
	}
}

func TestStore_GetAPIKey_StoredAPIKey(t *testing.T) {
	tmp := t.TempDir()
	store := NewStore(filepath.Join(tmp, "auth.json"))

	// Unset env var to test stored key
	t.Setenv("ANTHROPIC_API_KEY", "")

	if err := store.Set("anthropic", Credential{Type: "api_key", Key: "sk-ant-api03-stored"}); err != nil {
		t.Fatal(err)
	}

	key, isOAuth, err := store.GetAPIKey("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if key != "sk-ant-api03-stored" {
		t.Errorf("expected stored key, got %q", key)
	}
	if isOAuth {
		t.Error("API key should not be OAuth")
	}
}

func TestStore_GetAPIKey_NoCredentials(t *testing.T) {
	tmp := t.TempDir()
	store := NewStore(filepath.Join(tmp, "auth.json"))

	t.Setenv("ANTHROPIC_API_KEY", "")

	_, _, err := store.GetAPIKey("anthropic")
	if err == nil {
		t.Fatal("expected error for no credentials")
	}
}

// TestStore_GetAPIKey_ConcurrentRefreshSingleFlight verifies that many
// concurrent GetAPIKey calls with an expired OAuth token trigger exactly ONE
// network refresh. Without single-flighting, each goroutine would refresh with
// the same rotating refresh token and invalidate the others.
func TestStore_GetAPIKey_ConcurrentRefreshSingleFlight(t *testing.T) {
	tmp := t.TempDir()
	store := NewStore(filepath.Join(tmp, "auth.json"))
	t.Setenv("ANTHROPIC_API_KEY", "")

	// Expired OAuth credential.
	if err := store.Set("anthropic", Credential{
		Type:    "oauth",
		Access:  "old-access",
		Refresh: "refresh-0",
		Expires: time.Now().UnixMilli() - 1000,
	}); err != nil {
		t.Fatal(err)
	}

	var calls int32
	store.refresh = func(provider, refreshToken string) (*OAuthCredentials, error) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(20 * time.Millisecond) // widen the overlap window
		return &OAuthCredentials{
			Access:  "new-access",
			Refresh: "refresh-1",
			Expires: time.Now().UnixMilli() + 3_600_000,
		}, nil
	}

	const n = 8
	var wg sync.WaitGroup
	keys := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k, _, err := store.GetAPIKey("anthropic")
			keys[i], errs[i] = k, err
		}(i)
	}
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("refresh called %d times, want exactly 1 (single-flight)", got)
	}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		if keys[i] != "new-access" {
			t.Fatalf("goroutine %d got %q, want new-access", i, keys[i])
		}
	}
}

// TestStore_PeekOAuthToken verifies the read-only usage getter: it reports the
// OAuth/validity state without ever triggering a refresh (which would rotate the
// shared refresh token from a read-only caller).
func TestStore_PeekOAuthToken(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")

	t.Run("valid oauth", func(t *testing.T) {
		store := NewStore(filepath.Join(t.TempDir(), "auth.json"))
		store.refresh = func(string, string) (*OAuthCredentials, error) {
			t.Fatal("PeekOAuthToken must not refresh")
			return nil, nil
		}
		if err := store.Set("anthropic", Credential{
			Type: "oauth", Access: "live-access", Refresh: "r0",
			Expires: time.Now().UnixMilli() + 3_600_000,
		}); err != nil {
			t.Fatal(err)
		}
		token, isOAuth, valid := store.PeekOAuthToken("anthropic")
		if token != "live-access" || !isOAuth || !valid {
			t.Fatalf("got (%q, %v, %v), want (live-access, true, true)", token, isOAuth, valid)
		}
	})

	t.Run("expired oauth does not refresh", func(t *testing.T) {
		store := NewStore(filepath.Join(t.TempDir(), "auth.json"))
		store.refresh = func(string, string) (*OAuthCredentials, error) {
			t.Fatal("PeekOAuthToken must not refresh an expired token")
			return nil, nil
		}
		if err := store.Set("anthropic", Credential{
			Type: "oauth", Access: "old-access", Refresh: "r0",
			Expires: time.Now().UnixMilli() - 1000,
		}); err != nil {
			t.Fatal(err)
		}
		token, isOAuth, valid := store.PeekOAuthToken("anthropic")
		if token != "" || !isOAuth || valid {
			t.Fatalf("got (%q, %v, %v), want (\"\", true, false)", token, isOAuth, valid)
		}
	})

	t.Run("api key", func(t *testing.T) {
		store := NewStore(filepath.Join(t.TempDir(), "auth.json"))
		if err := store.Set("anthropic", Credential{Type: "api_key", Key: "sk-ant-api03-x"}); err != nil {
			t.Fatal(err)
		}
		token, isOAuth, valid := store.PeekOAuthToken("anthropic")
		if token != "" || isOAuth || valid {
			t.Fatalf("got (%q, %v, %v), want (\"\", false, false)", token, isOAuth, valid)
		}
	})

	t.Run("no credentials", func(t *testing.T) {
		store := NewStore(filepath.Join(t.TempDir(), "auth.json"))
		token, isOAuth, valid := store.PeekOAuthToken("anthropic")
		if token != "" || isOAuth || valid {
			t.Fatalf("got (%q, %v, %v), want (\"\", false, false)", token, isOAuth, valid)
		}
	})

	t.Run("env oauth token", func(t *testing.T) {
		store := NewStore(filepath.Join(t.TempDir(), "auth.json"))
		t.Setenv("ANTHROPIC_API_KEY", "sk-ant-oat-env")
		token, isOAuth, valid := store.PeekOAuthToken("anthropic")
		if token != "sk-ant-oat-env" || !isOAuth || !valid {
			t.Fatalf("got (%q, %v, %v), want (sk-ant-oat-env, true, true)", token, isOAuth, valid)
		}
	})

	t.Run("env api key is inert", func(t *testing.T) {
		store := NewStore(filepath.Join(t.TempDir(), "auth.json"))
		t.Setenv("ANTHROPIC_API_KEY", "sk-ant-api03-env")
		token, isOAuth, valid := store.PeekOAuthToken("anthropic")
		if token != "" || isOAuth || valid {
			t.Fatalf("got (%q, %v, %v), want (\"\", false, false)", token, isOAuth, valid)
		}
	})
}

func TestIsOAuthToken(t *testing.T) {
	tests := []struct {
		key    string
		expect bool
	}{
		{"sk-ant-api03-abc123", false},
		{"sk-ant-oat-abc123", true},
		{"", false},
		{"random-key", false},
	}

	for _, tt := range tests {
		got := IsOAuthToken(tt.key)
		if got != tt.expect {
			t.Errorf("IsOAuthToken(%q) = %v, want %v", tt.key, got, tt.expect)
		}
	}
}
