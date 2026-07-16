package attention

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ealeixandre/moa/pkg/bus"
)

// service.go — the Attention Service actor.
//
// A single goroutine (loop) owns all mutable state: snapshots, the item queue,
// item states, generation tokens, and the active client sink. Every other
// party — bus subscribers, the WS handler, Attach/Detach, Close — communicates
// with the loop over channels. The loop never hands out live pointers; reads
// get copies. This makes the whole thing race-free by construction and unit
// testable with a real bus.NewLocalBus() and no HTTP or microphone.

// Config holds the Service dependencies. All fields optional except as noted.
type Config struct {
	// Lang is the resolved briefing language code (e.g. "en", "es"); from
	// core.GetSTTLanguage. Empty -> English.
	Lang string

	// OnUndelivered is an optional future push hook. It is called when a P0
	// item is born without an active guardian; callers can use it to wake a
	// paired phone without putting APNs policy in this package.
	OnUndelivered func(AttentionItem)
}

// ClientSink is the minimal interface the loop uses to push messages to the one
// active voice client. The WS handler implements it. Sends must not block the
// loop: implementations buffer and drop the CONNECTION (not the message) on
// overflow — init repairs state on reconnect.
type ClientSink interface {
	// Send delivers one server->client message. Returns false if the sink is
	// dead (the loop then forgets it).
	Send(msg ServerMsg) bool
	// ID identifies the connection so the loop can detect supersession.
	ID() uint64
}

// Bounds on live state (design §2.3). Small: this is a single-user host.
const (
	maxLiveItems  = 200
	maxInboxCrit  = 64  // P0/control — never dropped
	maxInboxNorm  = 256 // future non-P0 — drop+log on overflow
	ackEscalation = 30 * time.Second
)

// Service is the attention brain. Construct with New, wire sessions with Attach,
// tear down with Close.
type Service struct {
	cfg  Config
	lang lang

	critInbox chan inboxMsg // P0/control events — bounded, never dropped
	ctrl      chan ctrlMsg  // external requests (attach/detach/init/ack/...)
	quit      chan struct{} // closed by Close
	done      chan struct{} // closed when loop exits
	closeOnce sync.Once

	itemSeq atomic.Uint64 // att_%d generator
	termSeq atomic.Uint64 // term_%d generator
	genSeq  atomic.Uint64 // per-attach generation generator

	wg sync.WaitGroup
}

// inboxMsg carries a bus event tagged with its session and generation, so the
// loop can drop events from a detached/superseded session.
type inboxMsg struct {
	sessionID string
	gen       uint64
	event     any
}

// ctrlMsg is an external control request handled by the loop. Exactly one of
// the fields is set. Requests that need a reply carry a reply channel.
type ctrlMsg struct {
	kind    ctrlKind
	session *attachReq
	detach  string // sessionID for detach
	ack     string // attention item id for ack
	// setClient swaps the active client (single-active policy).
	setClient ClientSink
	// clearClient removes a client if it is still the active one.
	clearClient ClientSink
	// meta updates a session's alias/title (from auto-title or manual rename).
	meta  *metaUpdate
	reply chan ctrlReply
}

// metaUpdate carries a session's new pronounceable alias and human title.
type metaUpdate struct {
	sessionID string
	alias     string
	title     string
}

type ctrlKind int

const (
	ctrlAttach ctrlKind = iota
	ctrlDetach
	ctrlSetClient
	ctrlClearClient
	ctrlAck
	ctrlGetStatus // reply with snapshot of unresolved items
	ctrlInit      // reply with the full init payload for a new client
	ctrlUpdateMeta
)

type attachReq struct {
	sessionID string
	alias     string
	title     string
	gen       uint64
	// seed carries the initial pending state so a session that already has a
	// pending ask/permission at attach time surfaces immediately.
	seedPerm  []seedPending
	seedAsk   []seedPending
	seedState bus.SessionState
	seedError string
}

type seedPending struct {
	refID    string
	toolName string
	args     map[string]any
	// ask-only:
	questions []bus.AskQuestion
}

type ctrlReply struct {
	items        []AttentionItem
	sessions     []SessionBrief
	terminations []RunTermination
}

// New creates a Service. It does not start the loop; call Start.
func New(cfg Config) *Service {
	return &Service{
		cfg:       cfg,
		lang:      resolveLang(cfg.Lang),
		critInbox: make(chan inboxMsg, maxInboxCrit),
		ctrl:      make(chan ctrlMsg),
		quit:      make(chan struct{}),
		done:      make(chan struct{}),
	}
}

// Start launches the actor loop. Idempotent-safe to call once.
func (s *Service) Start() {
	s.wg.Add(1)
	go s.loop()
}

// Close stops the loop and releases resources. Idempotent. After Close the loop
// emits nothing further. Safe to call from Manager.Shutdown.
func (s *Service) Close() {
	s.closeOnce.Do(func() { close(s.quit) })
	<-s.done
	s.wg.Wait()
}

// nextItemID returns a fresh att_%d id.
func (s *Service) nextItemID() string {
	return fmt.Sprintf("att_%d", s.itemSeq.Add(1))
}

func (s *Service) nextTerminationID() string {
	return fmt.Sprintf("term_%d", s.termSeq.Add(1))
}

// -- Public wiring API (called from pkg/serve) ------------------------------

