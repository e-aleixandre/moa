// Package attachment provides durable, content-addressed blob storage for
// session attachments.
package attachment

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultOrphanGrace = time.Hour
	stagingMaxAge      = 24 * time.Hour
)

var (
	attachmentIDPattern = regexp.MustCompile(`^att_[0-9a-f]{24}$`)
	sha256Pattern       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	sessionIDPattern    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

// Store is a durable, content-addressed attachment blob store shared across
// sessions. Blobs are keyed by sha256 of their bytes and reference-counted by
// occurrence; a blob is deleted when no session references it.
type Store struct {
	baseDir string

	mu sync.Mutex

	// now and orphanGrace are configurable within this package so tests can
	// exercise garbage collection without waiting.
	now         func() time.Time
	orphanGrace time.Duration
}

// PutMeta contains caller-validated metadata for a blob occurrence.
type PutMeta struct {
	Name   string
	Mime   string
	Kind   string
	Width  int
	Height int
}

// Descriptor is the persisted, byte-free reference to a stored blob. It is
// what a message/session carries instead of base64.
type Descriptor struct {
	ID        string    `json:"id"`
	SHA256    string    `json:"sha256"`
	Name      string    `json:"name"`
	Mime      string    `json:"mime"`
	Size      int64     `json:"size"`
	Kind      string    `json:"kind"`
	Width     int       `json:"width,omitempty"`
	Height    int       `json:"height,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type catalogEntry struct {
	Size     int64 `json:"size"`
	RefCount int64 `json:"refcount"`
}

type catalog map[string]catalogEntry

// New returns a Store rooted at baseDir, creating its directory tree if
// needed.
func New(baseDir string) (*Store, error) {
	if baseDir == "" {
		return nil, errors.New("attachment: empty base directory")
	}
	for _, dir := range []string{
		baseDir,
		filepath.Join(baseDir, "blobs", "sha256"),
		filepath.Join(baseDir, "sessions"),
		filepath.Join(baseDir, "staging"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("attachment: create directory %s: %w", dir, err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, fmt.Errorf("attachment: secure directory %s: %w", dir, err)
		}
	}
	return &Store{
		baseDir:     baseDir,
		now:         time.Now,
		orphanGrace: defaultOrphanGrace,
	}, nil
}

// DefaultBaseDir resolves the production attachments root. MOA_CONFIG_DIR is
// honored for container and custom deployments.
func DefaultBaseDir() (string, error) {
	if dir := os.Getenv("MOA_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "attachments", "v1"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "moa", "attachments", "v1"), nil
}

// Put stores raw bytes, deduplicating by their SHA-256 hash. It creates an
// occurrence descriptor but does not attach that occurrence to a session.
func (s *Store) Put(data []byte, meta PutMeta) (Descriptor, error) {
	stageName, d, err := s.stage(data, meta)
	if err != nil {
		return Descriptor{}, err
	}
	defer func() { _ = os.Remove(stageName) }()
	if err := s.withLock(func() error {
		return s.publishLocked(stageName, d.SHA256)
	}); err != nil {
		return Descriptor{}, err
	}
	return d, nil
}

// PutRef publishes data (deduplicating by hash) AND records a provisional
// reference from sessionID in ONE locked operation, so the blob can never be
// garbage-collected between publication and referencing. This is the method
// callers should use when the owning session is already known (the normal
// ingestion path). Returns the minted Descriptor (already referenced).
func (s *Store) PutRef(sessionID string, data []byte, meta PutMeta) (Descriptor, error) {
	if err := validateSessionID(sessionID); err != nil {
		return Descriptor{}, err
	}
	stageName, d, err := s.stage(data, meta)
	if err != nil {
		return Descriptor{}, err
	}
	defer func() { _ = os.Remove(stageName) }()
	if err := s.withLock(func() error {
		if err := s.publishLocked(stageName, d.SHA256); err != nil {
			return err
		}
		return s.addRefLocked(sessionID, d)
	}); err != nil {
		return Descriptor{}, err
	}
	return d, nil
}

func (s *Store) stage(data []byte, meta PutMeta) (string, Descriptor, error) {
	stage, err := os.CreateTemp(s.stagingDir(), ".upload-*.upload")
	if err != nil {
		return "", Descriptor{}, fmt.Errorf("attachment: create staging file: %w", err)
	}
	stageName := stage.Name()
	if err := stage.Chmod(0o600); err != nil {
		_ = stage.Close()
		_ = os.Remove(stageName)
		return "", Descriptor{}, fmt.Errorf("attachment: secure staging file: %w", err)
	}

	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(stage, hash), bytes.NewReader(data)); err != nil {
		_ = stage.Close()
		_ = os.Remove(stageName)
		return "", Descriptor{}, fmt.Errorf("attachment: write staging file: %w", err)
	}
	if err := stage.Sync(); err != nil {
		_ = stage.Close()
		_ = os.Remove(stageName)
		return "", Descriptor{}, fmt.Errorf("attachment: sync staging file: %w", err)
	}
	if err := stage.Close(); err != nil {
		_ = os.Remove(stageName)
		return "", Descriptor{}, fmt.Errorf("attachment: close staging file: %w", err)
	}

	sha := hex.EncodeToString(hash.Sum(nil))
	id, err := newOccurrenceID()
	if err != nil {
		_ = os.Remove(stageName)
		return "", Descriptor{}, err
	}
	descriptor := Descriptor{
		ID:        id,
		SHA256:    sha,
		Name:      sanitizeName(meta.Name),
		Mime:      meta.Mime,
		Size:      int64(len(data)),
		Kind:      meta.Kind,
		Width:     meta.Width,
		Height:    meta.Height,
		CreatedAt: s.now(),
	}
	if err := validateDescriptor(descriptor); err != nil {
		_ = os.Remove(stageName)
		return "", Descriptor{}, err
	}
	return stageName, descriptor, nil
}

// publishLocked publishes a fully synced staging file. The caller must hold
// the store lock.
func (s *Store) publishLocked(stageName, sha string) error {
	if err := validateSHA256(sha); err != nil {
		return err
	}
	final := s.blobPath(sha)
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		return fmt.Errorf("attachment: create blob directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(final), 0o700); err != nil {
		return fmt.Errorf("attachment: secure blob directory: %w", err)
	}
	// Link is an atomic no-clobber publish on the same filesystem. Unlike
	// os.Rename, it cannot replace an already-published blob from another Put.
	if err := os.Link(stageName, final); err != nil {
		if errors.Is(err, fs.ErrExist) {
			// Confirm a prior publisher's blob entry is durable before referencing it.
			if err := syncDir(filepath.Dir(final)); err != nil {
				return fmt.Errorf("attachment: sync blob directory: %w", err)
			}
			return nil
		}
		return fmt.Errorf("attachment: publish blob: %w", err)
	}
	// Persist the blob entry before any session index can reference it after a power loss.
	if err := syncDir(filepath.Dir(final)); err != nil {
		return fmt.Errorf("attachment: sync blob directory: %w", err)
	}
	if err := os.Remove(stageName); err != nil {
		return fmt.Errorf("attachment: remove published staging file: %w", err)
	}
	return nil
}

// AddRef records one attachment occurrence in a session. It is idempotent for
// an existing (sessionID, descriptor ID) pair.
func (s *Store) AddRef(sessionID string, d Descriptor) error {
	if err := validateSessionID(sessionID); err != nil {
		return err
	}
	if err := validateDescriptor(d); err != nil {
		return err
	}

	return s.withLock(func() error {
		return s.addRefLocked(sessionID, d)
	})
}

// addRefLocked records a reference to an already-published blob. The caller
// must hold the store lock.
func (s *Store) addRefLocked(sessionID string, d Descriptor) error {
	if err := s.verifyBlob(d); err != nil {
		return err
	}
	index, err := s.readSessionIndex(sessionID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			index = make(map[string]Descriptor)
		} else {
			return err
		}
	}
	if old, ok := index[d.ID]; ok {
		if old.SHA256 != d.SHA256 || old.Size != d.Size {
			return fmt.Errorf("attachment: occurrence %s already belongs to another blob", d.ID)
		}
		return nil
	}
	index[d.ID] = d
	if err := s.writeSessionIndex(sessionID, index); err != nil {
		return err
	}
	return s.rebuildCatalogFromIndexes()
}

// Open returns a read-only handle to an attachment only when the requested
// session owns the occurrence.
func (s *Store) Open(sessionID, attID string) (io.ReadCloser, Descriptor, error) {
	if err := validateSessionID(sessionID); err != nil {
		return nil, Descriptor{}, err
	}
	if err := validateAttachmentID(attID); err != nil {
		return nil, Descriptor{}, err
	}

	var file *os.File
	var d Descriptor
	err := s.withLock(func() error {
		index, err := s.readSessionIndex(sessionID)
		if err != nil {
			return err
		}
		var ok bool
		d, ok = index[attID]
		if !ok {
			return notFound(attID)
		}
		if err := validateDescriptor(d); err != nil {
			return notFound(attID)
		}
		file, err = openBlobReadOnly(s.blobPath(d.SHA256))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return notFound(attID)
			}
			return fmt.Errorf("attachment: open blob: %w", err)
		}
		info, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return fmt.Errorf("attachment: stat blob: %w", err)
		}
		if !info.Mode().IsRegular() || info.Size() != d.Size {
			_ = file.Close()
			return notFound(attID)
		}
		return nil
	})
	if err != nil {
		return nil, Descriptor{}, err
	}
	return file, d, nil
}

// Lookup returns the descriptor for a session-owned attachment occurrence.
func (s *Store) Lookup(sessionID, attID string) (Descriptor, bool) {
	if validateSessionID(sessionID) != nil || validateAttachmentID(attID) != nil {
		return Descriptor{}, false
	}
	var d Descriptor
	err := s.withLock(func() error {
		index, err := s.readSessionIndex(sessionID)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		var ok bool
		d, ok = index[attID]
		if !ok || validateDescriptor(d) != nil {
			d = Descriptor{}
		}
		return nil
	})
	return d, err == nil && d.ID != ""
}

// ReleaseSession removes every occurrence owned by a session and unlinks blobs
// which are no longer referenced by any remaining session index.
func (s *Store) ReleaseSession(sessionID string) error {
	if err := validateSessionID(sessionID); err != nil {
		return err
	}
	return s.withLock(func() error {
		return s.releaseSessionLocked(sessionID)
	})
}

// RemoveRef removes one occurrence reference from a session. It is idempotent
// when the occurrence is not owned by the session.
func (s *Store) RemoveRef(sessionID, attID string) error {
	if err := validateSessionID(sessionID); err != nil {
		return err
	}
	if err := validateAttachmentID(attID); err != nil {
		return err
	}
	return s.withLock(func() error {
		index, err := s.readSessionIndex(sessionID)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		d, ok := index[attID]
		if !ok {
			return nil
		}
		delete(index, attID)
		if err := s.writeSessionIndex(sessionID, index); err != nil {
			return err
		}
		// Persist the rewritten index before removing an unreferenced blob.
		if err := syncDir(s.sessionsDir()); err != nil {
			return fmt.Errorf("attachment: sync session directory: %w", err)
		}
		refs, err := s.catalogFromIndexes()
		if err != nil {
			return err
		}
		if err := s.writeCatalog(refs); err != nil {
			return err
		}
		if _, referenced := refs[d.SHA256]; referenced {
			return nil
		}
		if err := os.Remove(s.blobPath(d.SHA256)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("attachment: remove unreferenced blob: %w", err)
		}
		if err := syncDir(filepath.Dir(s.blobPath(d.SHA256))); err != nil {
			return fmt.Errorf("attachment: sync blob directory: %w", err)
		}
		return nil
	})
}

// releaseSessionLocked removes a session index and any blobs no longer
// referenced by remaining indexes. The caller must hold the store lock.
func (s *Store) releaseSessionLocked(sessionID string) error {
	index, err := s.readSessionIndex(sessionID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.Remove(s.sessionPath(sessionID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("attachment: remove session index: %w", err)
	}
	// Persist index removal before unlinking blobs so a surviving index never points at a deleted blob after power loss.
	if err := syncDir(s.sessionsDir()); err != nil {
		return fmt.Errorf("attachment: sync session directory: %w", err)
	}
	refs, err := s.catalogFromIndexes()
	if err != nil {
		return err
	}
	var first error
	if err := s.writeCatalog(refs); err != nil {
		first = err
	}
	removedDirs := make(map[string]struct{})
	for _, d := range index {
		if _, referenced := refs[d.SHA256]; referenced {
			continue
		}
		path := s.blobPath(d.SHA256)
		if err := os.Remove(path); err != nil {
			if !errors.Is(err, fs.ErrNotExist) && first == nil {
				first = fmt.Errorf("attachment: remove unreferenced blob: %w", err)
			}
			continue
		}
		removedDirs[filepath.Dir(path)] = struct{}{}
	}
	// Persist blob unlinks only after the session index removal is durable.
	for dir := range removedDirs {
		if err := syncDir(dir); err != nil && first == nil {
			first = fmt.Errorf("attachment: sync blob directory: %w", err)
		}
	}
	return first
}

// Reconcile rewrites the session ownership indexes and catalog from live
// descriptors, removes indexes absent from live, and garbage-collects old
// unreferenced blobs and stale staging files. It MUST run at startup (or in a
// maintenance window) with no concurrent uploads, AddRef, or PutRef for
// sessions absent from live: live is a snapshot and is not safe for an
// actively uploading store.
func (s *Store) Reconcile(live map[string][]Descriptor) error {
	if err := validateLive(live); err != nil {
		return err
	}
	return s.withLock(func() error {
		for _, descriptors := range live {
			for _, d := range descriptors {
				if err := s.verifyBlob(d); err != nil {
					return err
				}
			}
		}

		for sessionID, descriptors := range live {
			index := make(map[string]Descriptor, len(descriptors))
			for _, d := range descriptors {
				index[d.ID] = d
			}
			if err := s.writeSessionIndex(sessionID, index); err != nil {
				return err
			}
		}
		entries, err := os.ReadDir(s.sessionsDir())
		if err != nil {
			return fmt.Errorf("attachment: list session indexes: %w", err)
		}
		removedIndex := false
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			sessionID := strings.TrimSuffix(entry.Name(), ".json")
			if validateSessionID(sessionID) != nil {
				continue
			}
			if _, keep := live[sessionID]; !keep {
				if err := os.Remove(s.sessionPath(sessionID)); err != nil {
					if !errors.Is(err, fs.ErrNotExist) {
						return fmt.Errorf("attachment: remove stale session index: %w", err)
					}
					continue
				}
				removedIndex = true
			}
		}
		if removedIndex {
			// Persist stale index removals before orphan GC can unlink their blobs.
			if err := syncDir(s.sessionsDir()); err != nil {
				return fmt.Errorf("attachment: sync session directory: %w", err)
			}
		}

		return s.reconcileIndexesLocked()
	})
}

// ReconcileExisting is the lightweight startup reconcile path. It trusts the
// store's per-session indexes and is O(sessions-with-attachments), rather than
// walking conversation transcripts. Indexes for sessions absent from live are
// released, then the catalog, orphan blobs, and stale staging are reconciled.
func (s *Store) ReconcileExisting(liveSessionIDs map[string]bool) error {
	for sessionID := range liveSessionIDs {
		if err := validateSessionID(sessionID); err != nil {
			return err
		}
	}
	return s.withLock(func() error {
		entries, err := os.ReadDir(s.sessionsDir())
		if err != nil {
			return fmt.Errorf("attachment: list session indexes: %w", err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			sessionID := strings.TrimSuffix(entry.Name(), ".json")
			if validateSessionID(sessionID) != nil || liveSessionIDs[sessionID] {
				continue
			}
			if err := s.releaseSessionLocked(sessionID); err != nil {
				return err
			}
		}
		return s.reconcileIndexesLocked()
	})
}

// reconcileIndexesLocked rebuilds metadata and removes unowned on-disk state.
// The caller must hold the store lock.
func (s *Store) reconcileIndexesLocked() error {
	refs, err := s.catalogFromIndexes()
	if err != nil {
		return err
	}
	if err := s.writeCatalog(refs); err != nil {
		return err
	}
	if err := s.removeOrphanBlobs(refs); err != nil {
		return err
	}
	return s.removeStaleStaging()
}

func (s *Store) withLock(fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := acquireStoreLock(s.lockPath())
	if err != nil {
		return err
	}
	defer lock.Close() //nolint:errcheck
	return fn()
}

func (s *Store) verifyBlob(d Descriptor) error {
	info, err := os.Lstat(s.blobPath(d.SHA256))
	if err != nil {
		return fmt.Errorf("attachment: blob %s: %w", d.SHA256, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() != d.Size {
		return fmt.Errorf("attachment: blob %s does not match descriptor", d.SHA256)
	}
	return nil
}

func (s *Store) readSessionIndex(sessionID string) (map[string]Descriptor, error) {
	data, err := os.ReadFile(s.sessionPath(sessionID))
	if err != nil {
		return nil, err
	}
	index := make(map[string]Descriptor)
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, fmt.Errorf("attachment: decode session index: %w", err)
	}
	for id, d := range index {
		if id != d.ID || validateDescriptor(d) != nil {
			return nil, errors.New("attachment: invalid session index")
		}
	}
	return index, nil
}

func (s *Store) writeSessionIndex(sessionID string, index map[string]Descriptor) error {
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return fmt.Errorf("attachment: encode session index: %w", err)
	}
	return atomicWrite(s.sessionsDir(), s.sessionPath(sessionID), data)
}

func (s *Store) catalogFromIndexes() (catalog, error) {
	entries, err := os.ReadDir(s.sessionsDir())
	if err != nil {
		return nil, fmt.Errorf("attachment: list session indexes: %w", err)
	}
	refs := make(catalog)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		sessionID := strings.TrimSuffix(entry.Name(), ".json")
		if validateSessionID(sessionID) != nil {
			continue
		}
		index, err := s.readSessionIndex(sessionID)
		if err != nil {
			return nil, err
		}
		for _, d := range index {
			entry := refs[d.SHA256]
			if entry.RefCount != 0 && entry.Size != d.Size {
				return nil, fmt.Errorf("attachment: inconsistent sizes for blob %s", d.SHA256)
			}
			entry.Size = d.Size
			entry.RefCount++
			refs[d.SHA256] = entry
		}
	}
	return refs, nil
}

func (s *Store) rebuildCatalogFromIndexes() error {
	refs, err := s.catalogFromIndexes()
	if err != nil {
		return err
	}
	return s.writeCatalog(refs)
}

func (s *Store) writeCatalog(refs catalog) error {
	data, err := json.MarshalIndent(refs, "", "  ")
	if err != nil {
		return fmt.Errorf("attachment: encode catalog: %w", err)
	}
	return atomicWrite(s.baseDir, s.catalogPath(), data)
}

func (s *Store) removeOrphanBlobs(refs catalog) error {
	root := filepath.Join(s.baseDir, "blobs", "sha256")
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("attachment: list blobs: %w", err)
	}
	var first error
	removedDirs := make(map[string]struct{})
	for _, firstDir := range entries {
		if !firstDir.IsDir() || !regexp.MustCompile(`^[0-9a-f]{2}$`).MatchString(firstDir.Name()) {
			continue
		}
		secondEntries, err := os.ReadDir(filepath.Join(root, firstDir.Name()))
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		for _, secondDir := range secondEntries {
			if !secondDir.IsDir() || !regexp.MustCompile(`^[0-9a-f]{2}$`).MatchString(secondDir.Name()) {
				continue
			}
			files, err := os.ReadDir(filepath.Join(root, firstDir.Name(), secondDir.Name()))
			if err != nil {
				if first == nil {
					first = err
				}
				continue
			}
			for _, file := range files {
				sha := file.Name()
				if validateSHA256(sha) != nil || sha[:2] != firstDir.Name() || sha[2:4] != secondDir.Name() {
					continue
				}
				if _, referenced := refs[sha]; referenced {
					continue
				}
				path := s.blobPath(sha)
				info, err := os.Lstat(path)
				if err != nil {
					if !errors.Is(err, fs.ErrNotExist) && first == nil {
						first = err
					}
					continue
				}
				if s.now().Sub(info.ModTime()) < s.orphanGrace {
					continue
				}
				if err := os.Remove(path); err != nil {
					if !errors.Is(err, fs.ErrNotExist) && first == nil {
						first = fmt.Errorf("attachment: remove orphan blob: %w", err)
					}
					continue
				}
				removedDirs[filepath.Dir(path)] = struct{}{}
			}
		}
	}
	// Persist orphan unlinks after their indexes have been durably removed.
	for dir := range removedDirs {
		if err := syncDir(dir); err != nil && first == nil {
			first = fmt.Errorf("attachment: sync blob directory: %w", err)
		}
	}
	return first
}

func (s *Store) removeStaleStaging() error {
	entries, err := os.ReadDir(s.stagingDir())
	if err != nil {
		return fmt.Errorf("attachment: list staging files: %w", err)
	}
	var first error
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".upload") {
			continue
		}
		path := filepath.Join(s.stagingDir(), entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) && first == nil {
				first = err
			}
			continue
		}
		if s.now().Sub(info.ModTime()) < stagingMaxAge {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) && first == nil {
			first = fmt.Errorf("attachment: remove stale staging file: %w", err)
		}
	}
	return first
}

func (s *Store) basePath(parts ...string) string {
	return filepath.Join(append([]string{s.baseDir}, parts...)...)
}

func (s *Store) blobsDir() string    { return s.basePath("blobs", "sha256") }
func (s *Store) sessionsDir() string { return s.basePath("sessions") }
func (s *Store) stagingDir() string  { return s.basePath("staging") }
func (s *Store) catalogPath() string { return s.basePath("catalog.json") }
func (s *Store) lockPath() string    { return s.basePath("catalog.lock") }

func (s *Store) blobPath(sha string) string {
	return s.basePath("blobs", "sha256", sha[:2], sha[2:4], sha)
}

func (s *Store) sessionPath(sessionID string) string {
	return s.basePath("sessions", sessionID+".json")
}

func atomicWrite(dir, path string, data []byte) error {
	tmp, err := os.CreateTemp(dir, ".attachment-*.tmp")
	if err != nil {
		return fmt.Errorf("attachment: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("attachment: secure temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("attachment: write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("attachment: sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("attachment: close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("attachment: rename temp file: %w", err)
	}
	// Persist the rename so index and catalog updates survive power loss.
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("attachment: sync directory: %w", err)
	}
	return nil
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		if errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EOPNOTSUPP) {
			return nil
		}
		return err
	}
	defer dir.Close() //nolint:errcheck // cannot affect the completed directory sync
	if err := dir.Sync(); err != nil {
		if errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EOPNOTSUPP) {
			return nil
		}
		return err
	}
	return nil
}

func validateLive(live map[string][]Descriptor) error {
	for sessionID, descriptors := range live {
		if err := validateSessionID(sessionID); err != nil {
			return err
		}
		seen := make(map[string]Descriptor, len(descriptors))
		for _, d := range descriptors {
			if err := validateDescriptor(d); err != nil {
				return err
			}
			if old, duplicate := seen[d.ID]; duplicate && old != d {
				return fmt.Errorf("attachment: conflicting duplicate occurrence %s", d.ID)
			}
			seen[d.ID] = d
		}
	}
	return nil
}

func validateDescriptor(d Descriptor) error {
	if err := validateAttachmentID(d.ID); err != nil {
		return err
	}
	if err := validateSHA256(d.SHA256); err != nil {
		return err
	}
	if d.Size < 0 || d.Width < 0 || d.Height < 0 {
		return errors.New("attachment: invalid descriptor dimensions or size")
	}
	if _, _, err := mime.ParseMediaType(d.Mime); err != nil {
		return fmt.Errorf("attachment: invalid MIME type: %w", err)
	}
	return nil
}

func validateAttachmentID(id string) error {
	if !attachmentIDPattern.MatchString(id) {
		return errors.New("attachment: invalid attachment ID")
	}
	return nil
}

func validateSHA256(sha string) error {
	if !sha256Pattern.MatchString(sha) {
		return errors.New("attachment: invalid SHA-256")
	}
	return nil
}

func validateSessionID(sessionID string) error {
	if !sessionIDPattern.MatchString(sessionID) {
		return errors.New("attachment: invalid session ID")
	}
	return nil
}

func newOccurrenceID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("attachment: generate occurrence ID: %w", err)
	}
	return "att_" + hex.EncodeToString(b), nil
}

func sanitizeName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(filepath.Clean(name))
	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == 0 || r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name))
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, '/') {
		return "attachment"
	}
	return name
}

func notFound(attID string) error {
	return fmt.Errorf("attachment %s: %w", attID, fs.ErrNotExist)
}
