package book

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func run(t *testing.T, tool core.Tool, params map[string]any) core.Result {
	t.Helper()
	res, err := tool.Execute(context.Background(), params, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func resultText(res core.Result) string {
	var sb strings.Builder
	for _, c := range res.Content {
		sb.WriteString(c.Text)
	}
	return sb.String()
}

func newBook(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "decisions"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"PROJECT.md":                "# Project\n\nthe index\n",
		"people.md":                 "the boss cares about imports\n",
		"decisions/2026-08-vale.md": "vale-basta closed on 2026-08-01\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestListReadSearch(t *testing.T) {
	dir := newBook(t)
	tool := NewTool(dir, false)

	listed := resultText(run(t, tool, map[string]any{"action": "list"}))
	for _, want := range []string{"PROJECT.md", "people.md", "decisions/2026-08-vale.md"} {
		if !strings.Contains(listed, want) {
			t.Fatalf("list missing %q:\n%s", want, listed)
		}
	}

	read := resultText(run(t, tool, map[string]any{"action": "read", "path": "decisions/2026-08-vale.md"}))
	if !strings.Contains(read, "vale-basta closed") {
		t.Fatalf("read = %q", read)
	}

	found := resultText(run(t, tool, map[string]any{"action": "search", "query": "IMPORTS"}))
	if !strings.Contains(found, "people.md") {
		t.Fatalf("case-insensitive search = %q", found)
	}
	none := resultText(run(t, tool, map[string]any{"action": "search", "query": "absent-token"}))
	if !strings.Contains(none, "Nothing in the book") {
		t.Fatalf("empty search = %q", none)
	}
}

func TestReadOnlyToolRefusesWrites(t *testing.T) {
	dir := newBook(t)
	tool := NewTool(dir, false)

	if strings.Contains(string(tool.Parameters), "\"write\"") {
		t.Fatal("read-only book tool advertises write in its schema")
	}
	res := run(t, tool, map[string]any{"action": "write", "path": "x.md", "content": "no"})
	if !res.IsError {
		t.Fatal("read-only book tool accepted a write")
	}
	if _, err := os.Stat(filepath.Join(dir, "x.md")); err == nil {
		t.Fatal("refused write still created the file")
	}
}

func TestWriteAndAppend(t *testing.T) {
	dir := newBook(t)
	tool := NewTool(dir, true)

	run(t, tool, map[string]any{"action": "write", "path": "areas/import.md", "content": "first"})
	data, err := os.ReadFile(filepath.Join(dir, "areas", "import.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "first" {
		t.Fatalf("written content = %q", data)
	}
	run(t, tool, map[string]any{"action": "append", "path": "areas/import.md", "content": "second"})
	data, err = os.ReadFile(filepath.Join(dir, "areas", "import.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "first\nsecond" {
		t.Fatalf("appended content = %q", data)
	}
	// Append to a file that does not exist yet behaves as a write.
	run(t, tool, map[string]any{"action": "append", "path": "new.md", "content": "fresh"})
	if data, err := os.ReadFile(filepath.Join(dir, "new.md")); err != nil || string(data) != "fresh" {
		t.Fatalf("append to a new file = %q, %v", data, err)
	}
}

func TestPathTraversalIsRefused(t *testing.T) {
	dir := newBook(t)
	outside := filepath.Join(filepath.Dir(dir), "secret.txt")
	if err := os.WriteFile(outside, []byte("credentials"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := NewTool(dir, true)

	for _, path := range []string{"../secret.txt", "decisions/../../secret.txt", outside, "..", ""} {
		res := run(t, tool, map[string]any{"action": "read", "path": path})
		if !res.IsError {
			t.Fatalf("read escaped the book with path %q: %q", path, resultText(res))
		}
		res = run(t, tool, map[string]any{"action": "write", "path": path, "content": "owned"})
		if !res.IsError {
			t.Fatalf("write escaped the book with path %q", path)
		}
	}
	if data, _ := os.ReadFile(outside); string(data) != "credentials" {
		t.Fatalf("a file outside the book was rewritten: %q", data)
	}
}

func TestSymlinkOutOfTheBookIsRefused(t *testing.T) {
	dir := newBook(t)
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("elsewhere"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	tool := NewTool(dir, true)

	if res := run(t, tool, map[string]any{"action": "read", "path": "link.md"}); !res.IsError {
		t.Fatalf("read followed a symlink out of the book: %q", resultText(res))
	}
	if res := run(t, tool, map[string]any{"action": "write", "path": "link.md", "content": "owned"}); !res.IsError {
		t.Fatal("write followed a symlink out of the book")
	}
	if data, _ := os.ReadFile(outside); string(data) != "elsewhere" {
		t.Fatalf("symlink target rewritten: %q", data)
	}
}

func TestReadOnlyVariantKeepsReadsAndDropsWrites(t *testing.T) {
	dir := newBook(t)
	writable := NewTool(dir, true)

	child, ok := ReadOnlyVariant(writable)
	if !ok {
		t.Fatal("ReadOnlyVariant refused the book tool")
	}
	if strings.Contains(string(child.Parameters), "\"write\"") {
		t.Fatal("child book tool still advertises write")
	}
	if got := resultText(run(t, child, map[string]any{"action": "read", "path": "PROJECT.md"})); !strings.Contains(got, "the index") {
		t.Fatalf("child read = %q", got)
	}
	if res := run(t, child, map[string]any{"action": "write", "path": "PROJECT.md", "content": "rewritten"}); !res.IsError {
		t.Fatal("child wrote the book")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "PROJECT.md")); !strings.Contains(string(data), "the index") {
		t.Fatalf("PROJECT.md was rewritten by a child: %q", data)
	}
	if _, ok := ReadOnlyVariant(core.Tool{Name: "read"}); ok {
		t.Fatal("ReadOnlyVariant claimed a tool that is not the book")
	}
}

func TestReadTruncatesAHugeFile(t *testing.T) {
	dir := newBook(t)
	marker := "PAST-THE-CAP"
	if err := os.WriteFile(filepath.Join(dir, "big.md"), []byte(strings.Repeat("x", maxFileBytes)+marker), 0o600); err != nil {
		t.Fatal(err)
	}
	got := resultText(run(t, NewTool(dir, false), map[string]any{"action": "read", "path": "big.md"}))
	if strings.Contains(got, marker) {
		t.Fatal("read returned content past the cap")
	}
	if !strings.Contains(got, "truncated") {
		t.Fatal("truncated read does not say so")
	}
}

// The HTTP surface reads and writes through the same guard the tool uses.
// These cover what the tool's own tests cannot: sizes in the listing, no
// prompt-sized truncation on a read, and a traversal refused on a write.

func TestFilesListsTheBookWithSizes(t *testing.T) {
	dir := newBook(t)
	files, err := Files(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("Files = %d entries, want 3: %+v", len(files), files)
	}
	// Sorted by path, so the index is not first — the UI raises it, the store
	// does not reorder the book for it.
	want := []string{"PROJECT.md", "decisions/2026-08-vale.md", "people.md"}
	for i, entry := range files {
		if entry.Path != want[i] {
			t.Fatalf("Files[%d].Path = %q, want %q", i, entry.Path, want[i])
		}
		if entry.Bytes == 0 {
			t.Fatalf("%s reported 0 bytes", entry.Path)
		}
	}
}

func TestFilesOnAMissingBookIsEmptyNotAnError(t *testing.T) {
	files, err := Files(filepath.Join(t.TempDir(), "never-seeded"))
	if err != nil {
		t.Fatalf("Files on a missing book = %v, want no error", err)
	}
	if len(files) != 0 {
		t.Fatalf("Files on a missing book = %+v", files)
	}
}

func TestReadFileReturnsTheWholeFile(t *testing.T) {
	dir := t.TempDir()
	// Past the tool's 64KB prompt cap: a person opening a file wants the file.
	big := strings.Repeat("a", maxFileBytes+512)
	if err := os.WriteFile(filepath.Join(dir, "PROJECT.md"), []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := ReadFile(dir, "PROJECT.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != len(big) {
		t.Fatalf("ReadFile returned %d bytes, want %d (it truncated)", len(data), len(big))
	}
}

func TestReadAndWriteRefuseLeavingTheBook(t *testing.T) {
	dir := newBook(t)
	outside := filepath.Join(filepath.Dir(dir), "credentials.json")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../credentials.json", "/etc/passwd"} {
		if _, err := ReadFile(dir, path); err == nil {
			t.Fatalf("ReadFile(%q) was allowed out of the book", path)
		}
		if err := WriteFile(dir, path, []byte("x")); err == nil {
			t.Fatalf("WriteFile(%q) was allowed out of the book", path)
		}
	}
	if data, _ := os.ReadFile(outside); string(data) != "secret" {
		t.Fatal("a refused write still changed a file outside the book")
	}
}

func TestWriteFileReplacesAndRefusesOversize(t *testing.T) {
	dir := newBook(t)
	if err := WriteFile(dir, "PROJECT.md", []byte("# New index\n")); err != nil {
		t.Fatal(err)
	}
	data, err := ReadFile(dir, "PROJECT.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "# New index\n" {
		t.Fatalf("PROJECT.md = %q after write", data)
	}
	if err := WriteFile(dir, "PROJECT.md", make([]byte, maxWriteBytes+1)); err != ErrTooLarge {
		t.Fatalf("oversize write = %v, want ErrTooLarge", err)
	}
}
