// Package events is the wake-on-event inbox: external events (a mail reply, a
// CI failure) enter the server, are stored durably, and are either injected
// into the project's active session or wait in the inbox for the owner to
// decide. It owns the model and the store only; routing and injection live in
// pkg/serve.
package events

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

// Event states. An event has exactly one place: while `new` it is in the
// inbox, once `routed` it is the message in a session, and `dismissed` is the
// end of the line. There is no "read".
const (
	StateNew       = "new"
	StateRouted    = "routed"
	StateDismissed = "dismissed"
)

// Field limits. The body as a whole is capped by the HTTP layer; a single
// oversized field is still harmful on its own (a 1 MiB title would pollute
// every inbox render).
const (
	MaxSourceBytes  = 64
	MaxTitleBytes   = 200
	MaxBodyBytes    = 256 << 10
	MaxPayloadBytes = 64 << 10
	MaxKeyBytes     = 256
)

// Event is one external notification addressed to a project (a canonical cwd).
type Event struct {
	ID      string `json:"id"`
	Key     string `json:"key,omitempty"`     // idempotency key, e.g. "agentmail:<message_id>"
	Source  string `json:"source"`            // "agentmail" | "ci" | free-form label
	Project string `json:"project"`           // canonical cwd
	Title   string `json:"title"`
	Body    string `json:"body,omitempty"`
	// Payload is opaque caller data. It is stored and returned but never
	// injected into a session: it is bookkeeping for the emitter, not something
	// an agent should be asked to read.
	Payload  json.RawMessage `json:"payload,omitempty"`
	Created  time.Time       `json:"created"`
	State    string          `json:"state"`
	RoutedTo string          `json:"routed_to,omitempty"`
	RoutedAt time.Time       `json:"routed_at,omitzero"`
	// Suggested is the session the inbox would send this event to, computed at
	// ingress so the card can name it. Empty when the project had no candidate.
	Suggested string `json:"suggested,omitempty"`
}

// NewID mints an event identifier, using the same crypto/rand mechanism as
// core.NewMsgID.
func NewID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err == nil {
		return "ev_" + hex.EncodeToString(b)
	}
	return "ev_" + time.Now().UTC().Format("20060102150405.000000000")
}
