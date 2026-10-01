package session

import "testing"

// The creator's timezone is written at creation and must survive every
// runtime snapshot.
func TestCreatorTZIsPreserved(t *testing.T) {
	meta := map[string]any{
		MetaCreatorTZ: "Europe/Madrid",
		MetaModel:     "anthropic/x",
	}
	got := PreservedMetadata(meta)
	if got[MetaCreatorTZ] != meta[MetaCreatorTZ] {
		t.Fatalf("%s not preserved: %+v", MetaCreatorTZ, got)
	}
	if _, ok := got[MetaModel]; ok {
		t.Fatal("runtime key preserved")
	}
}
