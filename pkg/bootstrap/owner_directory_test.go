package bootstrap

import (
	"testing"

	"github.com/e-aleixandre/moa/pkg/owner"
)

func TestOwnerDirectoryReadsOwnerJSONEachTime(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	store, err := owner.Default()
	if err != nil {
		t.Fatal(err)
	}
	moa, err := store.Create(t.TempDir(), "moa", "", "", true, owner.Avatar{})
	if err != nil {
		t.Fatal(err)
	}
	infra, err := store.Create(t.TempDir(), "Infra", "", "", true, owner.Avatar{})
	if err != nil {
		t.Fatal(err)
	}
	moa.SessionID = "s-moa"
	if err := store.Save(moa); err != nil {
		t.Fatal(err)
	}
	var d ownerDirectory
	if ref, ok, err := d.OwnerOf("s-moa"); err != nil || !ok || ref.ID != moa.ID || ref.ProjectKey != moa.CodebaseKey {
		t.Fatalf("OwnerOf = %+v %v %v", ref, ok, err)
	}
	if _, ok, _ := d.OwnerOf("s-child"); ok {
		t.Fatal("a non-owner session resolved as an owner")
	}
	if ref, ok, _ := d.Find("infra"); !ok || ref.ID != infra.ID {
		t.Fatalf("Find by name = %+v", ref)
	}
	if ref, ok, _ := d.Find(infra.ID); !ok || ref.Name != "Infra" {
		t.Fatalf("Find by id = %+v", ref)
	}
	if ok, _ := d.CanSend(moa.ID, infra.ID); ok {
		t.Fatal("permission without a grant")
	}
	moa.CanSendTasksTo = []string{infra.ID}
	if err := store.Save(moa); err != nil {
		t.Fatal(err)
	}
	if ok, _ := d.CanSend(moa.ID, infra.ID); !ok {
		t.Fatal("grant not read")
	}
	if ok, _ := d.CanSend(infra.ID, moa.ID); ok {
		t.Fatal("permission is not one-way")
	}
}
