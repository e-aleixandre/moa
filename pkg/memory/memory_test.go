package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	return New(t.TempDir(), "/test/project")
}

func TestWriteDerivesScopeFromType(t *testing.T) {
	s := newStore(t)
	if err := s.Write(Memory{Name: "who-i-am", Description: "the user", Type: TypeUser, Body: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(Memory{Name: "uses-docker", Description: "builds", Type: TypeProject, Body: "y"}); err != nil {
		t.Fatal(err)
	}
	// user → global scope, project → project scope.
	if !fileExists(filepath.Join(s.GlobalDir(), "who-i-am.md")) {
		t.Error("user fact should be in global scope")
	}
	if !fileExists(filepath.Join(s.ProjectDir(), "uses-docker.md")) {
		t.Error("project fact should be in project scope")
	}
}

func TestWriteValidation(t *testing.T) {
	s := newStore(t)
	cases := []struct {
		name string
		m    Memory
	}{
		{"bad name", Memory{Name: "Bad Name", Description: "d", Type: TypeProject, Body: "b"}},
		{"invalid type", Memory{Name: "foo", Description: "d", Type: "bogus", Body: "b"}},
		{"empty description", Memory{Name: "foo", Description: "  ", Type: TypeProject, Body: "b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := s.Write(c.m); err == nil {
				t.Errorf("expected error for %s", c.name)
			}
		})
	}
}

func TestWriteExceedsMaxSize(t *testing.T) {
	s := newStore(t)
	big := strings.Repeat("x", MaxFactSize+1)
	if err := s.Write(Memory{Name: "big", Description: "d", Type: TypeProject, Body: big}); err == nil {
		t.Fatal("expected size error")
	}
}

func TestWriteReadRoundtrip(t *testing.T) {
	s := newStore(t)
	want := Memory{Name: "foo", Description: "a hook: with colon", Type: TypeFeedback, Body: "line1\nline2"}
	if err := s.Write(want); err != nil {
		t.Fatal(err)
	}
	// feedback → global scope; read by canonical ID.
	got, ok, err := s.Read("global/foo")
	if err != nil || !ok {
		t.Fatalf("read failed: ok=%v err=%v", ok, err)
	}
	if got.Description != want.Description {
		t.Errorf("description: got %q want %q", got.Description, want.Description)
	}
	if got.Type != TypeFeedback {
		t.Errorf("type: got %q", got.Type)
	}
	if got.Body != want.Body {
		t.Errorf("body: got %q want %q", got.Body, want.Body)
	}
	if got.ID() != "global/foo" {
		t.Errorf("id: got %q", got.ID())
	}
}

func TestReadBareNameResolves(t *testing.T) {
	s := newStore(t)
	if err := s.Write(Memory{Name: "solo", Description: "d", Type: TypeProject, Body: "b"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Read("solo"); err != nil || !ok {
		t.Fatalf("bare name should resolve: ok=%v err=%v", ok, err)
	}
}

func TestReadNotFound(t *testing.T) {
	s := newStore(t)
	m, ok, err := s.Read("nope")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Errorf("should not find %v", m)
	}
}

func TestScopeCollisionIsAmbiguous(t *testing.T) {
	s := newStore(t)
	// Same name in both scopes: user→global, reference→project.
	if err := s.Write(Memory{Name: "dup", Description: "g", Type: TypeUser, Body: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(Memory{Name: "dup", Description: "p", Type: TypeReference, Body: "b"}); err != nil {
		t.Fatal(err)
	}
	// Bare name → ambiguous error.
	if _, _, err := s.Read("dup"); err == nil {
		t.Error("bare ambiguous name should error on read")
	}
	if err := s.Delete("dup"); err == nil {
		t.Error("bare ambiguous name should error on delete")
	}
	// Qualified IDs resolve each.
	if _, ok, err := s.Read("global/dup"); err != nil || !ok {
		t.Errorf("global/dup should resolve: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.Read("project/dup"); err != nil || !ok {
		t.Errorf("project/dup should resolve: ok=%v err=%v", ok, err)
	}
}

func TestDelete(t *testing.T) {
	s := newStore(t)
	if err := s.Write(Memory{Name: "gone", Description: "d", Type: TypeProject, Body: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("project/gone"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Read("project/gone"); ok {
		t.Error("should be deleted")
	}
	if err := s.Delete("project/gone"); err == nil {
		t.Error("deleting missing fact should error")
	}
}

func TestWriteNotDestructive(t *testing.T) {
	s := newStore(t)
	if err := s.Write(Memory{Name: "a", Description: "da", Type: TypeProject, Body: "ba"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(Memory{Name: "b", Description: "db", Type: TypeProject, Body: "bb"}); err != nil {
		t.Fatal(err)
	}
	// Overwriting a doesn't touch b.
	if err := s.Write(Memory{Name: "a", Description: "da2", Type: TypeProject, Body: "ba2"}); err != nil {
		t.Fatal(err)
	}
	if m, ok, _ := s.Read("project/b"); !ok || m.Body != "bb" {
		t.Errorf("b should be untouched, got ok=%v body=%q", ok, m.Body)
	}
}

func TestListSortedProjectFirst(t *testing.T) {
	s := newStore(t)
	_ = s.Write(Memory{Name: "zed", Description: "d", Type: TypeUser, Body: "b"})       // global
	_ = s.Write(Memory{Name: "alpha", Description: "d", Type: TypeProject, Body: "b"})  // project
	_ = s.Write(Memory{Name: "beta", Description: "d", Type: TypeReference, Body: "b"}) // project
	list := s.List()
	got := make([]string, len(list))
	for i, m := range list {
		got[i] = m.ID()
	}
	want := []string{"project/alpha", "project/beta", "global/zed"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order: got %v want %v", got, want)
	}
}

func TestListExcludesReservedFiles(t *testing.T) {
	s := newStore(t)
	if err := os.MkdirAll(s.ProjectDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	// A generated index and a v1 backup must never appear as facts.
	_ = os.WriteFile(filepath.Join(s.ProjectDir(), "MEMORY.md"), []byte("# index\n"), 0o600)
	_ = os.WriteFile(s.projectRoot+"/MEMORY.md.v1.bak", []byte("old\n"), 0o600)
	_ = s.Write(Memory{Name: "real", Description: "d", Type: TypeProject, Body: "b"})
	list := s.List()
	if len(list) != 1 || list[0].Name != "real" {
		t.Errorf("expected only the real fact, got %+v", list)
	}
}

func TestListSkipsMalformed(t *testing.T) {
	s := newStore(t)
	if err := os.MkdirAll(s.ProjectDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(s.ProjectDir(), "broken.md"), []byte("no frontmatter here\n"), 0o600)
	_ = s.Write(Memory{Name: "ok", Description: "d", Type: TypeProject, Body: "b"})
	list := s.List()
	if len(list) != 1 || list[0].Name != "ok" {
		t.Errorf("malformed fact should be skipped, got %+v", list)
	}
}

func TestFormatIndex(t *testing.T) {
	s := newStore(t)
	if s.FormatIndex(nil) != "" {
		t.Error("empty index should be empty string")
	}
	_ = s.Write(Memory{Name: "foo", Description: "the hook", Type: TypeProject, Body: "b"})
	idx := s.FormatIndex(s.List())
	if !strings.Contains(idx, "project/foo") || !strings.Contains(idx, "the hook") {
		t.Errorf("index missing entry: %q", idx)
	}
}

func TestParseFactVariants(t *testing.T) {
	// CRLF + quoted description with a colon + unknown type → project default.
	raw := "---\r\nname: x\r\ndescription: \"a: b\"\r\ntype: bogus\r\n---\r\n\r\nbody\r\n"
	m, err := parseFact([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if m.Description != "a: b" {
		t.Errorf("description: got %q", m.Description)
	}
	if m.Type != TypeProject {
		t.Errorf("unknown type should default to project, got %q", m.Type)
	}
	if m.Body != "body" {
		t.Errorf("body: got %q", m.Body)
	}
}

func TestParseFactErrors(t *testing.T) {
	if _, err := parseFact([]byte("no frontmatter")); err == nil {
		t.Error("missing frontmatter should error")
	}
	if _, err := parseFact([]byte("---\nname: x\nunterminated")); err == nil {
		t.Error("unterminated frontmatter should error")
	}
}

func TestFilenameAuthoritative(t *testing.T) {
	s := newStore(t)
	if err := os.MkdirAll(s.ProjectDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	// Frontmatter name disagrees with filename: filename wins for the ID.
	raw := "---\nname: wrong\ndescription: d\ntype: project\n---\n\nbody\n"
	_ = os.WriteFile(filepath.Join(s.ProjectDir(), "right.md"), []byte(raw), 0o600)
	m, ok, err := s.Read("project/right")
	if err != nil || !ok {
		t.Fatalf("read failed: ok=%v err=%v", ok, err)
	}
	if m.Name != "right" {
		t.Errorf("filename should be authoritative, got name=%q", m.Name)
	}
}

func TestMigrateV1(t *testing.T) {
	s := newStore(t)
	v1 := filepath.Join(s.projectRoot, "MEMORY.md")
	if err := os.MkdirAll(s.projectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(v1, []byte("# old memory\n- fact one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateV1IfNeeded(); err != nil {
		t.Fatal(err)
	}
	// Legacy fact created, flat file retired to .v1.bak.
	m, ok, err := s.Read("project/notas-legado-v1")
	if err != nil || !ok {
		t.Fatalf("legacy fact missing: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(m.Body, "fact one") {
		t.Errorf("legacy body lost content: %q", m.Body)
	}
	if fileExists(v1) {
		t.Error("flat v1 file should be renamed away")
	}
	if !fileExists(v1 + ".v1.bak") {
		t.Error("v1 backup should exist")
	}
	// Idempotent: second run is a no-op (no flat file left).
	if err := s.MigrateV1IfNeeded(); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateV1RetriesAfterPartial(t *testing.T) {
	s := newStore(t)
	v1 := filepath.Join(s.projectRoot, "MEMORY.md")
	if err := os.MkdirAll(s.projectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(v1, []byte("content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Simulate a partial prior run: the fact was written but the flat file was
	// never renamed. The next run must still complete the migration.
	if err := os.MkdirAll(s.ProjectDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = s.Write(Memory{Name: "notas-legado-v1", Description: "d", Type: TypeProject, Body: "half"})
	if err := s.MigrateV1IfNeeded(); err != nil {
		t.Fatal(err)
	}
	if fileExists(v1) {
		t.Error("partial migration should have been completed (flat file retired)")
	}
}

func TestMigrateNoV1(t *testing.T) {
	s := newStore(t)
	if err := s.MigrateV1IfNeeded(); err != nil {
		t.Fatalf("no v1 file should be a clean no-op: %v", err)
	}
}

func TestPermissions(t *testing.T) {
	s := newStore(t)
	if err := s.Write(Memory{Name: "secret", Description: "d", Type: TypeProject, Body: "b"}); err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(s.ProjectDir())
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm()&0o077 != 0 {
		t.Errorf("dir too permissive: %o", dirInfo.Mode().Perm())
	}
	fileInfo, err := os.Stat(filepath.Join(s.ProjectDir(), "secret.md"))
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm()&0o077 != 0 {
		t.Errorf("file too permissive: %o", fileInfo.Mode().Perm())
	}
}
