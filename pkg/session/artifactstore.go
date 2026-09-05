package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// ArtifactsVersion is the on-disk schema version of the artifact sidecar.
const ArtifactsVersion = 1

// ErrArtifactsDeleted is returned by Upsert once DeleteReferences has run for
// this store instance: the conversation is gone, so a tool call still inside
// its critical section must not recreate the sidecar.
var ErrArtifactsDeleted = errors.New("session: artifacts deleted")

// Artifact is one durable reference from a conversation to a live canonical
// path. It never holds bytes, versions or inode identity: reads always open
// Path again, so an atomic replacement at the same location is visible.
type Artifact struct {
	ID          string    `json:"id"`
	Path        string    `json:"path"`
	Name        string    `json:"name"`
	Title       string    `json:"title,omitempty"`
	Description string    `json:"description,omitempty"`
	Mime        string    `json:"mime"`
	Size        int64     `json:"size"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ArtifactMeta carries the observed metadata of one publication. Title and
// Description only overwrite stored values when non-empty, so a bare re-send
// does not silently erase a caption the agent gave earlier.
type ArtifactMeta struct {
	Name        string
	Title       string
	Description string
	Mime        string
	Size        int64
}

type artifactCatalog struct {
	Version   int        `json:"version"`
	Artifacts []Artifact `json:"artifacts"`
}

// ArtifactStore persists one conversation's artifact references in a sidecar
// next to its session JSON: "<sessionDir>/<sessionID>.artifacts".
//
// The extension is deliberately NOT ".json": FileStore.List walks every
// "*.json" in the directory, and a catalog without a session ID in its header
// would be read in full on every session listing.
//
// The writer instance is unique and owned by the live ManagedSession; handlers
// open read-only instances, which the atomic rename always shows either the
// previous or the new catalog.
type ArtifactStore struct {
	path string

	mu      sync.Mutex
	deleted bool
}

// NewArtifactStore returns the store for one session. sessionDir is the
// directory holding "<sessionID>.json" (FileStore.Dir()). Nothing is created
// until the first Upsert.
func NewArtifactStore(sessionDir, sessionID string) *ArtifactStore {
	return &ArtifactStore{path: filepath.Join(sessionDir, sessionID+".artifacts")}
}

// Path returns the sidecar path (may not exist).
func (s *ArtifactStore) Path() string { return s.path }

// List returns the stored artifacts, newest update first (ties broken by ID so
// the order is stable). A missing sidecar is an empty collection, not an error.
func (s *ArtifactStore) List() ([]Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cat, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	sortArtifacts(cat.Artifacts)
	return cat.Artifacts, nil
}

// Get returns one artifact by ID.
func (s *ArtifactStore) Get(id string) (Artifact, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cat, err := s.loadLocked()
	if err != nil {
		return Artifact{}, false, err
	}
	for _, a := range cat.Artifacts {
		if a.ID == id {
			return a, true, nil
		}
	}
	return Artifact{}, false, nil
}

// Upsert publishes canonicalPath in this conversation and returns the stored
// artifact. One entry per canonical path: a re-send keeps the ID and
// CreatedAt and refreshes the observed metadata. Returns ErrArtifactsDeleted
// once the conversation has been deleted.
func (s *ArtifactStore) Upsert(canonicalPath string, meta ArtifactMeta) (Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleted {
		return Artifact{}, ErrArtifactsDeleted
	}
	cat, err := s.loadLocked()
	if err != nil {
		return Artifact{}, err
	}

	now := nowFunc().UTC()
	var stored Artifact
	found := false
	for i, a := range cat.Artifacts {
		if a.Path != canonicalPath {
			continue
		}
		a.Name = meta.Name
		a.Mime = meta.Mime
		a.Size = meta.Size
		if meta.Title != "" {
			a.Title = meta.Title
		}
		if meta.Description != "" {
			a.Description = meta.Description
		}
		a.UpdatedAt = now
		cat.Artifacts[i] = a
		stored, found = a, true
		break
	}
	if !found {
		stored = Artifact{
			ID:          newArtifactID(),
			Path:        canonicalPath,
			Name:        meta.Name,
			Title:       meta.Title,
			Description: meta.Description,
			Mime:        meta.Mime,
			Size:        meta.Size,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		cat.Artifacts = append(cat.Artifacts, stored)
	}

	if err := s.writeLocked(cat); err != nil {
		return Artifact{}, err
	}
	return stored, nil
}

// DeleteReferences removes the sidecar and tombstones this instance so a
// concurrent publication that had not yet entered the critical section cannot
// recreate it. Original files are never touched.
func (s *ArtifactStore) DeleteReferences() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = true
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("session: delete artifacts: %w", err)
	}
	return nil
}

func (s *ArtifactStore) loadLocked() (artifactCatalog, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return artifactCatalog{Version: ArtifactsVersion}, nil
		}
		return artifactCatalog{}, fmt.Errorf("session: read artifacts: %w", err)
	}
	var cat artifactCatalog
	if err := json.Unmarshal(data, &cat); err != nil {
		return artifactCatalog{}, fmt.Errorf("session: parse artifacts: %w", err)
	}
	return cat, nil
}

// writeLocked replaces the catalog atomically: temp file in the same directory
// (with a non-".json" extension so a crash cannot leave something the session
// listing would try to parse), fsync, rename, directory sync.
func (s *ArtifactStore) writeLocked(cat artifactCatalog) error {
	cat.Version = ArtifactsVersion
	if cat.Artifacts == nil {
		cat.Artifacts = []Artifact{}
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("session: artifacts mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(s.path)+".tmp*")
	if err != nil {
		return fmt.Errorf("session: artifacts write: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("session: artifacts write: %w", err)
	}
	if err := encodeCompactJSON(tmp, cat); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("session: artifacts marshal: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("session: artifacts sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("session: artifacts write: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("session: artifacts rename: %w", err)
	}
	return syncArtifactDir(dir)
}

func syncArtifactDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("session: artifacts dir sync: %w", err)
	}
	defer d.Close() //nolint:errcheck
	if err := d.Sync(); err != nil {
		return fmt.Errorf("session: artifacts dir sync: %w", err)
	}
	return nil
}

func sortArtifacts(list []Artifact) {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].UpdatedAt.Equal(list[j].UpdatedAt) {
			return list[i].ID < list[j].ID
		}
		return list[i].UpdatedAt.After(list[j].UpdatedAt)
	})
}

// newArtifactID generates the 32-hex identifier of an artifact. It is stable
// for the life of the reference: URLs handed to clients keep working across
// re-sends and restarts.
func newArtifactID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%032x", nowFunc().UnixNano())
	}
	return hex.EncodeToString(b)
}

// RemoveArtifacts deletes the sidecar of a session that has no live writer
// (an unloaded conversation being deleted).
func RemoveArtifacts(sessionDir, sessionID string) error {
	path := filepath.Join(sessionDir, sessionID+".artifacts")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("session: delete artifacts: %w", err)
	}
	return nil
}
