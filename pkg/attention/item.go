// Package attention implements the Attention Service: a server-side component
// that consumes the event bus of every active moa session and produces a
// priority-ordered "attention queue" of items already written for the ear.
//
// It is the brain of the voice orchestrator. A dumb realtime client (browser
// today, native app tomorrow) connects to a single WebSocket and reads spoken
// briefings instead of the raw per-session bus; it never decides what matters.
//
// Phase 1A scope (this file set): P0 items only — the things that STOP an agent
// and require the user: ask_user, permission_request, and error. No progress
// (P1/P2/P3), no digest, no LLM summaries, no coalescing. See
// tmp/plans/attention-service-design.md.
//
// Concurrency model: a single goroutine (Service.loop) owns ALL mutable state
// (snapshots, queue, item states, generations, the active client). Nothing else
// mutates it. External reads (init, get_status) and bus events arrive as
// messages on channels; the loop replies with copies, never live pointers.
package attention

import "time"

// Priority ranks how urgently an item needs the user's attention. Phase 1A only
// emits P0; higher-numbered levels are defined for later phases so the wire
// protocol is stable.
type Priority int

const (
	// P0Blocking: the agent is STOPPED waiting for the user (ask_user,
	// permission_request, error). Interrupt now.
	P0Blocking Priority = 0
	// P1Terminal: a run finished with an error / a goal ended. (Phase 2)
	P1Terminal Priority = 1
	// P2Progress: a run finished OK / a goal iteration was satisfied. (Phase 2)
	P2Progress Priority = 2
	// P3Ambient: subagents, tasks, cost. (Phase 3)
	P3Ambient Priority = 3
)

// Kind is the semantic type of an attention item. Used for dedup signatures and
// client rendering. Phase 1A uses only the P0 kinds.
type Kind string

const (
	KindAsk        Kind = "ask"        // agent is asking the user a question
	KindPermission Kind = "permission" // agent requests permission to run a tool
	KindError      Kind = "error"      // session entered the error state

	// Phase 2 progress/terminal kinds. These are EPHEMERAL briefings, not
	// tracked P0 items: the chief-of-staff telling you how things are going.
	KindRunOK       Kind = "run_ok"       // a run finished successfully (P2)
	KindGoalEnded   Kind = "goal_ended"   // a goal loop stopped (P1)
	KindGoalStalled Kind = "goal_stalled" // a goal made no progress (P1)
	KindVerifyFail  Kind = "verify_fail"  // auto-verify reported failures (P1)
)

// ItemState is the lifecycle state of an attention item. These are distinct on
// purpose (see design §2.2): being read aloud (announced) is not the same as
// the client confirming it spoke it (acked), which is not the same as the
// underlying request being answered (resolved).
type ItemState string

const (
	// StatePending: created, not yet delivered to a client.
	StatePending ItemState = "pending"
	// StateAnnounced: delivered to the active client to be spoken.
	StateAnnounced ItemState = "announced"
	// StateAcked: the client confirmed it relayed the item to the user. Stops
	// escalation/retry but does NOT resolve the underlying request.
	StateAcked ItemState = "acked"
	// StateResolved: the underlying permission/ask was answered (learned from
	// the bus), or the error cleared. Terminal.
	StateResolved ItemState = "resolved"
)

// RiskLevel is the conservative danger classification of a permission's command,
// computed by a DETERMINISTIC parser (never by an LLM). Over-classifies on
// doubt: a command we can't confidently parse is treated as at least medium.
type RiskLevel string

const (
	RiskLow    RiskLevel = "low"
	RiskMedium RiskLevel = "medium"
	RiskHigh   RiskLevel = "high"
)

// AttentionItem is one thing worth telling the user about. The Spoken field is
// already written for the ear. RefID carries moa's real pending ID (perm_%d /
// ask_%d) so the client can resolve it through the EXISTING HTTP endpoints —
// the Attention Service itself resolves nothing.
type AttentionItem struct {
	ID        string    `json:"id"`         // stable attention-item id (att_%d)
	Priority  Priority  `json:"priority"`   // P0..P3
	Kind      Kind      `json:"kind"`       // ask | permission | error | ...
	SessionID string    `json:"session_id"` // origin session
	Alias     string    `json:"alias"`      // pronounceable session name for speech
	Spoken    string    `json:"spoken"`     // text written for the ear
	State     ItemState `json:"state"`
	CreatedAt time.Time `json:"created_at"`

	// RefID is moa's real pending id for P0 ask/permission (perm_%d / ask_%d),
	// empty for errors. The client resolves via existing /ask & /permission
	// endpoints; resolution flows back through the bus, not through this service.
	RefID string `json:"ref_id,omitempty"`

	// Risk metadata — only meaningful for KindPermission. Populated by the
	// deterministic risk parser (assessRisk). Never softened by any model.
	RiskLevel RiskLevel `json:"risk_level,omitempty"`
	RiskFlags []string  `json:"risk_flags,omitempty"`
	// Verbatim is the exact command/args, ALWAYS captured even when not read
	// aloud, so a client can offer "read it to me literally".
	Verbatim string `json:"verbatim,omitempty"`
}

// clone returns a deep-enough copy for safe hand-off across the actor boundary.
// RiskFlags is the only slice; everything else is a value type.
func (it AttentionItem) clone() AttentionItem {
	if it.RiskFlags != nil {
		flags := make([]string, len(it.RiskFlags))
		copy(flags, it.RiskFlags)
		it.RiskFlags = flags
	}
	return it
}

// resolved reports whether the item is in its terminal state.
func (it AttentionItem) resolved() bool { return it.State == StateResolved }
