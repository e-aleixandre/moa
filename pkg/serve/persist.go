package serve

import (
	"log/slog"
	"sync"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

// servePersister implements bus.SessionPersister for serve sessions.
// Thread-safe. Writes are serialized by the bus persistence reactor's mutex,
// but markDeleted/titleFn may be called from any goroutine.
type servePersister struct {
	mu        sync.Mutex
	persisted *session.Session
	store     *session.FileStore
	titleFn   func() string // returns current session title under lock
	deleted   bool
	// preserved holds creation-time metadata the runtime knows nothing about
	// (origin, automation bookkeeping). collectMetadata rebuilds the map from
	// scratch on every snapshot, so without this the keys would vanish on the
	// first save after creation.
	preserved map[string]any
}

func newServePersister(persisted *session.Session, store *session.FileStore, titleFn func() string) *servePersister {
	return &servePersister{
		persisted: persisted,
		store:     store,
		titleFn:   titleFn,
		preserved: session.PreservedMetadata(persisted.Metadata),
	}
}

// Snapshot implements bus.SessionPersister.
func (sp *servePersister) Snapshot(messages []core.AgentMessage, epoch int, metadata map[string]any) error {
	sp.mu.Lock()
	if sp.deleted || sp.persisted == nil || sp.store == nil {
		sp.mu.Unlock()
		return nil
	}

	sp.persisted.Title = sp.titleFn()
	sp.persisted.Messages = make([]core.AgentMessage, len(messages))
	copy(sp.persisted.Messages, messages)
	sp.persisted.CompactionEpoch = epoch
	sp.persisted.Metadata = session.ApplyPreservedMetadata(metadata, sp.preserved)

	snapshot := *sp.persisted
	store := sp.store
	// Save under the lock so it serializes with saveTitle: otherwise a
	// concurrent out-of-band write could land between the copy above and the
	// Save below, and this Save would clobber it. The persistence reactor is
	// single-threaded, so the only contenders for this lock are the rare
	// out-of-band writers, making the in-lock I/O cost negligible.
	err := store.Save(&snapshot)
	sp.mu.Unlock()

	if err != nil {
		slog.Warn("session save failed", "error", err)
		return err
	}
	return nil
}

// SnapshotTree implements bus.TreePersister — saves tree entries instead of flat messages.
func (sp *servePersister) SnapshotTree(entries []session.Entry, leafID string, metadata map[string]any) error {
	sp.mu.Lock()
	if sp.deleted || sp.persisted == nil || sp.store == nil {
		sp.mu.Unlock()
		return nil
	}

	sp.persisted.Title = sp.titleFn()
	sp.persisted.Version = session.SessionVersion
	sp.persisted.Entries = make([]session.Entry, len(entries))
	copy(sp.persisted.Entries, entries)
	sp.persisted.LeafID = leafID
	sp.persisted.Metadata = session.ApplyPreservedMetadata(metadata, sp.preserved)
	// Clear v1 fields
	sp.persisted.Messages = nil
	sp.persisted.CompactionEpoch = 0

	snapshot := *sp.persisted
	store := sp.store
	// Save under the lock — see the rationale in Snapshot.
	err := store.Save(&snapshot)
	sp.mu.Unlock()

	if err != nil {
		slog.Warn("session save failed", "error", err)
		return err
	}
	return nil
}

// saveTitle persists a title change made out-of-band (e.g. background
// auto-titling) that would otherwise not land on disk until the next snapshot.
// The last snapshot's messages are reused, so this is safe to call any time.
//
// The write happens under the lock so it serializes with markDeleted: once a
// session is deleted this becomes a no-op and can never resurrect its file.
func (sp *servePersister) saveTitle(title, source string) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.deleted || sp.persisted == nil || sp.store == nil {
		return
	}
	sp.persisted.Title = title
	sp.persisted.TitleSource = source
	snapshot := *sp.persisted
	if err := sp.store.Save(&snapshot); err != nil {
		slog.Warn("session title save failed", "error", err)
	}
}

// recordIdempotencyKey writes the Automation API key into the session metadata
// and saves synchronously. It is called only after the run's first prompt was
// accepted, so a session that never got one is never resolvable by key. The key
// also joins the preserved set, so it survives the snapshot rebuilds that
// reconstruct Metadata from scratch (same treatment as origin).
func (sp *servePersister) recordIdempotencyKey(key string) error {
	if key == "" {
		return nil
	}
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.deleted || sp.persisted == nil || sp.store == nil {
		return nil
	}
	sp.persisted.SetIdempotencyKey(key)
	if sp.preserved == nil {
		sp.preserved = make(map[string]any, 1)
	}
	sp.preserved[session.MetaIdempotencyKey] = key
	snapshot := *sp.persisted
	return sp.store.Save(&snapshot)
}

// markDeleted prevents future Snapshot calls from writing to disk.
func (sp *servePersister) markDeleted() {
	sp.mu.Lock()
	sp.deleted = true
	sp.mu.Unlock()
}

// subagentStore returns a side store for persisting subagent transcripts next
// to this session's file, or nil if persistence is unavailable/deleted.
func (sp *servePersister) subagentStore(sessionID string) *session.SubagentStore {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.deleted || sp.store == nil {
		return nil
	}
	return session.NewSubagentStore(sp.store.Dir(), sessionID)
}
