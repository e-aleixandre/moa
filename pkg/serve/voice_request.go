package serve

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/owner"
	"github.com/e-aleixandre/moa/pkg/push"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/voice"
)

// An owner asks to talk to the user (request_call). The policy lives here,
// in code: owners start at Never, a call never falls from a failed plan to a
// paid API, one request rings once, and whatever does not become a call
// becomes the owner's written question.
//
// Requests live in memory. A restart ends the owner's run that waits on one,
// exactly as it ends a pending ask_user, so there is nothing to rebuild.

// Call request states. The first five are live.
const (
	// crAdmitting holds the line for a placement whose Opened check has not
	// passed yet: nobody sees it, nobody can answer it, a withdraw ends it.
	crAdmitting  = "admitting"
	crRinging    = "ringing"    // announced; missed after the ring time
	crHeld       = "held"       // the user is on a call: waits for Call or Cancel
	crAccepted   = "accepted"   // Call chosen on a held request; the call is being switched
	crConnecting = "connecting" // answered; the call is opening
	crAnswered   = "answered"
	crWritten    = "written"
	crCancelled  = "cancelled"
)

// Why a request became the written question (also told to the owner).
const (
	whyNever        = "never"
	whyPaused       = "paused"
	whyOutsideHours = "outside_hours"
	whyNoPlan       = "no_plan"
	whyBusy         = "busy"
	whyMissed       = "missed"
	whyDeclined     = "declined"
	whyPlanFailed   = "plan_failed"
)

var whyText = map[string]string{
	whyNever:        "calls from you are turned off (Never)",
	whyPaused:       "the user paused calls",
	whyOutsideHours: "it is outside the hours the user allows calls",
	whyNoPlan:       "no voice plan is signed in and the user has not accepted the cost of the paid provider for your calls",
	whyBusy:         "the user is already on a call with you or another owner is calling",
	whyMissed:       "the user did not answer",
	whyDeclined:     "the user chose to answer in writing",
	whyPlanFailed:   "the voice plan failed when the user answered; calls never fall back to a paid API",
}

// callAcceptWindow bounds the switch from a held request to its call.
const callAcceptWindow = 30 * time.Second

type callRequest struct {
	ID        string     `json:"id"`
	SessionID string     `json:"session_id"`
	OwnerID   string     `json:"owner_id"`
	OwnerName string     `json:"owner_name"`
	Reason    string     `json:"reason"`
	Urgency   string     `json:"urgency"`
	Level     string     `json:"level"`
	State     string     `json:"state"`
	Created   time.Time  `json:"created"`
	Expires   *time.Time `json:"expires_at,omitempty"`
	// QuestionID is the open question this call escalates, if any: a client
	// shows that question as being called about. Additive.
	QuestionID string           `json:"question_id,omitempty"`
	provider   voice.ProviderID // fixed when asked: answering never picks another
	question   string           // the written fallback, what the call is about
	invite     string           // the iPhones' ringing invitation (Ring), if any
	why        string
	stop       func() bool
	// withdrawCtx ends, with errRequestWithdrawn, when the request is
	// withdrawn; withdraw ends it.
	withdrawCtx context.Context
	withdraw    context.CancelCauseFunc
	done        chan struct{} // closed once terminal
}

type callRequestRegistry struct {
	m *Manager
	// ringOverride replaces the configured ring time (tests).
	ringOverride time.Duration

	// announced, when set by a test, sees every request about to be
	// announced: what rings is counted there.
	announced func(req *callRequest)

	// clients is the presence of the clients on the requests feed: whether a
	// document that can show the incoming card is visible (see handleCallRequestsWS).
	clients presenceSet

	mu   sync.Mutex
	live map[string]*callRequest
	subs map[chan struct{}]struct{}
}

func (m *Manager) callRequests() *callRequestRegistry {
	m.callReqOnce.Do(func() {
		m.callReqs = &callRequestRegistry{m: m, live: map[string]*callRequest{}, subs: map[chan struct{}]struct{}{}}
	})
	return m.callReqs
}

func (r *callRequestRegistry) ringTime() time.Duration {
	if r.ringOverride > 0 {
		return r.ringOverride
	}
	if s := voiceSettings().Calls.RingSeconds; s > 0 {
		return time.Duration(s) * time.Second
	}
	return core.DefaultCallRingSeconds * time.Second
}

