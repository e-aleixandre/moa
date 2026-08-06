package secrets

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func useTempBase(t *testing.T) string {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	return baseDir()
}

func TestStashWritesPrivateBatch(t *testing.T) {
	base := useTempBase(t)
	dir, err := Stash([]Entry{{Name: "db-produccion", Value: "hunter2"}, {Name: "net.rc", Value: "token"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Forget(dir) })
	if filepath.Dir(dir) != base || !batchPattern.MatchString(filepath.Base(dir)) {
		t.Fatalf("unexpected batch path %q", dir)
	}
	for _, path := range []string{base, dir, filepath.Join(dir, "db-produccion"), filepath.Join(dir, "net.rc")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o600)
		if path == base || path == dir {
			want = 0o700
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode = %o, want %o", path, info.Mode().Perm(), want)
		}
	}
	got, err := os.ReadFile(filepath.Join(dir, "db-produccion"))
	if err != nil || string(got) != "hunter2" {
		t.Fatalf("stored value = %q, err = %v", got, err)
	}
}

func TestStashValidationNeverLeaksValue(t *testing.T) {
	useTempBase(t)
	secret := "must-never-appear-in-an-error"
	tooLong := strings.Repeat("x", maxValueSize+1)
	cases := []struct {
		name    string
		entries []Entry
	}{
		{"empty batch", nil},
		{"too many", make([]Entry, maxEntries+1)},
		{"empty alias", []Entry{{Name: "", Value: secret}}},
		{"non-alphanumeric first character", []Entry{{Name: "-token", Value: secret}}},
		{"leading dot", []Entry{{Name: ".env", Value: secret}}},
		{"traversal", []Entry{{Name: "../x", Value: secret}}},
		{"slash", []Entry{{Name: "a/b", Value: secret}}},
		{"backslash", []Entry{{Name: `a\\b`, Value: secret}}},
		{"too long alias", []Entry{{Name: strings.Repeat("a", 65), Value: secret}}},
		{"duplicate", []Entry{{Name: "token", Value: secret}, {Name: "token", Value: secret}}},
		{"case-insensitive duplicate", []Entry{{Name: "token", Value: secret}, {Name: "TOKEN", Value: secret}}},
		{"empty value", []Entry{{Name: "token", Value: ""}}},
		{"large value", []Entry{{Name: "token", Value: tooLong}}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Stash(tt.entries)
			if err == nil {
				t.Fatal("Stash succeeded")
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), tooLong) {
				t.Fatalf("error leaked a value: %q", err)
			}
		})
	}
}

func TestStashAllowsBoundaryLimits(t *testing.T) {
	useTempBase(t)
	entries := make([]Entry, maxEntries)
	for i := range entries {
		entries[i] = Entry{Name: "a" + strings.Repeat("x", 63), Value: strings.Repeat("v", maxValueSize)}
		entries[i].Name = entries[i].Name[:63] + string(rune('a'+i%26))
	}
	dir, err := Stash(entries)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Forget(dir) })
}

func TestForgetRejectsOutsideBatch(t *testing.T) {
	base := useTempBase(t)
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(base), "keep")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Forget(outside); err == nil {
		t.Fatal("Forget accepted an outside path")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside path was removed: %v", err)
	}
}

func TestReapRemovesOnlyOldBatchesWithoutFollowingSymlinks(t *testing.T) {
	base := useTempBase(t)
	old, err := Stash([]Entry{{Name: "old", Value: "value"}})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := Stash([]Entry{{Name: "fresh", Value: "value"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(old, time.Now().Add(-batchTTL-time.Minute), time.Now().Add(-batchTTL-time.Minute)); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "batch-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	Reap()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old batch still exists: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh batch was removed: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("reaper followed symlink: %v", err)
	}
}

func TestReapPeriodicallyRemovesExpiredBatch(t *testing.T) {
	useTempBase(t)
	dir, err := Stash([]Entry{{Name: "old", Value: "value"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dir, time.Now().Add(-batchTTL-time.Minute), time.Now().Add(-batchTTL-time.Minute)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		reapPeriodically(ctx, time.Millisecond)
		close(done)
	}()
	deadline := time.After(time.Second)
	for {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			break
		}
		select {
		case <-deadline:
			t.Fatal("expired batch was not removed by periodic reaper")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("periodic reaper did not stop")
	}
}

func TestNoteIsSingleSafeInstruction(t *testing.T) {
	note := Note("/tmp/moa-secrets-1000/batch-abc", []string{"db", "netrc"})
	for _, want := range []string{"/tmp/moa-secrets-1000/batch-abc", "db", "netrc", "delete", "Never print", "repository", "echo"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note missing %q: %q", want, note)
		}
	}
	if strings.Count(note, "secret batch") != 1 {
		t.Fatalf("note is not one batch message: %q", note)
	}
}
