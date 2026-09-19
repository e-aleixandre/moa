// Package owner persists the project owner of a codebase: the single agent
// that holds a project's criteria, reads reports from the ordinary sessions
// working on it, and keeps a curated book about it.
//
// An owner lives in the codebase directory the memory store already uses:
//
//	~/.config/moa/codebases/<key>/owner.json   the entity
//	~/.config/moa/codebases/<key>/book/        its curated notes
//
// where <key> is core.CodebaseKey(root), so every git worktree of one
// repository answers to the same owner — the same identity memory is scoped
// by. There is at most one owner per codebase; a session is a child of that
// owner when its cwd resolves to the same key, without any manual link.
package owner

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/e-aleixandre/moa/pkg/book"
	"github.com/e-aleixandre/moa/pkg/core"
)

var (
	// ErrExists is returned when a codebase already has an owner: the one
	// owner per codebase rule is what makes "the owner of this project"
	// answerable without asking which one.
	ErrExists = errors.New("this codebase already has an owner")
	// ErrNotFound reports that no owner.json exists for the requested key/id.
	ErrNotFound = errors.New("owner not found")
	// ErrNoConfigDir reports that the config directory could not be resolved,
	// which disables owners entirely rather than writing to a relative path.
	ErrNoConfigDir = errors.New("cannot resolve the moa config directory")
)

// ProjectFile is the only book file injected into a prompt: the index the
// owner is responsible for keeping accurate.
const ProjectFile = "PROJECT.md"

// MaxProjectBytes caps what PROJECT.md contributes to a session prompt. It is
// paid on every turn of every child, so the index is bounded even if the owner
// lets the file grow.
const MaxProjectBytes = 8 * 1024

