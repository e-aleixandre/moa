// Package secretaction lets the owner hand a password to one exact action the
// agent prepared — a command on a PTY or a field in the browser — without the
// value ever reaching the model, the transcript, the bus or the disk.
//
// The request (what the card shows) is ordinary, non-secret metadata and may
// travel like any other pending approval. The answer does not: it goes from
// the HTTP handler straight into the waiting tool through an in-memory channel
// owned by the Broker, and the tool wipes it when its execution ends.
package secretaction

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// Request kinds.
const (
	KindCommand = "command"
	KindBrowser = "browser"
)

// Field kinds.
const (
	FieldPassword = "password"
	FieldUsername = "username"
	FieldText     = "text"
)

const (
	// MaxFields bounds the values one action may ask for.
	MaxFields = 4
	// MaxValueBytes bounds one value.
	MaxValueBytes = 16 << 10
)

var (
	ErrUnknownRequest = errors.New("unknown secret request")
	ErrMissingValue   = errors.New("a value is required for every field")
	ErrInvalidValue   = errors.New("invalid secret value")
)

// ValidFieldKind reports whether kind is one of the field kinds.
func ValidFieldKind(kind string) bool {
	return kind == FieldPassword || kind == FieldUsername || kind == FieldText
}

// Field is one value the action needs, as the card shows it. It never
// carries the value.
type Field struct {
	Index        int    `json:"index"`
	Label        string `json:"label"`
	Kind         string `json:"kind"`
	Alias        string `json:"alias,omitempty"`
	Element      string `json:"element,omitempty"`
	RealLabel    string `json:"realLabel,omitempty"`
	RealType     string `json:"realType,omitempty"`
	Autocomplete string `json:"autocomplete,omitempty"`
}

// Request is what the owner approves. Every field is fixed when the request
// is created, so what the card shows is what runs.
type Request struct {
	ID                string    `json:"id"`
	SessionID         string    `json:"sessionId"`
	Requester         string    `json:"requester"`
	Kind              string    `json:"kind"`
	Command           string    `json:"command,omitempty"`
	Cwd               string    `json:"cwd,omitempty"`
	SSHHost           string    `json:"sshHost,omitempty"`
	Origin            string    `json:"origin,omitempty"`
	Submit            bool      `json:"submit,omitempty"`
	ActionFingerprint string    `json:"actionFingerprint"`
	Fields            []Field   `json:"fields"`
	RequestedAt       time.Time `json:"requestedAt"`
}

// Answer is the owner's decision. Values are indexed like Request.Fields and
// belong to the receiver, which must Wipe them.
type Answer struct {
	Allow  bool
	Values [][]byte
}

// Wipe zeroes every value. Best effort: Go may have copied the bytes before.
func (a *Answer) Wipe() {
	for _, v := range a.Values {
		Wipe(v)
	}
	a.Values = nil
}

// Wipe zeroes b.
func Wipe(b []byte) {
	clear(b)
}

// Broker holds the pending requests of one session. It is the only path an
// answer takes: Resolve hands copies of the values to the goroutine blocked
// in Request through a buffered channel, and nothing else sees them.
type Broker struct {
	sessionID string

	mu         sync.Mutex
	pending    map[string]*pendingRequest
	onRequest  func(Request)
	onResolved func(id string, answered bool)
}

type pendingRequest struct {
	req      Request
	ctx      context.Context // the waiting tool's: once done, no answer is taken
	answer   chan Answer     // buffered(1): Resolve never blocks
	resolved bool
}

// NewBroker creates the broker of a session.
func NewBroker(sessionID string) *Broker {
	return &Broker{sessionID: sessionID, pending: make(map[string]*pendingRequest)}
}

// SetHooks installs the notifications of a request appearing and going away
// (answered reports an allow/deny rather than a cancellation). They receive
// no values. Either may be nil.
func (b *Broker) SetHooks(onRequest func(Request), onResolved func(id string, answered bool)) {
	b.mu.Lock()
	b.onRequest, b.onResolved = onRequest, onResolved
	b.mu.Unlock()
}

