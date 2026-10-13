package owner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The permission list is additive: absent from an owner.json that never had
// one, kept across a save, and one-way.
func TestCanSendTasksToRoundTrips(t *testing.T) {
	store := NewStore(t.TempDir())
	a, err := store.Create(t.TempDir(), "moa", "", "", true, Avatar{})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.CodebaseDir(a.CodebaseKey), "owner.json")
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "can_send_tasks_to") {
		t.Fatalf("new owner.json carries an empty permission list: %s", raw)
	}
	a.CanSendTasksTo = []string{"own_infra"}
	if err := store.Save(a); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.FindByID(a.ID)
	if err != nil || !found || !got.MaySendTasksTo("own_infra") || got.MaySendTasksTo("own_other") {
		t.Fatalf("reloaded = %+v, %v", got.CanSendTasksTo, err)
	}
}