// Owner is the persisted entity. Model and Thinking are the conversation's
// defaults at creation time; the session owns them afterwards, like any other.
type Owner struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	CodebaseKey string `json:"codebase_key"`
	Root        string `json:"root"`
	SessionID   string `json:"session_id,omitempty"`
	Model       string `json:"model,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	// AnswerAsks allows the owner to resolve a child's ask_user. Permissions
	// are never delegated: only questions.
	AnswerAsks bool `json:"answer_asks"`
	// CanonicalRef is the branch whose state areas/ describes (see git.go).
	// Detected once at creation and editable by hand; empty means the position
	// is unknown, which readers state explicitly instead of guessing. Additive:
	// an owner.json written before it existed simply has no canonical ref.
	CanonicalRef string `json:"canonical_ref,omitempty"`
	// Avatar is the identity mark (see avatar.go). Additive and optional: an
	// owner without one resolves to DefaultAvatar(CodebaseKey), so an owner.json
	// written before avatars existed needs no migration.
	Avatar Avatar `json:"avatar,omitzero"`
	// Heartbeat tunes the owner's own clock (see heartbeat.go). Additive and
	// optional: nil means the defaults.
	Heartbeat *Heartbeat `json:"heartbeat,omitempty"`
	Created   time.Time  `json:"created"`
}

// Store reads and writes owners under a config directory.
type Store struct {
	configDir string
}

// createMu serializes Create across the process. owner.json is created with
// O_EXCL (the cross-process guard), but the entity is only half of the work:
// the caller also attaches a conversation, and two concurrent creations racing
// there would leave one of them owning a session nothing points at.
var createMu sync.Mutex

// NewStore builds a Store over a moa config root (~/.config/moa).
func NewStore(configDir string) *Store { return &Store{configDir: configDir} }

// Default builds a Store over the resolved config directory, or an error when
// there is none.
func Default() (*Store, error) {
	dir := core.ConfigDir()
	if dir == "" {
		return nil, ErrNoConfigDir
	}
	return NewStore(dir), nil
}

// CodebaseDir is ~/.config/moa/codebases/<key>.
func (s *Store) CodebaseDir(key string) string {
	return filepath.Join(s.configDir, "codebases", key)
}

// BookDir is the owner's book directory for a codebase.
func (s *Store) BookDir(key string) string {
	return filepath.Join(s.CodebaseDir(key), "book")
}

func (s *Store) ownerPath(key string) string {
	return filepath.Join(s.CodebaseDir(key), "owner.json")
}

// Create writes a new owner for the codebase containing root and seeds its
// book with a PROJECT.md template. root is canonicalized so the stored path is
// the one CodebaseKey was computed from.
//
// avatar is the chosen identity mark. A zero one takes the deterministic
// default of the codebase; a non-zero one that is not in the closed lists is
// refused rather than stored, so nothing on disk can be undrawable.
func (s *Store) Create(root, name, model, thinking string, answerAsks bool, avatar Avatar) (Owner, error) {
	createMu.Lock()
	defer createMu.Unlock()
	if strings.TrimSpace(name) == "" {
		return Owner{}, errors.New("owner name is required")
	}
	canonical, err := core.CanonicalizePath(root)
	if err != nil {
		return Owner{}, fmt.Errorf("owner root: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return Owner{}, fmt.Errorf("owner root: %s is not a directory", canonical)
	}
	key := core.CodebaseKey(canonical)
	if avatar.IsZero() {
		avatar = DefaultAvatar(key)
	} else if !avatar.Valid() {
		return Owner{}, fmt.Errorf("owner avatar: shape must be one of %v and colour one of %v", AvatarShapes, AvatarColors)
	}
	own := Owner{
		ID:          newOwnerID(),
		Name:        strings.TrimSpace(name),
		CodebaseKey: key,
		Root:        canonical,
		Model:       model,
		Thinking:    thinking,
		AnswerAsks:  answerAsks,
		Avatar:      avatar,
		// Asked once, here: the answer is a property of the repository, not of
		// the turn, and every report and prompt needs the same one.
		CanonicalRef: DetectCanonicalRef(canonical),
		Created:      time.Now().UTC(),
	}
	// Create exclusively rather than check-then-write: the check and the write
	// are what makes "one owner per codebase" true, and only the filesystem can
	// make them one step across processes.
	if err := s.createExclusive(own); err != nil {
		return Owner{}, err
	}
	if err := s.seedBook(own); err != nil {
		return Owner{}, err
	}
	return own, nil
}

// createExclusive writes owner.json only if it does not exist yet, reporting
// ErrExists otherwise.
func (s *Store) createExclusive(own Owner) error {
	data, err := json.MarshalIndent(own, "", "  ")
	if err != nil {
		return err
	}
	path := s.ownerPath(own.CodebaseKey)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create codebase dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return ErrExists
	}
	if err != nil {
		return fmt.Errorf("create owner %s: %w", own.CodebaseKey, err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("write owner %s: %w", own.CodebaseKey, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Save writes owner.json atomically.
func (s *Store) Save(own Owner) error {
	if own.CodebaseKey == "" {
		return errors.New("owner has no codebase key")
	}
	data, err := json.MarshalIndent(own, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.ownerPath(own.CodebaseKey), append(data, '\n'), 0o600)
}

// FindByCodebase returns the owner of a codebase key, if any. A malformed
// owner.json is an error rather than "no owner": silently treating it as
// absent would let a second owner be created over the first.
func (s *Store) FindByCodebase(key string) (Owner, bool, error) {
	data, err := os.ReadFile(s.ownerPath(key))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return Owner{}, false, nil
	case err != nil:
		return Owner{}, false, fmt.Errorf("read owner %s: %w", key, err)
	}
	var own Owner
	if err := json.Unmarshal(data, &own); err != nil {
		return Owner{}, false, fmt.Errorf("parse owner %s: %w", key, err)
	}
	if own.CodebaseKey == "" {
		own.CodebaseKey = key
	}
	return own, true, nil
}

// FindByDir returns the owner responsible for a working directory.
func (s *Store) FindByDir(dir string) (Owner, bool, error) {
	if dir == "" {
		return Owner{}, false, nil
	}
	return s.FindByCodebase(core.CodebaseKey(dir))
}

// FindByID scans the codebase directories for an owner with this ID. Owners
// are few (one per project), so a scan is cheaper than a second index that
// could disagree with the files.
func (s *Store) FindByID(id string) (Owner, bool, error) {
	if id == "" {
		return Owner{}, false, nil
	}
	owners, err := s.List()
	if err != nil {
		return Owner{}, false, err
	}
	for _, own := range owners {
		if own.ID == id {
			return own, true, nil
		}
	}
	return Owner{}, false, nil
}

// List returns every owner on disk, sorted by name.
func (s *Store) List() ([]Owner, error) {
	entries, err := os.ReadDir(filepath.Join(s.configDir, "codebases"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Owner
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		own, found, err := s.FindByCodebase(entry.Name())
		if err != nil {
			return nil, err
		}
		if found {
			out = append(out, own)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Delete removes owner.json. The book is deliberately kept: it is curated
// project knowledge that outlives the agent that wrote it, and a new owner for
// the same codebase picks it up.
func (s *Store) Delete(key string) error {
	err := os.Remove(s.ownerPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	return err
}

// ProjectIndex returns PROJECT.md truncated to MaxProjectBytes, or "" when the
// book has none. A missing or unreadable file is not an error: the prompt
// simply carries no book section.
func (s *Store) ProjectIndex(key string) string {
	data, err := os.ReadFile(filepath.Join(s.BookDir(key), ProjectFile))
	if err != nil {
		return ""
	}
	return truncateUTF8(string(data), MaxProjectBytes)
}

// MaxOwnerPrefsBytes caps what OWNER.md contributes to the owner's prompt. It
// is the user's file and nobody trims it for them, so the prompt does.
const MaxOwnerPrefsBytes = 16 * 1024

// OwnerPrefs returns book/OWNER.md: the user's preferences for how this owner
// should work. It is read through the same loader PROJECT.md uses, so editing
// it reaches a live conversation on /reload, and it is never written by the
// owner (pkg/book refuses it): instructions the agent can rewrite are not
// instructions.
func (s *Store) OwnerPrefs(key string) string {
	data, err := os.ReadFile(filepath.Join(s.BookDir(key), book.OwnerFile))
	if err != nil {
		return ""
	}
	return truncateUTF8(string(data), MaxOwnerPrefsBytes)
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

// seedBook creates the book directory and writes the template of the schema
// (pkg/book) for every file that is absent. An existing book is left untouched
// file by file: a new owner over an old book inherits what is there and only
// gains the parts of the shape that were missing.
//
// Each file is created with O_EXCL rather than checked and then written: the
// check and the write are what makes "never overwrite" true, and only the
// filesystem can make them one step. An unreadable or otherwise failing
// creation fails the seed — silently treating an error as "absent" is how a
// book gets overwritten by a template.
//
// The template is a shape with an explanation inside each file, never invented
// facts: an owner that cannot tell a placeholder from a verified fact is worse
// than an owner with an empty book.
func (s *Store) seedBook(own Owner) error {
	dir := s.BookDir(own.CodebaseKey)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create book dir: %w", err)
	}
	for rel, content := range book.Template() {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf("create book dir: %w", err)
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue // the user's file, or a previous owner's: kept
		}
		if err != nil {
			return fmt.Errorf("seed %s: %w", rel, err)
		}
		if _, err := f.WriteString(content); err != nil {
			_ = f.Close()
			return fmt.Errorf("seed %s: %w", rel, err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("seed %s: %w", rel, err)
		}
	}
	return nil
}

// newOwnerID mints an opaque identifier. The "own_" prefix keeps it
// recognizable in logs and URLs next to session IDs.
func newOwnerID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("own_%d", time.Now().UnixNano())
	}
	return "own_" + hex.EncodeToString(b)
}
