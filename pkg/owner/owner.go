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
	"time"

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
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	CodebaseKey string    `json:"codebase_key"`
	Root        string    `json:"root"`
	SessionID   string    `json:"session_id,omitempty"`
	Model       string    `json:"model,omitempty"`
	Thinking    string    `json:"thinking,omitempty"`
	// AnswerAsks allows the owner to resolve a child's ask_user. Permissions
	// are never delegated: only questions.
	AnswerAsks bool      `json:"answer_asks"`
	Created    time.Time `json:"created"`
}

// Store reads and writes owners under a config directory.
type Store struct {
	configDir string
}

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
func (s *Store) Create(root, name, model, thinking string, answerAsks bool) (Owner, error) {
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
	if _, found, err := s.FindByCodebase(key); err != nil {
		return Owner{}, err
	} else if found {
		return Owner{}, ErrExists
	}
	own := Owner{
		ID:          newOwnerID(),
		Name:        strings.TrimSpace(name),
		CodebaseKey: key,
		Root:        canonical,
		Model:       model,
		Thinking:    thinking,
		AnswerAsks:  answerAsks,
		Created:     time.Now().UTC(),
	}
	if err := s.Save(own); err != nil {
		return Owner{}, err
	}
	if err := s.seedBook(own); err != nil {
		return Owner{}, err
	}
	return own, nil
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

// projectTemplate is what a fresh book starts with: the shape of the index,
// not content invented on the owner's behalf.
const projectTemplate = `# Project

What this project is, in two or three lines.

## Current state

What is being worked on right now.

## Decisions that bind

Decisions nobody should reopen without the owner, and where the full record
lives in this book.

## Map of this book

- decisions/ — dated record of what was decided, by whom and why
- people.md — who asks for what, and how
- areas/<name>.md — deep context per module or area
`

// seedBook creates the book directory and writes PROJECT.md if absent. An
// existing book is left untouched: a new owner over an old book inherits it.
func (s *Store) seedBook(own Owner) error {
	dir := s.BookDir(own.CodebaseKey)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create book dir: %w", err)
	}
	path := filepath.Join(dir, ProjectFile)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return writeFileAtomic(path, []byte(projectTemplate), 0o600)
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
