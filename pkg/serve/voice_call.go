package serve

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/voice"
)

// voiceOpenTimeout bounds creating an upstream call (handshake, refresh and,
// for ChatGPT, ICE/DTLS).
const voiceOpenTimeout = 20 * time.Second

// voiceTombstones bounds how many ended call IDs keep answering hangup.
const voiceTombstones = 256

// voiceCallRegistry owns every voice call of one server. It is closed by
// Manager.Shutdown, so no call outlives the sessions it records into.
type voiceCallRegistry struct {
	mgr   *Manager
	store *auth.Store
	opts  VoiceCallOptions
	// admission is the open-attempt budget shared with Live voice's rules:
	// in-flight and global attempts count failures, the principal's
	// allowance only counts calls that opened.
	admission *voiceOpenBudget

	ctx    context.Context // cancelled on Close: aborts opens in flight
	cancel context.CancelFunc

	mu         sync.Mutex
	closed     bool
	principals map[string]*voiceSlot              // one reservation per principal
	calls      map[string]*voiceCall              // active calls by ID
	plans      map[string]auth.CredentialSnapshot // plan sign-in each ChatGPT-plan call opened with
	models     map[string]core.Model              // backend model each call opened with, kept until it finishes
	ended      map[string]string                  // tombstone: call ID → principal
	endedOrder []string
	recent     []voiceUsage   // usage of the last calls, newest last, bounded
	wg         sync.WaitGroup // reservations and controllers

	invitesOnce sync.Once
	invites     *voiceInvites // incoming-call invitations (voice_invite.go)
}

func newVoiceCallRegistry(mgr *Manager, store *auth.Store, opts VoiceCallOptions) *voiceCallRegistry {
	opts.Limits = opts.Limits.withDefaults()
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &voiceCallRegistry{mgr: mgr, store: store, opts: opts, admission: newVoiceOpenBudget(), ctx: ctx, cancel: cancel,
		principals: map[string]*voiceSlot{}, calls: map[string]*voiceCall{}, plans: map[string]auth.CredentialSnapshot{}, models: map[string]core.Model{}, ended: map[string]string{}}
}

// voiceSlot is a principal's reservation. tearingDown is closed once its
// call has started closing the upstream; released when the slot is free.
type voiceSlot struct {
	tearingDown chan struct{}
	released    chan struct{}
	downOnce    sync.Once
}

func (s *voiceSlot) markTearingDown() { s.downOnce.Do(func() { close(s.tearingDown) }) }

// reserve takes the principal's single call slot before any upstream is
// opened, so two concurrent creates cannot both reach a provider. A slot
// whose call is already closing its upstream is waited for, bounded by the
// teardown budget, so redialing right after a drop is not refused.
func (r *voiceCallRegistry) reserve(principal string) (slot *voiceSlot, closed bool) {
	for attempt := 0; ; attempt++ {
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			return nil, true
		}
		cur := r.principals[principal]
		if cur == nil {
			slot = &voiceSlot{tearingDown: make(chan struct{}), released: make(chan struct{})}
			r.principals[principal] = slot
			r.wg.Add(1)
			r.mu.Unlock()
			return slot, false
		}
		r.mu.Unlock()
		if attempt > 0 {
			return nil, false
		}
		select {
		case <-cur.tearingDown:
		default:
			return nil, false
		}
		select {
		case <-cur.released:
		case <-time.After(r.opts.Limits.Teardown):
			return nil, false
		}
	}
}

func (r *voiceCallRegistry) freeSlot(principal string, slot *voiceSlot) {
	r.mu.Lock()
	if r.principals[principal] == slot {
		delete(r.principals, principal)
		close(slot.released)
	}
	r.mu.Unlock()
}

func (r *voiceCallRegistry) release(principal string, slot *voiceSlot) {
	r.freeSlot(principal, slot)
	r.wg.Done()
}

