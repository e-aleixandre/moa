// Package memory provides cross-session memory persistence as a set of
// typed, single-fact files with a lightweight frontmatter header.
//
// Facts live in two scopes:
//   - global  (~/.config/moa/global/memory/<slug>.md)      — user, feedback
//   - project (~/.config/moa/projects/<hash>/memory/<slug>.md) — project, reference
//
// where <hash> is SHA256(CanonicalizePath(workspaceRoot))[:16]. Only the index
// (one line per fact) is injected into the prompt; full bodies are read on
// demand. The index is derived from the files at load — moa never writes a
// MEMORY.md of its own.
package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ealeixandre/moa/pkg/core"
)

const (
	// MaxFactSize is the hard per-fact limit (16KB).
	MaxFactSize = 16 * 1024
	// maxIndexBytes caps the size of the index injected into the prompt.
	maxIndexBytes = 8 * 1024
)

// Scope is where a fact lives.
type Scope int

const (
	ScopeProject Scope = iota // project-local (project, reference)
	ScopeGlobal               // cross-project (user, feedback)
)

func (s Scope) String() string {
	if s == ScopeGlobal {
		return "global"
	}
	return "project"
}

// Type classifies a fact and (via scopeForType) decides its scope.
type Type string

const (
	TypeUser      Type = "user"
	TypeFeedback  Type = "feedback"
	TypeProject   Type = "project"
	TypeReference Type = "reference"
)

// ValidType reports whether t is one of the four known types.
func ValidType(t Type) bool {
	switch t {
	case TypeUser, TypeFeedback, TypeProject, TypeReference:
		return true
	}
	return false
}

// ScopeForType routes a type to its scope (D2): user/feedback are global,
// everything else is project-local.
func ScopeForType(t Type) Scope {
	if t == TypeUser || t == TypeFeedback {
		return ScopeGlobal
	}
	return ScopeProject
}

// slugRe validates a fact name: lowercase ASCII kebab-case.
var slugRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidName reports whether name is a valid kebab-case ASCII slug.
func ValidName(name string) bool { return slugRe.MatchString(name) }

// Memory is a single fact.
type Memory struct {
	Name        string
	Description string
	Type        Type
	Body        string
	Scope       Scope
	Path        string // absolute path to the file (set on read/list)
}

// ID is the canonical, scope-qualified identifier used in the index and by the
// read/delete actions (e.g. "project/uses-docker").
func (m Memory) ID() string { return m.Scope.String() + "/" + m.Name }

// Store manages the global and project memory directories for one workspace.
type Store struct {
	globalDir   string // ~/.config/moa/global/memory
	projectDir  string // ~/.config/moa/projects/<hash>/memory
	projectRoot string // ~/.config/moa/projects/<hash> — holds the v1 MEMORY.md
}

// New builds a Store. configDir is the moa config root (~/.config/moa);
// workspaceRoot selects the project scope.
func New(configDir, workspaceRoot string) *Store {
	projectRoot := filepath.Join(configDir, "projects", projectHash(workspaceRoot))
	return &Store{
		globalDir:   filepath.Join(configDir, "global", "memory"),
		projectDir:  filepath.Join(projectRoot, "memory"),
		projectRoot: projectRoot,
	}
}

// GlobalDir returns the global memory directory.
func (s *Store) GlobalDir() string { return s.globalDir }

// ProjectDir returns this workspace's project memory directory.
func (s *Store) ProjectDir() string { return s.projectDir }

func (s *Store) dirFor(scope Scope) string {
	if scope == ScopeGlobal {
		return s.globalDir
	}
	return s.projectDir
}

