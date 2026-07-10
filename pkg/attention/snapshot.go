package attention

import "github.com/ealeixandre/moa/pkg/bus"

// snapshot.go — per-session aggregated state owned by Service.loop.
//
// The snapshot is the durable truth the service keeps about each session,
// updated from whitelisted bus events. It intentionally records only what the
// voice layer needs and avoids asserting fragile "facts": we track tool CALL
// counts, not "files edited", because the latter can't be inferred reliably
// from the event stream (design §2.3, review point 11).

// sessionSnapshot is the live state of one attached session. Only Service.loop
// reads or writes it.
type sessionSnapshot struct {
	id    string
	gen   uint64           // generation token; stale-event guard
	alias string           // pronounceable name for speech
	title string           // human title (may change via auto-title)
	state bus.SessionState // real state type from the bus, not a raw string

	// Pending requests, keyed by moa's real ref id (perm_%d / ask_%d). Maps,
	// not single values: ApprovalManager can hold more than one (verified in
	// pkg/bus/approvals.go — PendingInfo only returns the first, but the manager
	// stores many). Each maps to the attention item id created for it, so a
	// resolution event can find and resolve the right item.
	pendingPerm map[string]string // refID -> attentionItemID
	pendingAsk  map[string]string // refID -> attentionItemID

	// lastError is the most recent error message seen via StateChanged(error),
	// used to dedupe RunEnded{Err} against it within a short window.
	lastError string

	// Phase 2 novelty filter: signatures of the last progress briefing emitted
	// per kind, so a repeated identical verdict (e.g. the same goal-iteration
	// feedback) isn't narrated twice. Keyed by Kind.
	lastBriefSig map[Kind]string
}

func newSessionSnapshot(id, alias, title string, gen uint64) *sessionSnapshot {
	return &sessionSnapshot{
		id:           id,
		gen:          gen,
		alias:        alias,
		title:        title,
		state:        bus.StateIdle,
		pendingPerm:  make(map[string]string),
		pendingAsk:   make(map[string]string),
		lastBriefSig: make(map[Kind]string),
	}
}
