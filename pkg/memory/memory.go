// Package memory provides cross-session memory persistence as a set of
// typed, single-fact files with a lightweight frontmatter header.
//
// Facts live in two scopes:
//   - global  (~/.config/moa/global/memory/<slug>.md)          — cross-project
//   - project (~/.config/moa/codebases/<key>/memory/<slug>.md) — this repository
//
// where <key> is core.CodebaseKey(workspaceRoot): the identity of the
// repository the workspace belongs to, so every git worktree of one repo reads
// and writes the same facts and deleting a worktree no longer orphans what was
// learned in it. Only the index (one line per fact) is injected into the
// prompt; full bodies are read on demand. The index is derived from the files
// at load — moa never writes a MEMORY.md of its own.
package memory

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/e-aleixandre/moa/pkg/core"
)

const (
	// MaxFactSize is the hard per-fact limit (16KB).
	MaxFactSize = 16 * 1024
	// maxIndexBytes caps the size of the index injected into the prompt.
	maxIndexBytes = 8 * 1024
	// MaxDescriptionBytes caps the one-line hook at write time. The
	// description is the only part of a fact paid for on every turn, so a long
	// one taxes every session; detail belongs in the body.
	MaxDescriptionBytes = 180
	// longDescriptionBytes is where a description stops being a hook and
	// starts being prose. Advisory only: never a reason to reject a write.
	longDescriptionBytes = 120
)

// Scope is where a fact lives.
type Scope int

const (
	ScopeProject Scope = iota // this repository only
	ScopeGlobal               // every project
)

func (s Scope) String() string {
	if s == ScopeGlobal {
		return "global"
	}
	return "project"
}

// Type is the legacy fact classification. The four-value taxonomy existed only
// to pick one of two scopes, so `scope:` is the source of truth. `type:` is
// still written for interoperability with older binaries that route files by
// it, and an existing compatible value is preserved on rewrite.
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

// ScopeForType routes a legacy type to its scope (D2): user/feedback are
// global, everything else is project-local. Only reachable through old files.
func ScopeForType(t Type) Scope {
	if t == TypeUser || t == TypeFeedback {
		return ScopeGlobal
	}
	return ScopeProject
}

// ParseScope maps the tool-facing scope name to a Scope.
func ParseScope(s string) (Scope, bool) {
	switch s {
	case "global":
		return ScopeGlobal, true
	case "project":
		return ScopeProject, true
	}
	return 0, false
}

// Lifecycle is a fact's declared expiry contract. It replaces an inferred
// bool: "no invalidate_when" used to mean durable, which silently promoted
// every pre-lifecycle file to permanent instead of admitting that it never
// declared anything.
type Lifecycle int

const (
	// LifecycleLegacy is a file written before lifecycles existed: it declares
	// neither `durable: true` nor `invalidate_when`. Readable, but a write
	// must upgrade it to one of the other two.
	LifecycleLegacy Lifecycle = iota
	// LifecycleDurable is an explicit `durable: true`.
	LifecycleDurable
	// LifecycleConditional carries a checkable `invalidate_when` condition.
	LifecycleConditional
)

func (l Lifecycle) String() string {
	switch l {
	case LifecycleDurable:
		return "durable"
	case LifecycleConditional:
		return "conditional"
	}
	return "legacy"
}

// slugRe validates a fact name: lowercase ASCII kebab-case.
var slugRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidName reports whether name is a valid kebab-case ASCII slug.
func ValidName(name string) bool { return slugRe.MatchString(name) }

// Memory is a single fact.
type Memory struct {
	Name           string
	Description    string
	Type           Type // legacy routing field, preserved when compatible with Scope
	InvalidateWhen string
	Lifecycle      Lifecycle
	Body           string
	Scope          Scope
	Path           string // absolute path to the file (set on read/list)
}

// ID is the canonical, scope-qualified identifier used in the index and by the
// read/delete actions (e.g. "project/uses-docker").
func (m Memory) ID() string { return m.Scope.String() + "/" + m.Name }

