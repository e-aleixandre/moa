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
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
	return newTool(dir, writable, false)
}

// newTool is the single builder. hideReserved makes the user's files (OWNER.md)
// invisible rather than merely unwritable, which is what a child of the owner
// gets: the preferences the user wrote for the owner are not context for a
// session the owner delegated to.
func newTool(dir string, writable, hideReserved bool) core.Tool {
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
				"description": "list: the book's files, optionally under one path. read: one file's content. search: ranked search across the book, best files first. write: replace a file. append: add to the end of a file."
			},
			"path": {
				"type": "string",
				"description": "Path inside the book, relative and without \"..\" (e.g. \"areas/erp/albaranes.md\"). Required for read, write and append; optional for list (a directory)."
			},
			"query": {
				"type": "string",
				"description": "What to look for, in words: the search is ranked, titles and aliases count most, accents and stopwords are ignored (for search)."
			},
			"limit": {
				"type": "integer",
				"description": "For search: how many files to return (default 10, max 25)."
			},
			"content": {
				"type": "string",
				"description": "Markdown to write (for write and append)."
			}
		},
		"required": ["action"]
	}`, actions)

	lockKey := bookLockPrefix + dir
	hidden := func(string) bool { return false }
	if hideReserved {
		hidden = func(rel string) bool { return ReservedFiles[rel] }
	}
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
			rawPath, _ := params["path"].(string)
			// The path is normalized ONCE, here, and every rule below is applied
			// to the clean form: checking "./OWNER.md" against the reserved list
			// before cleaning it is how a reserved file stops being reserved.
			path := CleanPath(rawPath)
			switch action {
			case "list":
				return list(dir, path, hidden)
			case "read":
				if hidden(path) {
					return core.ErrorResult(fmt.Sprintf("%s is not in the book", rawPath)), nil
				}
				return read(dir, path)
			case "search":
				query, _ := params["query"].(string)
				return search(dir, query, toInt(params["limit"]), hidden)
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

// bookLockPrefix names the book directory inside the tool's lock key. It is
// also how the read-only variant recovers the directory of the tool it
// downgrades, which has no other handle on it.
const bookLockPrefix = "book:"

// ReadOnlyVariant returns the reading half of a book tool. It exists for
// subagents: a child inherits its parent's tools, and an owner's child must be
// able to read the book without rewriting the project's record from the
// paragraph of context it was handed. It reports false for any tool that is
// not the book.
//
// The downgrade is the schema as well as the check: a child offered "write" in
// its tool list would attempt it every time the task sounds like note-taking.
// It also hides the reserved files — book/OWNER.md is the user's instructions
// to the owner, and "never to children" has to be invisibility, not a refusal
// to write.
func ReadOnlyVariant(t core.Tool) (core.Tool, bool) {
	if t.Name != ToolName || t.Execute == nil || t.LockKey == nil {
		return core.Tool{}, false
	}
	lockKey := t.LockKey(nil)
	dir, ok := strings.CutPrefix(lockKey, bookLockPrefix)
	if !ok {
		return core.Tool{}, false
	}
	child := newTool(dir, false, true)
	// The parent's lock key names the real directory; keep it so a child's read
	// still serializes against the owner's write.
	child.LockKey = t.LockKey
	return child, true
}

// CleanPath normalizes a book path to the one form every rule is applied to:
// slash-separated, no leading "./", no trailing slash. It is the first thing
// any action does with a path, so "./OWNER.md" and "areas/../OWNER.md" are the
// same file to the reserved-file check and to the walk.
func CleanPath(rel string) string {
	rel = strings.TrimSpace(filepath.ToSlash(rel))
	if rel == "" {
		return ""
	}
	if filepath.IsAbs(rel) {
		// Left as-is: resolve refuses it, and cleaning would turn an absolute
		// path into a plausible relative one.
		return rel
	}
	clean := filepath.ToSlash(filepath.Clean(rel))
	if clean == "." {
		return ""
	}
	return strings.Trim(clean, "/")
}

// resolve maps a clean book-relative path (see CleanPath) to an absolute one
// inside dir, refusing anything that could leave the book: absolute paths,
// "..", and symlinks pointing outside. The book holds the project's decisions
// and sits in the config directory next to memory and credentials; a traversal
// here reads or rewrites files that have nothing to do with it.
func resolve(dir, rel string) (string, error) {
	rel = CleanPath(rel)
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path must be relative to the book")
	}
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("path must stay inside the book")
	}
	full := filepath.Join(dir, filepath.FromSlash(rel))
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

// Entry is one file of the book as an API caller sees it: the path the book
// speaks (relative, slash-separated) and how big it is, so a list can be drawn
// without reading every file.
type Entry struct {
	Path     string    `json:"path"`
	Bytes    int64     `json:"bytes"`
	Modified time.Time `json:"modified"`
}

// Files lists the book for a reader that is not the model: same walk the tool's
// "list" action does, with the sizes a UI needs and no truncation notice in the
// middle of the data. A missing book directory is an empty book, not an error:
// an owner whose book was never seeded still has a book page to open.
func Files(dir string) ([]Entry, error) {
	var out []Entry
	err := walkBook(dir, func(rel, full string) error {
		info, infoErr := os.Stat(full)
		if infoErr != nil {
			return nil
		}
		out = append(out, Entry{
			Path:     rel,
			Bytes:    info.Size(),
			Modified: info.ModTime().UTC(),
		})
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	if len(out) > maxListEntries {
		out = out[:maxListEntries]
	}
	return out, nil
}

// ErrTooLarge reports a write past maxWriteBytes.
var ErrTooLarge = fmt.Errorf("the file is larger than %dKB; split it across book files", maxWriteBytes/1024)

// ReadFile returns one book file verbatim, refusing anything that would leave
// the book (resolve is the single guard, shared with the tool). The tool's
// 64KB truncation is deliberately NOT applied: it exists to protect a prompt,
// and a person opening a file wants the file.
func ReadFile(dir, rel string) ([]byte, error) {
	full, err := resolve(dir, rel)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(full)
}

// WriteFile replaces one book file, through the same path guard and the same
// atomic write the tool uses.
func WriteFile(dir, rel string, content []byte) error {
	if len(content) > maxWriteBytes {
		return ErrTooLarge
	}
	full, err := resolve(dir, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return err
	}
	return writeFileAtomic(full, content, 0o600)
}

// list shows the book's files, optionally under one directory, each with the
// title from its frontmatter: an area is a path, so listing it is how the
// owner sees what an area holds without an index file to keep in sync.
//
// Files under work/ also carry how long ago they moved: "what is stopped and
// since when" is a question the owner is asked on every turn, and a listing
// without times cannot answer it.
func list(dir, under string, hidden func(rel string) bool) (core.Result, error) {
	prefix := CleanPath(under)
	type entry struct {
		path, title string
		modified    time.Time
	}
	var files []entry
	err := walkBook(dir, func(rel, full string) error {
		if hidden != nil && hidden(rel) {
			return nil
		}
		if prefix != "" && rel != prefix && !strings.HasPrefix(rel, prefix+"/") {
			return nil
		}
		title := ""
		if data, readErr := os.ReadFile(full); readErr == nil && len(data) <= maxFileBytes {
			fm, _ := parseFrontmatter(string(data))
			title = fm.Title
		}
		var modified time.Time
		if info, statErr := os.Stat(full); statErr == nil {
			modified = info.ModTime()
		}
		files = append(files, entry{path: rel, title: title, modified: modified})
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return core.ErrorResult(fmt.Sprintf("cannot read the book: %v", err)), nil
	}
	if len(files) == 0 {
		if prefix != "" {
			return core.TextResult(fmt.Sprintf("Nothing in the book under %s.", prefix)), nil
		}
		return core.TextResult("The book is empty."), nil
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	truncated := false
	if len(files) > maxListEntries {
		files = files[:maxListEntries]
		truncated = true
	}
	var sb strings.Builder
	now := time.Now()
	for _, f := range files {
		sb.WriteString("- ")
		sb.WriteString(f.path)
		if f.title != "" {
			sb.WriteString(" — ")
			sb.WriteString(f.title)
		}
		if strings.HasPrefix(f.path, "work/") && !f.modified.IsZero() {
			fmt.Fprintf(&sb, " (moved %s)", RelativeAge(now.Sub(f.modified)))
		}
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

// search ranks the book (see search.go) rather than listing every line that
// contains the query: a substring match returns the forty files that mention a
// word once above the one sheet that is about it.
func search(dir, query string, limit int, hidden func(rel string) bool) (core.Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return core.ErrorResult("query is required for search"), nil
	}
	hits, total, err := searchBook(dir, query, limit, hidden)
	if err != nil {
		return core.ErrorResult(fmt.Sprintf("cannot search the book: %v", err)), nil
	}
	return core.TextResult(formatSearch(hits, total, query)), nil
}

// toInt reads a JSON number parameter, which arrives as float64.
func toInt(value any) int {
	switch n := value.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

// write refuses the reserved files on the already-normalized path (see
// CleanPath): the refusal is the tool's, and a cooperative agent with bash
// could still edit the file by hand — what this guarantees is that the book
// tool never does it.
func write(dir, rel, content string, appendTo bool) (core.Result, error) {
	if ReservedFiles[CleanPath(rel)] {
		return core.ErrorResult(fmt.Sprintf("%s belongs to the user: it is what they ask of you, and you do not write it", rel)), nil
	}
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

// RelativeAge says how long ago something happened, at the resolution a
// decision is made on: minutes for today, hours, then days. The phrase carries
// its own "ago" so "just now ago" cannot happen. It is exported because the
// owner's sessions listing answers the same question about a conversation.
func RelativeAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}
