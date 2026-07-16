package attention

import (
	"time"

	"github.com/ealeixandre/moa/pkg/bus"
)

// wire.go — the /api/pulse/guardian/ws protocol (design §4) and the bus event
// whitelist.
//
// Server->client messages are a small tagged union. init is authoritative: on
// (re)connect the client REPLACES its local state with it. There is no seq /
// replay in v1 — a reconnect always re-inits. Client->server messages are
// handled in ws.go.

// ProtocolVersion is sent in init so a client can refuse a mismatched server.
const ProtocolVersion = 1

// ServerMsg is one message sent to the active voice client.
type ServerMsg struct {
	Type string `json:"type"` // "init" | "attention" | "item_update" | "briefing" | "roster" | "inactive" | "error"
	V    int    `json:"v,omitempty"`

	// init
	Items    []AttentionItem `json:"items,omitempty"`    // all unresolved P0 items
	Sessions []SessionBrief  `json:"sessions,omitempty"` // the roster of attached sessions (init + roster)

	// attention (a new item) / item_update (state change of an existing item)
	Item *AttentionItem `json:"item,omitempty"`

	// briefing (Phase 2): an ephemeral progress/terminal note. It is NOT
	// tracked or resolvable. The sole exception is a successful run completion,
	// whose Termination metadata is retained for recovery in init.
	Briefing *Briefing `json:"briefing,omitempty"`

	// terminations contains successful run completions awaiting explicit client
	// acknowledgement. It appears only in init and is replaced by the next init,
	// like Items and Sessions. Clients must deduplicate by ID and send
	// ack_termination only after speaking the notice.
	Terminations []RunTermination `json:"terminations,omitempty"`

	// error
	RequestID string `json:"request_id,omitempty"`
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
}

// SessionBrief is the voice client's view of one live agent session: enough to
// name it, know its state, and address orders to it (via the existing HTTP
// endpoints). It carries moa's real session id so the client can POST to
// /api/sessions/{id}/... The client never derives what an agent is doing from
// the bus — the server hands it this compact, authoritative view.
type SessionBrief struct {
	SessionID   string `json:"session_id"`
	Alias       string `json:"alias"`
	Title       string `json:"title"`
	State       string `json:"state"`         // idle | running | permission | error
	PendingAsks int    `json:"pending_asks"`  // count of unanswered questions
	PendingPerm int    `json:"pending_perms"` // count of unresolved permissions
}

// Briefing is an ephemeral spoken note about progress (a run finished, a goal
// ended, a verify failed). Unlike an AttentionItem it carries no id, no state,
// no ref — the chief-of-staff mentions it and moves on.
type Briefing struct {
	Priority  Priority `json:"priority"`   // P1 (terminal) or P2 (progress)
	Kind      Kind     `json:"kind"`       // run_ok | goal_ended | goal_stalled | verify_fail
	SessionID string   `json:"session_id"` // origin session
	Alias     string   `json:"alias"`      // pronounceable session name
	Spoken    string   `json:"spoken"`     // text written for the ear

	// Termination is set only for a successful run completion. It lets a voice
	// client ask for the full result through the existing messages endpoint.
	Termination *RunTermination `json:"termination,omitempty"`
}

// RunTermination is the durable, at-least-once completion notice for one
// successful agent run. It is not an AttentionItem: it is neither actionable
// nor resolvable. Ref identifies the transcript containing the full answer;
// GET /api/sessions/{session_id}/messages is the existing detail endpoint.
type RunTermination struct {
	ID        string         `json:"id"`
	SessionID string         `json:"session_id"`
	Alias     string         `json:"alias"`
	Spoken    string         `json:"spoken"`
	Summary   string         `json:"summary"`
	CreatedAt time.Time      `json:"created_at"`
	Ref       TerminationRef `json:"ref"`
}

// TerminationRef points a client at the completed run's transcript. RunGen
// distinguishes successive runs in a session; the messages endpoint exposes
// the complete owner-authorized conversation rather than a special voice API.
type TerminationRef struct {
	SessionID   string `json:"session_id"`
	RunGen      uint64 `json:"run_gen"`
	MessagesURL string `json:"messages_url"`
}

// ClientMsg is one message received from the active voice client. ack confirms
// an attention item; ack_termination confirms a run notice after it was
// spoken; get_status requests an authoritative init snapshot.
type ClientMsg struct {
	Type          string `json:"type"`           // "ack" | "ack_termination" | "get_status"
	RequestID     string `json:"request_id"`     // echoed on errors
	ItemID        string `json:"item_id"`        // for ack
	TerminationID string `json:"termination_id"` // for ack_termination
}

// whitelisted reports whether a bus event is one the Attention Service acts on
// in Phase 1A. Everything else (deltas, tool streaming, cost, context, tasks,
// subagents, compaction, config) is ignored here — future phases extend this
// list, they never fall back to "everything minus noise" (review point 3).
func whitelisted(ev any) bool {
	switch ev.(type) {
	case bus.AskUserRequested,
		bus.AskUserResolved,
		bus.PermissionRequested,
		bus.PermissionResolved,
		bus.StateChanged,
		bus.RunStarted,
		bus.RunEnded,
		// Phase 2: progress/terminal briefings.
		bus.GoalEnded,
		bus.GoalIterationEnded,
		bus.GoalChanged,
		bus.AutoVerifyEnded:
		return true
	default:
		return false
	}
}