// Store manages the global and project memory directories for one workspace.
type Store struct {
	globalDir    string // ~/.config/moa/global/memory
	projectDir   string // ~/.config/moa/codebases/<key>/memory
	codebaseRoot string // ~/.config/moa/codebases/<key>
	configDir    string // ~/.config/moa
	codebaseKey  string
	// legacyProjectRoot is ~/.config/moa/projects/<ProjectHash>: where project
	// memory lived while a project was identified by its path, and where the v1
	// MEMORY.md was left. Only the migrations in migrate_codebase.go and
	// MigrateV1IfNeeded look at it; nothing reads facts from there at runtime.
	legacyProjectRoot string
}

// New builds a Store. configDir is the moa config root (~/.config/moa);
// workspaceRoot selects the project scope.
//
// It does not migrate anything: call Migrate for that, once, at startup.
func New(configDir, workspaceRoot string) *Store {
	key := core.CodebaseKey(workspaceRoot)
	codebaseRoot := filepath.Join(configDir, "codebases", key)
	return &Store{
		globalDir:         filepath.Join(configDir, "global", "memory"),
		projectDir:        filepath.Join(codebaseRoot, "memory"),
		codebaseRoot:      codebaseRoot,
		configDir:         configDir,
		codebaseKey:       key,
		legacyProjectRoot: filepath.Join(configDir, "projects", core.ProjectHash(workspaceRoot)),
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
	st := IndexStatusOf(mems)
	var sb strings.Builder
	sb.WriteString(st.text)
	if st.Dropped > 0 {
		slog.Warn("memory: index truncated in prompt",
			"limit_bytes", maxIndexBytes, "facts", len(mems),
			"dropped_project", st.DroppedProject, "dropped_global", st.DroppedGlobal)
		fmt.Fprintf(&sb, "- … (%d more facts not shown; use the memory tool's list action to see all)\n", st.Dropped)
	}
	return sb.String()
}

// IndexStatus reports how much of the prompt index budget a fact set uses and
// how many facts do not fit. It is the same computation FormatIndex performs
// (including the two-way roll-over), exposed so a write can tell the agent
// that what it just saved may never reach the prompt.
type IndexStatus struct {
	UsedBytes      int
	BudgetBytes    int
	Facts          int
	Dropped        int
	DroppedProject int
	DroppedGlobal  int
	text           string
}

// IndexStatusOf renders the index for mems and measures it.
func IndexStatusOf(mems []Memory) IndexStatus {
	if len(mems) == 0 {
		return IndexStatus{BudgetBytes: maxIndexBytes}
	}
	// Global facts are cross-project preferences and standing user
	// instructions; project facts are far more numerous and churn faster.
	// A single ordered budget starved globals completely (measured: 0 of 28
	// globals reached the prompt with 102 project facts present), which
	// silently dropped standing instructions. Reserve a share of the budget
	// for globals, then let project facts use whatever is left.
	var globals, projects []Memory
	for _, m := range mems {
		if m.Scope == ScopeGlobal {
			globals = append(globals, m)
		} else {
			projects = append(projects, m)
		}
	}

	// Roll over unused budget in BOTH directions: render each scope against
	// its reserved share first, then let each one claim whatever the other
	// left unused. A one-way roll-over wasted most of the budget in the
	// mirror case (many globals, few project facts).
	globalBudget := maxIndexBytes * globalIndexShareNum / globalIndexShareDen
	projectFirst, _ := renderIndexLines(projects, maxIndexBytes-globalBudget)
	globalText, globalDropped := renderIndexLines(globals, maxIndexBytes-len(projectFirst))
	projectText, projectDropped := renderIndexLines(projects, maxIndexBytes-len(globalText))

	text := projectText + globalText
	return IndexStatus{
		UsedBytes:      len(text),
		BudgetBytes:    maxIndexBytes,
		Facts:          len(mems),
		Dropped:        globalDropped + projectDropped,
		DroppedProject: projectDropped,
		DroppedGlobal:  globalDropped,
		text:           text,
	}
}

// globalIndexShare reserves a fraction of the index budget for global facts.
const (
	globalIndexShareNum = 1
	globalIndexShareDen = 3
)

// renderIndexLines emits one bullet per fact until budget is exhausted.
// Returns the rendered text and how many facts did not fit.
func renderIndexLines(mems []Memory, budget int) (string, int) {
	var sb strings.Builder
	for i, m := range mems {
		line := "- " + m.ID() + " — " + m.Description + "\n"
		if sb.Len()+len(line) > budget {
			return sb.String(), len(mems) - i
		}
		sb.WriteString(line)
	}
	return sb.String(), 0
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

// Write creates or overwrites a single fact in m.Scope. An invalid name,
// description or lifecycle declaration is a hard error (D10). It returns an
// advisory note (possibly empty) for the caller to surface: a write is not
// rejected for style, only for correctness.
func (s *Store) Write(m Memory) (string, error) {
	if !ValidName(m.Name) {
		return "", fmt.Errorf("invalid name %q: use kebab-case ascii [a-z0-9-]", m.Name)
	}
	if strings.TrimSpace(m.Description) == "" {
		return "", errors.New("description is required")
	}
	// The description is a one-line hook in a line-oriented header: the same
	// reason invalidate_when rejects line breaks applies here, plus control
	// characters that would corrupt the rendered index.
	if strings.ContainsAny(m.Description, "\r\n") {
		return "", errors.New("description must be a single line")
	}
	if i := strings.IndexFunc(m.Description, isControl); i >= 0 {
		return "", fmt.Errorf("description must not contain control characters (found %q)", m.Description[i])
	}
	if n := len(m.Description); n > MaxDescriptionBytes {
		return "", fmt.Errorf("description is %d bytes, over the %d-byte limit: it is a one-line hook shown in the index, so keep the detail in the body", n, MaxDescriptionBytes)
	}
	// Whitespace cannot express a checkable lifecycle. Normalize it so a
	// durable declaration with an empty condition also keeps the legacy durable
	// on-disk representation (no invalidate_when field).
	if strings.TrimSpace(m.InvalidateWhen) == "" {
		m.InvalidateWhen = ""
	}
	durable := m.Lifecycle == LifecycleDurable
	if m.InvalidateWhen == "" && !durable {
		return "", errors.New("memory must declare its lifecycle: an ephemeral fact needs invalidate_when with a condition another agent can check without asking the user (for example, \"when issue #84 is closed\"), while a permanent fact needs durable: true (for example, a user preference)")
	}
	if m.InvalidateWhen != "" && durable {
		return "", errors.New("invalidate_when and durable are mutually exclusive: declare either a checkable expiry condition or durable: true")
	}
	if m.InvalidateWhen != "" {
		m.Lifecycle = LifecycleConditional
	}
	// Frontmatter is line-oriented. Reject line breaks rather than silently
	// changing the condition: a changed condition could make a fact appear to
	// expire for a different reason, and raw newlines could inject header keys.
	if strings.ContainsAny(m.InvalidateWhen, "\r\n") {
		return "", errors.New("invalidate_when must be a single line")
	}
	data := serialize(m)
	if len(data) > MaxFactSize {
		return "", fmt.Errorf("fact exceeds %dKB limit (%d bytes)", MaxFactSize/1024, len(data))
	}
	dir := s.dirFor(m.Scope)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating memory dir: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(dir, m.Name+".md"), data); err != nil {
		return "", err
	}
	var note string
	if n := len(m.Description); n > longDescriptionBytes {
		note = fmt.Sprintf("Note: the description is %d bytes (limit %d) and is paid for on every turn — shorter hooks read better in the index.", n, MaxDescriptionBytes)
	}
	return note, nil
}

// isControl reports whether r is a C0/C1 control character (tab included: the
// index is rendered as a single bullet line).
func isControl(r rune) bool { return r < 0x20 || (r >= 0x7f && r <= 0x9f) }

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

// v1FactName is the fact a flat v1 MEMORY.md becomes. The codebase migration
// wraps sibling worktrees' flat files under the same name and the same bytes,
// so whichever migration runs first the other finds its work already done.
const v1FactName = "notas-legado-v1"

func v1Fact(body string) Memory {
	return Memory{
		Name:        v1FactName,
		Description: "notas migradas de la memoria v1, pendientes de curar",
		Body:        body,
		Lifecycle:   LifecycleDurable,
		Scope:       ScopeProject,
	}
}

// MigrateV1IfNeeded wraps a flat v1 MEMORY.md into a single legacy fact, then
// retires the flat file. Idempotent even across partial failures: the flat file
// is only renamed after the fact is safely written, so an interrupted run
// simply retries next time (D6).
//
// The flat file is looked for under the path-keyed project directory because
// that is the only place v1 ever wrote it; the fact it becomes is written to
// the current (codebase-keyed) directory like any other. It must therefore run
// after the codebase migration — see Migrate.
func (s *Store) MigrateV1IfNeeded() error {
	v1Path := filepath.Join(s.legacyProjectRoot, "MEMORY.md")
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

	legacy := v1Fact(string(data))
	if err := os.MkdirAll(s.projectDir, 0o700); err != nil {
		return err
	}
	// The codebase migration may already have wrapped this very file, under
	// this name or a suffixed one if something else claimed it. Writing again
	// would either duplicate the notes or overwrite a fact that is not this
	// one, so only write when nothing of it is here yet.
	if !s.holdsV1Notes(serialize(legacy)) {
		if err := writeFileAtomic(filepath.Join(s.projectDir, legacy.Name+".md"), serialize(legacy)); err != nil {
			return err
		}
	}
	return os.Rename(v1Path, bak)
}

// holdsV1Notes reports whether these exact bytes are already filed under the
// v1 fact's name or one of the "-N" names a merge would have moved it to.
func (s *Store) holdsV1Notes(want []byte) bool {
	entries, err := os.ReadDir(s.projectDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".md")
		if name != v1FactName && !strings.HasPrefix(name, v1FactName+"-") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.projectDir, e.Name()))
		if err != nil {
			continue
		}
		if bytes.Equal(bytes.TrimSpace(data), bytes.TrimSpace(want)) {
			return true
		}
		// A suffixed copy carries a rewritten `name:` line, so compare bodies
		// too: the notes are what must not be duplicated, not the header.
		if m, err := parseFact(data); err == nil {
			if w, err := parseFact(want); err == nil && m.Body == w.Body {
				return true
			}
		}
	}
	return false
}

