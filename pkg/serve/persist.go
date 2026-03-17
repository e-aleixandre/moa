package serve

import (
	"log/slog"
	"sync"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/session"
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
}

func newServePersister(persisted *session.Session, store *session.FileStore, titleFn func() string) *servePersister {
	return &servePersister{
		persisted: persisted,
		store:     store,
		titleFn:   titleFn,
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
	sp.persisted.Metadata = metadata

	snapshot := *sp.persisted
	store := sp.store
	sp.mu.Unlock()

	if err := store.Save(&snapshot); err != nil {
		slog.Warn("session save failed", "error", err)
		return err
	}
	return nil
}

// markDeleted prevents future Snapshot calls from writing to disk.
func (sp *servePersister) markDeleted() {
	sp.mu.Lock()
	sp.deleted = true
	sp.mu.Unlock()
}
