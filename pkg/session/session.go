// Package session manages persistent conversation sessions.
//
// Sessions are stored as JSON files in ~/.config/moa/sessions/.
// Each session contains conversation messages, metadata, and a unique ID.
// The Store provides CRUD operations with atomic writes (temp + rename)
// to prevent corruption on crash.
package session

import (
	"errors"
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
type SessionStore interface {
	Create() *Session
	Save(sess *Session) error
	Load(id string) (*Session, error)
	Latest() (*Session, error)
	List() ([]Summary, error)
	Delete(id string) error
}

// Session represents a persistent conversation.
type Session struct {
	ID              string              `json:"id"`
	Created         time.Time           `json:"created"`
	Updated         time.Time           `json:"updated"`
	Title           string              `json:"title"`
	Messages        []core.AgentMessage `json:"messages"`
	CompactionEpoch int                 `json:"compaction_epoch,omitempty"`
	Metadata        map[string]any      `json:"metadata,omitempty"` // extensible: model, cost, tags, etc.
}

// Summary is a lightweight session descriptor without messages.
// Used for listing sessions without loading full conversation data.
type Summary struct {
	ID      string    `json:"id"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	Title   string    `json:"title"`
}

// SetTitle sets the session title from a user message.
// Only sets if title is empty (first message). Truncates to maxLen.
func (s *Session) SetTitle(text string, maxLen int) {
	if s.Title != "" || text == "" {
		return
	}
	if len(text) > maxLen {
		text = text[:maxLen] + "…"
	}
	s.Title = text
}
