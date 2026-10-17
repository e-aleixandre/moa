package skill

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func bookTidyBody(t *testing.T) string {
	t.Helper()
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	for _, s := range Discover(t.TempDir(), Options{Builtin: true}) {
		if s.Name == "book-tidy" {
			if !s.Builtin || !s.ModelInvocable() || !s.UserInvocable {
				t.Fatal("book-tidy must be a built-in reachable by owner and user")
			}
			body, err := Load(s)
			if err != nil {
				t.Fatal(err)
			}
			return body
		}
	}
	t.Fatal("book-tidy does not ship")
	return ""
}

// The method's limits are the real ones; a skill that states different numbers
// sends the owner to verify against the wrong bar.
func TestBookTidyStatesTheMethodAndLimits(t *testing.T) {
	body := bookTidyBody(t)
	for _, want := range []string{"8192", "64 KiB", "48 KiB", "7 KiB", "24 KiB", "sha256", "one tap",
		"snapshots", "0700", "0600", "last 10", "90 days", "never purge silently", "OWNER.md"} {
		if !strings.Contains(body, want) {
			t.Errorf("skill does not mention %q", want)
		}
	}
}

// The snapshot recipe is code the owner will run on a real book: run it.
func TestBookTidySnapshotRecipeWorks(t *testing.T) {
	body := bookTidyBody(t)
	m := regexp.MustCompile("(?s)```bash snapshot\n(.*?)```").FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no snapshot recipe in the skill")
	}
	cb := t.TempDir()
	book := filepath.Join(cb, "book")
	if err := os.MkdirAll(filepath.Join(book, "decisions"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"PROJECT.md": "# idx\n", "decisions/a b.md": "año\n"}
	for name, c := range files {
		if err := os.WriteFile(filepath.Join(book, name), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", "-c", m[1])
	cmd.Env = append(os.Environ(), "CB="+cb)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("recipe failed: %v\n%s", err, out)
	}
	snaps, _ := filepath.Glob(filepath.Join(cb, "snapshots", "*-*"))
	if len(snaps) != 1 || !regexp.MustCompile(`/\d{8}T\d{6}Z-[0-9a-f]{12}$`).MatchString(snaps[0]) {
		t.Fatalf("snapshot dirs = %v\n%s", snaps, out)
	}
	if left, _ := filepath.Glob(filepath.Join(cb, "snapshots", ".tmp.*")); len(left) != 0 {
		t.Fatalf("temp dir left behind: %v", left)
	}
	_ = filepath.Walk(filepath.Join(cb, "snapshots"), func(p string, info os.FileInfo, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o600)
		if info.IsDir() {
			want = 0o700
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode %v, want %v", p, info.Mode().Perm(), want)
		}
		return nil
	})
	for name, c := range files {
		got, err := os.ReadFile(filepath.Join(snaps[0], "book", name))
		if err != nil || string(got) != c {
			t.Errorf("%s in snapshot = %q, %v", name, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(snaps[0], "MANIFEST.sha256")); err != nil {
		t.Error(err)
	}
}

func runBlock(t *testing.T, name string, env ...string) (string, error) {
	t.Helper()
	m := regexp.MustCompile("(?s)```bash " + name + "\n(.*?)```").FindStringSubmatch(bookTidyBody(t))
	if m == nil {
		t.Fatalf("no %s recipe in the skill", name)
	}
	cmd := exec.Command("bash", "-c", m[1])
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func putFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func onlySnapshot(t *testing.T, cb string) string {
	t.Helper()
	snaps, _ := filepath.Glob(filepath.Join(cb, "snapshots", "2*"))
	if len(snaps) != 1 {
		t.Fatalf("snapshots = %v", snaps)
	}
	return snaps[0]
}

// Restoring must return the book to exactly the snapshot: paths and bytes,
// including removing what the reorganization added.
func TestBookTidyRestoreReturnsTheExactBook(t *testing.T) {
	cb := t.TempDir()
	book := filepath.Join(cb, "book")
	putFile(t, book, "PROJECT.md", "# idx\n")
	putFile(t, book, "work/old.md", "old\n")
	putFile(t, book, "line\nbreak.md", "newline in the name\n")
	if out, err := runBlock(t, "snapshot", "CB="+cb); err != nil {
		t.Fatalf("snapshot: %v\n%s", err, out)
	}
	snap := onlySnapshot(t, cb)

	// A reorganization: move, add, rewrite the index.
	if err := os.Rename(filepath.Join(book, "work/old.md"), filepath.Join(book, "moved.md")); err != nil {
		t.Fatal(err)
	}
	putFile(t, book, "areas/new.md", "added\n")
	putFile(t, book, "PROJECT.md", "# rewritten\n")

	out, err := runBlock(t, "restore", "CB="+cb, "SNAP="+snap)
	if err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	if diff, err := exec.Command("diff", "-r", filepath.Join(snap, "book"), book).CombinedOutput(); err != nil {
		t.Fatalf("restored book differs from the snapshot: %v\n%s", err, diff)
	}
	kept, _ := filepath.Glob(filepath.Join(cb, "book.before-restore.*"))
	if len(kept) != 1 {
		t.Fatalf("the replaced book must be kept: %v", kept)
	}
	if got, _ := os.ReadFile(filepath.Join(kept[0], "areas/new.md")); string(got) != "added\n" {
		t.Errorf("the kept book lost the reorganized content: %q", got)
	}
}

func TestBookTidySnapshotRefusesSymlinks(t *testing.T) {
	cb := t.TempDir()
	book := filepath.Join(cb, "book")
	putFile(t, book, "target.md", "x\n")
	if err := os.Symlink(filepath.Join(book, "target.md"), filepath.Join(book, "alias.md")); err != nil {
		t.Fatal(err)
	}
	out, err := runBlock(t, "snapshot", "CB="+cb)
	if err == nil || !strings.Contains(out, "symlinks") {
		t.Fatalf("symlinked book accepted: %v\n%s", err, out)
	}
	if snaps, _ := filepath.Glob(filepath.Join(cb, "snapshots", "*")); len(snaps) != 0 {
		t.Fatalf("a refused snapshot left %v", snaps)
	}
}

func TestBookTidySnapshotRefusesAnEmptyBookClearly(t *testing.T) {
	cb := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cb, "book"), 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := runBlock(t, "snapshot", "CB="+cb)
	if err == nil || !strings.Contains(out, "no files") {
		t.Fatalf("empty book: %v\n%s", err, out)
	}
}

// Reorganizing is applied only after the user's tap, deletion or not.
func TestBookTidyRequiresTheTapBeforeApplying(t *testing.T) {
	body := bookTidyBody(t)
	for _, want := range []string{"even if nothing is deleted", "wait for the user's tap", "Without that tap, nothing is written"} {
		if !strings.Contains(body, want) {
			t.Errorf("skill does not say %q", want)
		}
	}
}

func readTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() {
			b, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(root, p)
			out[rel] = string(b)
		}
		return nil
	})
	return out
}

