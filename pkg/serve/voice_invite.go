package serve

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/e-aleixandre/moa/pkg/push"
	"github.com/e-aleixandre/moa/pkg/voice"
)

// Incoming calls: an invitation rings the user's iPhones through their VoIP
// destinations; nothing reaches a voice provider until one of them answers,
// and then the call is opened by the same path as one the user places. The
// cancellation of a ringing call travels on the invitation's state (long
// poll), never on a second VoIP push (Apple forbids it).
//
// Invitations live in memory: a ring lasts seconds, and after a restart an
// unknown invitation answers 404, which the app ends as failed.
//
// Who may ring and when (owner policy, D4) is not decided here; RingDevices
// is the contract the caller of that policy uses.

const (
	voiceInviteTTL     = 45 * time.Second
	voiceInviteMaxTTL  = 2 * time.Minute
	voiceInviteRinging = 8  // at most this many invitations ring at once
	voiceInviteKept    = 64 // ended invitations kept for their status
	voiceInvitePoll    = 25 * time.Second
	voiceInviteNameMax = 80
)

// Invitation states. ringing → answering → answered | failed, or ringing →
// declined | cancelled | expired.
const (
	inviteRinging   = "ringing"
	inviteAnswering = "answering"
	inviteAnswered  = "answered"
	inviteFailed    = "failed" // answered, but the call could not be opened
	inviteDeclined  = "declined"
	inviteCancelled = "cancelled"
	inviteExpired   = "expired"
)

var (
	errNoIncomingDevice = errors.New("no device can receive incoming calls")
	// inviteLostClaim is the reason of an answer that lost its Claim.
	inviteLostClaim   = "answered_elsewhere"
	errTooManyRinging = errors.New("too many calls ringing")
	errInviteNotFound = errors.New("invitation not found")
)

// RingOptions shapes one invitation.
type RingOptions struct {
	Name     string        // shown by CallKit; the session title when empty
	Provider string        // the voice provider to open on answer; the default plan when empty
	TTL      time.Duration // how long it rings; voiceInviteTTL when zero
	// Intro is why moa calls: the voice speaks first and says it. nil is a
	// test call, and the voice says so.
	Intro *callIntro
	// Claim, if set, is asked once the user answers and before the call
	// opens; false makes the answer lose (answered_elsewhere): an owner's
	// call request answered on another screen.
	Claim func() bool
}

// InviteOutcome is how an invitation ended, for the caller that rang it.
type InviteOutcome struct {
	State  string // answered, failed, declined, cancelled or expired
	CallID string // the call opened on answer
	Reason string // why it failed, when it did
}

type voiceInvite struct {
	id, sessionID, name, provider string
	expires                       time.Time
	intro                         *callIntro // never nil: an incoming call always opens by saying why
	claim                         func() bool

	state    string
	answerer string         // principal that won the answer
	result   map[string]any // the answer's response, replayed to its winner
	status   int
	callID   string
	reason   string
	left     chan struct{} // closed when it stops ringing
	resolved chan struct{} // closed when it reaches a final state
	timer    *time.Timer
}

type voiceInvites struct {
	mu    sync.Mutex
	byID  map[string]*voiceInvite
	order []string // ended invitations, oldest first
}

func (r *voiceCallRegistry) inviteTable() *voiceInvites {
	r.invitesOnce.Do(func() { r.invites = &voiceInvites{byID: map[string]*voiceInvite{}} })
	return r.invites
}

// newInviteID is a random RFC 4122 v4 UUID: the app uses it as the CallKit
// call UUID, so every device rings the same call.
func newInviteID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// CanRing reports whether any device has an incoming-call destination; when
// none does, a caller falls back to a notification instead of ringing.
func (r *voiceCallRegistry) CanRing() bool {
	return r.mgr.pushVoIP != nil && len(r.mgr.pushVoIP.Store().All()) > 0
}