// List scans both scopes and returns all facts, project scope first, then by
// name. Global and project facts with the same name coexist as distinct IDs.
func (s *Store) List() []Memory {
	byID := make(map[string]Memory)
	s.scanScope(s.globalDir, ScopeGlobal, byID)
	s.scanScope(s.projectDir, ScopeProject, byID)

	out := make([]Memory, 0, len(byID))
	for _, m := range byID {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Scope != out[j].Scope {
			return out[i].Scope < out[j].Scope // ScopeProject(0) before ScopeGlobal(1)
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// FormatIndex renders the index as one bullet per fact (no framing — the caller
// adds it). Empty if no facts. Bounded by maxIndexBytes with a truncation note.
func (s *Store) FormatIndex(mems []Memory) string {
	if len(mems) == 0 {
		return ""
	}
	var sb strings.Builder
	truncated := false
	for _, m := range mems {
		line := "- " + m.ID() + " — " + m.Description + "\n"
		if sb.Len()+len(line) > maxIndexBytes {
			truncated = true
			break
		}
		sb.WriteString(line)
	}
	if truncated {
		slog.Warn("memory: index truncated in prompt", "limit_bytes", maxIndexBytes, "facts", len(mems))
		sb.WriteString("- … (index truncated; use the memory tool's list action to see all)\n")
	}
	return sb.String()
}

// Read returns the full fact for a canonical ID ("project/foo", "global/foo")
// or a bare name. A bare name that exists in both scopes is an error (D9).
func (s *Store) Read(id string) (Memory, bool, error) {
	path, scope, err := s.resolve(id)
	if err != nil {
		return Memory{}, false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Memory{}, false, nil
		}
		return Memory{}, false, err
	}
	m, err := parseFact(data)
	if err != nil {
		return Memory{}, false, err
	}
	m.Scope = scope
	m.Path = path
	m.Name = strings.TrimSuffix(filepath.Base(path), ".md")
	return m, true, nil
}

// Write creates or overwrites a single fact. Scope is derived from Type (D2);
// an invalid type or name is a hard error (D10).
func (s *Store) Write(m Memory) error {
	if !ValidName(m.Name) {
		return fmt.Errorf("invalid name %q: use kebab-case ascii [a-z0-9-]", m.Name)
	}
	if !ValidType(m.Type) {
		return fmt.Errorf("invalid type %q: use user|feedback|project|reference", m.Type)
	}
	if strings.TrimSpace(m.Description) == "" {
		return errors.New("description is required")
	}
	data := serialize(m)
	if len(data) > MaxFactSize {
		return fmt.Errorf("fact exceeds %dKB limit (%d bytes)", MaxFactSize/1024, len(data))
	}
	dir := s.dirFor(ScopeForType(m.Type))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating memory dir: %w", err)
	}
	return writeFileAtomic(filepath.Join(dir, m.Name+".md"), data)
}

// Delete removes a single fact by canonical ID or bare name.
func (s *Store) Delete(id string) error {
	path, _, err := s.resolve(id)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("memory %q not found", id)
	}
	return err
}

// MigrateV1IfNeeded wraps a flat v1 MEMORY.md into a single legacy fact, then
// retires the flat file. Idempotent even across partial failures: the flat file
// is only renamed after the fact is safely written, so an interrupted run
// simply retries next time (D6).
func (s *Store) MigrateV1IfNeeded() error {
	v1Path := filepath.Join(s.projectRoot, "MEMORY.md")
	data, err := os.ReadFile(v1Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to migrate (fresh, or already migrated)
		}
		return err
	}
	bak := v1Path + ".v1.bak"

	if strings.TrimSpace(string(data)) == "" {
		return os.Rename(v1Path, bak) // empty v1: just retire it
	}

	legacy := Memory{
		Name:        "notas-legado-v1",
		Description: "notas migradas de la memoria v1, pendientes de curar",
		Type:        TypeProject,
		Body:        string(data),
		Scope:       ScopeProject,
	}
	if err := os.MkdirAll(s.projectDir, 0o700); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(s.projectDir, legacy.Name+".md"), serialize(legacy)); err != nil {
		return err
	}
	return os.Rename(v1Path, bak)
}

// resolve maps an ID to a file path and scope. A scope-qualified ID resolves
// directly; a bare name is looked up in both scopes and rejected if ambiguous.
// A bare name found in neither scope defaults to the project path (so callers
// surface a clean "not found").
func (s *Store) resolve(id string) (path string, scope Scope, err error) {
	if scopeStr, name, ok := strings.Cut(id, "/"); ok {
		switch scopeStr {
		case "project":
			return filepath.Join(s.projectDir, name+".md"), ScopeProject, nil
		case "global":
			return filepath.Join(s.globalDir, name+".md"), ScopeGlobal, nil
		default:
			return "", 0, fmt.Errorf("invalid scope %q: use \"project/%s\" or \"global/%s\"", scopeStr, name, name)
		}
	}
	pPath := filepath.Join(s.projectDir, id+".md")
	gPath := filepath.Join(s.globalDir, id+".md")
	inP, inG := fileExists(pPath), fileExists(gPath)
	if inP && inG {
		return "", 0, fmt.Errorf("%q exists in both scopes; qualify it as \"project/%s\" or \"global/%s\"", id, id, id)
	}
	if inG {
		return gPath, ScopeGlobal, nil
	}
	return pPath, ScopeProject, nil
}