// Request publishes req and blocks until the owner answers or ctx ends. The
// returned values belong to the caller, who must Wipe the Answer.
func (b *Broker) Request(ctx context.Context, req Request) (Answer, error) {
	if err := ctx.Err(); err != nil {
		return Answer{}, err
	}
	id, err := newRequestID()
	if err != nil {
		return Answer{}, err
	}
	p := &pendingRequest{ctx: ctx, answer: make(chan Answer, 1)}
	b.mu.Lock()
	req.ID = id
	req.SessionID = b.sessionID
	req.RequestedAt = time.Now()
	req.Fields = append([]Field(nil), req.Fields...)
	for i := range req.Fields {
		req.Fields[i].Index = i
	}
	p.req = req
	b.pending[req.ID] = p
	onRequest := b.onRequest
	b.mu.Unlock()

	if onRequest != nil {
		onRequest(req)
	}

	select {
	case ans := <-p.answer:
		if err := ctx.Err(); err != nil {
			// Both were ready and the answer was picked: cancellation still
			// wins, the tool must not use the values.
			ans.Wipe()
			return Answer{}, err
		}
		return ans, nil
	case <-ctx.Done():
	}

	b.mu.Lock()
	answered := p.resolved
	if !answered {
		p.resolved = true
		delete(b.pending, req.ID)
	}
	onResolved := b.onResolved
	b.mu.Unlock()
	if answered {
		// Resolve won the race: its values are in the channel and nobody will
		// use them.
		ans := <-p.answer
		ans.Wipe()
		return Answer{}, ctx.Err()
	}
	if onResolved != nil {
		onResolved(req.ID, false)
	}
	return Answer{}, ctx.Err()
}

// Resolve answers a pending request. values are indexed like the request's
// fields; on allow every field needs a non-empty value. The broker copies
// them: the caller keeps (and must wipe) its own slices whatever the result.
func (b *Broker) Resolve(id string, allow bool, values [][]byte) error {
	b.mu.Lock()
	p, ok := b.pending[id]
	if !ok || p.resolved || p.ctx.Err() != nil {
		// A cancelled request is about to be removed by its Request.
		b.mu.Unlock()
		return ErrUnknownRequest
	}
	var ans Answer
	if allow {
		if len(values) != len(p.req.Fields) {
			b.mu.Unlock()
			return ErrMissingValue
		}
		for _, v := range values {
			if len(v) == 0 {
				b.mu.Unlock()
				return ErrMissingValue
			}
			if len(v) > MaxValueBytes {
				b.mu.Unlock()
				return ErrInvalidValue
			}
		}
		ans.Values = make([][]byte, len(values))
		for i, v := range values {
			ans.Values[i] = append(make([]byte, 0, len(v)), v...)
		}
	}
	ans.Allow = allow
	p.resolved = true
	delete(b.pending, id)
	onResolved := b.onResolved
	b.mu.Unlock()

	p.answer <- ans
	if onResolved != nil {
		onResolved(id, true)
	}
	return nil
}

// newRequestID returns an ID no other request of any session, broker or
// process will have: an approval names exactly one card, never a later one
// that happens to reuse a counter.
func newRequestID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("secret request id: %w", err)
	}
	return "sec_" + hex.EncodeToString(b[:]), nil
}

// Pending lists the open requests, oldest first.
func (b *Broker) Pending() []Request {
	b.mu.Lock()
	out := make([]Request, 0, len(b.pending))
	for _, p := range b.pending {
		out = append(out, p.req)
	}
	b.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].RequestedAt.Before(out[j].RequestedAt) })
	return out
}

// IsPending reports whether id is still waiting.
func (b *Broker) IsPending(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.pending[id]
	return ok && !p.resolved && p.ctx.Err() == nil
}

// Requester names who asks: "main" or "subagent · <job id>". Subagents run
// their tools under a context tagged with their job ID.
func Requester(ctx context.Context) string {
	if id := core.AgentIDFromContext(ctx); id != "" {
		return "subagent · " + id
	}
	return "main"
}
