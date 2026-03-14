package session

import (
	"crypto/rand"
	"crypto/sha256"
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
)

// nowFunc is the clock used for timestamps. Tests can override it.
var nowFunc = time.Now

// Compile-time check: FileStore implements SessionStore.
var _ SessionStore = (*FileStore)(nil)

// FileStore manages session persistence on disk.
// Sessions are stored as individual JSON files in a directory.
// Writes are atomic (temp file + rename) to prevent corruption.
type FileStore struct {
	dir string
	mu  sync.Mutex
}

// NewFileStore creates a FileStore for sessions scoped to the given CWD.
// baseDir is the root sessions directory (empty = ~/.config/moa/sessions/).
// cwd determines the project subdirectory. Empty cwd uses baseDir directly (legacy/tests).
func NewFileStore(baseDir, cwd string) (*FileStore, error) {
	if baseDir == "" {
		var err error
		baseDir, err = defaultBaseDir()
		if err != nil {
			return nil, fmt.Errorf("session: cannot resolve home directory: %w", err)
		}
	}
	dir := baseDir
	if cwd != "" {
		dir = filepath.Join(baseDir, scopeKey(cwd))
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("session: cannot create directory %s: %w", dir, err)
	}
	return &FileStore{dir: dir}, nil
}

// Dir returns the session storage directory.
func (s *FileStore) Dir() string {
	return s.dir
}

// Create creates a new empty session with a unique ID.
func (s *FileStore) Create() *Session {
	return &Session{
		ID:       newID(),
		Created:  nowFunc(),
		Updated:  nowFunc(),
		Metadata: make(map[string]any),
	}
}

// Save writes a session to disk atomically.
// Updates the session's Updated timestamp before writing.
func (s *FileStore) Save(sess *Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess.Updated = nowFunc()

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
		_ = os.Remove(tmp) // cleanup on rename failure
		return fmt.Errorf("session: rename error: %w", err)
	}
	return nil
}

// Load reads a session by ID.
// Returns ErrNotFound (wrapped) if the session does not exist.
func (s *FileStore) Load(id string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked(id)
}

func (s *FileStore) loadLocked(id string) (*Session, error) {
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("session %s: %w", id, ErrNotFound)
		}
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
func (s *FileStore) Latest() (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	summaries, err := s.listLocked()
	if err != nil {
		return nil, err
	}
	if len(summaries) == 0 {
		return nil, nil
	}
	return s.loadLocked(summaries[0].ID)
}

// summaryReadLimit caps how many bytes we read from each session file for
// listing. Summary fields (id, title, created, updated, metadata) appear
// before the messages array, so a small prefix suffices. Sessions with very
// large metadata may need more, but the JSON decoder handles partial reads
// gracefully (fields it finds are populated, the rest are zero).
const summaryReadLimit = 4096

// List returns summaries of all sessions, sorted by Updated descending (newest first).
// Only reads the first summaryReadLimit bytes of each file — avoids loading
// multi-megabyte message arrays just to show a session list.
func (s *FileStore) List() ([]Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked()
}

func (s *FileStore) listLocked() ([]Summary, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("session: list error: %w", err)
	}

	var summaries []Summary
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		sum, err := readSummary(filepath.Join(s.dir, e.Name()))
		if err != nil || sum.ID == "" {
			continue
		}
		summaries = append(summaries, sum)
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].Updated.After(summaries[j].Updated)
	})

	return summaries, nil
}