// callPlacement is one call an owner places: request_call, or the
// escalation of its own unanswered question.
type callPlacement struct {
	// ID is the request's id; empty mints one. An escalation passes the
	// attempt it committed before anything rings.
	ID        string
	SessionID string
	Owner     owner.Owner
	Reason    string
	Urgency   string
	// Question is what the call is about: request_call's written fallback,
	// or the escalated question itself.
	Question   string
	QuestionID string
	// Now is the time the policy is read at (allowed hours, pause).
	Now time.Time
	// Opened runs while the request holds the line but before anyone can
	// see or answer it; false withdraws it unannounced. A withdraw that lands
	// after Opened looked still finds the request (see admit).
	Opened func(req *callRequest) bool
}

// place is the shared call orchestration: it applies the policy, announces
// the request once and waits until it ends. ctx ending withdraws it at any
// point (Stop of the run, or of the session that asked). It returns the
// request and how it ended, or no request and why there was no call (then
// nothing was announced).
func (r *callRequestRegistry) place(ctx context.Context, p callPlacement) (req *callRequest, state, why string) {
	req, why = r.openFor(p)
	if req == nil {
		return nil, "", why
	}
	id := req.ID
	// Stopping withdraws the request at any point from here on, delivery and
	// opening included: a withdrawn request never becomes a call (see
	// callAnswer).
	stop := context.AfterFunc(ctx, func() {
		r.resolve(id, liveStates, crCancelled, "")
	})
	defer stop()
	if p.Opened != nil {
		r.admit(req, p.Opened(req))
	}
	if r.isState(id, crRinging) && r.announced != nil {
		r.announced(req)
	}
	r.announce(ctx, req)
	<-req.done
	state, why = r.outcome(req)
	return req, state, why
}

// withdraw ends a live request as cancelled (its question was answered,
// closed, or its session stopped). Unknown or ended ids are left alone.
func (r *callRequestRegistry) withdraw(id string) {
	if id != "" {
		r.resolve(id, liveStates, crCancelled, "")
	}
}

// liveStates are the states a withdraw ends.
var liveStates = []string{crAdmitting, crRinging, crHeld, crAccepted, crConnecting}

