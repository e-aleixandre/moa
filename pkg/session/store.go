package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// ValidateID accepts the opaque identifier generated for persisted sessions.
// Keeping IDs filename-safe prevents external API inputs from escaping a store.
func ValidateID(id string) error {
	if len(id) != 24 {
		return fmt.Errorf("invalid session ID")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return fmt.Errorf("invalid session ID")
	}
	return nil
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

// Create creates a new empty session with a unique ID (v2 format).
func (s *FileStore) Create() *Session {
	return &Session{
		ID:       newID(),
		Version:  SessionVersion,
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
	return s.saveLocked(sess)
}

func (s *FileStore) saveLocked(sess *Session) error {
	sess.Updated = nowFunc()
	return s.writeLocked(sess)
}

// writeLocked validates and atomically writes sess to disk WITHOUT touching
// Updated. Callers that want normal save semantics (bump Updated) should use
// saveLocked; callers that need to persist a change without reordering
// session lists (e.g. SetArchived) call writeLocked directly.
func (s *FileStore) writeLocked(sess *Session) error {
	if err := ValidateID(sess.ID); err != nil {
		return err
	}
	// Validate v2 entries before persisting
	if sess.Version >= SessionVersion && len(sess.Entries) > 0 {
		if err := ValidateEntries(sess.Entries, sess.LeafID); err != nil {
			return fmt.Errorf("session: validation error: %w", err)
		}
	}

	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return fmt.Errorf("session: marshal error: %w", err)
	}

	path := s.path(sess.ID)

	// Atomic write: temp file in same directory, then rename.
	// Same directory ensures same filesystem for atomic rename.
	tmp, err := os.CreateTemp(s.dir, ".session-*.tmp")
	if err != nil {
		return fmt.Errorf("session: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("session: chmod temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("session: write error: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("session: sync error: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("session: close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("session: rename error: %w", err)
	}
	return nil
}

// SetArchived toggles the archived flag on a session, preserving Updated so
// archiving does not reorder session lists (archive is presentation-only).
func (s *FileStore) SetArchived(id string, archived bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.loadLocked(id)
	if err != nil {
		return err
	}
	if sess.Archived == archived {
		return nil
	}
	sess.Archived = archived
	return s.writeLocked(sess)
}

// Load reads a session by ID.
// Returns ErrNotFound (wrapped) if the session does not exist.
func (s *FileStore) Load(id string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked(id)
}

// LoadReadOnly reads a session without performing the legacy v1 migration.
// Consumers which promise a read-only operation (for example transcript
// export) must use this method: Load may write a migrated copy to disk.
func (s *FileStore) LoadReadOnly(id string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadReadOnlyLocked(id)
}

func (s *FileStore) loadReadOnlyLocked(id string) (*Session, error) {
	if err := ValidateID(id); err != nil {
		return nil, fmt.Errorf("session %s: %w", id, ErrNotFound)
	}
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
	if sess.ID != id {
		return nil, fmt.Errorf("session: ID does not match filename")
	}
	return &sess, nil
}

func (s *FileStore) loadLocked(id string) (*Session, error) {
	if err := ValidateID(id); err != nil {
		return nil, fmt.Errorf("session %s: %w", id, ErrNotFound)
	}
	path := s.path(id)
	data, err := os.ReadFile(path)
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
	if sess.ID != id {
		return nil, fmt.Errorf("session: ID does not match filename")
	}

	// Auto-migrate v1 → v2
	if sess.Version < SessionVersion && len(sess.Messages) > 0 {
		if err := MigrateV1ToV2(&sess); err != nil {
			return nil, fmt.Errorf("session: migration error: %w", err)
		}
		// Validate migrated tree
		if err := ValidateEntries(sess.Entries, sess.LeafID); err != nil {
			return nil, fmt.Errorf("session: post-migration validation error: %w", err)
		}
		// Back up original before writing migrated version
		backup := path + ".v1.bak"
		if err := os.WriteFile(backup, data, 0600); err != nil {
			return nil, fmt.Errorf("session: write migration backup: %w", err)
		}
		// Persist migrated session
		if err := s.saveLocked(&sess); err != nil {
			return nil, fmt.Errorf("session: persist migration: %w", err)
		}
	} else if sess.Version == 0 {
		// Empty v1 session — just stamp version
		sess.Version = SessionVersion
		if err := s.saveLocked(&sess); err != nil {
			return nil, fmt.Errorf("session: persist migration: %w", err)
		}
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

// List returns summaries of all sessions, sorted by Updated descending (newest first).
// Uses a streaming json.Decoder that stops as soon as it reaches the
// entries/messages field, so it never reads (or allocates) the — potentially
// multi-megabyte — conversation history just to show a session list.
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

// heavySessionFields are the Session keys that hold the (potentially huge)
// conversation history. readSummary stops decoding as soon as it sees one of
// these keys, so their values are never read off disk.
var heavySessionFields = map[string]bool{
	"entries":  true,
	"messages": true,
}

// readSummary reads only the header fields from a session file using a
// streaming json.Decoder: it walks the top-level object key by key and stops
// as soon as it reaches "entries" or "messages" (the conversation history),
// which in a well-formed Session always comes after the summary fields (see
// the field-ordering comment on Session). This avoids reading the
// potentially multi-megabyte tail of the file at all, regardless of how
// large the leading metadata happens to be — unlike a fixed byte-prefix
// read, it never truncates mid-value.
//
// If anything about the file doesn't match the expected shape (unusual
// metadata that fails to decode into map[string]any, non-object top level,
// etc.), it falls back to a full read + json.Unmarshal into Summary.
func readSummary(path string) (Summary, error) {
	f, err := os.Open(path)
	if err != nil {
		return Summary{}, err
	}
	defer f.Close() //nolint:errcheck

	sum, ok := decodeSummaryPrefix(f)
	if ok {
		return sum, nil
	}

	// Fall back to a full read + Unmarshal. This covers malformed/unusual
	// files (e.g. metadata containing a type our streaming path doesn't
	// expect) that the fast path couldn't handle.
	full, err := os.ReadFile(path)
	if err != nil {
		return Summary{}, err
	}
	var fullSum Summary
	if err := json.Unmarshal(full, &fullSum); err != nil {
		return Summary{}, err
	}
	return fullSum, nil
}

// decodeSummaryPrefix streams the leading summary fields of a session object
// out of r, stopping before any heavySessionFields key. The second return
// value reports whether the streaming decode succeeded; on false, the caller
// should fall back to a full read.
func decodeSummaryPrefix(r io.Reader) (Summary, bool) {
	dec := json.NewDecoder(r)

	tok, err := dec.Token()
	if err != nil {
		return Summary{}, false
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return Summary{}, false
	}

	var sum Summary
	sawID := false
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return Summary{}, false
		}
		key, ok := keyTok.(string)
		if !ok {
			return Summary{}, false
		}
		if heavySessionFields[key] {
			// Everything we need comes before the conversation history —
			// stop here without touching it.
			break
		}
		switch key {
		case "id":
			if err := dec.Decode(&sum.ID); err != nil {
				return Summary{}, false
			}
			sawID = true
		case "created":
			if err := dec.Decode(&sum.Created); err != nil {
				return Summary{}, false
			}
		case "updated":
			if err := dec.Decode(&sum.Updated); err != nil {
				return Summary{}, false
			}
		case "title":
			if err := dec.Decode(&sum.Title); err != nil {
				return Summary{}, false
			}
		case "title_source":
			if err := dec.Decode(&sum.TitleSource); err != nil {
				return Summary{}, false
			}
		case "archived":
			if err := dec.Decode(&sum.Archived); err != nil {
				return Summary{}, false
			}
		case "metadata":
			if err := dec.Decode(&sum.Metadata); err != nil {
				return Summary{}, false
			}
		default:
			// Unknown/uninteresting field (version, leaf_id,
			// compaction_epoch, ...): decode-and-discard to advance past
			// its value without caring about its shape.
			var discard json.RawMessage
			if err := dec.Decode(&discard); err != nil {
				return Summary{}, false
			}
		}
	}

	if !sawID || sum.ID == "" {
		return Summary{}, false
	}
	return sum, true
}

// Delete removes a session by ID.
func (s *FileStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ValidateID(id); err != nil {
		if strings.ContainsAny(id, `/\\`) || strings.Contains(id, "..") {
			return err
		}
		// Delete has historically been idempotent, including for unknown IDs.
		return nil
	}
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
	if err := ValidateID(id); err != nil {
		return nil, nil, fmt.Errorf("session %s: %w", id, ErrNotFound)
	}
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
		store := &FileStore{dir: filepath.Join(baseDir, e.Name())}
		sess, err := store.Load(id)
		if err == nil {
			return sess, store, nil
		}
	}
	return nil, nil, fmt.Errorf("session %s: %w", id, ErrNotFound)
}

// FindSessionReadOnly searches all project stores without migrating or writing
// the matching session. It is the read-only counterpart to FindSession.
func FindSessionReadOnly(baseDir, id string) (*Session, *FileStore, error) {
	if err := ValidateID(id); err != nil {
		return nil, nil, fmt.Errorf("session %s: %w", id, ErrNotFound)
	}
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
		store := &FileStore{dir: filepath.Join(baseDir, e.Name())}
		sess, err := store.LoadReadOnly(id)
		if err == nil {
			return sess, store, nil
		}
	}
	return nil, nil, fmt.Errorf("session %s: %w", id, ErrNotFound)
}

// DeleteByID searches all project stores under baseDir and deletes the session.
func DeleteByID(baseDir, id string) error {
	if err := ValidateID(id); err != nil {
		return fmt.Errorf("session %s: %w", id, ErrNotFound)
	}
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
		store := &FileStore{dir: filepath.Join(baseDir, e.Name())}
		if err := store.Delete(id); err == nil {
			// Also remove the subagent transcript side directory, if any.
			_ = os.RemoveAll(filepath.Join(baseDir, e.Name(), id+".subagents"))
			return nil
		}
	}
	return fmt.Errorf("session %s: %w", id, ErrNotFound)
}
