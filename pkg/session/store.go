package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Store manages session persistence on disk.
// Sessions are stored as individual JSON files in a directory.
// Writes are atomic (temp file + rename) to prevent corruption.
type Store struct {
	dir string
}

// NewStore creates a Store at the given directory.
// If dir is empty, defaults to ~/.config/moa/sessions/.
// Creates the directory if it doesn't exist.
func NewStore(dir string) (*Store, error) {
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("session: cannot resolve home directory: %w", err)
		}
		dir = filepath.Join(home, ".config", "moa", "sessions")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("session: cannot create directory %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

// Dir returns the session storage directory.
func (s *Store) Dir() string {
	return s.dir
}

// Create creates a new empty session with a unique ID.
func (s *Store) Create() *Session {
	return &Session{
		ID:       newID(),
		Created:  time.Now(),
		Updated:  time.Now(),
		Metadata: make(map[string]any),
	}
}

// Save writes a session to disk atomically.
// Updates the session's Updated timestamp before writing.
func (s *Store) Save(sess *Session) error {
	sess.Updated = time.Now()

	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return fmt.Errorf("session: marshal error: %w", err)
	}

	path := s.path(sess.ID)

	// Atomic write: temp file in same directory, then rename.
	// Same directory ensures same filesystem for atomic rename.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("session: write error: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) // cleanup on rename failure
		return fmt.Errorf("session: rename error: %w", err)
	}
	return nil
}

// Load reads a session by ID.
func (s *Store) Load(id string) (*Session, error) {
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		return nil, fmt.Errorf("session: read error: %w", err)
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("session: unmarshal error: %w", err)
	}
	return &sess, nil
}

// Latest returns the most recently updated session.
// Returns nil, nil if no sessions exist.
func (s *Store) Latest() (*Session, error) {
	summaries, err := s.List()
	if err != nil {
		return nil, err
	}
	if len(summaries) == 0 {
		return nil, nil
	}
	// List returns sorted by Updated desc — first is latest
	return s.Load(summaries[0].ID)
}

// List returns summaries of all sessions, sorted by Updated descending (newest first).
// Does not load message content — only reads enough to populate Summary fields.
func (s *Store) List() ([]Summary, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("session: list error: %w", err)
	}

	var summaries []Summary
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		// Read file to get metadata (skip .tmp files)
		if strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue // skip unreadable files
		}
		// Parse only summary fields (fast — stops at messages)
		var sum Summary
		if err := json.Unmarshal(data, &sum); err != nil {
			continue // skip corrupt files
		}
		if sum.ID == "" {
			continue // skip invalid
		}
		summaries = append(summaries, sum)
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].Updated.After(summaries[j].Updated)
	})

	return summaries, nil
}

// Delete removes a session by ID.
func (s *Store) Delete(id string) error {
	path := s.path(id)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("session: delete error: %w", err)
	}
	return nil
}

// path returns the filesystem path for a session ID.
func (s *Store) path(id string) string {
	return filepath.Join(s.dir, id+".json")
}

// newID generates a unique session ID.
// Format: 24-char hex string from 12 random bytes.
func newID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp if crypto/rand fails (shouldn't happen)
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
