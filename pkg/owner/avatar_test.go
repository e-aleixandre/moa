package owner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	// Golden values, computed before triangle and cloud became selectable:
	// adding shapes must not change the face of any owner that never chose.
	cases := map[string]Avatar{
		"winerim-backend": {Shape: "blob", Color: "mint"},
		"winerim-web":     {Shape: "drop", Color: "mauve"},
		"moa":             {Shape: "circle", Color: "lilac"},
		"facturas-api":    {Shape: "drop", Color: "mauve"},
		"catas-app":       {Shape: "circle", Color: "sage"},
		"etiquetas-pdf":   {Shape: "pill", Color: "peach"},
		"landing-2026":    {Shape: "blob", Color: "rose"},
		"sommelier-bot":   {Shape: "circle", Color: "azure"},
		"dotfiles":        {Shape: "pill", Color: "sky"},
		"":                {Shape: "squircle", Color: "sage"},
	}
	for key, want := range cases {
		if got := DefaultAvatar(key); got != want {
			t.Fatalf("DefaultAvatar(%q) = %+v, want %+v", key, got, want)
		}
	}
}

// Triangle and cloud are selectable: accepted on create and read back from
// owner.json exactly as chosen.
func TestSelectableShapesRoundTripThroughOwnerJSON(t *testing.T) {
	for _, shape := range []string{"triangle", "cloud"} {
		cfg := t.TempDir()
		store := NewStore(cfg)
		want := Avatar{Shape: shape, Color: "sky"}
		own, err := store.Create(t.TempDir(), "Winerim", "", "", true, want)
		if err != nil {
			t.Fatalf("create with %s: %v", shape, err)
		}
		loaded, found, err := NewStore(cfg).FindByCodebase(own.CodebaseKey)
		if err != nil || !found {
			t.Fatalf("reload: found=%v err=%v", found, err)
		}
		if loaded.Avatar != want || loaded.ResolvedAvatar() != want {
			t.Fatalf("%s: loaded %+v, resolved %+v, want %+v", shape, loaded.Avatar, loaded.ResolvedAvatar(), want)
		}
	}
}

// The default pool is the original six, in their original order, and the
// opt-in shapes never come out of the hash.
func TestDefaultAvatarNeverPicksAnOptInShape(t *testing.T) {
	if !slices.Equal(DefaultAvatarShapes, []string{"circle", "squircle", "blob", "hexagon", "drop", "pill"}) {
		t.Fatalf("default pool changed: %v", DefaultAvatarShapes)
	}
	if !slices.Contains(AvatarShapes, "triangle") || !slices.Contains(AvatarShapes, "cloud") {
		t.Fatalf("selectable shapes = %v", AvatarShapes)
	}
	for i := range 5000 {
		got := DefaultAvatar(fmt.Sprintf("codebase-%d", i)).Shape
		if !slices.Contains(DefaultAvatarShapes, got) {
			t.Fatalf("DefaultAvatar picked %q", got)
		}
	}
}
