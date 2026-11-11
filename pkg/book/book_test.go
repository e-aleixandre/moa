package book

import (
	"context"
	"fmt"
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

func writeSized(t *testing.T, dir, rel string, size int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(strings.Repeat("a", size)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWriteRefusesWhatLeavesTheLimits(t *testing.T) {
	const kib = 1024
	tests := []struct {
		name    string
		path    string
		size    int
		refused bool
	}{
		{"index at 8KiB", "PROJECT.md", 8 * kib, false},
		{"index at 8KiB+1", "PROJECT.md", 8*kib + 1, true},
		{"doc at 64KiB", "work/x.md", 64 * kib, false},
		{"doc at 64KiB+1", "work/x.md", 64*kib + 1, true},
		{"big non-index is not an index case", "work/y.md", 10 * kib, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := newBook(t)
			res := run(t, NewTool(dir, true), map[string]any{"action": "write", "path": tc.path, "content": strings.Repeat("a", tc.size)})
			if res.IsError != tc.refused {
				t.Fatalf("refused = %v, want %v: %s", res.IsError, tc.refused, resultText(res))
			}
			_, err := os.Stat(filepath.Join(dir, tc.path))
			if tc.refused && err == nil && tc.path == "work/x.md" {
				t.Fatal("a refused write left a file behind")
			}
			if tc.refused && !strings.Contains(resultText(res), "book-tidy") {
				t.Fatalf("refusal does not say how to continue: %q", resultText(res))
			}
		})
	}
}

func TestIndexRefusalSaysItIsAMap(t *testing.T) {
	dir := newBook(t)
	res := run(t, NewTool(dir, true), map[string]any{"action": "write", "path": "PROJECT.md", "content": strings.Repeat("a", 9000)})
	if !res.IsError || !strings.Contains(resultText(res), "map") || !strings.Contains(resultText(res), "another file") {
		t.Fatalf("index refusal: %q", resultText(res))
	}
}

// The size is the final file, not the appended chunk, and counts bytes.
func TestAppendIsRefusedOnTheFinalSize(t *testing.T) {
	dir := newBook(t)
	tool := NewTool(dir, true)
	writeSized(t, dir, "PROJECT.md", 8*1024-10)
	if res := run(t, tool, map[string]any{"action": "append", "path": "PROJECT.md", "content": "0123"}); res.IsError {
		t.Fatalf("append under the limit refused: %s", resultText(res))
	}
	before, _ := os.ReadFile(filepath.Join(dir, "PROJECT.md"))
	if res := run(t, tool, map[string]any{"action": "append", "path": "PROJECT.md", "content": strings.Repeat("x", 20)}); !res.IsError {
		t.Fatal("append past the limit accepted")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "PROJECT.md"))
	if string(before) != string(after) {
		t.Fatal("a refused append changed the file")
	}
	// 4500 two-byte runes is 9000 bytes.
	if res := run(t, tool, map[string]any{"action": "write", "path": "PROJECT.md", "content": strings.Repeat("á", 4500)}); !res.IsError {
		t.Fatal("limit measured in runes, not bytes")
	}
}

// A book that is already over the limit must not trap its owner: the file can
// be read in pages and rewritten smaller, step by step, but not made bigger.
func TestAnOversizedBookCanBeSplitWithTheTool(t *testing.T) {
	dir := newBook(t)
	tool := NewTool(dir, true)
	var sb strings.Builder
	for i := 0; sb.Len() < 150*1024; i++ {
		fmt.Fprintf(&sb, "line %05d á\n", i)
	}
	original := sb.String()
	if err := os.MkdirAll(filepath.Join(dir, "work"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "work", "big.md"), []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "PROJECT.md"), []byte(strings.Repeat("i", 30*1024)), 0o600); err != nil {
		t.Fatal(err)
	}

	// Reading in pages reproduces the file, tail included.
	var got strings.Builder
	offset := 0
	for pages := 0; pages < 10; pages++ {
		text := resultText(run(t, tool, map[string]any{"action": "read", "path": "work/big.md", "offset": float64(offset)}))
		i := strings.LastIndex(text, "\n\n[truncated at 64KB; work/big.md continues at offset ")
		if i < 0 {
			got.WriteString(text)
			break
		}
		got.WriteString(text[:i])
		if _, err := fmt.Sscanf(text[i:], "\n\n[truncated at 64KB; work/big.md continues at offset %d]", &offset); err != nil {
			t.Fatalf("no next offset in %q", text[i:])
		}
	}
	if got.String() != original {
		t.Fatalf("paged read differs from the file (%d vs %d bytes)", got.Len(), len(original))
	}

	// Parts go to their own files; the original shrinks in place.
	third := len(original) / 3
	for third > 0 && original[third]&0xC0 == 0x80 {
		third--
	}
	parts := []string{original[:third], original[third:]}
	if res := run(t, tool, map[string]any{"action": "write", "path": "work/big-2.md", "content": parts[1][:60*1024]}); res.IsError {
		t.Fatalf("part refused: %s", resultText(res))
	}
	if res := run(t, tool, map[string]any{"action": "append", "path": "work/big.md", "content": "more"}); !res.IsError {
		t.Fatal("an oversized file was allowed to grow")
	}
	if res := run(t, tool, map[string]any{"action": "write", "path": "work/big.md", "content": original[:100*1024]}); res.IsError {
		t.Fatalf("shrinking an oversized file refused: %s", resultText(res))
	}
	if res := run(t, tool, map[string]any{"action": "write", "path": "work/big.md", "content": "map to parts\n"}); res.IsError {
		t.Fatalf("final shrink refused: %s", resultText(res))
	}
	// The same for an oversized index: shorter is fine, growing is not.
	if res := run(t, tool, map[string]any{"action": "write", "path": "PROJECT.md", "content": strings.Repeat("i", 31*1024)}); !res.IsError {
		t.Fatal("an oversized index was allowed to grow")
	}
	if res := run(t, tool, map[string]any{"action": "write", "path": "PROJECT.md", "content": strings.Repeat("i", 20*1024)}); res.IsError {
		t.Fatalf("shrinking an oversized index refused: %s", resultText(res))
	}
	if res := run(t, tool, map[string]any{"action": "write", "path": "PROJECT.md", "content": "# map\n"}); res.IsError {
		t.Fatalf("writing the short index refused: %s", resultText(res))
	}
}

func TestReadOffsetOutsideTheFileIsAnError(t *testing.T) {
	dir := newBook(t)
	writeSized(t, dir, "people.md", 100)
	if res := run(t, NewTool(dir, false), map[string]any{"action": "read", "path": "people.md", "offset": float64(101)}); !res.IsError {
		t.Fatal("offset past the end accepted")
	}
}

func TestFailedWriteDoesNotClaimSuccessOrWarn(t *testing.T) {
	dir := newBook(t)
	tool := NewTool(dir, true)
	res := run(t, tool, map[string]any{"action": "write", "path": "../escape.md", "content": strings.Repeat("a", 60*1024)})
	if !res.IsError || strings.Contains(resultText(res), "Wrote") {
		t.Fatalf("failed write: %v %q", res.IsError, resultText(res))
	}
}
