package session

import (
	"strings"
	"testing"
)

func TestValidateEntries_Valid(t *testing.T) {
	entries := []Entry{
		{ID: "a", ParentID: "", Type: EntryMessage},
		{ID: "b", ParentID: "a", Type: EntryMessage},
		{ID: "c", ParentID: "b", Type: EntryMessage},
	}
	if err := ValidateEntries(entries, "c"); err != nil {
		t.Fatalf("should be valid: %v", err)
	}
}

func TestValidateEntries_Empty(t *testing.T) {
	if err := ValidateEntries(nil, ""); err != nil {
		t.Fatalf("empty should be valid: %v", err)
	}
}

func TestValidateEntries_DuplicateID(t *testing.T) {
	entries := []Entry{
		{ID: "a", ParentID: "", Type: EntryMessage},
		{ID: "a", ParentID: "", Type: EntryMessage},
	}
	err := ValidateEntries(entries, "a")
	if err == nil {
		t.Fatal("should detect duplicate")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("error should mention duplicate: %v", err)
	}
}

func TestValidateEntries_OrphanParent(t *testing.T) {
	entries := []Entry{
		{ID: "a", ParentID: "missing", Type: EntryMessage},
	}
	err := ValidateEntries(entries, "a")
	if err == nil {
		t.Fatal("should detect missing parent")
	}
	if !strings.Contains(err.Error(), "missing parent") {
		t.Fatalf("error should mention missing parent: %v", err)
	}
}

func TestValidateEntries_LeafNotFound(t *testing.T) {
	entries := []Entry{
		{ID: "a", ParentID: "", Type: EntryMessage},
	}
	err := ValidateEntries(entries, "missing_leaf")
	if err == nil {
		t.Fatal("should detect missing leaf")
	}
	if !strings.Contains(err.Error(), "leaf") {
		t.Fatalf("error should mention leaf: %v", err)
	}
}

func TestValidateEntries_Cycle(t *testing.T) {
	entries := []Entry{
		{ID: "a", ParentID: "b", Type: EntryMessage},
		{ID: "b", ParentID: "a", Type: EntryMessage},
	}
	err := ValidateEntries(entries, "a")
	if err == nil {
		t.Fatal("should detect cycle")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("error should mention cycle: %v", err)
	}
}

func TestValidateEntries_BranchedTree(t *testing.T) {
	// Valid tree with branches:
	//   a → b → c
	//   a → d
	entries := []Entry{
		{ID: "a", ParentID: "", Type: EntryMessage},
		{ID: "b", ParentID: "a", Type: EntryMessage},
		{ID: "c", ParentID: "b", Type: EntryMessage},
		{ID: "d", ParentID: "a", Type: EntryMessage},
	}
	// Valid with leaf at c
	if err := ValidateEntries(entries, "c"); err != nil {
		t.Fatalf("branched tree should be valid: %v", err)
	}
	// Valid with leaf at d
	if err := ValidateEntries(entries, "d"); err != nil {
		t.Fatalf("branched tree should be valid with leaf d: %v", err)
	}
}
