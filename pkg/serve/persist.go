package serve

import (
	"log/slog"
	"sync"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

// servePersister implements bus.SessionPersister for serve sessions.
// Thread-safe. Writes are serialized by the bus persistence reactor's mutex,
// but markDeleted/titleState may be called from any goroutine.
type servePersister struct {
	mu        sync.Mutex
	persisted *session.Session
	store     *session.FileStore
	// titleState returns the session's authoritative title, its source, and
	// whether the owning runtime is being torn down. Every write reads it under
	// sp.mu, so no caller can persist a title it observed earlier: the last
	// writer always saves the current state rather than its own stale copy.
	titleState func() (title, source string, closing bool)
	deleted    bool
	// preserved holds creation-time metadata the runtime knows nothing about
	// (origin, automation bookkeeping). collectMetadata rebuilds the map from
	// scratch on every snapshot, so without this the keys would vanish on the
	// first save after creation.
	preserved map[string]any
	// subagents is created on first use and kept: the store memoizes the
	// sidecar summaries it read, so a fresh instance per call would decode
	// every transcript header again on each WebSocket init.
	subagents *session.SubagentStore
	// onSaved runs after every successful save, outside the lock: task notices
	// count as delivered once they are on disk.
	onSaved func()
	// mcpTools returns the session's current MCP tool snapshot for
	// MetaMCPTools; ok is false when the session has no MCP servers. Nil
	// leaves whatever the session file held.
	mcpTools func() (snapshot any, ok bool)
}

func newServePersister(persisted *session.Session, store *session.FileStore, titleState func() (string, string, bool)) *servePersister {
	return &servePersister{
		persisted:  persisted,
		store:      store,
		titleState: titleState,
		preserved:  session.PreservedMetadata(persisted.Metadata),
	}
}

// Snapshot implements bus.SessionPersister.
func (sp *servePersister) Snapshot(messages []core.AgentMessage, epoch int, metadata map[string]any) error {
	return sp.snapshot(messages, epoch, metadata, false)
}

func (sp *servePersister) SnapshotAcknowledged(messages []core.AgentMessage, epoch int, metadata map[string]any) error {
	return sp.snapshot(messages, epoch, metadata, true)
}

func (sp *servePersister) snapshot(messages []core.AgentMessage, epoch int, metadata map[string]any, acknowledged bool) error {
	metadata = sp.withMCPTools(metadata)
	sp.mu.Lock()
	if sp.deleted || sp.persisted == nil || sp.store == nil {
		sp.mu.Unlock()
		if acknowledged {
			return core.ErrProviderWaitNotSaved
		}
		return nil
	}

	// A snapshot deliberately ignores `closing`: the final flush of a closing
	// runtime is exactly what must reach disk.
	snapshot := *sp.persisted
	snapshot.Title, snapshot.TitleSource, _ = sp.titleState()
	snapshot.Messages = make([]core.AgentMessage, len(messages))
	copy(snapshot.Messages, messages)
	snapshot.CompactionEpoch = epoch
	sp.settleVoiceOwed(&snapshot)
	snapshot.Metadata = session.ApplyPreservedMetadata(metadata, sp.preserved)

	store := sp.store
	// Save under the lock so it serializes with saveTitle: otherwise a
	// concurrent out-of-band write could land between the copy above and the
	// Save below, and this Save would clobber it. The persistence reactor is
	// single-threaded, so the only contenders for this lock are the rare
	// out-of-band writers, making the in-lock I/O cost negligible.
	err := store.Save(&snapshot)
	if err == nil {
		*sp.persisted = snapshot
	}
	sp.mu.Unlock()

	if err != nil {
		slog.Warn("session save failed", "error", err)
		return err
	}
	sp.saved()
	return nil
}

// SnapshotTree implements bus.TreePersister — saves tree entries instead of flat messages.
func (sp *servePersister) SnapshotTree(entries []session.Entry, leafID string, metadata map[string]any) error {
	return sp.snapshotTree(entries, leafID, metadata, false)
}

func (sp *servePersister) SnapshotTreeAcknowledged(entries []session.Entry, leafID string, metadata map[string]any) error {
	return sp.snapshotTree(entries, leafID, metadata, true)
}

func (sp *servePersister) snapshotTree(entries []session.Entry, leafID string, metadata map[string]any, acknowledged bool) error {
	metadata = sp.withMCPTools(metadata)
	sp.mu.Lock()
	if sp.deleted || sp.persisted == nil || sp.store == nil {
		sp.mu.Unlock()
		if acknowledged {
			return core.ErrProviderWaitNotSaved
		}
		return nil
	}

	snapshot := *sp.persisted
	snapshot.Title, snapshot.TitleSource, _ = sp.titleState() // see Snapshot
	snapshot.Version = session.SessionVersion
	snapshot.Entries = make([]session.Entry, len(entries))
	copy(snapshot.Entries, entries)
	snapshot.LeafID = leafID
	sp.settleVoiceOwed(&snapshot)
	snapshot.Metadata = session.ApplyPreservedMetadata(metadata, sp.preserved)
	// Clear v1 fields
	snapshot.Messages = nil
	snapshot.CompactionEpoch = 0

	store := sp.store
	// Save under the lock — see the rationale in Snapshot.
	err := store.Save(&snapshot)
	if err == nil {
		*sp.persisted = snapshot
	}
	sp.mu.Unlock()

	if err != nil {
		slog.Warn("session save failed", "error", err)
		return err
	}
	sp.saved()
	return nil
}

// withMCPTools sets the live MCP tool snapshot into a snapshot's metadata, so
// the saved one is never the stale copy loaded with the session. Called before
// sp.mu: it reads the session's controller.
func (sp *servePersister) withMCPTools(metadata map[string]any) map[string]any {
	if sp.mcpTools == nil {
		return metadata
	}
	snap, ok := sp.mcpTools()
	sp.mu.Lock()
	delete(sp.preserved, session.MetaMCPTools)
	sp.mu.Unlock()
	if !ok {
		return metadata
	}
	if metadata == nil {
		metadata = make(map[string]any, 1)
	}
	metadata[session.MetaMCPTools] = snap
	return metadata
}

func (sp *servePersister) saved() {
	if sp.onSaved != nil {
		sp.onSaved()
	}
}

// has reports whether the last saved transcript satisfies fn.
func (sp *servePersister) has(fn func(*session.Session) bool) bool {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	return sp.persisted != nil && fn(sp.persisted)
}

// saveTitle persists the session's current title out-of-band (e.g. background
// auto-titling, a rename) that would otherwise not land on disk until the next
// snapshot. The last snapshot's messages are reused, so this is safe to call
// any time.
//
// It takes no arguments on purpose: a caller that computed a title and was then
// descheduled must not write that stale value over a newer one. The write
// happens under the lock so it serializes with markDeleted: once a session is
// deleted this becomes a no-op and can never resurrect its file.
func (sp *servePersister) saveTitle() {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.deleted || sp.persisted == nil || sp.store == nil {
		return
	}
	title, source, closing := sp.titleState()
	if closing {
		// This runtime is being torn down: its own final flush owns the file,
		// and a resumed runtime may already have replaced it on disk.
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

// recordOwnerDetached sets or clears the detach marker and saves synchronously.
// It is changed at runtime, so it is written to the preserved set as well as
// the persisted metadata; clearing must delete it from both, because
// ApplyPreservedMetadata only ever adds keys and a stale preserved entry would
// re-detach the session on the next snapshot.
func (sp *servePersister) recordOwnerDetached(detached bool) error {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.deleted || sp.persisted == nil || sp.store == nil {
		return nil
	}
	previousPersisted, hadPersisted := sp.persisted.Metadata[session.MetaOwnerDetached]
	previousPreserved, hadPreserved := sp.preserved[session.MetaOwnerDetached]
	sp.persisted.SetOwnerDetached(detached)
	if detached {
		if sp.preserved == nil {
			sp.preserved = make(map[string]any, 1)
		}
		sp.preserved[session.MetaOwnerDetached] = true
	} else {
		delete(sp.preserved, session.MetaOwnerDetached)
	}
	snapshot := *sp.persisted
	if err := sp.store.Save(&snapshot); err != nil {
		if hadPersisted {
			sp.persisted.Metadata[session.MetaOwnerDetached] = previousPersisted
		} else {
			delete(sp.persisted.Metadata, session.MetaOwnerDetached)
		}
		if hadPreserved {
			if sp.preserved == nil {
				sp.preserved = make(map[string]any, 1)
			}
			sp.preserved[session.MetaOwnerDetached] = previousPreserved
		} else {
			delete(sp.preserved, session.MetaOwnerDetached)
		}
		return err
	}
	return nil
}

// oweVoiceRelay records a voice relay as owed until a save holds it as a
// message; the caller saves before the relay counts as delivered.
func (sp *servePersister) oweVoiceRelay(deliveryID string, fields map[string]any) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.preserved == nil {
		sp.preserved = make(map[string]any, 2)
	}
	rec := map[string]any{"delivery_id": deliveryID}
	for k, v := range fields {
		rec[k] = v
	}
	sp.preserved[session.MetaVoiceRelays] = append(voiceRelayRecords(sp.preserved[session.MetaVoiceRelays]), rec)
}

// forgetVoiceRelay withdraws a relay that was never admitted.
func (sp *servePersister) forgetVoiceRelay(deliveryID string) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	var keep []any
	for _, r := range voiceRelayRecords(sp.preserved[session.MetaVoiceRelays]) {
		if r.(map[string]any)["delivery_id"] != deliveryID {
			keep = append(keep, r)
		}
	}
	sp.setVoiceRelays(keep)
}

// owedVoiceRelays is the relays still owed, in the order they were given.
func (sp *servePersister) owedVoiceRelays() []map[string]any {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	var out []map[string]any
	for _, r := range voiceRelayRecords(sp.preserved[session.MetaVoiceRelays]) {
		out = append(out, r.(map[string]any))
	}
	return out
}

func (sp *servePersister) setVoiceRelays(relays []any) {
	if len(relays) == 0 {
		delete(sp.preserved, session.MetaVoiceRelays)
		return
	}
	sp.preserved[session.MetaVoiceRelays] = relays
}

// voiceRelayRecords reads MetaVoiceRelays, as set at runtime or as decoded
// from a saved header: a list of objects, anything else dropped.
func voiceRelayRecords(v any) []any {
	list, _ := v.([]any)
	out := make([]any, 0, len(list))
	for _, r := range list {
		if m, ok := r.(map[string]any); ok {
			if id, _ := m["delivery_id"].(string); id != "" {
				out = append(out, m)
			}
		}
	}
	return out
}

// settleVoiceOwed derives, on every save, what the snapshot owes: the relays
// not yet saved as messages, and the voice marker. The marker is true exactly
// when a saved minutes record has wake=true and the snapshot lacks its wake,
// or a relay is still owed; nothing sets it independently of the content
// being saved, so it can never disagree with the saved messages (a marker
// set before its record entered the snapshot was once dropped by an
// interleaved save and lost the wake after a crash).
//
// The marker stays in the header, as an index, because startup reads only
// headers: scanning every message of every session file at boot was
// rejected. Called under sp.mu.
func (sp *servePersister) settleVoiceOwed(s *session.Session) {
	saved, wakeOwed := voiceSaved(s)
	var relays []any
	for _, r := range voiceRelayRecords(sp.preserved[session.MetaVoiceRelays]) {
		if !saved[r.(map[string]any)["delivery_id"].(string)] {
			relays = append(relays, r)
		}
	}
	sp.setVoiceRelays(relays)
	if wakeOwed || len(relays) > 0 {
		if sp.preserved == nil {
			sp.preserved = make(map[string]any, 1)
		}
		sp.preserved[session.MetaVoiceWake] = true
	} else {
		delete(sp.preserved, session.MetaVoiceWake)
	}
}

// voiceSaved is the voice delivery IDs the session's messages hold, and
// whether they hold a minutes record whose wake they do not. One linear pass
// per save, over messages the save serializes anyway; sessions without voice
// records allocate nothing.
func voiceSaved(s *session.Session) (saved map[string]bool, wakeOwed bool) {
	var wakes []string
	visit := func(m *core.AgentMessage) {
		id := voiceDeliveryID(m.Custom)
		if id == "" {
			return
		}
		if saved == nil {
			saved = map[string]bool{}
		}
		saved[id] = true
		if m.Custom["kind"] == "minutes" && m.Custom["wake"] == true {
			wakes = append(wakes, voiceWakeID(id))
		}
	}
	if len(s.Entries) == 0 {
		for i := range s.Messages {
			visit(&s.Messages[i])
		}
	} else {
		for i := range s.Entries {
			if s.Entries[i].Type == session.EntryMessage {
				visit(&s.Entries[i].Message)
			}
		}
	}
	for _, w := range wakes {
		if !saved[w] {
			wakeOwed = true
		}
	}
	return saved, wakeOwed
}

// markDeleted prevents future Snapshot calls from writing to disk.
func (sp *servePersister) markDeleted() {
	sp.mu.Lock()
	sp.deleted = true
	sp.mu.Unlock()
}

// subagentStore returns a side store for persisting subagent transcripts next
// to this session's file, or nil if persistence is unavailable/deleted.
// storeDir is the directory of the session's file, or "" once deleted.
func (sp *servePersister) storeDir() string {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.deleted || sp.store == nil {
		return ""
	}
	return sp.store.Dir()
}

func (sp *servePersister) subagentStore(sessionID string) *session.SubagentStore {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.deleted || sp.store == nil {
		return nil
	}
	if sp.subagents == nil {
		sp.subagents = session.NewSubagentStore(sp.store.Dir(), sessionID)
	}
	return sp.subagents
}

// Serialize acknowledged sidecar writes with deletion, like root snapshots.
func (sp *servePersister) saveSubagentTranscript(sessionID string, transcript session.SubagentTranscript) error {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.deleted || sp.store == nil {
		return core.ErrProviderWaitNotSaved
	}
	if sp.subagents == nil {
		sp.subagents = session.NewSubagentStore(sp.store.Dir(), sessionID)
	}
	return sp.subagents.Save(transcript)
}

// saveSubagentCacheAudit overwrites the job's cache audit with a snapshot. The
// deleted check and the disk write share sp.mu, so a late snapshot can never
// recreate a side directory that deletion removed.
func (sp *servePersister) saveSubagentCacheAudit(sessionID string, a session.SubagentCacheAudit) error {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.deleted || sp.store == nil {
		return nil
	}
	if sp.subagents == nil {
		sp.subagents = session.NewSubagentStore(sp.store.Dir(), sessionID)
	}
	return sp.subagents.SaveCacheAudit(a)
}

// sessionDir returns the directory holding this session's json file, or "".
func (sp *servePersister) sessionDir() string {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.deleted || sp.store == nil {
		return ""
	}
	return sp.store.Dir()
}
