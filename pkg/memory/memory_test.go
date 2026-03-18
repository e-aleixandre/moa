package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadEmpty(t *testing.T) {
	s := New(t.TempDir())
	content, err := s.Load("/some/project")
	if err != nil {
		t.Fatal(err)
	}
	if content != "" {
		t.Errorf("expected empty, got %q", content)
	}
}

func TestSaveAndLoad(t *testing.T) {
	s := New(t.TempDir())
	root := "/test/project"
	want := "# Memory\n\n- Use Docker for builds\n"

	if err := s.Save(root, want); err != nil {
		t.Fatal(err)
	}

	got, err := s.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSaveEmptyDeletes(t *testing.T) {
	s := New(t.TempDir())
	root := "/test/project"

	if err := s.Save(root, "some content"); err != nil {
		t.Fatal(err)
	}
	// Verify file exists.
	if _, err := os.Stat(s.FilePath(root)); err != nil {
		t.Fatal("file should exist after save")
	}

	// Save empty → delete.
	if err := s.Save(root, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.FilePath(root)); !os.IsNotExist(err) {
		t.Error("file should be deleted after empty save")
	}
}

func TestSaveEmptyNonExistent(t *testing.T) {
	s := New(t.TempDir())
	// Saving empty when file doesn't exist should not error.
	if err := s.Save("/nonexistent", ""); err != nil {
		t.Fatal(err)
	}
}

func TestSaveCreatesDirectories(t *testing.T) {
	base := filepath.Join(t.TempDir(), "deep", "nested")
	s := New(base)
	if err := s.Save("/test", "hello"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load("/test")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestSaveExceedsMaxSize(t *testing.T) {
	s := New(t.TempDir())
	big := strings.Repeat("x", MaxSize+1)
	err := s.Save("/test", big)
	if err == nil {
		t.Fatal("expected error for oversized content")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSavePermissions(t *testing.T) {
	s := New(t.TempDir())
	root := "/test/project"
	if err := s.Save(root, "secret"); err != nil {
		t.Fatal(err)
	}

	// Check directory permissions.
	dirInfo, err := os.Stat(s.ProjectDir(root))
	if err != nil {
		t.Fatal(err)
	}
	dirPerm := dirInfo.Mode().Perm()
	if dirPerm&0o077 != 0 {
		t.Errorf("directory too permissive: %o", dirPerm)
	}

	// Check file permissions.
	fileInfo, err := os.Stat(s.FilePath(root))
	if err != nil {
		t.Fatal(err)
	}
	filePerm := fileInfo.Mode().Perm()
	if filePerm&0o077 != 0 {
		t.Errorf("file too permissive: %o", filePerm)
	}
}

func TestProjectDirDeterministic(t *testing.T) {
	s := New("/base")
	d1 := s.ProjectDir("/my/project")
	d2 := s.ProjectDir("/my/project")
	if d1 != d2 {
		t.Errorf("not deterministic: %q vs %q", d1, d2)
	}
}

func TestProjectDirDistinct(t *testing.T) {
	s := New("/base")
	d1 := s.ProjectDir("/project/a")
	d2 := s.ProjectDir("/project/b")
	if d1 == d2 {
		t.Error("different paths should produce different hashes")
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		max      int
		expected string
	}{
		{
			name:     "under limit",
			content:  "line1\nline2\nline3",
			max:      5,
			expected: "line1\nline2\nline3",
		},
		{
			name:     "at limit",
			content:  "line1\nline2\nline3",
			max:      3,
			expected: "line1\nline2\nline3",
		},
		{
			name:     "over limit",
			content:  "line1\nline2\nline3\nline4\nline5",
			max:      3,
			expected: "line1\nline2\nline3",
		},
		{
			name:     "single line",
			content:  "hello",
			max:      1,
			expected: "hello",
		},
		{
			name:     "empty",
			content:  "",
			max:      5,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Truncate(tt.content, tt.max)
			if got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}
