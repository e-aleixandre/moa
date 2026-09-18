// Package book implements the project owner's book: the curated, on-disk
// record of a project that lives beside its memory
// (~/.config/moa/codebases/<key>/book/).
//
// Memory holds atomic operational facts any session may write. The book holds
// criteria and narrative, and has a single author: the owner. That is why it
// is a separate store with a separate tool — two write policies cannot share
// one.
//
// Only PROJECT.md is injected into a prompt (see pkg/owner). Everything else
// is read on demand through this tool, so the book can grow without taxing
// every turn.
package book

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/e-aleixandre/moa/pkg/core"
)

const (
	// maxFileBytes caps a single read. A book file past this is being used as
	// an archive, and the tail is dropped rather than the context blown.
	maxFileBytes = 64 * 1024
	// maxSearchBytes caps a whole search response, like the memory tool's.
	maxSearchBytes = 6 * 1024
	// maxListEntries caps a listing so a book with thousands of files still
	// answers something usable.
	maxListEntries = 200
	// maxWriteBytes caps one write. The book is prose, not a data dump.
	maxWriteBytes = 256 * 1024
)

// ToolName is the registered name, so callers can find the tool in a registry
// without repeating the literal.
const ToolName = "book"

// NewTool builds the book tool over dir. When writable is false the tool
// offers reading only: every session of the codebase reads the book, but only
// the owner writes it, and a tool that merely refused at execution time would
// still invite the attempt on every turn.
func NewTool(dir string, writable bool) core.Tool {
	description := "Read the project book: the owner's curated record of this project " +
		"(decisions and why they were taken, people and what they ask for, deep context per " +
		"area). PROJECT.md is its index and is already in your context; use this to read the " +
		"detail behind a line. The book is the owner's: you read it, you do not write it."
	actions := `["list", "read", "search"]`
	effect := core.EffectReadOnly
	if writable {
		description = "Read and write the project book: your curated record of this project " +
			"(decisions and why, people and what they ask for, deep context per area). " +
			"PROJECT.md is the index every session of this project is given — keep it accurate " +
			"and short, and put the detail in other files. Write what you decided and why, " +
			"when you decided it, not a summary of what you did."
		actions = `["list", "read", "search", "write", "append"]`
		effect = core.EffectWritePath
	}
	params := fmt.Sprintf(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": %s,
				"description": "list: every file in the book. read: one file's content. search: find text across the book. write: replace a file. append: add to the end of a file."
			},
			"path": {
				"type": "string",
				"description": "Path inside the book, relative and without \"..\" (e.g. \"decisions/2026-08-import.md\"). Required for read, write and append."
			},
			"query": {
				"type": "string",
				"description": "Text to look for, case-insensitive (for search)."
			},
			"content": {
				"type": "string",
				"description": "Markdown to write (for write and append)."
			}
		},
		"required": ["action"]
	}`, actions)

	lockKey := "book:" + dir
	return core.Tool{
		Name:        ToolName,
		Label:       "Project book",
		Description: description,
		Parameters:  json.RawMessage(params),
		Effect:      effect,
		LockKey: func(map[string]any) string {
			return lockKey
		},
		Execute: func(_ context.Context, params map[string]any, _ func(core.Result)) (core.Result, error) {
			action, _ := params["action"].(string)
			path, _ := params["path"].(string)
			switch action {
			case "list":
				return list(dir)
			case "read":
				return read(dir, path)
			case "search":
				query, _ := params["query"].(string)
				return search(dir, query)
			case "write", "append":
				if !writable {
					return core.ErrorResult("the book is written by the project owner only"), nil
				}
				content, _ := params["content"].(string)
				return write(dir, path, content, action == "append")
			default:
				return core.ErrorResult(fmt.Sprintf("unknown action %q", action)), nil
			}
		},
	}
}

// ReadOnlyVariant returns the reading half of a book tool, delegating to the
// original for the actions it keeps. It exists for subagents: a child inherits
// its parent's tools, and an owner's child must be able to read the book
// without rewriting the project's record from the paragraph of context it was
// handed. It reports false for any tool that is not the book.
//
// The downgrade is the schema as well as the check: a child offered "write" in
// its tool list would attempt it every time the task sounds like note-taking.
func ReadOnlyVariant(t core.Tool) (core.Tool, bool) {
	if t.Name != ToolName || t.Execute == nil {
		return core.Tool{}, false
	}
	shape := NewTool("", false)
	inner := t.Execute
	shape.Execute = func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
		switch action, _ := params["action"].(string); action {
		case "list", "read", "search":
			return inner(ctx, params, onUpdate)
		default:
			return core.ErrorResult("the book is written by the project owner only"), nil
		}
	}
	// The parent's lock key names the real directory; keep it so a child's read
	// still serializes against the owner's write.
	shape.LockKey = t.LockKey
	return shape, true
}

// resolve maps a book-relative path to an absolute one inside dir, refusing
// anything that could leave the book: absolute paths, "..", and symlinks
// pointing outside. The book holds the project's decisions and sits in the
// config directory next to memory and credentials; a traversal here reads or
// rewrites files that have nothing to do with it.
func resolve(dir, rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path must be relative to the book")
	}
	clean := filepath.Clean(filepath.ToSlash(rel))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path must stay inside the book")
	}
	full := filepath.Join(dir, filepath.FromSlash(clean))
	// Resolve symlinks on whatever part of the path already exists: a link
	// inside the book pointing out of it must not become a door, while a file
	// that does not exist yet (a write) is still a legal target.
	if resolved, err := canonicalWithinDir(dir, full); err != nil {
		return "", err
	} else {
		full = resolved
	}
	return full, nil
}

// canonicalWithinDir resolves the deepest existing ancestor of path and checks
// the result still lives under dir.
func canonicalWithinDir(dir, path string) (string, error) {
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("the book directory is unavailable: %w", err)
	}
	probe := path
	var trailing []string
	for {
		resolved, err := filepath.EvalSymlinks(probe)
		if err == nil {
			for i := len(trailing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, trailing[i])
			}
			if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
				return "", fmt.Errorf("path must stay inside the book")
			}
			return resolved, nil
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", fmt.Errorf("path must stay inside the book")
		}
		trailing = append(trailing, filepath.Base(probe))
		probe = parent
	}
}

func list(dir string) (core.Result, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable corner of the book: skip it, don't fail the call
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return core.ErrorResult(fmt.Sprintf("cannot read the book: %v", err)), nil
	}
	if len(files) == 0 {
		return core.TextResult("The book is empty."), nil
	}
	sort.Strings(files)
	truncated := false
	if len(files) > maxListEntries {
		files = files[:maxListEntries]
		truncated = true
	}
	var sb strings.Builder
	for _, f := range files {
		sb.WriteString("- ")
		sb.WriteString(f)
		sb.WriteString("\n")
	}
	if truncated {
		fmt.Fprintf(&sb, "… (listing truncated at %d files; use search)\n", maxListEntries)
	}
	return core.TextResult(sb.String()), nil
}

func read(dir, rel string) (core.Result, error) {
	full, err := resolve(dir, rel)
	if err != nil {
		return core.ErrorResult(err.Error()), nil
	}
	data, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			return core.ErrorResult(fmt.Sprintf("%s is not in the book", rel)), nil
		}
		return core.ErrorResult(fmt.Sprintf("cannot read %s: %v", rel, err)), nil
	}
	if len(data) > maxFileBytes {
		return core.TextResult(truncateUTF8(string(data), maxFileBytes) +
			fmt.Sprintf("\n\n[truncated at %dKB]", maxFileBytes/1024)), nil
	}
	return core.TextResult(string(data)), nil
}

func search(dir, query string) (core.Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return core.ErrorResult("query is required for search"), nil
	}
	needle := strings.ToLower(query)
	var sb strings.Builder
	hits := 0
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if sb.Len() >= maxSearchBytes {
			return filepath.SkipAll
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil || len(data) > maxFileBytes {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return nil
		}
		for i, line := range strings.Split(string(data), "\n") {
			if !strings.Contains(strings.ToLower(line), needle) {
				continue
			}
			hits++
			entry := fmt.Sprintf("- %s:%d — %s\n", filepath.ToSlash(rel), i+1, strings.TrimSpace(line))
			if sb.Len()+len(entry) > maxSearchBytes {
				return filepath.SkipAll
			}
			sb.WriteString(entry)
		}
		return nil
	})
	if err != nil {
		return core.ErrorResult(fmt.Sprintf("cannot search the book: %v", err)), nil
	}
	if hits == 0 {
		return core.TextResult(fmt.Sprintf("Nothing in the book matches %q.", query)), nil
	}
	return core.TextResult(sb.String()), nil
}

func write(dir, rel, content string, appendTo bool) (core.Result, error) {
	if len(content) > maxWriteBytes {
		return core.ErrorResult(fmt.Sprintf("content is larger than %dKB; split it across book files", maxWriteBytes/1024)), nil
	}
	full, err := resolve(dir, rel)
	if err != nil {
		return core.ErrorResult(err.Error()), nil
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return core.ErrorResult(fmt.Sprintf("cannot create %s: %v", rel, err)), nil
	}
	if appendTo {
		existing, readErr := os.ReadFile(full)
		if readErr != nil && !os.IsNotExist(readErr) {
			return core.ErrorResult(fmt.Sprintf("cannot read %s: %v", rel, readErr)), nil
		}
		if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
			existing = append(existing, '\n')
		}
		content = string(existing) + content
	}
	if err := writeFileAtomic(full, []byte(content), 0o600); err != nil {
		return core.ErrorResult(fmt.Sprintf("cannot write %s: %v", rel, err)), nil
	}
	verb := "Wrote"
	if appendTo {
		verb = "Appended to"
	}
	return core.TextResult(fmt.Sprintf("%s %s in the book.", verb, rel)), nil
}

// truncateUTF8 caps s at limit bytes without splitting a rune.
func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}
