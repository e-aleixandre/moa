package push

import (
	"path/filepath"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"
)

func sub(endpoint string) webpush.Subscription {
	return webpush.Subscription{
		Endpoint: endpoint,
		Keys:     webpush.Keys{P256dh: "p", Auth: "a"},
	}
}

func TestStore_AddAllRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subs.json")
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if s.Len() != 0 {
		t.Fatalf("fresh store not empty: %d", s.Len())
	}

	if err := s.Add(sub("https://push.example/a")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.Add(sub("https://push.example/b")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Re-adding the same endpoint replaces, does not duplicate.
	if err := s.Add(sub("https://push.example/a")); err != nil {
		t.Fatalf("Add dup: %v", err)
	}
	if s.Len() != 2 {
		t.Fatalf("expected 2 subs, got %d", s.Len())
	}

	if err := s.Remove("https://push.example/a"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if s.Len() != 1 {
		t.Fatalf("expected 1 sub after remove, got %d", s.Len())
	}
	// Removing a missing endpoint is a no-op.
	if err := s.Remove("https://push.example/missing"); err != nil {
		t.Fatalf("Remove missing: %v", err)
	}
}

func TestStore_Persistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subs.json")
	s1, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := s1.Add(sub("https://push.example/x")); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// A fresh store over the same path sees the persisted subscription.
	s2, err := NewStore(path)
	if err != nil {
		t.Fatalf("reload NewStore: %v", err)
	}
	if s2.Len() != 1 {
		t.Fatalf("expected 1 persisted sub, got %d", s2.Len())
	}
	if s2.All()[0].Endpoint != "https://push.example/x" {
		t.Fatalf("wrong endpoint: %q", s2.All()[0].Endpoint)
	}
}
