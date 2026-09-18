package owner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestCreateStoresTheChosenAvatar(t *testing.T) {
	cfg := t.TempDir()
	root := t.TempDir()
	store := NewStore(cfg)

	want := Avatar{Shape: "hexagon", Color: "lilac"}
	own, err := store.Create(root, "Winerim", "", "", true, want)
	if err != nil {
		t.Fatal(err)
	}
	if own.Avatar != want {
		t.Fatalf("avatar = %+v, want %+v", own.Avatar, want)
	}
	data, err := os.ReadFile(filepath.Join(cfg, "codebases", own.CodebaseKey, "owner.json"))
	if err != nil {
		t.Fatal(err)
	}
	var onDisk Owner
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk.Avatar != want {
		t.Fatalf("persisted avatar = %+v, want %+v", onDisk.Avatar, want)
	}
}

func TestCreateRefusesAnAvatarOutsideTheClosedLists(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.Create(t.TempDir(), "Winerim", "", "", true, Avatar{Shape: "star", Color: "lilac"}); err == nil {
		t.Fatal("a shape outside the list was accepted")
	}
	if _, err := store.Create(t.TempDir(), "Winerim", "", "", true, Avatar{Shape: "circle", Color: "crimson"}); err == nil {
		t.Fatal("a colour outside the list was accepted")
	}
}

func TestCreateDefaultsTheAvatarDeterministically(t *testing.T) {
	root := t.TempDir()
	own, err := NewStore(t.TempDir()).Create(root, "Winerim", "", "", true, Avatar{})
	if err != nil {
		t.Fatal(err)
	}
	want := DefaultAvatar(core.CodebaseKey(own.Root))
	if own.Avatar != want {
		t.Fatalf("default avatar = %+v, want %+v", own.Avatar, want)
	}
	if !own.Avatar.Valid() {
		t.Fatalf("default avatar %+v is not in the closed lists", own.Avatar)
	}
}

// An owner.json written before avatars existed has no field at all, and must
// still resolve to a face every client can draw — without a migration.
func TestResolvedAvatarFallsBackForAnOwnerWithoutOne(t *testing.T) {
	var own Owner
	if err := json.Unmarshal([]byte(`{"id":"own_1","name":"Winerim","codebase_key":"winerim"}`), &own); err != nil {
		t.Fatal(err)
	}
	if !own.Avatar.IsZero() {
		t.Fatalf("avatar = %+v, want the zero value", own.Avatar)
	}
	got := own.ResolvedAvatar()
	if got != DefaultAvatar("winerim") || !got.Valid() {
		t.Fatalf("resolved avatar = %+v", got)
	}
}

// `sand` left the palette because it was the amber-ish tile and amber is the
// "waiting on you" dot. An owner that chose it keeps its SHAPE and gets the
// nearest surviving colour, rather than being sent back to the hash and coming
// out as a different face entirely.
func TestResolvedAvatarMigratesARetiredColour(t *testing.T) {
	own := Owner{CodebaseKey: "winerim", Avatar: Avatar{Shape: "hexagon", Color: "sand"}}
	got := own.ResolvedAvatar()
	if got != (Avatar{Shape: "hexagon", Color: "sage"}) {
		t.Fatalf("resolved avatar = %+v, want hexagon/sage", got)
	}
	if !got.Valid() {
		t.Fatalf("migrated avatar %+v is not in the closed lists", got)
	}
	// A colour that never existed is not a rename: that one does fall back.
	unknown := Owner{CodebaseKey: "winerim", Avatar: Avatar{Shape: "hexagon", Color: "crimson"}}
	if got := unknown.ResolvedAvatar(); got != DefaultAvatar("winerim") {
		t.Fatalf("unknown colour resolved to %+v, want the deterministic default", got)
	}
}

// The default is a pure function of the codebase key, which is what makes the
// Go and the JS implementations agree without either one asking the other.
// These values are the ones src/components/Owners/OwnerAvatar.jsx computes.
func TestDefaultAvatarMatchesTheFrontendHash(t *testing.T) {
	cases := map[string]Avatar{
		"winerim-backend": {Shape: "blob", Color: "mint"},
		"winerim-web":     {Shape: "drop", Color: "mauve"},
		"moa":             {Shape: "circle", Color: "lilac"},
	}
	for key, want := range cases {
		if got := DefaultAvatar(key); got != want {
			t.Fatalf("DefaultAvatar(%q) = %+v, want %+v", key, got, want)
		}
	}
}
