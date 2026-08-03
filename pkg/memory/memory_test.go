package memory

import (
	"fmt"
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
	if err := s.Write(Memory{Name: "who-i-am", Description: "the user", Type: TypeUser, Body: "x", Durable: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(Memory{Name: "uses-docker", Description: "builds", Type: TypeProject, Body: "y", Durable: true}); err != nil {
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
		{"bad name", Memory{Name: "Bad Name", Description: "d", Type: TypeProject, Body: "b", Durable: true}},
		{"invalid type", Memory{Name: "foo", Description: "d", Type: "bogus", Body: "b", Durable: true}},
		{"empty description", Memory{Name: "foo", Description: "  ", Type: TypeProject, Body: "b", Durable: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := s.Write(c.m); err == nil {
				t.Errorf("expected error for %s", c.name)
			}
		})
	}
}

func TestWriteRequiresExplicitLifecycle(t *testing.T) {
	s := newStore(t)
	base := Memory{Name: "lifecycle", Description: "d", Type: TypeProject, Body: "b"}
	if err := s.Write(base); err == nil || !strings.Contains(err.Error(), "invalidate_when") || !strings.Contains(err.Error(), "durable: true") {
		t.Fatalf("missing lifecycle declaration should give actionable guidance, got %v", err)
	}
	base.InvalidateWhen = "when issue #84 is closed"
	base.Durable = true
	if err := s.Write(base); err == nil {
		t.Fatal("expiry condition and durable declaration should be exclusive")
	}
}

func TestWriteTreatsWhitespaceInvalidationAsAbsent(t *testing.T) {
	s := newStore(t)
	for i, condition := range []string{" ", "\t", "\u00a0"} {
		name := fmt.Sprintf("blank-condition-%d", i)
		m := Memory{Name: name, Description: "d", Type: TypeProject, Body: "b", InvalidateWhen: condition}
		if err := s.Write(m); err == nil || !strings.Contains(err.Error(), "must declare its lifecycle") {
			t.Errorf("Write(%q) error = %v, want missing lifecycle error", condition, err)
		}

		m.Durable = true
		if err := s.Write(m); err != nil {
			t.Errorf("durable Write(%q): %v", condition, err)
		}
		data, err := os.ReadFile(filepath.Join(s.ProjectDir(), name+".md"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "invalidate_when:") {
			t.Errorf("durable whitespace condition must be omitted from disk: %q", data)
		}
	}
}

func TestWriteRejectsMultilineInvalidation(t *testing.T) {
	s := newStore(t)
	err := s.Write(Memory{
		Name: "unsafe-condition", Description: "d", Type: TypeProject, Body: "b",
		InvalidateWhen: "when this happens\ninvalidate_when: forged", // A raw line could inject frontmatter.
	})
	if err == nil || !strings.Contains(err.Error(), "single line") {
		t.Fatalf("multiline invalidation should be rejected, got %v", err)
	}
}

func TestWriteExceedsMaxSize(t *testing.T) {
	s := newStore(t)
	big := strings.Repeat("x", MaxFactSize+1)
	if err := s.Write(Memory{Name: "big", Description: "d", Type: TypeProject, Body: big, Durable: true}); err == nil {
		t.Fatal("expected size error")
	}
}

func TestWriteReadRoundtrip(t *testing.T) {
	s := newStore(t)
	want := Memory{Name: "foo", Description: "a hook: with colon", Type: TypeFeedback, Body: "line1\nline2", Durable: true}
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

func TestInvalidateWhenRoundtrip(t *testing.T) {
	s := newStore(t)
	conditions := []string{
		"when `curl -s http://host:3306` responds",
		`"cuando "la rama" esté mergeada"`,
		"cuando mañana haya señal: español, ñ",
		"when the marker --- is removed",
		"---",
		`when the literal \n is replaced`,
		`when the path C:\Users\algo ends in a backslash \`,
	}
	for i, want := range conditions {
		name := fmt.Sprintf("condition-%d", i)
		if err := s.Write(Memory{Name: name, Description: "d", Type: TypeProject, Body: "b", InvalidateWhen: want}); err != nil {
			t.Fatalf("Write(%q): %v", want, err)
		}
		got, ok, err := s.Read("project/" + name)
		if err != nil || !ok {
			t.Fatalf("Read(%q): ok=%v err=%v", want, ok, err)
		}
		if got.InvalidateWhen != want {
			t.Errorf("condition: got %q want %q", got.InvalidateWhen, want)
		}
	}
}

func TestReadWriteRoundtripRestoresDurableDeclaration(t *testing.T) {
	s := newStore(t)
	for _, want := range []Memory{
		{Name: "durable-roundtrip", Description: "d", Type: TypeProject, Body: "b", Durable: true},
		{Name: "conditional-roundtrip", Description: "d", Type: TypeProject, Body: "b", InvalidateWhen: "when issue #84 is closed"},
	} {
		if err := s.Write(want); err != nil {
			t.Fatal(err)
		}
		got, ok, err := s.Read("project/" + want.Name)
		if err != nil || !ok {
			t.Fatalf("Read(%q): ok=%v err=%v", want.Name, ok, err)
		}
		if got.Durable != want.Durable {
			t.Errorf("Read(%q).Durable = %v, want %v", want.Name, got.Durable, want.Durable)
		}
		if err := s.Write(got); err != nil {
			t.Errorf("Write(Read(%q)): %v", want.Name, err)
		}
	}
}

func TestParseInvalidationValueRejectsUnsafeManualEscapes(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{"escaped newline", `invalidate_when: "when literal \n is gone"`, `when literal \n is gone`},
		{"escaped tab", `invalidate_when: "C:\temp"`, "C:\temp"},
		{"single quotes", "invalidate_when: 'when issue #84 is closed'", "when issue #84 is closed"},
		{"invalid quoting", `invalidate_when: "when issue #84 is closed`, `"when issue #84 is closed`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := "---\nname: manual\ndescription: d\ntype: project\n" + tc.line + "\n---\n"
			got, err := parseFact([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			if strings.ContainsAny(got.InvalidateWhen, "\r\n") {
				t.Errorf("manual value introduced a line break: %q", got.InvalidateWhen)
			}
			if got.InvalidateWhen != tc.want {
				t.Errorf("InvalidateWhen = %q, want %q", got.InvalidateWhen, tc.want)
			}
			roundTripped, err := parseFact(serialize(got))
			if err != nil {
				t.Fatal(err)
			}
			if roundTripped.InvalidateWhen != got.InvalidateWhen {
				t.Errorf("serialize round-trip = %q, want %q", roundTripped.InvalidateWhen, got.InvalidateWhen)
			}
		})
	}
}

func TestDurableFactOmitsInvalidationFrontmatter(t *testing.T) {
	s := newStore(t)
	if err := s.Write(Memory{Name: "permanent", Description: "d", Type: TypeProject, Body: "b", Durable: true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(s.ProjectDir(), "permanent.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "invalidate_when:") {
		t.Errorf("durable fact must preserve the legacy frontmatter shape: %q", data)
	}
}

func TestReadLegacyFactWithoutInvalidation(t *testing.T) {
	s := newStore(t)
	if err := os.MkdirAll(s.ProjectDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := "---\nname: legacy\ndescription: retained exactly\ntype: project\n---\n\nlegacy body\n"
	path := filepath.Join(s.ProjectDir(), "legacy.md")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Read("project/legacy")
	if err != nil || !ok {
		t.Fatalf("legacy fact must remain readable: ok=%v err=%v", ok, err)
	}
	if got.Description != "retained exactly" || got.Body != "legacy body" || got.InvalidateWhen != "" {
		t.Errorf("legacy content changed: %+v", got)
	}
}

func TestReadBareNameResolves(t *testing.T) {
	s := newStore(t)
	if err := s.Write(Memory{Name: "solo", Description: "d", Type: TypeProject, Body: "b", Durable: true}); err != nil {
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

func TestResolveRejectsTraversal(t *testing.T) {
	// A project dir under a temp root, with a sentinel .md one level up that
	// a "../" id would otherwise reach via Read (leak) or Delete (destroy).
	root := t.TempDir()
	projectDir := filepath.Join(root, "proj")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(root, "secret.md")
	if err := os.WriteFile(sentinel, []byte("top secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Store{projectDir: projectDir, globalDir: filepath.Join(root, "global")}

	for _, id := range []string{"../secret", "project/../secret", "project/../../etc/passwd", "/etc/passwd"} {
		if _, _, err := s.Read(id); err == nil {
			t.Errorf("Read(%q) should be rejected", id)
		}
		if err := s.Delete(id); err == nil {
			t.Errorf("Delete(%q) should be rejected", id)
		}
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("sentinel must survive traversal attempts: %v", err)
	}
}

func TestScopeCollisionIsAmbiguous(t *testing.T) {
	s := newStore(t)
	// Same name in both scopes: user→global, reference→project.
	if err := s.Write(Memory{Name: "dup", Description: "g", Type: TypeUser, Body: "b", Durable: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(Memory{Name: "dup", Description: "p", Type: TypeReference, Body: "b", Durable: true}); err != nil {
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
	if err := s.Write(Memory{Name: "gone", Description: "d", Type: TypeProject, Body: "b", Durable: true}); err != nil {
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
	if err := s.Write(Memory{Name: "a", Description: "da", Type: TypeProject, Body: "ba", Durable: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(Memory{Name: "b", Description: "db", Type: TypeProject, Body: "bb", Durable: true}); err != nil {
		t.Fatal(err)
	}
	// Overwriting a doesn't touch b.
	if err := s.Write(Memory{Name: "a", Description: "da2", Type: TypeProject, Body: "ba2", Durable: true}); err != nil {
		t.Fatal(err)
	}
	if m, ok, _ := s.Read("project/b"); !ok || m.Body != "bb" {
		t.Errorf("b should be untouched, got ok=%v body=%q", ok, m.Body)
	}
}

func TestListSortedProjectFirst(t *testing.T) {
	s := newStore(t)
	_ = s.Write(Memory{Name: "zed", Description: "d", Type: TypeUser, Body: "b", Durable: true})       // global
	_ = s.Write(Memory{Name: "alpha", Description: "d", Type: TypeProject, Body: "b", Durable: true})  // project
	_ = s.Write(Memory{Name: "beta", Description: "d", Type: TypeReference, Body: "b", Durable: true}) // project
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
	_ = os.WriteFile(s.legacyProjectRoot+"/MEMORY.md.v1.bak", []byte("old\n"), 0o600)
	_ = s.Write(Memory{Name: "real", Description: "d", Type: TypeProject, Body: "b", Durable: true})
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
	_ = s.Write(Memory{Name: "ok", Description: "d", Type: TypeProject, Body: "b", Durable: true})
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
	_ = s.Write(Memory{Name: "foo", Description: "the hook", Type: TypeProject, Body: "b", Durable: true})
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

func TestParseFactQuotedDescriptionPreservesLegacyQuoteStripping(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{"internal quotes", `description: "el usuario dijo "vale" y se fue"`, `el usuario dijo "vale" y se fue`},
		{"backslashes", `description: "ruta C:\Users\algo"`, `ruta C:\Users\algo`},
		{"trailing backslash", `description: "termina en backslash \"`, `termina en backslash \`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := "---\nname: x\n" + tc.line + "\ntype: project\n---\n"
			m, err := parseFact([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			if m.Description != tc.want {
				t.Errorf("description: got %q want %q", m.Description, tc.want)
			}
		})
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
	v1 := filepath.Join(s.legacyProjectRoot, "MEMORY.md")
	if err := os.MkdirAll(s.legacyProjectRoot, 0o700); err != nil {
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
	v1 := filepath.Join(s.legacyProjectRoot, "MEMORY.md")
	if err := os.MkdirAll(s.legacyProjectRoot, 0o700); err != nil {
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
	_ = s.Write(Memory{Name: "notas-legado-v1", Description: "d", Type: TypeProject, Body: "half", Durable: true})
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
	if err := s.Write(Memory{Name: "secret", Description: "d", Type: TypeProject, Body: "b", Durable: true}); err != nil {
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

// TestFormatIndex_GlobalsSurviveManyProjectFacts locks the fix for the
// starvation bug: with a large project corpus, global facts (standing user
// instructions) were pushed out of the prompt entirely.
func TestFormatIndex_GlobalsSurviveManyProjectFacts(t *testing.T) {
	var mems []Memory
	for i := 0; i < 200; i++ {
		mems = append(mems, Memory{
			Name: fmt.Sprintf("project-fact-%03d", i), Scope: ScopeProject,
			Description: strings.Repeat("d", 100),
		})
	}
	for i := 0; i < 28; i++ {
		mems = append(mems, Memory{
			Name: fmt.Sprintf("global-fact-%03d", i), Scope: ScopeGlobal,
			Description: strings.Repeat("g", 100),
		})
	}
	s := &Store{}
	idx := s.FormatIndex(mems)

	if len(idx) > maxIndexBytes+256 {
		t.Fatalf("index over budget: %d bytes", len(idx))
	}
	var globals, projects int
	for _, line := range strings.Split(idx, "\n") {
		if strings.Contains(line, "global/") {
			globals++
		}
		if strings.Contains(line, "project/") {
			projects++
		}
	}
	if globals == 0 {
		t.Fatal("no global facts in index: standing user instructions would never reach the model")
	}
	if projects == 0 {
		t.Fatal("no project facts in index")
	}
	if !strings.Contains(idx, "more facts not shown") {
		t.Fatal("expected truncation note")
	}
}

// TestFormatIndex_FewGlobalsDoNotWasteBudget verifies the reserved share rolls
// over to project facts when there are few globals.
func TestFormatIndex_FewGlobalsDoNotWasteBudget(t *testing.T) {
	var mems []Memory
	for i := 0; i < 200; i++ {
		mems = append(mems, Memory{
			Name: fmt.Sprintf("project-fact-%03d", i), Scope: ScopeProject,
			Description: strings.Repeat("d", 100),
		})
	}
	mems = append(mems, Memory{Name: "only-global", Scope: ScopeGlobal, Description: "x"})

	s := &Store{}
	idx := s.FormatIndex(mems)
	if !strings.Contains(idx, "global/only-global") {
		t.Fatal("the single global fact must be present")
	}
	if n := strings.Count(idx, "project/"); n < 60 {
		t.Fatalf("unused global budget did not roll over: only %d project facts", n)
	}
}

// TestFormatIndex_RollsOverBothWays locks the mirror case: with many globals
// and few project facts, a one-way roll-over left most of the budget unused
// while dropping hundreds of globals.
func TestFormatIndex_RollsOverBothWays(t *testing.T) {
	var mems []Memory
	for i := 0; i < 500; i++ {
		mems = append(mems, Memory{
			Name: fmt.Sprintf("global-fact-%03d", i), Scope: ScopeGlobal,
			Description: strings.Repeat("g", 100),
		})
	}
	for i := 0; i < 5; i++ {
		mems = append(mems, Memory{
			Name: fmt.Sprintf("project-fact-%03d", i), Scope: ScopeProject,
			Description: strings.Repeat("d", 100),
		})
	}
	s := &Store{}
	idx := s.FormatIndex(mems)

	if len(idx) < maxIndexBytes*3/4 {
		t.Fatalf("budget under-used: %d of %d bytes", len(idx), maxIndexBytes)
	}
	if n := strings.Count(idx, "global/"); n < 50 {
		t.Fatalf("globals starved despite free budget: only %d shown", n)
	}
	if n := strings.Count(idx, "project/"); n != 5 {
		t.Fatalf("all 5 project facts should fit, got %d", n)
	}
	if len(idx) > maxIndexBytes+256 {
		t.Fatalf("index over budget: %d bytes", len(idx))
	}
}
