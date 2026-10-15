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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/e-aleixandre/moa/pkg/auth"
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

	ctx    context.Context // cancelled on Close: aborts opens in flight
	cancel context.CancelFunc

	mu         sync.Mutex
	closed     bool
	principals map[string]bool       // one reservation per principal
	calls      map[string]*voiceCall // active calls by ID
	ended      map[string]string     // tombstone: call ID → principal
	endedOrder []string
	wg         sync.WaitGroup // reservations and controllers
}

func newVoiceCallRegistry(mgr *Manager, store *auth.Store, opts VoiceCallOptions) *voiceCallRegistry {
	opts.Limits = opts.Limits.withDefaults()
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &voiceCallRegistry{mgr: mgr, store: store, opts: opts, ctx: ctx, cancel: cancel,
		principals: map[string]bool{}, calls: map[string]*voiceCall{}, ended: map[string]string{}}
}

// reserve takes the principal's single call slot before any upstream is
// opened, so two concurrent creates cannot both reach a provider.
func (r *voiceCallRegistry) reserve(principal string) (ok, closed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false, true
	}
	if r.principals[principal] {
		return false, false
	}
	r.principals[principal] = true
	r.wg.Add(1)
	return true, false
}

func (r *voiceCallRegistry) release(principal string) {
	r.mu.Lock()
	delete(r.principals, principal)
	r.mu.Unlock()
	r.wg.Done()
}

// register publishes an opened call. It fails when the registry closed while
// the upstream was opening; the caller then closes that upstream.
func (r *voiceCallRegistry) register(c *voiceCall) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	r.calls[c.id] = c
	return true
}

// finish retires an ended call: tombstone, admission and wait group.
func (r *voiceCallRegistry) finish(c *voiceCall) {
	r.mu.Lock()
	delete(r.calls, c.id)
	delete(r.principals, c.principal)
	r.ended[c.id] = c.principal
	r.endedOrder = append(r.endedOrder, c.id)
	if len(r.endedOrder) > voiceTombstones {
		delete(r.ended, r.endedOrder[0])
		r.endedOrder = r.endedOrder[1:]
	}
	r.mu.Unlock()
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
	}
	if err := json.NewDecoder(io.LimitReader(req.Body, voice.MaxControlJSON)).Decode(&in); err != nil {
		voiceError(w, http.StatusBadRequest, map[string]any{"error": "invalid_request"})
		return
	}
	sess, ok := r.mgr.Get(in.SessionID)
	if !ok {
		voiceError(w, http.StatusNotFound, map[string]any{"error": "session_not_found"})
		return
	}
	provider := voice.ProviderID(in.Provider)
	if provider == "" {
		p, found := r.defaultPlan()
		if !found {
			voiceError(w, http.StatusServiceUnavailable, r.planUnavailable("", "no_plan"))
			return
		}
		provider = p
	}
	adapter, ok := r.adapter(provider)
	if !ok {
		voiceError(w, http.StatusBadRequest, map[string]any{"error": "unknown_provider"})
		return
	}
	principal := identity.auditID()
	reserved, closed := r.reserve(principal)
	if closed {
		voiceError(w, http.StatusServiceUnavailable, map[string]any{"error": "shutting_down"})
		return
	}
	if !reserved {
		voiceError(w, http.StatusConflict, map[string]any{"error": "call_active"})
		return
	}
	id := newVoiceCallID()
	ctx, cancel := context.WithTimeout(r.ctx, voiceOpenTimeout)
	up, err := adapter.Open(ctx, voice.CallSpec{
		CallID: id, SessionID: sess.ID, Voice: voiceProviders[provider].voice,
		Instructions: voice.CommonInstructions, BackendModel: voiceModel(), Tools: voice.ToolSpecs(),
	})
	cancel()
	if err != nil {
		r.release(principal)
		oe := voice.Classify(err)
		slog.Info("voice call upstream refused", "provider", provider, "class", oe.Class, "status", oe.Status)
		if provider.Paid() {
			voiceError(w, http.StatusBadGateway, map[string]any{"error": "provider_unavailable", "reason": oe.Class})
			return
		}
		voiceError(w, http.StatusServiceUnavailable, r.planUnavailable(provider, oe.Class))
		return
	}
	c := newVoiceCall(r, id, principal, sess.ID, provider, adapter.Billing(), up)
	if !r.register(c) {
		ctx, cancel := context.WithTimeout(context.Background(), r.opts.Limits.Teardown)
		_ = up.Close(ctx, "shutdown")
		cancel()
		r.release(principal)
		voiceError(w, http.StatusServiceUnavailable, map[string]any{"error": "shutting_down"})
		return
	}
	go c.run()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{"call_id": id, "provider": provider, "billing": c.billing, "ws_url": "/api/voice/calls/" + id + "/ws"})
}