// readSummary reads only the header fields from a session file. It reads at
// most summaryReadLimit bytes and parses what it can. Because JSON keys appear
// in order (id, created, updated, title, messages...), the small prefix
// contains everything we need. Malformed trailing JSON from the truncation is
// handled by falling back to a full read.
func readSummary(path string) (Summary, error) {
	f, err := os.Open(path)
	if err != nil {
		return Summary{}, err
	}
	defer f.Close() //nolint:errcheck

	// Try partial read first.
	buf := make([]byte, summaryReadLimit)
	n, _ := f.Read(buf)
	if n == 0 {
		return Summary{}, fmt.Errorf("empty file")
	}

	var sum Summary
	if err := json.Unmarshal(buf[:n], &sum); err == nil {
		return sum, nil
	}

	// Partial JSON failed (truncated mid-value). Fall back to full read.
	// This only happens for files with >4KB of metadata before the messages
	// array, which is rare.
	full, err := os.ReadFile(path)
	if err != nil {
		return Summary{}, err
	}
	if err := json.Unmarshal(full, &sum); err != nil {
		return Summary{}, err
	}
	return sum, nil
}

// Delete removes a session by ID.
func (s *FileStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.path(id)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("session: delete error: %w", err)
	}
	return nil
}

// path returns the filesystem path for a session ID.
func (s *FileStore) path(id string) string {
	return filepath.Join(s.dir, id+".json")
}

// newID generates a unique session ID.
// Format: 24-char hex string from 12 random bytes.
func newID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp if crypto/rand fails (shouldn't happen)
		return fmt.Sprintf("%d", nowFunc().UnixNano())
	}
	return hex.EncodeToString(b)
}

// scopeKey returns a directory name for a CWD: <basename>_<hash12>.
// Uses SHA-256 of the cleaned path for uniqueness and the last path
// component for human readability.
func scopeKey(cwd string) string {
	clean := filepath.Clean(cwd)
	base := filepath.Base(clean)
	// Sanitize: strip separators, fall back for root/empty paths.
	base = strings.ReplaceAll(base, string(filepath.Separator), "")
	base = strings.ReplaceAll(base, ":", "") // Windows drive letters
	if base == "" || base == "." {
		base = "root"
	}
	h := sha256.Sum256([]byte(clean))
	return base + "_" + hex.EncodeToString(h[:6]) // 12 hex chars
}

// defaultBaseDir returns the default sessions root directory.
func defaultBaseDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "moa", "sessions"), nil
}

// ListAll returns summaries from all project-scoped stores under baseDir.
// If baseDir is empty, uses the default. Returns partial results on errors.
func ListAll(baseDir string) ([]Summary, error) {
	if baseDir == "" {
		var err error
		baseDir, err = defaultBaseDir()
		if err != nil {
			return nil, err
		}
	}
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var all []Summary
	var errs []error
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		store := &FileStore{dir: filepath.Join(baseDir, e.Name())}
		sums, err := store.List()
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		all = append(all, sums...)
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].Updated.After(all[j].Updated)
	})
	return all, errors.Join(errs...)
}

// FindSession searches all project stores under baseDir for a session by ID.
// Returns the session, the store it was found in, and any error.
func FindSession(baseDir, id string) (*Session, *FileStore, error) {
	if baseDir == "" {
		var err error
		baseDir, err = defaultBaseDir()
		if err != nil {
			return nil, nil, fmt.Errorf("session %s: %w", id, ErrNotFound)
		}
	}
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return nil, nil, fmt.Errorf("session %s: %w", id, ErrNotFound)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(baseDir, e.Name())
		path := filepath.Join(dir, id+".json")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var sess Session
		if err := json.Unmarshal(data, &sess); err != nil {
			continue
		}
		return &sess, &FileStore{dir: dir}, nil
	}
	return nil, nil, fmt.Errorf("session %s: %w", id, ErrNotFound)
}

// DeleteByID searches all project stores under baseDir and deletes the session.
func DeleteByID(baseDir, id string) error {
	if baseDir == "" {
		var err error
		baseDir, err = defaultBaseDir()
		if err != nil {
			return fmt.Errorf("session %s: %w", id, ErrNotFound)
		}
	}
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return fmt.Errorf("session %s: %w", id, ErrNotFound)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(baseDir, e.Name(), id+".json")
		if err := os.Remove(path); err == nil {
			return nil
		}
	}
	return fmt.Errorf("session %s: %w", id, ErrNotFound)
}