// register publishes an opened call with the plan sign-in it was opened
// with, if any. It fails when the registry closed while the upstream was
// opening; the caller then closes that upstream.
func (r *voiceCallRegistry) register(c *voiceCall, plan *auth.CredentialSnapshot, model core.Model) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	r.calls[c.id] = c
	r.models[c.id] = model
	if plan != nil {
		r.plans[c.id] = *plan
	}
	return true
}

// openedModel is the backend model call id opened with.
func (r *voiceCallRegistry) openedModel(id string) (core.Model, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.models[id]
	return m, ok
}

// openedPlan is the plan sign-in call id was opened with.
func (r *voiceCallRegistry) openedPlan(id string) (auth.CredentialSnapshot, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	plan, ok := r.plans[id]
	return plan, ok
}

// releasePrincipal frees the principal's slot once the upstream is closed:
// a new call may start while this one still writes its minutes.
func (r *voiceCallRegistry) releasePrincipal(c *voiceCall) {
	r.freeSlot(c.principal, c.slot)
}

// finish retires an ended call: tombstone and wait group.
func (r *voiceCallRegistry) finish(c *voiceCall) {
	r.mu.Lock()
	delete(r.calls, c.id)
	delete(r.plans, c.id)
	delete(r.models, c.id)
	r.ended[c.id] = c.principal
	r.endedOrder = append(r.endedOrder, c.id)
	if len(r.endedOrder) > voiceTombstones {
		delete(r.ended, r.endedOrder[0])
		r.endedOrder = r.endedOrder[1:]
	}
	r.mu.Unlock()
	r.mgr.callRequests().callsEnded()
	r.wg.Done()
}

func (r *voiceCallRegistry) lookup(id string) (c *voiceCall, principal string, found bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.calls[id]; ok {
		return c, c.principal, true
	}
	p, ok := r.ended[id]
	return nil, p, ok
}

// Close ends every call without reconnection grace and waits, bounded by
// the minutes and teardown budgets, for them to finish.
func (r *voiceCallRegistry) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	calls := make([]*voiceCall, 0, len(r.calls))
	for _, c := range r.calls {
		calls = append(calls, c)
	}
	r.mu.Unlock()
	r.cancel()
	for _, c := range calls {
		c.requestEnd("shutdown")
	}
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(r.opts.Limits.Minutes + r.opts.Limits.Teardown + 2*time.Second):
		slog.Warn("voice calls did not finish before shutdown")
	}
}

func newVoiceCallID() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func voiceError(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, body)
}

// handleCreate is POST /api/voice/calls.
func (r *voiceCallRegistry) handleCreate(w http.ResponseWriter, req *http.Request) {
	identity, ok := requestAuthIdentity(req)
	if !ok {
		voiceError(w, http.StatusUnauthorized, map[string]any{"error": "unauthenticated"})
		return
	}
	var in struct {
		SessionID string `json:"session_id"`
		Provider  string `json:"provider"`
		Note      string `json:"note"`
		// CallRequestID answers an owner's request_call: its provider and
		// reason are fixed by the request, never chosen here.
		CallRequestID string `json:"call_request_id"`
	}
	if err := json.NewDecoder(io.LimitReader(req.Body, voice.MaxControlJSON)).Decode(&in); err != nil {
		voiceError(w, http.StatusBadRequest, map[string]any{"error": "invalid_request"})
		return
	}
	var intro *callIntro
	var answering *callRequest
	if in.CallRequestID != "" {
		req, ok := r.mgr.callRequests().claim(in.CallRequestID, in.SessionID)
		if !ok {
			voiceError(w, http.StatusConflict, map[string]any{"error": "request_not_live"})
			return
		}
		answering, in.Provider = req, string(req.provider)
		intro = req.intro()
	}
	var answer *callAnswer
	if answering != nil {
		answer = r.mgr.callRequests().answering(answering)
	}
	status, out, retry := r.openCall(r.ctx, identity.auditID(), in.SessionID, in.Provider, intro, answer)
	if answering != nil && status != http.StatusCreated {
		// An owner's call is the plan it was asked with or nothing: any
		// failure makes it the written question and offers no paid API (I21).
		r.mgr.callRequests().resolve(answering.ID, []string{crConnecting}, crWritten, whyPlanFailed)
		delete(out, "paid_option")
	}
	if retry > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
	}
	voiceError(w, status, out)
}

