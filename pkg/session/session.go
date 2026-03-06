// Package session manages persistent conversation sessions.
//
// Sessions are stored as JSON files in ~/.config/moa/sessions/.
// Each session contains conversation messages, metadata, and a unique ID.
// The Store provides CRUD operations with atomic writes (temp + rename)
// to prevent corruption on crash.
package session

import (
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

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