// admit ends a request's admission: ok makes it ring (or wait, held, if the
// user is on a call now), otherwise it is withdrawn. A request withdrawn
// while it was admitted is already gone and stays gone.
func (r *callRequestRegistry) admit(req *callRequest, ok bool) {
	if !ok {
		r.resolve(req.ID, []string{crAdmitting}, crCancelled, "")
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, live := r.live[req.ID]; !live || cur.State != crAdmitting {
		return
	}
	if onCall, _ := r.m.voiceCallState(req.SessionID); onCall {
		req.State = crHeld
	} else {
		req.State = crRinging
		r.arm(req, r.ringTime())
	}
	r.changedLocked()
}

// open applies the policy to a request_call at the current time.
func (r *callRequestRegistry) open(sessionID string, own owner.Owner, reason, urgency, question string) (*callRequest, string) {
	return r.openFor(callPlacement{SessionID: sessionID, Owner: own, Reason: reason, Urgency: urgency, Question: question})
}

// openFor applies the policy. It returns the live request, or why there is no
// call (then nothing was announced).
func (r *callRequestRegistry) openFor(p callPlacement) (*callRequest, string) {
	sessionID, own, reason, urgency, question := p.SessionID, p.Owner, p.Reason, p.Urgency, p.Question
	level := own.Calls()
	if level == owner.CallNever {
		return nil, whyNever
	}
	now := p.Now
	if now.IsZero() {
		now = time.Now()
	}
	if why := callWindowWhy(voiceSettings().Calls, now); why != "" {
		return nil, why
	}
	provider, ok := r.m.ownerCallProvider(own)
	if !ok {
		return nil, whyNoPlan
	}
	// Calls are looked at under r.mu, the lock callsEnded also holds when it
	// looks: a request held for a call that is ending is either seen by
	// callsEnded or not held at all.
	r.mu.Lock()
	defer r.mu.Unlock()
	onCall, withThisOwner := r.m.voiceCallState(sessionID)
	if withThisOwner || len(r.live) > 0 {
		return nil, whyBusy
	}
	id := p.ID
	if id == "" {
		id = newVoiceCallID()
	}
	req := &callRequest{
		ID: id, SessionID: sessionID, OwnerID: own.ID, OwnerName: own.Name,
		Reason: reason, Urgency: urgency, Level: level, State: crRinging, Created: now,
		QuestionID: p.QuestionID, provider: provider, question: question, done: make(chan struct{}),
	}
	req.withdrawCtx, req.withdraw = context.WithCancelCause(context.Background())
	r.live[req.ID] = req
	if p.Opened != nil {
		// Hold the line, unseen, until admit.
		req.State = crAdmitting
		return req, ""
	}
	if onCall {
		req.State = crHeld
	} else {
		r.arm(req, r.ringTime())
	}
	r.changedLocked()
	return req, ""
}

// arm (under mu) makes req missed after d unless it changes state first.
func (r *callRequestRegistry) arm(req *callRequest, d time.Duration) {
	if req.stop != nil {
		req.stop()
	}
	exp := time.Now().Add(d)
	req.Expires = &exp
	id, state := req.ID, req.State
	req.stop = time.AfterFunc(d, func() { r.resolve(id, []string{state}, crWritten, whyMissed) }).Stop
}

// resolve moves a live request from one of from to state. It is the only way
// out of a live state, under one lock: the first caller wins.
func (r *callRequestRegistry) resolve(id string, from []string, state, why string) (*callRequest, bool) {
	r.mu.Lock()
	req, ok := r.live[id]
	if !ok || !slices.Contains(from, req.State) {
		r.mu.Unlock()
		return nil, false
	}
	invite := r.moveLocked(req, state, why)
	r.mu.Unlock()
	if invite != "" {
		// No-op once the iPhone is answering: that answer claimed this
		// request through the invitation and lost or won on its own.
		if vr := r.m.voiceRegistry(); vr != nil {
			vr.CancelInvite(invite)
		}
	}
	return req, true
}

// moveLocked is every transition of a live request, under r.mu. It returns
// the invitation that must stop ringing, if any (cancel it after unlocking).
func (r *callRequestRegistry) moveLocked(req *callRequest, state, why string) string {
	invite := ""
	if req.State == crRinging {
		invite = req.invite
	}
	if req.stop != nil {
		req.stop()
		req.stop = nil
	}
	req.State, req.why, req.Expires = state, why, nil
	if state == crCancelled {
		req.withdraw(errRequestWithdrawn)
	}
	if state == crAnswered || state == crWritten || state == crCancelled {
		delete(r.live, req.ID)
		close(req.done)
	}
	r.changedLocked()
	return invite
}

// errRequestWithdrawn is why an opening that answers a withdrawn request
// stops: the owner's run was stopped.
var errRequestWithdrawn = errors.New("call request withdrawn")

// callAnswer ties a call being opened to the owner's request it answers.
// withdrawn ends when the request is withdrawn: the opening checks it before
// reaching the provider and its handshake is cancelled. publish is the one
// place the request's outcome is decided: under r.mu it registers the call
// and marks the request answered, or reports the request was withdrawn.
// Stop takes the same lock, so exactly one of them wins, and answered is
// never told before the call is registered.
type callAnswer struct {
	withdrawn context.Context
	publish   func(register func() bool) (registered, withdrawn bool)
}

func (r *callRequestRegistry) answering(req *callRequest) *callAnswer {
	return &callAnswer{
		withdrawn: req.withdrawCtx,
		publish: func(register func() bool) (bool, bool) {
			r.mu.Lock()
			defer r.mu.Unlock()
			if cur, ok := r.live[req.ID]; !ok || cur.State != crConnecting {
				return false, true
			}
			if !register() {
				return false, false
			}
			r.moveLocked(req, crAnswered, "")
			return true, false
		},
	}
}

// declined is an iPhone's Decline, decided before it is acknowledged: it
// reports false while the request is being answered elsewhere.
// The invitation is left to the Decline that asked (it ends it as declined).
func (r *callRequestRegistry) declined(req *callRequest) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.live[req.ID]; ok && cur.State == crRinging {
		r.moveLocked(req, crWritten, whyDeclined)
		return true
	}
	return req.State == crWritten || req.State == crCancelled
}