// openCall opens a call for principal into a session: the one path behind
// both a call the user places and an incoming invitation they answer. It
// answers the HTTP status and body to send, and a Retry-After when limited.
// Ending scope (r.ctx or narrower) aborts an opening that has not answered.
// intro and answer belong to calls moa places (see callAnswer).
func (r *voiceCallRegistry) openCall(scope context.Context, principal, sessionID, providerName string, intro *callIntro, answer *callAnswer) (int, map[string]any, int) {
	if _, ok := r.mgr.Get(sessionID); !ok {
		return http.StatusNotFound, map[string]any{"error": "session_not_found"}, 0
	}
	return r.openCallWith(scope, principal, providerName, intro, answer, func() (*ManagedSession, int, map[string]any) {
		if sess, ok := r.mgr.Get(sessionID); ok {
			return sess, 0, nil
		}
		return nil, http.StatusNotFound, map[string]any{"error": "session_not_found"}
	})
}

// openCallWith is openCall with the session resolved only once the plan,
// the admission budget and the principal's slot have all said yes: a
// resolver with side effects (reopening an owner) runs for admitted calls
// and never for refused ones.
func (r *voiceCallRegistry) openCallWith(scope context.Context, principal, providerName string, intro *callIntro, answer *callAnswer, resolve func() (*ManagedSession, int, map[string]any)) (int, map[string]any, int) {
	if answer != nil {
		ctx, cancel := context.WithCancelCause(scope)
		stop := context.AfterFunc(answer.withdrawn, func() { cancel(errRequestWithdrawn) })
		defer stop()
		defer cancel(nil)
		scope = ctx
	}
	withdrawn := func() bool { return errors.Is(context.Cause(scope), errRequestWithdrawn) }
	if withdrawn() {
		return http.StatusConflict, map[string]any{"error": "request_withdrawn"}, 0
	}
	provider := voice.ProviderID(providerName)
	if provider == "" {
		p, found := r.defaultPlan()
		if !found {
			return http.StatusServiceUnavailable, r.planUnavailable("", "no_plan"), 0
		}
		provider = p
	}
	adapter, creds, ok := r.adapter(provider)
	if !ok {
		return http.StatusBadRequest, map[string]any{"error": "unknown_provider"}, 0
	}
	retry, admitted := r.admission.acquire(principal)
	if !admitted {
		return http.StatusTooManyRequests, map[string]any{"error": "rate_limited"}, retry
	}
	defer r.admission.release()
	slot, closed := r.reserve(principal)
	if closed {
		return http.StatusServiceUnavailable, map[string]any{"error": "shutting_down"}, 0
	}
	if slot == nil {
		return http.StatusConflict, map[string]any{"error": "call_active"}, 0
	}
	sess, status, out := resolve()
	if sess == nil {
		r.release(principal, slot)
		return status, out, 0
	}
	if withdrawn() {
		r.release(principal, slot)
		return http.StatusConflict, map[string]any{"error": "request_withdrawn"}, 0
	}
	id := newVoiceCallID()
	// Read once: the call keeps this model to its end, whatever Settings says later.
	model := voiceModel()
	ctx, cancel := context.WithTimeout(scope, voiceOpenTimeout)
	up, err := adapter.Open(ctx, voice.CallSpec{
		CallID: id, SessionID: sess.ID, Voice: voiceProviders[provider].voice,
		Instructions: voice.CommonInstructions, BackendModel: model, Tools: voice.ToolSpecs(), Opening: intro.opening(),
		InitialContext: intro.context(provider == voice.ChatGPTPlan) + r.mgr.voiceFallbackContext(sess.ID, r.opts.Limits.BriefChars),
	})
	cancel()
	if err == nil && scope.Err() != nil && r.ctx.Err() == nil {
		ctx, cancel := context.WithTimeout(context.Background(), r.opts.Limits.Teardown)
		_ = up.Close(ctx, "unauthorized")
		cancel()
		err = scope.Err()
	}
	if err != nil && withdrawn() {
		r.release(principal, slot)
		return http.StatusConflict, map[string]any{"error": "request_withdrawn"}, 0
	}
	if err != nil && scope.Err() != nil && r.ctx.Err() == nil {
		r.release(principal, slot)
		return http.StatusUnauthorized, map[string]any{"error": "unauthenticated"}, 0
	}
	if err != nil {
		r.release(principal, slot)
		oe := voice.Classify(err)
		slog.Info("voice call upstream refused", "provider", provider, "class", oe.Class, "status", oe.Status)
		if provider.Paid() {
			return http.StatusBadGateway, map[string]any{"error": "provider_unavailable", "reason": oe.Class}, 0
		}
		return http.StatusServiceUnavailable, r.planUnavailable(provider, oe.Class), 0
	}
	c := newVoiceCall(r, id, principal, sess.ID, provider, adapter.Billing(), up)
	c.costStart = sessionCostUSD(sess)
	c.slot = slot
	var plan *auth.CredentialSnapshot
	if provider == voice.ChatGPTPlan {
		opened := creds.opened()
		plan = &opened
	}
	registered, lost := false, false
	if answer != nil {
		registered, lost = answer.publish(func() bool { return r.register(c, plan, model) })
	} else {
		registered = r.register(c, plan, model)
	}
	if lost {
		// The request this call answers was withdrawn (Stop) while it
		// opened: it never becomes a call.
		ctx, cancel := context.WithTimeout(context.Background(), r.opts.Limits.Teardown)
		_ = up.Close(ctx, "withdrawn")
		cancel()
		r.release(principal, slot)
		return http.StatusConflict, map[string]any{"error": "request_withdrawn"}, 0
	}
	if !registered {
		ctx, cancel := context.WithTimeout(context.Background(), r.opts.Limits.Teardown)
		_ = up.Close(ctx, "shutdown")
		cancel()
		r.release(principal, slot)
		return http.StatusServiceUnavailable, map[string]any{"error": "shutting_down"}, 0
	}
	r.admission.established(principal)
	go c.run()
	return http.StatusCreated, map[string]any{"call_id": id, "provider": provider, "billing": c.billing, "ws_url": "/api/voice/calls/" + id + "/ws"}, 0
}

