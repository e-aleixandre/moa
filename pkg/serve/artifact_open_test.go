package serve

import (
	"os"
	"path/filepath"
	"testing"
)

func writeArtifactFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOpenArtifactPath_RegularFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.txt")
	writeArtifactFile(t, path, "hello")

	f, info, err := openArtifactPath(path)
	if err != nil {
		t.Fatalf("open regular file: %v", err)
	}
	defer f.Close() //nolint:errcheck
	if info.Size() != 5 {
		t.Errorf("size = %d, want 5", info.Size())
	}
	buf := make([]byte, 5)
	if _, err := f.ReadAt(buf, 0); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "hello" {
		t.Errorf("content = %q", buf)
	}
}

func TestOpenArtifactPath_RejectsRelativePath(t *testing.T) {
	if _, _, err := openArtifactPath("relative/path.txt"); err == nil {
		t.Fatal("relative path accepted")
	}
}

func TestOpenArtifactPath_RejectsSymlinkLeaf(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	writeArtifactFile(t, target, "secret")
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	if f, _, err := openArtifactPath(link); err == nil {
		_ = f.Close()
		t.Fatal("a symlinked leaf was served")
	}
}

// A parent replaced by a symlink after publication must fail closed, at every
// level of the chain — this is what os.OpenRoot(filepath.Dir(path)) would miss.
func TestOpenArtifactPath_RejectsSymlinkedAncestorAtEveryLevel(t *testing.T) {
	for _, level := range []int{0, 1, 2} {
		base := t.TempDir()
		real := filepath.Join(base, "real")
		deep := filepath.Join(real, "a", "b", "c")
		if err := os.MkdirAll(deep, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(deep, "report.txt")
		writeArtifactFile(t, path, "expected")
		if f, _, err := openArtifactPath(path); err != nil {
			t.Fatalf("baseline open failed: %v", err)
		} else {
			_ = f.Close()
		}

		// Replace one ancestor directory with a symlink to an attacker tree
		// holding the same layout, then check the swapped file is not served.
		components := []string{"a", "b", "c"}
		victimDir := filepath.Join(append([]string{real}, components[:level+1]...)...)
		evil := filepath.Join(base, "evil")
		remainder := filepath.Join(append([]string{evil}, components[level+1:]...)...)
		if err := os.MkdirAll(remainder, 0o755); err != nil {
			t.Fatal(err)
		}
		writeArtifactFile(t, filepath.Join(remainder, "report.txt"), "attacker")
		if err := os.RemoveAll(victimDir); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(evil, victimDir); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}

		f, _, err := openArtifactPath(path)
		if err == nil {
			buf := make([]byte, 16)
			n, _ := f.ReadAt(buf, 0)
			_ = f.Close()
			t.Fatalf("level %d: symlinked ancestor was traversed, served %q", level, buf[:n])
		}
	}
}

func TestOpenArtifactPath_RejectsDirectoryAndMissing(t *testing.T) {
	dir := t.TempDir()
	if f, _, err := openArtifactPath(dir); err == nil {
		_ = f.Close()
		t.Fatal("a directory was opened as an artifact")
	}
	if f, _, err := openArtifactPath(filepath.Join(dir, "missing.txt")); err == nil {
		_ = f.Close()
		t.Fatal("a missing path was opened")
	}
}

// A legitimate atomic replacement at the same location is exactly what the
// product promises: the next open returns the new bytes, and the descriptor
// already handed out keeps serving the bytes it was opened on.
func TestOpenArtifactPath_AtomicRenameServesCurrentContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.txt")
	writeArtifactFile(t, path, "first")

	old, _, err := openArtifactPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close() //nolint:errcheck

	staged := filepath.Join(dir, "staged")
	writeArtifactFile(t, staged, "second")
	if err := os.Rename(staged, path); err != nil {
		t.Fatal(err)
	}

	fresh, info, err := openArtifactPath(path)
	if err != nil {
		t.Fatalf("open after atomic rename: %v", err)
	}
	defer fresh.Close() //nolint:errcheck
	buf := make([]byte, info.Size())
	if _, err := fresh.ReadAt(buf, 0); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "second" {
		t.Errorf("new open = %q, want second", buf)
	}
	// The already-open descriptor is pinned to the file it opened.
	prev := make([]byte, 5)
	if _, err := old.ReadAt(prev, 0); err != nil {
		t.Fatal(err)
	}
	if string(prev) != "first" {
		t.Errorf("open descriptor = %q, want first", prev)
	}
}