// outcome reads a request's final state and why.
func (r *callRequestRegistry) outcome(req *callRequest) (state, why string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return req.State, req.why
}

// announce tells the user a request rings. Ring rings the iPhones through
// CallKit when one can ring; otherwise, and for Ask first, it is the urgent
// call_request push (none while any client is in view). Open apps ring
// from the requests feed either way.
func (r *callRequestRegistry) announce(ctx context.Context, req *callRequest) {
	if !r.isState(req.ID, crRinging) {
		return
	}
	if req.Level == owner.CallRing && r.ringDevices(ctx, req) {
		return
	}
	sess, ok := r.m.Get(req.SessionID)
	if !ok || r.m.pushPolicy == nil {
		return
	}
	id := req.ID
	headline := req.OwnerName + " wants to talk"
	if req.Level == owner.CallRing {
		headline = req.OwnerName + " is calling"
	}
	r.m.pushPolicy.Handle(push.Signal{
		Kind: push.KindCallRequest, SessionID: req.SessionID, Title: sess.title(), Headline: headline,
		// Any visible client rings itself from its incoming card, whatever it
		// shows (a conversation, Tasks, the inbox): a system notification would
		// only duplicate it. Feed presence covers current clients; the
		// conversation presence covers a page still running an older bundle.
		RequestID: id, Watched: r.clients.watched() || r.m.anyViewerVisible(),
		StillPending: func() bool { return r.isState(id, crRinging) },
	})
}

// ringDevices rings the iPhones for req. The invitation and the request
// end as one: answering on an iPhone claims the request before the call
// opens and commits it before the call is published, and every other end
// of the invitation reaches the request synchronously through Done, so
// neither depends on the relay's acknowledgement. It reports whether an
// iPhone rings.
func (r *callRequestRegistry) ringDevices(ctx context.Context, req *callRequest) bool {
	vr := r.m.voiceRegistry()
	if vr == nil || !vr.CanRing() {
		return false
	}
	id := req.ID
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	inviteID, err := vr.RingDevices(ctx, req.SessionID, RingOptions{
		Name: req.OwnerName, Provider: string(req.provider), TTL: r.ringTime(), Intro: req.intro(),
		Claim:   func() bool { _, ok := r.resolve(id, []string{crRinging}, crConnecting, ""); return ok },
		Answer:  r.answering(req),
		Decline: func() bool { return r.declined(req) },
		Done: func(out InviteOutcome) {
			switch out.State {
			case inviteFailed:
				if out.Reason != inviteLostClaim {
					r.resolve(id, []string{crConnecting}, crWritten, whyPlanFailed)
				}
			case inviteDeclined:
				r.resolve(id, []string{crRinging}, crWritten, whyDeclined)
			case inviteExpired:
				r.resolve(id, []string{crRinging}, crWritten, whyMissed)
			}
		},
	})
	cancel()
	if err != nil {
		slog.Info("call request: no iPhone rang", "err", err)
		return false
	}
	// The request may have ended on another screen while the push was being
	// delivered: then this invitation must stop ringing. Cancelling an
	// invitation an iPhone already answered does nothing.
	r.mu.Lock()
	live := r.live[id]
	ringing := live != nil && live.State == crRinging
	if ringing {
		live.invite = inviteID
	}
	r.mu.Unlock()
	if !ringing {
		vr.CancelInvite(inviteID)
	}
	return true
}

func (r *callRequestRegistry) isState(id, state string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	req, ok := r.live[id]
	return ok && req.State == state
}

// claim takes a ringing or accepted request of sessionID for the call that
// answers it.
func (r *callRequestRegistry) claim(id, sessionID string) (*callRequest, bool) {
	r.mu.Lock()
	req, ok := r.live[id]
	r.mu.Unlock()
	if !ok || req.SessionID != sessionID {
		return nil, false
	}
	return r.resolve(id, []string{crRinging, crAccepted}, crConnecting, "")
}