// planUnavailable never starts the paid alternative: it only names it when
// its key is selectable, for an explicit second action by the user.
func (r *voiceCallRegistry) planUnavailable(plan voice.ProviderID, reason string) map[string]any {
	out := map[string]any{"error": "plan_unavailable", "reason": reason}
	candidates := []voice.ProviderID{voice.XAIAPI, voice.OpenAILiveAPI}
	if paid, ok := pairedPaid[plan]; ok {
		candidates = []voice.ProviderID{paid}
	}
	// The paid provider chosen in Settings is offered first; it is still
	// only an offer for the user's explicit tap.
	if chosen := voice.ProviderID(voiceSettings().PaidProvider); chosen.Paid() {
		candidates = append([]voice.ProviderID{chosen}, candidates...)
	}
	for _, p := range candidates {
		if r.selectable(p) {
			out["paid_option"] = map[string]any{"provider": p, "billing": voiceProviders[p].billing}
			break
		}
	}
	return out
}

// handleHangup is POST /api/voice/calls/{id}/hangup. It is idempotent and
// waits for the call to finish, so its minutes are durable on return.
func (r *voiceCallRegistry) handleHangup(w http.ResponseWriter, req *http.Request) {
	identity, ok := requestAuthIdentity(req)
	if !ok {
		voiceError(w, http.StatusUnauthorized, map[string]any{"error": "unauthenticated"})
		return
	}
	c, principal, found := r.lookup(req.PathValue("id"))
	if !found {
		voiceError(w, http.StatusNotFound, map[string]any{"error": "call_not_found"})
		return
	}
	if principal != identity.auditID() {
		voiceError(w, http.StatusForbidden, map[string]any{"error": "not_your_call"})
		return
	}
	if c != nil {
		c.requestEnd("hangup")
		select {
		case <-c.done:
		case <-time.After(r.opts.Limits.Minutes + r.opts.Limits.Teardown + 5*time.Second):
		case <-req.Context().Done():
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// sameOrigin reports whether a browser Origin names this very host.
func sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && strings.EqualFold(u.Host, host)
}

// handleWS is GET /api/voice/calls/{id}/ws. Every check happens before the
// upgrade: a browser cookie needs this server's Origin (cross-site pages can
// open WebSockets with cookies), only a native header credential may come
// without one, and the call must belong to the caller's principal.
func (r *voiceCallRegistry) handleWS(w http.ResponseWriter, req *http.Request) {
	callID := req.PathValue("id")
	// client_note is a short, client-chosen diagnostic (e.g. why the iOS app
	// handed the call to the page); it is only logged.
	note := voiceClientNote(req.URL.Query().Get("client_note"))
	identity, ok := requestAuthIdentity(req)
	refuse := func(status int, why string) {
		slog.Info("voice call socket refused", "call", callID, "status", status, "why", why, "header_auth", identity.Header, "note", note)
		http.Error(w, why, status)
	}
	if !ok {
		refuse(http.StatusUnauthorized, "unauthenticated")
		return
	}
	if !identity.Header && !sameOrigin(req.Header.Get("Origin"), req.Host) {
		refuse(http.StatusForbidden, "same-origin upgrade required")
		return
	}
	c, principal, found := r.lookup(callID)
	if !found || c == nil {
		refuse(http.StatusNotFound, "call not found")
		return
	}
	if principal != identity.auditID() {
		refuse(http.StatusForbidden, "not your call")
		return
	}
	lease, err := deviceLeaseForWebSocket(req, func(string) { c.requestEnd("device_revoked") })
	if err != nil {
		refuse(http.StatusServiceUnavailable, "device unavailable")
		return
	}
	if lease != nil {
		defer lease.release()
	}
	conn, err := websocket.Accept(w, req, wsAcceptOptions())
	if err != nil {
		slog.Info("voice call socket upgrade failed", "call", callID, "err", err, "note", note)
		return
	}
	conn.SetReadLimit(voice.MaxControlJSON)
	cl := newVoiceClient(conn, r.opts.Limits.BufferedBytes)
	defer cl.close()
	if !c.attach(cl) {
		cl.finish(map[string]any{"type": "ended", "reason": c.endReason()})
		return
	}
	attached := time.Now()
	slog.Info("voice call socket attached", "call", callID, "header_auth", identity.Header, "note", note)
	go cl.keepalive(voicePingInterval, voicePongTimeout, voiceLivenessWindow)
	readErr := c.readClient(cl)
	slog.Info("voice call socket detached", "call", callID, "after", time.Since(attached).Round(time.Millisecond),
		"close_status", websocket.CloseStatus(readErr), "err", readErr)
	c.detach(cl)
}

// voiceClientNote keeps a logged client note short and printable.
func voiceClientNote(s string) string {
	if len(s) > 120 {
		s = s[:120]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// voiceClientControls bounds the JSON frames queued towards the client.
const voiceClientControls = 32

// voiceClient is the one browser/native transport of a call: one FIFO,
// budgeted separately for audio bytes and control frames, drained by a
// single writer with a write deadline.
type voiceClient struct {
	conn   *websocket.Conn
	limit  int64 // audio bytes queued or being written
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once

	// heard is when the last frame or pong came from the client (unix
	// nanoseconds).
	heard atomic.Int64

	mu sync.Mutex
	// playedBy is when the audio sent so far finishes playing in real time.
	playedBy time.Time
	queue    []voiceFrame
	audio    int64
	controls int
	wake     chan struct{} // capacity 1: the queue is not empty
}

type voiceFrame struct {
	kind  websocket.MessageType
	data  []byte
	audio int64
	last  bool
}

func newVoiceClient(conn *websocket.Conn, limit int) *voiceClient {
	ctx, cancel := context.WithCancel(context.Background())
	cl := &voiceClient{conn: conn, limit: int64(limit), ctx: ctx, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1)}
	go cl.write()
	return cl
}

func (cl *voiceClient) write() {
	defer close(cl.done)
	for {
		f, ok := cl.next()
		if !ok {
			select {
			case <-cl.ctx.Done():
				return
			case <-cl.wake:
			}
			continue
		}
		ctx, cancel := context.WithTimeout(cl.ctx, wsWriteTimeout)
		err := cl.conn.Write(ctx, f.kind, f.data)
		cancel()
		cl.release(f)
		if err != nil {
			cl.cancel()
			_ = cl.conn.CloseNow()
			return
		}
		if f.last {
			_ = cl.conn.CloseNow()
			return
		}
	}
}

// next pops the oldest frame unless the writer must stop. A popped frame
// keeps its budget until written.
func (cl *voiceClient) next() (voiceFrame, bool) {
	if cl.ctx.Err() != nil {
		return voiceFrame{}, false
	}
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if len(cl.queue) == 0 {
		return voiceFrame{}, false
	}
	f := cl.queue[0]
	cl.queue[0] = voiceFrame{}
	cl.queue = cl.queue[1:]
	return f, true
}

func (cl *voiceClient) release(f voiceFrame) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if f.kind == websocket.MessageBinary {
		cl.audio -= f.audio
	} else {
		cl.controls--
	}
}

// enqueue refuses instead of growing: a slow reader ends its call.
func (cl *voiceClient) enqueue(f voiceFrame) bool {
	cl.mu.Lock()
	if f.kind == websocket.MessageBinary {
		if cl.audio+f.audio > cl.limit {
			cl.mu.Unlock()
			return false
		}
		cl.audio += f.audio
		if now := time.Now(); cl.playedBy.Before(now) {
			cl.playedBy = now
		}
		cl.playedBy = cl.playedBy.Add(time.Duration(f.audio) * time.Second / voicePCMBytesPerSecond)
	} else {
		if cl.controls >= voiceClientControls {
			cl.mu.Unlock()
			return false
		}
		cl.controls++
	}
	cl.queue = append(cl.queue, f)
	cl.mu.Unlock()
	select {
	case cl.wake <- struct{}{}:
	default:
	}
	return true
}

func (cl *voiceClient) sendAudio(pcm []byte) bool {
	return cl.enqueue(voiceFrame{kind: websocket.MessageBinary, data: pcm, audio: int64(len(pcm))})
}

func (cl *voiceClient) sendJSON(v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		return false
	}
	return cl.enqueue(voiceFrame{kind: websocket.MessageText, data: b})
}

// finish sends a last frame and closes; a full queue closes at once.
func (cl *voiceClient) finish(v any) {
	b, _ := json.Marshal(v)
	if !cl.enqueue(voiceFrame{kind: websocket.MessageText, data: b, last: true}) {
		cl.close()
	}
}

func (cl *voiceClient) close() {
	cl.once.Do(func() {
		select {
		case <-cl.done:
		case <-time.After(2 * time.Second):
		}
		cl.cancel()
		_ = cl.conn.CloseNow()
	})
}

// A call's socket is pinged so a client that vanished without closing it
// (lost network, phone off, app killed) is noticed in seconds; it then
// detaches like any closed socket and Grace runs. A client is alive while
// something arrives from it: microphone audio (every 20 ms unless muted),
// a control, or a pong. A pong can wait behind up to a minute of queued
// provider audio, so a late one is not death by itself; nothing heard for
// voiceLivenessWindow is. Variables so tests can shrink them.
var (
	voicePingInterval   = 5 * time.Second
	voicePongTimeout    = 5 * time.Second
	voiceLivenessWindow = 15 * time.Second
	// voiceBackloggedWindow applies while audio waits for the client: a muted
	// client's pong can sit behind up to a minute of it.
	voiceBackloggedWindow = 65 * time.Second
)

// keepalive pings until the client goes and drops the transport once
// nothing has been heard from it for window.
func (cl *voiceClient) keepalive(interval, timeout, window time.Duration) {
	cl.heardNow()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-cl.ctx.Done():
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(cl.ctx, timeout)
			err := cl.conn.Ping(ctx)
			cancel()
			if err == nil {
				cl.heardNow()
			}
			if cl.ctx.Err() != nil {
				return
			}
			limit := window
			if cl.backlogged() {
				limit = max(window, voiceBackloggedWindow)
			}
			if silent := time.Since(time.Unix(0, cl.heard.Load())); silent > limit {
				slog.Info("voice call socket silent", "for", silent.Round(time.Millisecond), "error", err)
				cl.kill()
				return
			}
		}
	}
}

