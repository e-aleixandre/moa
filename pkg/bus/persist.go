package bus

import (
	"strings"
	"sync"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/session"
)

// SessionPersister abstracts session persistence.
type SessionPersister interface {
	// Snapshot persists the current session state.
	// Called by the persistence reactor. Implementations must be safe for
	// sequential calls (reactor serializes, but calls may come from
	// different goroutines).
	Snapshot(messages []core.AgentMessage, epoch int, metadata map[string]any) error
}

// TreePersister extends SessionPersister with tree-based persistence.
// Implementations that support v2 sessions should implement this interface.
type TreePersister interface {
	SessionPersister
	// SnapshotTree persists the session tree entries and leaf.
	SnapshotTree(entries []session.Entry, leafID string, metadata map[string]any) error
}

// collectMetadata gathers all session metadata for persistence.
// Uses the same map[string]any format as session.Session.Metadata
// (the existing persistence format — no schema change).
func collectMetadata(sctx *SessionContext) map[string]any {
	meta := make(map[string]any)
	m := sctx.Agent.Model()
	if m.Provider != "" {
		meta["model"] = m.Provider + "/" + m.ID
	} else if m.ID != "" {
		meta["model"] = m.ID
	}
	if lvl := sctx.Agent.ThinkingLevel(); lvl != "" {
		meta["thinking"] = lvl
	}
	if g := sctx.GetGate(); g != nil {
		meta["permission_mode"] = string(g.Mode())
	} else {
		meta["permission_mode"] = "yolo"
	}
	if sctx.TaskStore != nil {
		for k, v := range sctx.TaskStore.SaveToMetadata() {
			meta[k] = v
		}
	}
	if sctx.PlanMode != nil {
		for k, v := range sctx.PlanMode.SaveState() {
			meta[k] = v
		}
	}
	if sctx.PathPolicy != nil {
		meta["path_scope"] = sctx.PathPolicy.Scope()
		if paths := sctx.PathPolicy.AllowedPaths(); len(paths) > 0 {
			meta["allowed_paths"] = paths
		}
	}
	return meta
}

// RegisterPersistenceReactor subscribes to state-changing events and auto-saves.
// Saves are serialized through a mutex to prevent concurrent Snapshot calls.
// If the persister implements TreePersister and the session has a tree,
// it saves tree entries instead of flat messages.
func RegisterPersistenceReactor(b EventBus, sctx *SessionContext, p SessionPersister) {
	var mu sync.Mutex
	tp, hasTree := p.(TreePersister)

	save := func() {
		mu.Lock()
		defer mu.Unlock()
		meta := collectMetadata(sctx)

		// Prefer tree-based persistence when available
		if hasTree && sctx.Tree != nil && sctx.Tree.Len() > 0 {
			_ = tp.SnapshotTree(sctx.Tree.Entries(), sctx.Tree.LeafID(), meta)
			return
		}

		msgs := sctx.Agent.Messages()
		epoch := sctx.Agent.CompactionEpoch()
		_ = p.Snapshot(msgs, epoch, meta)
	}
	b.Subscribe(func(e RunEnded) { save() })
	b.Subscribe(func(e CommandExecuted) { save() })
	b.Subscribe(func(e ConfigChanged) { save() })
	b.Subscribe(func(e TasksUpdated) { save() })
	b.Subscribe(func(e CompactionEnded) { save() })
	b.Subscribe(func(e PlanModeChanged) { save() })
}

// extractFinalAssistantText returns the text of the last assistant message
// from the given slice. Used to populate RunEnded.FinalText.
func extractFinalAssistantText(msgs []core.AgentMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" {
			var parts []string
			for _, c := range msgs[i].Content {
				if c.Type == "text" && c.Text != "" {
					parts = append(parts, c.Text)
				}
			}
			return strings.Join(parts, "")
		}
	}
	return ""
}
