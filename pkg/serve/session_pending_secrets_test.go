package serve

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The roster is all a client has for a session it holds no socket to, so it
// names the open password requests by id: enough to tell a new one from one
// already announced, and nothing of what they ask.
func TestRosterNamesOpenSecretRequests(t *testing.T) {
	mgr := newOwnerTestManager(t, context.Background())
	root := t.TempDir()
	ownerWithSession(t, mgr, root, "Winerim")
	child, err := mgr.CreateSession(CreateOpts{CWD: root, Title: "blocked", Origin: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	quiet, err := mgr.CreateSession(CreateOpts{CWD: root, Title: "quiet", Origin: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	ids := func(id string) ([]string, string) {
		t.Helper()
		for _, info := range mgr.List() {
			if info.ID == id {
				raw, _ := json.Marshal(info)
				return info.PendingSecretIDs, string(raw)
			}
		}
		t.Fatalf("session %s not in the roster", id)
		return nil, ""
	}

	if got, raw := ids(child.ID); len(got) != 0 || strings.Contains(raw, "pending_secret") {
		t.Fatalf("idle session reports %v, json %s", got, raw)
	}
	first, cancelFirst := askSecret(t, child)
	if got, raw := ids(child.ID); len(got) != 1 || got[0] != first || strings.Contains(raw, secretCanary) {
		t.Fatalf("blocked session reports %v (want [%s]); json leaks or omits: %s", got, first, raw)
	}
	if got, _ := ids(quiet.ID); len(got) != 0 {
		t.Fatalf("another session reports %v", got)
	}

	// A replacement keeps the count at one but not the identity.
	cancelFirst()
	pollUntil(t, 5*time.Second, "first request gone", func() bool { got, _ := ids(child.ID); return len(got) == 0 })
	second, _ := askSecret(t, child)
	if got, _ := ids(child.ID); len(got) != 1 || got[0] != second || second == first {
		t.Fatalf("replacement reports %v, want [%s] distinct from %s", got, second, first)
	}
}