// backlogged reports audio sent towards the client that it cannot have
// played yet: queued here, in socket buffers or in its player. Our own queue
// alone is not enough, since loopback and mobile buffers hold seconds of it.
func (cl *voiceClient) backlogged() bool {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return time.Now().Before(cl.playedBy)
}

// voicePCMBytesPerSecond is PCM s16le mono at 24 kHz.
const voicePCMBytesPerSecond = 48000

func (cl *voiceClient) heardNow() { cl.heard.Store(time.Now().UnixNano()) }

// kill drops the transport immediately (replaced or slow).
func (cl *voiceClient) kill() {
	cl.cancel()
	_ = cl.conn.CloseNow()
}

// readClient accepts binary PCM and the three controls. Anything else is a
// protocol violation that ends the call: the client can never inject
// transcripts, tool calls, signaling or credentials.
// readClient reads until the client goes or the call ends it; the error is
// why the read stopped (nil when the call itself ended it), for the log.
func (c *voiceCall) readClient(cl *voiceClient) error {
	for {
		kind, data, err := cl.conn.Read(cl.ctx)
		if err != nil {
			return err
		}
		cl.heardNow()
		if kind == websocket.MessageBinary {
			if len(data) == 0 || len(data)%2 != 0 || len(data) > voice.MaxPCMFrameBytes {
				c.requestEnd("protocol_violation")
				return nil
			}
			if c.muted.Load() || c.ending.Load() {
				continue
			}
			if err := c.up.SendAudio(cl.ctx, data); err != nil {
				c.requestEnd("audio_backpressure")
				return nil
			}
			continue
		}
		var msg struct {
			Type  string `json:"type"`
			Muted *bool  `json:"muted"`
		}
		if json.Unmarshal(data, &msg) != nil {
			c.requestEnd("protocol_violation")
			return nil
		}
		switch msg.Type {
		case "ping":
			cl.sendJSON(map[string]any{"type": "pong"})
		case "mute":
			if msg.Muted == nil {
				c.requestEnd("protocol_violation")
				return nil
			}
			c.muted.Store(*msg.Muted)
		case "hangup":
			c.requestEnd("hangup")
			return nil
		default:
			c.requestEnd("protocol_violation")
			return nil
		}
	}
}
