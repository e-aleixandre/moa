// Package session manages persistent conversation sessions.
//
// Sessions are stored as JSON files in ~/.config/moa/sessions/.
// Each session contains conversation messages, metadata, and a unique ID.
// The Store provides CRUD operations with atomic writes (temp + rename)
// to prevent corruption on crash.
package session

import (
	"errors"
	"strings"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

// ErrNotFound is returned by Load when the session ID does not exist.
var ErrNotFound = errors.New("session: not found")

// SessionStore abstracts session persistence.
// FileStore implements this for disk-based storage.
// External consumers (e.g., HTTP servers) implement it for database storage.
//
// Contract:
//   - Create returns a new Session with a unique ID and timestamps set. It does NOT persist — call Save.
//   - Save persists the session. It MUST set Updated to the current time before writing.
//   - Load returns the session or ErrNotFound (wrapped or direct — use errors.Is).
//   - Latest returns the most recently updated session, or (nil, nil) if the store is empty.
//   - List returns summaries sorted by Updated descending. Empty store returns (nil, nil).
//   - Delete is idempotent — deleting a non-existent session returns nil.
//   - SetArchived toggles the archived flag and persists it WITHOUT touching
//     Updated (archiving is presentation-only and must not reorder lists).
type SessionStore interface {
	Create() *Session
	Save(sess *Session) error
	Load(id string) (*Session, error)
	Latest() (*Session, error)
	List() ([]Summary, error)
	Delete(id string) error
	SetArchived(id string, archived bool) error
}

// SessionVersion is the current session format version.
// V1 (implicit 0): flat Messages array.
// V2: entry-based tree with branching support.
const SessionVersion = 2

// Session represents a persistent conversation.
//
// Field ordering matters: summary fields (ID, Version, Title, Metadata) come first
// so the readSummary partial-read optimization (4KB prefix) still works.
type Session struct {
	// Header fields — read by partial-read list optimization
	ID      string    `json:"id"`
	Version int       `json:"version"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	Title   string    `json:"title"`
	// TitleSource records how Title was set: "manual" (user renamed) or "auto"
	// (derived / LLM-generated). Empty is legacy and treated as auto.
	TitleSource string `json:"title_source,omitempty"`
	// Archived marks a session as closed-but-kept (presentation-only; does
	// not unload it from memory). Must stay top-level (not in Metadata,
	// which is rebuilt from scratch on every snapshot via collectMetadata).
	Archived bool           `json:"archived,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`

	// V2: entry-based tree log
	LeafID  string  `json:"leaf_id,omitempty"`
	Entries []Entry `json:"entries,omitempty"`

	// V1 legacy (only present in old sessions, cleared after migration)
	Messages        []core.AgentMessage `json:"messages,omitempty"`
	CompactionEpoch int                 `json:"compaction_epoch,omitempty"`
}

// Summary is a lightweight session descriptor without messages.
// Used for listing sessions without loading full conversation data.
type Summary struct {
	ID          string         `json:"id"`
	Created     time.Time      `json:"created"`
	Updated     time.Time      `json:"updated"`
	Title       string         `json:"title"`
	TitleSource string         `json:"title_source,omitempty"`
	Archived    bool           `json:"archived,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// RuntimeMetadata keys used for persisting session configuration.
const (
	MetaModel          = "model"
	MetaCWD            = "cwd"
	MetaPermissionMode = "permission_mode"
	MetaThinking       = "thinking"
	MetaPathScope      = "path_scope"
	MetaAllowedPaths   = "allowed_paths"
	MetaCompactAt      = "compact_at"
)

// CompactAtMeta returns the persisted soft compaction threshold in tokens, or 0
// when none was set (the default window-based behavior). Metadata round-trips
// through JSON, so a number read back from disk arrives as float64 while one
// set in this process is still an int — both are accepted.
func (s *Session) CompactAtMeta() int {
	if s.Metadata == nil {
		return 0
	}
	switch v := s.Metadata[MetaCompactAt].(type) {
	case int:
		return v
	case float64:
		return int(v)
	}
	return 0
}

// SetRuntimeMetadata persists the core session configuration (model, cwd,
// permission mode, thinking level) into Metadata. Called on every state
// change and at session creation. Centralizes what gets persisted so all
// frontends (TUI, serve, headless CLI) stay consistent.
func (s *Session) SetRuntimeMetadata(model, cwd, permissionMode, thinking string) {
	if s.Metadata == nil {
		s.Metadata = make(map[string]any)
	}
	s.Metadata[MetaModel] = model
	s.Metadata[MetaCWD] = cwd
	s.Metadata[MetaPermissionMode] = permissionMode
	s.Metadata[MetaThinking] = thinking
}

// RuntimeMeta returns the persisted runtime configuration from Metadata.
// Missing keys return empty strings.
func (s *Session) RuntimeMeta() (model, cwd, permissionMode, thinking string) {
	if s.Metadata == nil {
		return
	}
	model, _ = s.Metadata[MetaModel].(string)
	cwd, _ = s.Metadata[MetaCWD].(string)
	permissionMode, _ = s.Metadata[MetaPermissionMode].(string)
	thinking, _ = s.Metadata[MetaThinking].(string)
	return
}

// SetPathMetadata persists path scope and allowed paths to session metadata.
func (s *Session) SetPathMetadata(scope string, allowedPaths []string) {
	if s.Metadata == nil {
		s.Metadata = make(map[string]any)
	}
	s.Metadata[MetaPathScope] = scope
	// Store as []any for JSON compatibility (map[string]any values).
	paths := make([]any, len(allowedPaths))
	for i, p := range allowedPaths {
		paths[i] = p
	}
	s.Metadata[MetaAllowedPaths] = paths
}

// PathMeta returns the persisted path configuration from Metadata.
func (s *Session) PathMeta() (scope string, allowedPaths []string) {
	if s.Metadata == nil {
		return
	}
	scope, _ = s.Metadata[MetaPathScope].(string)
	if raw, ok := s.Metadata[MetaAllowedPaths].([]any); ok {
		for _, v := range raw {
			if p, ok := v.(string); ok {
				allowedPaths = append(allowedPaths, p)
			}
		}
	} else if raw, ok := s.Metadata[MetaAllowedPaths].([]string); ok {
		allowedPaths = append(allowedPaths, raw...)
	}
	return
}

// Title source values. Empty (legacy) is treated as auto.
const (
	TitleSourceAuto   = "auto"
	TitleSourceManual = "manual"
)

// truncateTitle caps a title to maxLen runes, appending an ellipsis when it
// was longer.
func truncateTitle(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) > maxLen {
		return string(runes[:maxLen]) + "…"
	}
	return s
}

// SetTitle sets the session title from a user message.
// Only sets if title is empty (first message). Truncates to maxLen.
func (s *Session) SetTitle(text string, maxLen int) {
	if s.Title != "" || text == "" {
		return
	}
	s.Title = truncateTitle(text, maxLen)
}

// TitleIsManual reports whether the user explicitly renamed the session.
// A legacy empty source counts as auto.
func (s *Session) TitleIsManual() bool {
	return s.TitleSource == TitleSourceManual
}

// SetAutoTitle applies an auto-generated title, unless the user has manually
// renamed the session. Empty/whitespace titles are ignored.
func (s *Session) SetAutoTitle(title string, maxLen int) {
	if s.TitleIsManual() {
		return
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return
	}
	s.Title = truncateTitle(title, maxLen)
	s.TitleSource = TitleSourceAuto
}

// Rename sets a user-chosen title and marks it manual so auto-titling never
// overwrites it.
func (s *Session) Rename(title string, maxLen int) {
	s.Title = truncateTitle(strings.TrimSpace(title), maxLen)
	s.TitleSource = TitleSourceManual
}
