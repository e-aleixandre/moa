package push

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrGenerateVAPID_GeneratesAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vapid.json")

	v1, err := LoadOrGenerateVAPID(path)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if v1.Public == "" || v1.Private == "" {
		t.Fatal("generated keys are empty")
	}

	// File exists with 0600 perms.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %v, want 0600", perm)
	}

	// Second call loads the SAME keys (stable across restarts).
	v2, err := LoadOrGenerateVAPID(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if v2 != v1 {
		t.Errorf("keys not stable: %+v vs %+v", v1, v2)
	}
}

func TestLoadOrGenerateVAPID_CorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vapid.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrGenerateVAPID(path); err == nil {
		t.Fatal("expected error on corrupt file")
	}
}