func snapshotFixture(t *testing.T) (cb, snap string) {
	t.Helper()
	cb = t.TempDir()
	book := filepath.Join(cb, "book")
	putFile(t, book, "PROJECT.md", "# idx\n")
	putFile(t, book, "work/old.md", "old\n")
	if out, err := runBlock(t, "snapshot", "CB="+cb); err != nil {
		t.Fatalf("snapshot: %v\n%s", err, out)
	}
	snap = onlySnapshot(t, cb)
	putFile(t, book, "PROJECT.md", "# live, rewritten\n")
	return cb, snap
}

// Names the recipe did not create are never touched: staging and backup are
// unique by construction, whatever already sits in the codebase directory.
func TestBookTidyRestoreNeverTouchesWhatItDidNotCreate(t *testing.T) {
	cb, snap := snapshotFixture(t)
	putFile(t, cb, "book.restore.AAAAAA/ONLY-COPY.md", "unique\n")
	putFile(t, cb, "book.before-restore.BBBBBB/PROJECT.md", "older backup\n")
	if out, err := runBlock(t, "restore", "CB="+cb, "SNAP="+snap); err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	for _, rel := range []string{"book.restore.AAAAAA/ONLY-COPY.md", "book.before-restore.BBBBBB/PROJECT.md"} {
		if _, err := os.Stat(filepath.Join(cb, rel)); err != nil {
			t.Errorf("%s was destroyed: %v", rel, err)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(cb, "book/PROJECT.md")); string(got) != "# idx\n" {
		t.Errorf("book not restored: %q", got)
	}
}

// If installing the restored book fails, the book that was live is back, as
// the tree itself and not nested, and a pre-existing backup is untouched.
func TestBookTidyRestoreRollsBackWhenInstallFails(t *testing.T) {
	cb, snap := snapshotFixture(t)
	putFile(t, cb, "book.before-restore.BBBBBB/PROJECT.md", "older backup\n")
	bin := t.TempDir()
	fake := "#!/bin/bash\nfor a in \"$@\"; do case \"$a\" in */book.restore.*) [ \"${@: -1}\" = \"$CB/book\" ] && exit 1;; esac; done\nexec /usr/bin/mv \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "mv"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := runBlock(t, "restore", "CB="+cb, "SNAP="+snap, "PATH="+bin+":"+os.Getenv("PATH"))
	if err == nil || !strings.Contains(out, "book left as it was") {
		t.Fatalf("expected a clean failure: %v\n%s", err, out)
	}
	if got := readTree(t, filepath.Join(cb, "book")); len(got) != 2 || got["PROJECT.md"] != "# live, rewritten\n" {
		t.Errorf("live book not back as it was: %v", got)
	}
	if got, _ := os.ReadFile(filepath.Join(cb, "book.before-restore.BBBBBB/PROJECT.md")); string(got) != "older backup\n" {
		t.Errorf("pre-existing backup altered: %q", got)
	}
	if left, _ := filepath.Glob(filepath.Join(cb, "book.restore.*")); len(left) != 0 {
		t.Errorf("staging left behind: %v", left)
	}
}

// A snapshot that does not match its manifest exactly is not installed.
func TestBookTidyRestoreRejectsAnIncompleteOrDamagedSnapshot(t *testing.T) {
	damage := map[string]func(t *testing.T, snap string){
		"extra file not in the manifest": func(t *testing.T, snap string) { putFile(t, snap, "book/extra.md", "unverified\n") },
		"truncated manifest": func(t *testing.T, snap string) {
			m := filepath.Join(snap, "MANIFEST.sha256")
			b, _ := os.ReadFile(m)
			_ = os.WriteFile(m, b[:bytesIndexNL(b)+1], 0o600)
		},
		"manifest cut mid-entry": func(t *testing.T, snap string) {
			m := filepath.Join(snap, "MANIFEST.sha256")
			b, _ := os.ReadFile(m)
			_ = os.WriteFile(m, b[:len(b)-20], 0o600)
		},
		"changed content": func(t *testing.T, snap string) { putFile(t, snap, "book/work/old.md", "tampered\n") },
		"missing file":    func(t *testing.T, snap string) { _ = os.Remove(filepath.Join(snap, "book/work/old.md")) },
	}
	for name, fn := range damage {
		t.Run(name, func(t *testing.T) {
			cb, snap := snapshotFixture(t)
			fn(t, snap)
			if out, err := runBlock(t, "restore", "CB="+cb, "SNAP="+snap); err == nil {
				t.Fatalf("damaged snapshot restored:\n%s", out)
			}
			if got := readTree(t, filepath.Join(cb, "book")); len(got) != 2 || got["PROJECT.md"] != "# live, rewritten\n" {
				t.Errorf("live book changed: %v", got)
			}
			if left, _ := filepath.Glob(filepath.Join(cb, "book.*")); len(left) != 0 {
				t.Errorf("leftovers: %v", left)
			}
		})
	}
}

func bytesIndexNL(b []byte) int { return strings.IndexByte(string(b), '\n') }