// callsEnded writes every held request once no call is left: the user hung
// up without choosing.
func (r *callRequestRegistry) callsEnded() {
	r.mu.Lock()
	if active, _ := r.m.voiceCallState(""); active {
		r.mu.Unlock()
		return
	}
	var held []string
	for id, req := range r.live {
		if req.State == crHeld {
			held = append(held, id)
		}
	}
	r.mu.Unlock()
	for _, id := range held {
		r.resolve(id, []string{crHeld}, crWritten, whyMissed)
	}
}

// accept chooses Call on a held request.
func (r *callRequestRegistry) accept(id string) bool {
	if _, ok := r.resolve(id, []string{crHeld}, crAccepted, ""); !ok {
		return false
	}
	r.mu.Lock()
	if req, ok := r.live[id]; ok && req.State == crAccepted {
		r.arm(req, callAcceptWindow)
	}
	r.mu.Unlock()
	return true
}

func (r *callRequestRegistry) snapshot() []callRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]callRequest, 0, len(r.live))
	for _, req := range r.live {
		if req.State == crAdmitting {
			continue
		}
		c := *req
		c.done, c.stop = nil, nil
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

func (r *callRequestRegistry) changedLocked() {
	for ch := range r.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (r *callRequestRegistry) subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	r.mu.Lock()
	r.subs[ch] = struct{}{}
	r.mu.Unlock()
	return ch, func() {
		r.mu.Lock()
		delete(r.subs, ch)
		r.mu.Unlock()
	}
}

// callWindowWhy is why calls cannot be announced at now (paused, outside the
// allowed hours), or "".
func callWindowWhy(calls core.VoiceCallsConfig, now time.Time) string {
	if until, err := time.Parse(time.RFC3339, calls.PausedUntil); err == nil && now.Before(until) {
		return whyPaused
	}
	if !callHoursAllow(calls.Hours, now) {
		return whyOutsideHours
	}
	return ""
}

// nextCallWindow is the first instant at or after now when a call may be
// announced: after the pause, inside the allowed hours. The hours are read in
// their own zone day by day, so a daylight-saving change moves the instant
// with the wall clock; a start that falls in a skipped hour is normalized
// forward by time.Date.
func nextCallWindow(calls core.VoiceCallsConfig, now time.Time) time.Time {
	t := now
	if until, err := time.Parse(time.RFC3339, calls.PausedUntil); err == nil && t.Before(until) {
		t = until
	}
	if callHoursAllow(calls.Hours, t) {
		return t
	}
	h := calls.Hours
	from, _ := parseHHMM(h.From)
	loc, _ := time.LoadLocation(h.TZ)
	local := t.In(loc)
	for d := 0; d <= 2; d++ {
		start := time.Date(local.Year(), local.Month(), local.Day()+d, from/60, from%60, 0, 0, loc)
		if !start.Before(t) && callHoursAllow(h, start) {
			return start
		}
	}
	return t // unreachable with valid hours: callHoursAllow allows unreadable ones
}

// callHoursAllow reports whether now is inside the allowed daily window. nil
// allows any time; so does an unreadable window, so a broken setting never
// silences calls the user turned on (settings validate it on save).
func callHoursAllow(h *core.VoiceCallHours, now time.Time) bool {
	if h == nil {
		return true
	}
	from, ok1 := parseHHMM(h.From)
	until, ok2 := parseHHMM(h.Until)
	loc, err := time.LoadLocation(h.TZ)
	if !ok1 || !ok2 || err != nil || from == until {
		return true
	}
	t := now.In(loc)
	m := t.Hour()*60 + t.Minute()
	if from < until {
		return m >= from && m < until
	}
	return m >= from || m < until
}

func parseHHMM(s string) (int, bool) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

// voiceRegistry is the registry whose credentials decide availability.
func (m *Manager) voiceRegistry() *voiceCallRegistry {
	m.voiceCallsMu.Lock()
	defer m.voiceCallsMu.Unlock()
	if len(m.voiceCalls) == 0 {
		return nil
	}
	return m.voiceCalls[0]
}

func (m *Manager) voicePlanSignedIn() bool {
	r := m.voiceRegistry()
	if r == nil {
		return false
	}
	_, ok := r.defaultPlan()
	return ok
}

// ownerCallsPaidProvider is the paid provider owner calls would use without
// a plan: the one chosen in Settings, else the first with a key.
func (m *Manager) ownerCallsPaidProvider() voice.ProviderID {
	if p := voice.ProviderID(voiceSettings().PaidProvider); p.Paid() {
		return p
	}
	r := m.voiceRegistry()
	if r == nil {
		return ""
	}
	for _, p := range []voice.ProviderID{voice.XAIAPI, voice.OpenAILiveAPI} {
		if r.selectable(p) {
			return p
		}
	}
	return ""
}

// ownerCallProvider is the provider a call request of own uses: a plan when
// one is signed in, else the paid provider whose cost the user accepted for
// this owner, while it is still the chosen one and has a key (I21, I22).
func (m *Manager) ownerCallProvider(own owner.Owner) (voice.ProviderID, bool) {
	r := m.voiceRegistry()
	if r == nil {
		return "", false
	}
	if p, ok := r.defaultPlan(); ok {
		return p, true
	}
	paid := m.ownerCallsPaidProvider()
	if paid == "" || own.CallCostAck == nil || own.CallCostAck.Provider != string(paid) || !r.selectable(paid) {
		return "", false
	}
	return paid, true
}

// callLevelError is a rejected call_level, with a code clients act on (the
// provider is the paid one it concerns, if any). It is ErrInvalidCallLevel.
type callLevelError struct{ code, provider, msg string }

func (e *callLevelError) Error() string { return ErrInvalidCallLevel.Error() + ": " + e.msg }
func (e *callLevelError) Unwrap() error { return ErrInvalidCallLevel }

// setCallLevel validates a level change: without a plan, any level but
// never needs the cost of the current paid provider accepted (I22).
func (m *Manager) setCallLevel(own *owner.Owner, level string, ack *string) error {
	level = strings.TrimSpace(level)
	if !owner.ValidCallLevel(level) {
		return &callLevelError{code: "invalid_level", msg: "must be never, ask_first or ring"}
	}
	if level != owner.CallNever && !m.voicePlanSignedIn() {
		paid := m.ownerCallsPaidProvider()
		if paid == "" {
			return &callLevelError{code: "no_plan", msg: "no voice plan is signed in and no paid provider has a key"}
		}
		if r := m.voiceRegistry(); r == nil || !r.selectable(paid) {
			return &callLevelError{code: "api_key_required", provider: string(paid), msg: "no voice plan is signed in and " + string(paid) + " has no API key: add one under Providers"}
		}
		if ack == nil || strings.TrimSpace(*ack) != string(paid) {
			return &callLevelError{code: "cost_ack_required", provider: string(paid), msg: "accept the per-minute cost of " + string(paid) + " first (cost_ack_provider)"}
		}
		own.CallCostAck = &owner.CallCostAck{Provider: string(paid), At: time.Now().UTC()}
	}
	own.CallLevel = level
	return nil
}

// voiceCallState reports whether any call is active, and whether one is with
// sessionID.
func (m *Manager) voiceCallState(sessionID string) (active, with bool) {
	m.voiceCallsMu.Lock()
	regs := append([]*voiceCallRegistry(nil), m.voiceCalls...)
	m.voiceCallsMu.Unlock()
	for _, reg := range regs {
		reg.mu.Lock()
		for _, c := range reg.calls {
			active = true
			if c.sessionID == sessionID {
				with = true
			}
		}
		reg.mu.Unlock()
	}
	return active, with
}

// callingOwner is the owner whose own conversation sessionID is.
func (m *Manager) callingOwner(sessionID string) (owner.Owner, error) {
	sess, ok := m.Get(sessionID)
	if !ok || sess.Kind != session.KindOwner {
		return owner.Owner{}, errors.New("request_call is only available to project owners")
	}
	ref := m.ownerRefFor(sess.CWD)
	store, err := m.ownerStore()
	if err != nil || ref.id == "" {
		return owner.Owner{}, errors.New("this conversation has no owner")
	}
	own, found, err := store.FindByID(ref.id)
	if err != nil || !found || own.SessionID != sessionID {
		return owner.Owner{}, errors.New("request_call is only available in the owner's own conversation")
	}
	return own, nil
}