// Attach registers a session and returns an unsubscribe-style detach func plus
// the bus handler wiring. The caller is responsible for subscribing the
// returned handler to the session bus and calling the returned func on session
// teardown. Generation tokens make late events harmless after detach.
//
// aliasTitle is (alias, title); seedInfo is the current pending approval state
// (may be zero). Returns a detach func to append to the session's unsub list.
func (s *Service) Attach(b bus.EventBus, sessionID, alias, title string) func() {
	gen := s.genSeq.Add(1)
	// Subscribe before reading the seed snapshot, but keep events behind a
	// per-attach gate until the actor owns the session. This closes the
	// snapshot→subscribe race without allowing events to arrive before attach.
	var gateMu sync.Mutex
	ready := false
	var buffered []inboxMsg
	unsub := b.SubscribeAll(func(ev any) {
		if !whitelisted(ev) {
			return
		}
		m := inboxMsg{sessionID: sessionID, gen: gen, event: ev}
		gateMu.Lock()
		if !ready {
			buffered = append(buffered, m)
			gateMu.Unlock()
			return
		}
		gateMu.Unlock()
		s.forward(m)
	})

	// Seed from current pending state so an already-waiting session surfaces.
	var req attachReq
	req.sessionID = sessionID
	req.alias = alias
	req.title = title
	req.gen = gen
	if info, err := bus.QueryTyped[bus.GetPendingApproval, bus.PendingApprovalInfo](b, bus.GetPendingApproval{}); err == nil {
		if info.Permission != nil {
			req.seedPerm = append(req.seedPerm, seedPending{
				refID: info.Permission.ID, toolName: info.Permission.ToolName, args: info.Permission.Args,
			})
		}
		if info.Ask != nil {
			req.seedAsk = append(req.seedAsk, seedPending{
				refID: info.Ask.ID, questions: info.Ask.Questions,
			})
		}
	}
	if st, err := bus.QueryTyped[bus.GetSessionState, string](b, bus.GetSessionState{}); err == nil {
		req.seedState = bus.SessionState(st)
	}
	if errText, err := bus.QueryTyped[bus.GetSessionError, string](b, bus.GetSessionError{}); err == nil {
		req.seedError = errText
	}

	// Wait until attach has reached the actor, then flush in publication order.
	// Hold the gate during the flush so a later event cannot overtake a buffered
	// event in the attention service.
	s.sendCtrlReply(ctrlMsg{kind: ctrlAttach, session: &req})
	gateMu.Lock()
	for _, m := range buffered {
		s.forward(m)
	}
	buffered = nil
	ready = true
	gateMu.Unlock()

	// The detach func: unsubscribe first (stop new events), then tell the loop
	// to invalidate the generation and purge the session's items.
	var once sync.Once
	return func() {
		once.Do(func() {
			unsub()
			s.sendCtrl(ctrlMsg{kind: ctrlDetach, detach: sessionID})
		})
	}
}

// forward pushes a bus event to the loop. P0-relevant events go on the critical
// inbox (bounded, blocking is acceptable — only this session's subscriber
// goroutine waits, never the agent, which has its own FIFO). Never blocks after
// Close.
func (s *Service) forward(m inboxMsg) {
	select {
	case s.critInbox <- m:
	case <-s.quit:
	}
}

// sendCtrl sends a control message, giving up if the service is closing.
func (s *Service) sendCtrl(m ctrlMsg) {
	select {
	case s.ctrl <- m:
	case <-s.quit:
	}
}

// sendCtrlReply sends a control message and waits for the reply, returning zero
// on shutdown.
func (s *Service) sendCtrlReply(m ctrlMsg) ctrlReply {
	reply := make(chan ctrlReply, 1)
	m.reply = reply
	select {
	case s.ctrl <- m:
	case <-s.quit:
		return ctrlReply{}
	}
	select {
	case r := <-reply:
		return r
	case <-s.quit:
		return ctrlReply{}
	}
}

var _ = context.Background // reserved for future timer contexts

// -- Client-facing API (called from the WS handler) -------------------------

// SetActiveClient makes c the single active voice sink and immediately sends it
// an authoritative init. Any previous client is told it's inactive.
func (s *Service) SetActiveClient(c ClientSink) {
	s.sendCtrl(ctrlMsg{kind: ctrlSetClient, setClient: c})
}

// ClearActiveClient removes c as the sink if it is still the active one (on WS
// disconnect). No-op if a newer client already superseded it.
func (s *Service) ClearActiveClient(c ClientSink) {
	s.sendCtrl(ctrlMsg{kind: ctrlClearClient, clearClient: c})
}

// Ack marks an item acknowledged (the client relayed it). Stops escalation;
// does not resolve. Idempotent.
func (s *Service) Ack(itemID string) {
	s.sendCtrl(ctrlMsg{kind: ctrlAck, ack: itemID})
}

// Status returns the current unresolved items (for get_status).
func (s *Service) Status() []AttentionItem {
	return s.sendCtrlReply(ctrlMsg{kind: ctrlGetStatus}).items
}

// Roster returns the current attached-session roster. It is public so HTTP
// clients can consume the same global attention state as the guardian.
func (s *Service) Roster() []SessionBrief {
	return s.sendCtrlReply(ctrlMsg{kind: ctrlInit}).sessions
}

// Snapshot returns the authoritative guardian init payload. get_status uses
// this same shape so the caller can replace, rather than merge, local state.
func (s *Service) Snapshot() ServerMsg {
	r := s.sendCtrlReply(ctrlMsg{kind: ctrlInit})
	return ServerMsg{
		Type:         "init",
		V:            ProtocolVersion,
		Items:        r.items,
		Sessions:     r.sessions,
		Terminations: r.terminations,
	}
}

// UpdateMeta updates a session's spoken alias and human title (e.g. after
// auto-title generation or a manual rename). No-op if the session isn't
// attached. Refreshes the client roster.
func (s *Service) UpdateMeta(sessionID, alias, title string) {
	s.sendCtrl(ctrlMsg{kind: ctrlUpdateMeta, meta: &metaUpdate{
		sessionID: sessionID, alias: alias, title: title,
	}})
}