// RingDevices rings every paired device with an incoming-call destination
// for a call into sessionID. It returns once the relay took (or refused) the
// pushes; the invitation then rings until answered, declined, cancelled or
// expired: WaitInvite reports how it ended.
func (r *voiceCallRegistry) RingDevices(ctx context.Context, sessionID string, opts RingOptions) (string, error) {
	if !r.CanRing() {
		return "", errNoIncomingDevice
	}
	voip := r.mgr.pushVoIP
	sess, ok := r.mgr.Get(sessionID)
	if !ok {
		return "", fmt.Errorf("session %s: %w", sessionID, errInviteNotFound)
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = voiceInviteTTL
	}
	ttl = min(ttl, voiceInviteMaxTTL)
	name := opts.Name
	if name == "" {
		name = sess.title()
	}
	if name == "" {
		name = "moa"
	}
	inv := &voiceInvite{
		id: newInviteID(), sessionID: sess.ID, name: cutRunes(name, voiceInviteNameMax), provider: opts.Provider, intro: incomingIntro(opts.Intro), claim: opts.Claim,
		expires: time.Now().Add(ttl), state: inviteRinging,
		left: make(chan struct{}), resolved: make(chan struct{}),
	}
	t := r.inviteTable()
	t.mu.Lock()
	ringing := 0
	for _, other := range t.byID {
		if other.state == inviteRinging || other.state == inviteAnswering {
			ringing++
		}
	}
	if ringing >= voiceInviteRinging {
		t.mu.Unlock()
		return "", errTooManyRinging
	}
	t.byID[inv.id] = inv
	inv.timer = time.AfterFunc(ttl, func() { r.endInvite(inv.id, inviteRinging, inviteExpired) })
	t.mu.Unlock()

	delivered := voip.Deliver(ctx, push.Notification{
		Title: inv.name, SessionID: inv.sessionID, Kind: push.KindCall, Level: push.LevelUrgent,
		Call: &push.CallInvite{ID: inv.id, Expires: inv.expires},
	})
	if delivered == 0 {
		r.endInvite(inv.id, inviteRinging, inviteCancelled)
		return "", errNoIncomingDevice
	}
	slog.Info("voice invite ringing", "invite", inv.id, "session", inv.sessionID, "devices", delivered)
	return inv.id, nil
}

func cutRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// CancelInvite stops an invitation that is still ringing. The devices learn
// it from the invitation's state.
func (r *voiceCallRegistry) CancelInvite(id string) {
	r.endInvite(id, inviteRinging, inviteCancelled)
}