// resolve maps an ID to a file path and scope. A scope-qualified ID resolves
// directly; a bare name is looked up in both scopes and rejected if ambiguous.
// A bare name found in neither scope defaults to the project path (so callers
// surface a clean "not found").
func (s *Store) resolve(id string) (path string, scope Scope, err error) {
	if scopeStr, name, ok := strings.Cut(id, "/"); ok {
		if !ValidName(name) {
			return "", 0, fmt.Errorf("invalid name %q: use kebab-case ascii [a-z0-9-]", name)
		}
		switch scopeStr {
		case "project":
			return filepath.Join(s.projectDir, name+".md"), ScopeProject, nil
		case "global":
			return filepath.Join(s.globalDir, name+".md"), ScopeGlobal, nil
		default:
			return "", 0, fmt.Errorf("invalid scope %q: use \"project/%s\" or \"global/%s\"", scopeStr, name, name)
		}
	}
	if !ValidName(id) {
		return "", 0, fmt.Errorf("invalid name %q: use kebab-case ascii [a-z0-9-]", id)
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
// scope/durable/invalidate_when, plus the legacy type) followed by the
// markdown body. invalidate_when has its own quoted-value parsing rules.
// Tolerates CRLF, optional quotes around values, and `:` inside a value.
// A file with no `scope:` falls back to its legacy type, and an
// unknown/missing type to project (D10). Missing or unterminated frontmatter
// is an error. Nothing here validates lengths: Write guards what moa writes,
// while everything already on disk must stay readable.
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
	var durable, scopeDeclared bool
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
		case "scope":
			if sc, ok := ParseScope(val); ok {
				m.Scope = sc
				scopeDeclared = true
			}
		case "durable":
			durable = val == "true"
		case "invalidate_when":
			m.InvalidateWhen = parseInvalidationValue(line, val)
		}
	}
	// Files written before `scope:` existed only carry `type:`; map it. A file
	// with neither is project-scoped, as an unknown type always was (D10).
	if !scopeDeclared {
		m.Scope = ScopeForType(m.Type)
	}
	// The lifecycle is read, never inferred: a file that declares neither is
	// legacy, not permanent.
	switch {
	case m.InvalidateWhen != "":
		m.Lifecycle = LifecycleConditional
	case durable:
		m.Lifecycle = LifecycleDurable
	default:
		m.Lifecycle = LifecycleLegacy
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

// parseInvalidationValue decodes the Go-quoted form written by serialize.
// It uses the original line because splitKV deliberately preserves the legacy
// quote-stripping behavior for all shared frontmatter fields.
func parseInvalidationValue(line, fallback string) string {
	i := strings.IndexByte(line, ':')
	if i < 0 {
		return fallback
	}
	if value, err := strconv.Unquote(strings.TrimSpace(line[i+1:])); err == nil && !strings.ContainsAny(value, "\r\n") {
		return value
	}
	return fallback
}

// serialize renders a fact back to its file form. Scope is the source of
// truth; type is emitted only for interoperability with older binaries that
// route files by the legacy field.
func serialize(m Memory) []byte {
	var sb strings.Builder
	sb.WriteString("---\nname: ")
	sb.WriteString(m.Name)
	sb.WriteString("\ndescription: ")
	sb.WriteString(m.Description)
	sb.WriteString("\nscope: ")
	sb.WriteString(m.Scope.String())
	legacyType := m.Type
	// Keep a compatible original type to avoid gratuitous corpus rewrites. A
	// missing or incompatible one must still be replaced so an older binary
	// routes the fact to the scope declared above.
	if !ValidType(legacyType) || ScopeForType(legacyType) != m.Scope {
		legacyType = defaultLegacyType(m.Scope)
	}
	sb.WriteString("\ntype: ")
	sb.WriteString(string(legacyType))
	if m.Lifecycle == LifecycleDurable {
		sb.WriteString("\ndurable: true")
	}
	if m.InvalidateWhen != "" {
		sb.WriteString("\ninvalidate_when: ")
		sb.WriteString(strconv.Quote(m.InvalidateWhen))
	}
	sb.WriteString("\n---\n\n")
	sb.WriteString(strings.TrimRight(m.Body, "\n"))
	sb.WriteByte('\n')
	return []byte(sb.String())
}

func defaultLegacyType(scope Scope) Type {
	if scope == ScopeGlobal {
		return TypeUser
	}
	return TypeProject
}

// writeFileAtomic replaces path with data, and is durable rather than merely
// atomic: the rename makes the new contents visible in one step to other
// processes, but only the fsyncs make them survive a power loss. Both matter
// for the same reason — a fact the user asked moa to remember has no other
// copy, and a crash that leaves a directory entry pointing at unwritten blocks
// turns it into an empty file.
//
// The temporary file is uniquely named. A fixed "<fact>.tmp" was survivable
// while a project directory belonged to one working directory; now that every
// worktree of a repository shares it, two sessions writing the same fact would
// interleave into one temp file and rename each other's half-written bytes
// into place.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("writing memory: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeds

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing memory: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing memory: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing memory: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing memory: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("saving memory: %w", err)
	}
	// The rename itself is a directory change, and it is no more durable than
	// the data was: without this, a crash can leave the old name, no name, or
	// the new one.
	return syncDir(dir)
}

// syncDir flushes a directory's own entries. A failure is reported but does
// not undo the rename: the file is in place, it is just not guaranteed to
// still be there after a power cut, and removing it would be strictly worse.
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("saving memory: %w", err)
	}
	defer func() { _ = f.Close() }()
	if err := f.Sync(); err != nil {
		// Some filesystems refuse fsync on a directory (and lose nothing by
		// it). Not a reason to fail a write that already landed.
		slog.Debug("memory: cannot flush directory", "dir", dir, "error", err)
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
