package auth

import (
	"os"
	"path/filepath"
	"testing"
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

	store.Set("test", Credential{Type: "api_key", Key: "secret"})

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

	store.Set("anthropic", Credential{Type: "api_key", Key: "sk-ant-api03-stored"})

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
