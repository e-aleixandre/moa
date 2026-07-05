package serve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionAttachDir_Valid(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("MOA_ATTACHMENTS_DIR", tmp)

	id := "0123456789abcdef"
	dir, err := sessionAttachDir(id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(tmp, id)
	if dir != want {
		t.Errorf("got %q, want %q", dir, want)
	}
}

func TestSessionAttachDir_Invalid(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("MOA_ATTACHMENTS_DIR", tmp)

	long65 := strings.Repeat("a", 65)
	ids := []string{"", "../etc", "GG..", "abc", long65, "a/b"}
	for _, id := range ids {
		if _, err := sessionAttachDir(id); err == nil {
			t.Errorf("expected error for id %q", id)
		}
	}
}

func TestSafeBase(t *testing.T) {
	longStem := strings.Repeat("a", 300) + ".txt"

	cases := []struct {
		name string
		want string
	}{
		{"../../etc/passwd", "passwd"},
		{"..\\..\\windows\\x.txt", "x.txt"},
		{"report.csv", "report.csv"},
		{"", ""},
		{".", ""},
		{"..", ""},
		{"bad\x00name.txt", "badname.txt"},
		{"ctrl\x01\x1fchars.txt", "ctrlchars.txt"},
	}
	for _, c := range cases {
		got := safeBase(c.name)
		if got != c.want {
			t.Errorf("safeBase(%q) = %q, want %q", c.name, got, c.want)
		}
	}

	got := safeBase(longStem)
	if !strings.HasSuffix(got, ".txt") {
		t.Errorf("safeBase(long) = %q, want suffix .txt", got)
	}
	if len(got) > 200 {
		t.Errorf("safeBase(long) length = %d, want <= 200", len(got))
	}
}

func TestWriteUnique_Collision(t *testing.T) {
	dir := t.TempDir()

	p1, err := writeUnique(dir, "data.txt", []byte("first"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Base(p1) != "data.txt" {
		t.Errorf("got %q, want data.txt", filepath.Base(p1))
	}

	p2, err := writeUnique(dir, "data.txt", []byte("second"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Base(p2) != "data-2.txt" {
		t.Errorf("got %q, want data-2.txt", filepath.Base(p2))
	}

	b1, err := os.ReadFile(p1)
	if err != nil || string(b1) != "first" {
		t.Errorf("p1 contents = %q, %v", b1, err)
	}
	b2, err := os.ReadFile(p2)
	if err != nil || string(b2) != "second" {
		t.Errorf("p2 contents = %q, %v", b2, err)
	}
}

func TestWriteUnique_EmptyName(t *testing.T) {
	dir := t.TempDir()

	p1, err := writeUnique(dir, "", []byte("a"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Base(p1) != "attachment" {
		t.Errorf("got %q, want attachment", filepath.Base(p1))
	}

	p2, err := writeUnique(dir, "../", []byte("b"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Base(p2) != "attachment-2" {
		t.Errorf("got %q, want attachment-2", filepath.Base(p2))
	}
}

func TestEnsureBaseDir_Symlink(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "target")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(tmp, "base")
	if err := os.Symlink(target, base); err != nil {
		t.Fatal(err)
	}

	t.Setenv("MOA_ATTACHMENTS_DIR", base)

	if _, err := ensureBaseDir(); err == nil {
		t.Error("expected error for symlinked base dir")
	}
}

func TestEnsureSessionAttachDir_Creates(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("MOA_ATTACHMENTS_DIR", tmp)

	id := "abcdef0123456789"
	dir, err := ensureSessionAttachDir(id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat error: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("expected %q to be a directory", dir)
	}
}