// planUnavailable never starts the paid alternative: it only names it when
// its key is selectable, for an explicit second action by the user.
func (r *voiceCallRegistry) planUnavailable(plan voice.ProviderID, reason string) map[string]any {
	out := map[string]any{"error": "plan_unavailable", "reason": reason}
	candidates := []voice.ProviderID{voice.XAIAPI, voice.OpenAILiveAPI}
	if paid, ok := pairedPaid[plan]; ok {
		candidates = []voice.ProviderID{paid}
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
	identity, ok := requestAuthIdentity(req)
	if !ok {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	if !identity.Header && !sameOrigin(req.Header.Get("Origin"), req.Host) {
		http.Error(w, "same-origin upgrade required", http.StatusForbidden)
		return
	}
	c, principal, found := r.lookup(req.PathValue("id"))
	if !found || c == nil {
		http.Error(w, "call not found", http.StatusNotFound)
		return
	}
	if principal != identity.auditID() {
		http.Error(w, "not your call", http.StatusForbidden)
		return
	}
	lease, err := deviceLeaseForWebSocket(req, func(string) { c.requestEnd("device_revoked") })
	if err != nil {
		http.Error(w, "device unavailable", http.StatusServiceUnavailable)
		return
	}
	if lease != nil {
		defer lease.release()
	}
	conn, err := websocket.Accept(w, req, wsAcceptOptions())
	if err != nil {
		return
	}
	conn.SetReadLimit(voice.MaxControlJSON)
	cl := newVoiceClient(conn, r.opts.Limits.BufferedBytes)
	defer cl.close()
	if !c.attach(cl) {
		cl.finish(map[string]any{"type": "ended", "reason": c.endReason()})
		return
	}
	c.readClient(cl)
	c.detach(cl)
}

// voiceClient is the one browser/native transport of a call: a bounded queue
// drained by a single writer with a write deadline.
type voiceClient struct {
	conn    *websocket.Conn
	out     chan voiceFrame
	pending atomic.Int64 // audio bytes queued
	limit   int64
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	once    sync.Once
}

type voiceFrame struct {
	kind  websocket.MessageType
	data  []byte
	audio int64
	last  bool
}

func newVoiceClient(conn *websocket.Conn, limit int) *voiceClient {
	ctx, cancel := context.WithCancel(context.Background())
	cl := &voiceClient{conn: conn, out: make(chan voiceFrame, 128), limit: int64(limit), ctx: ctx, cancel: cancel, done: make(chan struct{})}
	go cl.write()
	return cl
}

func (cl *voiceClient) write() {
	defer close(cl.done)
	for {
		select {
		case <-cl.ctx.Done():
			return
		case f := <-cl.out:
			ctx, cancel := context.WithTimeout(cl.ctx, wsWriteTimeout)
			err := cl.conn.Write(ctx, f.kind, f.data)
			cancel()
			cl.pending.Add(-f.audio)
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
}

func (cl *voiceClient) enqueue(f voiceFrame) bool {
	select {
	case cl.out <- f:
		return true
	default:
		return false
	}
}

// sendAudio refuses instead of growing: a slow reader ends its call.
func (cl *voiceClient) sendAudio(pcm []byte) bool {
	n := int64(len(pcm))
	if cl.pending.Add(n) > cl.limit {
		cl.pending.Add(-n)
		return false
	}
	if !cl.enqueue(voiceFrame{kind: websocket.MessageBinary, data: pcm, audio: n}) {
		cl.pending.Add(-n)
		return false
	}
	return true
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

// kill drops the transport immediately (replaced or slow).
func (cl *voiceClient) kill() {
	cl.cancel()
	_ = cl.conn.CloseNow()
}

// readClient accepts binary PCM and the three controls. Anything else is a
// protocol violation that ends the call: the client can never inject
// transcripts, tool calls, signaling or credentials.
func (c *voiceCall) readClient(cl *voiceClient) {
	for {
		kind, data, err := cl.conn.Read(cl.ctx)
		if err != nil {
			return
		}
		if kind == websocket.MessageBinary {
			if len(data) == 0 || len(data)%2 != 0 || len(data) > voice.MaxPCMFrameBytes {
				c.requestEnd("protocol_violation")
				return
			}
			if c.muted.Load() || c.ending.Load() {
				continue
			}
			if err := c.up.SendAudio(cl.ctx, data); err != nil {
				c.requestEnd("audio_backpressure")
				return
			}
			continue
		}
		var msg struct {
			Type  string `json:"type"`
			Muted *bool  `json:"muted"`
		}
		if json.Unmarshal(data, &msg) != nil {
			c.requestEnd("protocol_violation")
			return
		}
		switch msg.Type {
		case "ping":
			cl.sendJSON(map[string]any{"type": "pong"})
		case "mute":
			if msg.Muted == nil {
				c.requestEnd("protocol_violation")
				return
			}
			c.muted.Store(*msg.Muted)
		case "hangup":
			c.requestEnd("hangup")
			return
		default:
			c.requestEnd("protocol_violation")
			return
		}
	}
}

var errVoiceEnding = errors.New("the call is ending")