// scanScope reads all <dir>/*.md facts into out (keyed by canonical ID),
// skipping reserved files (MEMORY.md and its backups) and malformed facts.
func (s *Store) scanScope(dir string, scope Scope, out map[string]Memory) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // missing dir = no facts
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || isReservedFile(e.Name()) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("memory: cannot read fact", "path", path, "error", err)
			continue
		}
		m, err := parseFact(data)
		if err != nil {
			slog.Warn("memory: skipping malformed fact", "path", path, "error", err)
			continue
		}
		// The filename is authoritative for the slug (keeps ID↔file 1:1).
		fileName := strings.TrimSuffix(e.Name(), ".md")
		if m.Name != "" && m.Name != fileName {
			slog.Warn("memory: frontmatter name differs from filename", "path", path, "frontmatter", m.Name)
		}
		m.Name = fileName
		m.Scope = scope
		m.Path = path
		out[m.ID()] = m
	}
}

// isReservedFile reports whether name is a generated/legacy index file rather
// than a fact. moa never writes MEMORY.md, but a v1 backup or a hand-placed
// index must not be parsed as a fact (D3).
func isReservedFile(name string) bool {
	return name == "MEMORY.md" || strings.HasPrefix(name, "MEMORY.md.")
}

// parseFact parses a fact file: a `---` frontmatter block (name/description/
// type) followed by the markdown body. Tolerates CRLF, optional quotes around
// values, and `:` inside a value. An unknown/missing type defaults to project
// (D10). Missing or unterminated frontmatter is an error.
func parseFact(data []byte) (Memory, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return Memory{}, errors.New("missing frontmatter")
	}
	closeIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			closeIdx = i
			break
		}
	}
	if closeIdx < 0 {
		return Memory{}, errors.New("unterminated frontmatter")
	}

	var m Memory
	for _, line := range lines[1:closeIdx] {
		key, val, ok := splitKV(line)
		if !ok {
			continue
		}
		switch key {
		case "name":
			m.Name = val
		case "description":
			m.Description = val
		case "type":
			m.Type = Type(val)
		}
	}
	if !ValidType(m.Type) {
		m.Type = TypeProject
	}
	m.Body = strings.Trim(strings.Join(lines[closeIdx+1:], "\n"), "\n")
	return m, nil
}

// splitKV splits "key: value" on the first colon, trims whitespace and one
// layer of surrounding quotes.
func splitKV(line string) (key, val string, ok bool) {
	i := strings.IndexByte(line, ':')
	if i < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:i])
	val = strings.TrimSpace(line[i+1:])
	if len(val) >= 2 {
		if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
			val = val[1 : len(val)-1]
		}
	}
	return key, val, key != ""
}

// serialize renders a fact back to its file form.
func serialize(m Memory) []byte {
	var sb strings.Builder
	sb.WriteString("---\nname: ")
	sb.WriteString(m.Name)
	sb.WriteString("\ndescription: ")
	sb.WriteString(m.Description)
	sb.WriteString("\ntype: ")
	sb.WriteString(string(m.Type))
	sb.WriteString("\n---\n\n")
	sb.WriteString(strings.TrimRight(m.Body, "\n"))
	sb.WriteByte('\n')
	return []byte(sb.String())
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("writing memory: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("saving memory: %w", err)
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// projectHash returns a 16-char hex hash of the canonical workspace path.
func projectHash(workspaceRoot string) string {
	canonical, err := core.CanonicalizePath(workspaceRoot)
	if err != nil {
		canonical = filepath.Clean(workspaceRoot)
	}
	h := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(h[:8])
}