// WaitInvite blocks until the invitation reaches a final state or ctx ends.
func (r *voiceCallRegistry) WaitInvite(ctx context.Context, id string) (InviteOutcome, error) {
	t := r.inviteTable()
	t.mu.Lock()
	inv, ok := t.byID[id]
	t.mu.Unlock()
	if !ok {
		return InviteOutcome{}, errInviteNotFound
	}
	select {
	case <-inv.resolved:
	case <-ctx.Done():
		return InviteOutcome{}, ctx.Err()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return InviteOutcome{State: inv.state, CallID: inv.callID, Reason: inv.reason}, nil
}

// endInvite moves an invitation from state from to state to, once.
func (r *voiceCallRegistry) endInvite(id, from, to string) bool {
	t := r.inviteTable()
	t.mu.Lock()
	defer t.mu.Unlock()
	inv, ok := t.byID[id]
	if !ok || inv.state != from {
		return false
	}
	inv.state = to
	if from == inviteRinging {
		close(inv.left)
	}
	if to != inviteAnswering {
		inv.timer.Stop()
		close(inv.resolved)
		t.retire(id)
		slog.Info("voice invite ended", "invite", id, "state", to)
	}
	return true
}

// retire keeps an ended invitation answering its status, bounded.
func (t *voiceInvites) retire(id string) {
	t.order = append(t.order, id)
	for len(t.order) > voiceInviteKept {
		delete(t.byID, t.order[0])
		t.order = t.order[1:]
	}
}

func (r *voiceCallRegistry) invite(id string) (*voiceInvite, bool) {
	t := r.inviteTable()
	t.mu.Lock()
	defer t.mu.Unlock()
	inv, ok := t.byID[id]
	return inv, ok
}

// answer gives the call to the first principal that answers. The winner
// answering again (a retry) gets the same result; anyone else gets 409.
func (r *voiceCallRegistry) answer(scope context.Context, id, principal string) (int, map[string]any) {
	t := r.inviteTable()
	t.mu.Lock()
	inv, ok := t.byID[id]
	if !ok {
		t.mu.Unlock()
		return http.StatusNotFound, map[string]any{"error": "invite_not_found"}
	}
	switch {
	case inv.state == inviteRinging && time.Now().Before(inv.expires):
		inv.state, inv.answerer = inviteAnswering, principal
		close(inv.left)
		inv.timer.Stop()
	case inv.state == inviteRinging:
		t.mu.Unlock()
		return http.StatusConflict, map[string]any{"error": "invite_expired", "state": inviteExpired}
	case (inv.state == inviteAnswering || inv.state == inviteAnswered || inv.state == inviteFailed) && inv.answerer == principal:
		t.mu.Unlock()
		<-inv.resolved
		t.mu.Lock()
		defer t.mu.Unlock()
		return inv.status, inv.result
	case inv.state == inviteAnswering || inv.state == inviteAnswered || inv.state == inviteFailed:
		t.mu.Unlock()
		return http.StatusConflict, map[string]any{"error": "answered_elsewhere"}
	default:
		state := inv.state
		t.mu.Unlock()
		return http.StatusConflict, map[string]any{"error": "invite_" + state, "state": state}
	}
	t.mu.Unlock()

	if inv.claim != nil && !inv.claim() {
		t.mu.Lock()
		defer t.mu.Unlock()
		inv.state, inv.reason = inviteFailed, inviteLostClaim
		inv.status, inv.result = http.StatusConflict, map[string]any{"error": "answered_elsewhere"}
		close(inv.resolved)
		t.retire(id)
		return inv.status, inv.result
	}
	status, out, _ := r.openCall(scope, principal, inv.sessionID, inv.provider, inv.intro)
	if inv.claim != nil {
		delete(out, "paid_option") // an owner's call never offers the paid API (I21)
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	inv.status, inv.result = status, out
	if status == http.StatusCreated {
		inv.state = inviteAnswered
		inv.callID, _ = out["call_id"].(string)
	} else {
		inv.state = inviteFailed
		inv.reason, _ = out["error"].(string)
	}
	close(inv.resolved)
	t.retire(id)
	slog.Info("voice invite answered", "invite", id, "state", inv.state, "reason", inv.reason)
	return status, out
}

func (r *voiceCallRegistry) inviteStatus(inv *voiceInvite, principal string) map[string]any {
	t := r.inviteTable()
	t.mu.Lock()
	defer t.mu.Unlock()
	out := map[string]any{"id": inv.id, "state": inv.state, "session_id": inv.sessionID, "expires_at": inv.expires.Unix()}
	if inv.answerer != "" {
		out["answered_here"] = inv.answerer == principal
	}
	if inv.answerer == principal && inv.callID != "" {
		out["call_id"] = inv.callID
	}
	return out
}

// handleInviteCreate is POST /api/voice/invites: the owner rings their own
// devices, e.g. a test call. Owners ringing by themselves go through
// RingDevices from their own policy, not this route.
func (r *voiceCallRegistry) handleInviteCreate(w http.ResponseWriter, req *http.Request) {
	var in struct {
		SessionID string `json:"session_id"`
		Name      string `json:"name"`
		// Reason, optional, is what the voice opens the test call with.
		Reason   string `json:"reason"`
		Provider string `json:"provider"`
	}
	if err := json.NewDecoder(io.LimitReader(req.Body, voice.MaxControlJSON)).Decode(&in); err != nil || in.SessionID == "" {
		voiceError(w, http.StatusBadRequest, map[string]any{"error": "invalid_request"})
		return
	}
	if in.Provider != "" {
		if _, _, ok := r.adapter(voice.ProviderID(in.Provider)); !ok {
			voiceError(w, http.StatusBadRequest, map[string]any{"error": "unknown_provider"})
			return
		}
	}
	id, err := r.RingDevices(req.Context(), in.SessionID, RingOptions{Name: in.Name, Provider: in.Provider, Intro: testIntro(in.Reason)})
	switch {
	case errors.Is(err, errInviteNotFound):
		voiceError(w, http.StatusNotFound, map[string]any{"error": "session_not_found"})
	case errors.Is(err, errNoIncomingDevice):
		voiceError(w, http.StatusConflict, map[string]any{"error": "no_incoming_device"})
	case errors.Is(err, errTooManyRinging):
		w.Header().Set("Retry-After", strconv.Itoa(int(voiceInviteTTL/time.Second)))
		voiceError(w, http.StatusTooManyRequests, map[string]any{"error": "too_many_ringing"})
	case err != nil:
		voiceError(w, http.StatusInternalServerError, map[string]any{"error": "ring_failed"})
	default:
		inv, _ := r.invite(id)
		identity, _ := requestAuthIdentity(req)
		voiceError(w, http.StatusCreated, r.inviteStatus(inv, identity.auditID()))
	}
}

// handleInviteStatus is GET /api/voice/invites/{id}. With ?wait=ringing it
// holds the answer while the invitation still rings (bounded): the ringing
// device's signal that it was answered elsewhere, declined or cancelled.
func (r *voiceCallRegistry) handleInviteStatus(w http.ResponseWriter, req *http.Request) {
	identity, ok := requestAuthIdentity(req)
	if !ok {
		voiceError(w, http.StatusUnauthorized, map[string]any{"error": "unauthenticated"})
		return
	}
	inv, ok := r.invite(req.PathValue("id"))
	if !ok {
		voiceError(w, http.StatusNotFound, map[string]any{"error": "invite_not_found"})
		return
	}
	if req.URL.Query().Get("wait") == inviteRinging {
		// The device's lease ends the wait when its access is withdrawn.
		withdrawn := make(chan struct{})
		var once sync.Once
		lease, err := deviceLeaseForWebSocket(req, func(string) { once.Do(func() { close(withdrawn) }) })
		if err != nil {
			voiceError(w, http.StatusUnauthorized, map[string]any{"error": "unauthenticated"})
			return
		}
		defer lease.release()
		select {
		case <-inv.left:
		case <-time.After(voiceInvitePoll):
		case <-withdrawn:
		case <-req.Context().Done():
			return
		}
		if deviceLeaseClosed(lease) {
			voiceError(w, http.StatusUnauthorized, map[string]any{"error": "unauthenticated"})
			return
		}
	}
	voiceError(w, http.StatusOK, r.inviteStatus(inv, identity.auditID()))
}

// handleInviteAnswer is POST /api/voice/invites/{id}/answer.
func (r *voiceCallRegistry) handleInviteAnswer(w http.ResponseWriter, req *http.Request) {
	identity, ok := requestAuthIdentity(req)
	if !ok {
		voiceError(w, http.StatusUnauthorized, map[string]any{"error": "unauthenticated"})
		return
	}
	// The answer, and the call it opens, live under the device's lease:
	// revoking the device, or withdrawing the VoIP destination of a
	// Moa-Voice answer, aborts the opening or ends the call. The call's
	// end releases it.
	scope, cancel := context.WithCancel(r.ctx)
	lease, err := deviceLeaseForWebSocket(req, func(string) { cancel() })
	if err != nil {
		cancel()
		voiceError(w, http.StatusUnauthorized, map[string]any{"error": "unauthenticated"})
		return
	}
	status, out := r.answer(scope, req.PathValue("id"), identity.auditID())
	var c *voiceCall
	if id, _ := out["call_id"].(string); status == http.StatusCreated {
		c, _, _ = r.lookup(id)
	}
	if c == nil || lease == nil {
		lease.release()
		cancel()
	} else {
		stop := context.AfterFunc(scope, func() {
			if r.ctx.Err() == nil { // not a server shutdown
				c.requestEnd("device_revoked")
			}
		})
		go func() {
			<-c.done
			stop()
			lease.release()
			cancel()
		}()
	}
	voiceError(w, status, out)
}

// handleInviteDecline is POST /api/voice/invites/{id}/decline. Declining one
// that is already declined is a success.
func (r *voiceCallRegistry) handleInviteDecline(w http.ResponseWriter, req *http.Request) {
	id := req.PathValue("id")
	inv, ok := r.invite(id)
	if !ok {
		voiceError(w, http.StatusNotFound, map[string]any{"error": "invite_not_found"})
		return
	}
	if r.endInvite(id, inviteRinging, inviteDeclined) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	identity, _ := requestAuthIdentity(req)
	st := r.inviteStatus(inv, identity.auditID())
	if st["state"] == inviteDeclined {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	voiceError(w, http.StatusConflict, map[string]any{"error": "invite_" + st["state"].(string), "state": st["state"]})
}
