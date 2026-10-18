package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// Cache keep-alive.
//
// While an agent is not calling the model (idle, blocked on ask_user, waiting
// on a tool), its provider-side prompt cache expires one TTL after the last
// request that read it. Keep-alive resends that last request, byte for byte,
// as a zero-output prewarm shortly before the TTL runs out, for as long as the
// policy says a wake is still likely enough to pay for the pings. The ping's
// answer never enters the conversation: what the model sees next is exactly
// what it would have seen without it.
//
// The clock is anchored at the instant a request is sent (the provider's TTL
// counts from there, not from message_start), on the monotonic clock. Nothing
// is pinged while a request of the agent is in flight, and a real request
// never waits for a ping: it cancels it. Nothing here persists: a restart
// forgets every snapshot.

// pingTimeout bounds one ping. A ping answers with no output, so it takes
// about as long as reading the prefix.
const pingTimeout = 2 * time.Minute

// KeepAliveClock is the time source of keep-alive (fake in tests).
type KeepAliveClock interface {
	Now() time.Time
	// AfterFunc calls f in its own goroutine after d; the returned func
	// stops it.
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

type systemKeepAliveClock struct{}

func (systemKeepAliveClock) Now() time.Time { return time.Now() }
func (systemKeepAliveClock) AfterFunc(d time.Duration, f func()) func() bool {
	return time.AfterFunc(d, f).Stop
}

// KeepAliveConfig turns keep-alive on for an agent (see SetKeepAlive).
type KeepAliveConfig struct {
	Policy core.KeepAlivePolicy
	Role   string // core.KeepAliveRole*
	// PendingWork reports background work that will wake the agent (an async
	// subagent, a bash job): an idle agent with some counts as waiting. nil =
	// never. Called with no agent lock held.
	PendingWork func() bool
	// OnEvent receives every ping, failed ping and warm wake. Called from
	// keep-alive's goroutines, never with a lock held.
	OnEvent func(KeepAliveEvent)
	Clock   KeepAliveClock // nil = the system clock
}

// Keep-alive event kinds.
const (
	KeepAlivePing      = "ping"       // a ping was answered; Usage and CostUSD are its own
	KeepAlivePingError = "ping_error" // a ping failed; keep-alive is off until the next request
	KeepAliveWake      = "wake"       // the first real response after pings; Usage is that response's
)

// KeepAliveEvent is one keep-alive observation, for cost and telemetry.
type KeepAliveEvent struct {
	Kind          string
	At            time.Time
	Role          string
	State         string // ping: what the agent was waiting for
	Model         core.Model
	ContextTokens int // the prefix the ping kept warm
	Usage         core.Usage
	CostUSD       float64
	Err           error
	// Wake only: the pings since the previous real request, what they cost,
	// and how long the agent slept.
	Pings       int
	PingCostUSD float64
	Slept       time.Duration
}

// keepAliveSnapshot is the last request sent, frozen at send time, and what
// must still hold for it to be the prefix of the next one.
type keepAliveSnapshot struct {
	req           core.Request
	pw            core.CachePrewarmer
	sentAt        time.Time // the last real request: the horizon's origin
	anchor        time.Time // the last request or ping that refreshed the cache
	ttl, every    time.Duration
	contextTokens int
	settled       bool // its response arrived; only then can it be pinged

	revision  uint64
	epoch     int
	trimMark  string
	msgCount  int
	lastMsgID string
	toolsHash [32]byte
}

type keepAlive struct {
	a     *Agent
	cfg   KeepAliveConfig
	clock KeepAliveClock
	ctx   context.Context
	close context.CancelFunc

	mu         sync.Mutex
	snap       *keepAliveSnapshot
	gen        uint64 // bumped by every real request and every suspension
	inFlight   bool
	toolsBusy  bool
	askUser    bool
	stopTimer  func() bool
	pingCancel context.CancelFunc
	// Pings since the last real request, reported with the next one.
	pings    int
	pingCost float64
	wake     *KeepAliveEvent // pending for the response in flight
}

// SetKeepAlive turns cache keep-alive on with cfg, or off with nil. Off is
// the default and costs nothing: no snapshot is taken.
func (a *Agent) SetKeepAlive(cfg *KeepAliveConfig) {
	var next *keepAlive
	if cfg != nil {
		next = &keepAlive{a: a, cfg: *cfg, clock: cfg.Clock}
		if next.clock == nil {
			next.clock = systemKeepAliveClock{}
		}
		next.ctx, next.close = context.WithCancel(context.Background())
	}
	if old := a.keepAlive.Swap(next); old != nil {
		old.shutdown()
	}
}

// keepAliveSuspend drops the snapshot until the next request: whatever was
// warm is no longer worth keeping (a stop, a rewritten context).
func (a *Agent) keepAliveSuspend() {
	if ka := a.keepAlive.Load(); ka != nil {
		ka.mu.Lock()
		ka.suspendLocked()
		ka.mu.Unlock()
	}
}

func (ka *keepAlive) shutdown() {
	ka.mu.Lock()
	ka.suspendLocked()
	ka.mu.Unlock()
	ka.close()
}

func (ka *keepAlive) suspendLocked() {
	ka.gen++
	ka.snap = nil
	ka.cancelPingLocked()
	ka.stopTimerLocked()
}

func (ka *keepAlive) cancelPingLocked() {
	if ka.pingCancel != nil {
		ka.pingCancel()
		ka.pingCancel = nil
	}
}

func (ka *keepAlive) stopTimerLocked() {
	if ka.stopTimer != nil {
		ka.stopTimer()
		ka.stopTimer = nil
	}
}

func (ka *keepAlive) armLocked(d time.Duration) {
	ka.stopTimerLocked()
	if d < 0 {
		d = 0
	}
	ka.stopTimer = ka.clock.AfterFunc(d, ka.evaluate)
}

func toolsHash(specs []core.ToolSpec) [32]byte {
	b, _ := json.Marshal(specs)
	return sha256.Sum256(b)
}

// sending records req as the request the agent is about to send. Called by
// the loop right before provider.Stream; it never blocks on a ping.
func (ka *keepAlive) sending(req core.Request, provider core.Provider, state *AgentState, revision uint64) {
	now := ka.clock.Now()
	var snap *keepAliveSnapshot
	ttl := cacheTTL(req.Options.CacheRetention)
	every := ka.cfg.Policy.PingEvery(ttl)
	// OpenAI and the rest: nothing (their OAuth backends refuse a prewarm).
	if req.Model.Provider == "anthropic" && every > 0 {
		if pw, ok := core.ProviderPrewarmer(provider); ok {
			ping := req
			ping.Messages = slices.Clone(req.Messages)
			ping.Tools = slices.Clone(req.Tools)
			// The callbacks belong to the turn that sent it; the binding is
			// set from the source that answers it.
			ping.Options.OnProviderDispatch = nil
			ping.Options.OnProviderPrepare = nil
			ping.Options.OnProviderRetry = nil
			ping.Options.ProviderRetryAttempt = 0
			ping.Options.ProviderBinding = nil
			ping.Options.OnFastUnavailable = nil
			ping.Options.OnRequestFingerprint = nil
			snap = &keepAliveSnapshot{
				req: ping, pw: pw, sentAt: now, anchor: now, ttl: ttl, every: every,
				revision: revision, epoch: state.CompactionEpoch, trimMark: state.TrimWatermarkMsgID,
				msgCount: len(state.Messages), toolsHash: toolsHash(req.Tools),
			}
			if n := len(state.Messages); n > 0 {
				snap.lastMsgID = state.Messages[n-1].MsgID
			}
		}
	}
	ka.mu.Lock()
	defer ka.mu.Unlock()
	var prevSent time.Time
	if ka.snap != nil {
		prevSent = ka.snap.sentAt
	}
	ka.suspendLocked()
	ka.snap = snap
	ka.inFlight = true
	if ka.pings > 0 {
		ka.wake = &KeepAliveEvent{Kind: KeepAliveWake, Role: ka.cfg.Role, Model: req.Model, Pings: ka.pings, PingCostUSD: ka.pingCost}
		if !prevSent.IsZero() {
			ka.wake.Slept = now.Sub(prevSent)
		}
	}
	ka.pings, ka.pingCost = 0, 0
}

// settled records the response to the request in flight; msg is nil when
// none usable arrived, and then nothing is kept warm until the next request.
func (ka *keepAlive) settled(msg *core.Message) {
	var wake *KeepAliveEvent
	ka.mu.Lock()
	ka.inFlight = false
	s := ka.snap
	switch {
	case s == nil:
	case msg == nil || msg.Usage == nil:
		ka.suspendLocked()
	default:
		s.settled = true
		s.contextTokens = msg.Usage.Input + msg.Usage.CacheRead + msg.Usage.CacheWrite
		if src := msg.ProviderSource; src != nil {
			b := *src
			s.req.Options.ProviderBinding = &b
		}
		ka.armLocked(s.anchor.Add(s.every).Sub(ka.clock.Now()))
		if ka.wake != nil {
			wake = ka.wake
			wake.At = ka.clock.Now()
			wake.Usage = *msg.Usage
			wake.ContextTokens = s.contextTokens
		}
	}
	ka.wake = nil
	ka.mu.Unlock()
	ka.emit(wake)
}

// tools marks a tool batch running (names) or finished (nil).
func (ka *keepAlive) tools(names []string) {
	ka.mu.Lock()
	ka.toolsBusy = names != nil
	ka.askUser = slices.Contains(names, "ask_user")
	ka.mu.Unlock()
	if names != nil {
		// A ping may have come due while the loop was between steps.
		ka.evaluate()
	}
}

// runEnded marks the agent idle.
func (ka *keepAlive) runEnded() {
	ka.mu.Lock()
	if ka.inFlight {
		// The run ended without the request settling: its outcome is unknown.
		ka.inFlight = false
		ka.suspendLocked()
	}
	ka.toolsBusy, ka.askUser = false, false
	ka.mu.Unlock()
	ka.evaluate()
}

func (ka *keepAlive) emit(e *KeepAliveEvent) {
	if e != nil && ka.cfg.OnEvent != nil {
		ka.cfg.OnEvent(*e)
	}
}

// evaluate pings if one is due and the agent is in a state that allows it,
// or arms the timer for when one will be. Idempotent; safe from any
// goroutine with no lock held.
func (ka *keepAlive) evaluate() {
	ka.mu.Lock()
	s := ka.snap
	if s == nil || !s.settled || ka.inFlight || ka.pingCancel != nil || ka.ctx.Err() != nil {
		ka.mu.Unlock()
		return
	}
	now := ka.clock.Now()
	if due := s.anchor.Add(s.every); now.Before(due) {
		ka.armLocked(due.Sub(now))
		ka.mu.Unlock()
		return
	}
	if now.Sub(s.anchor) >= s.ttl {
		// Too late: a ping now would write the whole prefix cold.
		ka.suspendLocked()
		ka.mu.Unlock()
		return
	}
	gen, toolsBusy, askUser := ka.gen, ka.toolsBusy, ka.askUser
	ka.mu.Unlock()

	state, ok := ka.state(toolsBusy, askUser)
	if !ok {
		return // between steps of a run: the next transition re-evaluates
	}
	valid := ka.a.keepAliveMatches(s)
	horizon := ka.cfg.Policy.Horizon(ka.cfg.Role, state, s.contextTokens)

	ka.mu.Lock()
	defer ka.mu.Unlock()
	if gen != ka.gen || ka.snap != s || ka.inFlight || ka.pingCancel != nil {
		return
	}
	if !valid || now.Sub(s.sentAt) >= horizon {
		// Changed context, or past the horizon: only a new request re-arms.
		ka.suspendLocked()
		return
	}
	ctx, cancel := context.WithTimeout(ka.ctx, pingTimeout)
	ka.pingCancel = cancel
	ka.stopTimerLocked()
	go ka.ping(ctx, cancel, s, gen, now, state)
}

// state classifies an agent not calling the model. ok is false while a run
// is between steps (about to send).
func (ka *keepAlive) state(toolsBusy, askUser bool) (string, bool) {
	ka.a.mu.Lock()
	running := ka.a.cancel != nil
	ka.a.mu.Unlock()
	switch {
	case toolsBusy && askUser:
		return core.KeepAliveAskUser, true
	case toolsBusy:
		return core.KeepAliveWaiting, true
	case running:
		return "", false
	case ka.cfg.PendingWork != nil && ka.cfg.PendingWork():
		return core.KeepAliveWaiting, true
	}
	return core.KeepAliveIdle, true
}

func (ka *keepAlive) ping(ctx context.Context, cancel context.CancelFunc, s *keepAliveSnapshot, gen uint64, sentAt time.Time, state string) {
	usage, err := s.pw.Prewarm(ctx, s.req)
	cancelled := ctx.Err() != nil && errors.Is(err, context.Canceled)
	cancel()
	ev := &KeepAliveEvent{At: ka.clock.Now(), Role: ka.cfg.Role, State: state, Model: s.req.Model, ContextTokens: s.contextTokens}

	ka.mu.Lock()
	current := gen == ka.gen && ka.snap == s
	if current {
		ka.pingCancel = nil
	}
	switch {
	case err == nil:
		ev.Kind, ev.Usage = KeepAlivePing, usage
		if p := s.req.Model.Pricing; p != nil {
			ev.CostUSD = p.Cost(usage)
		}
		ka.pings++
		ka.pingCost += ev.CostUSD
		if current {
			s.anchor = sentAt
			ka.armLocked(s.every - ka.clock.Now().Sub(sentAt))
		}
	case cancelled:
		ev = nil // a real request took over
	case errors.Is(err, core.ErrPrewarmUnsupported):
		ev = nil
		slog.Debug("cache keep-alive: prewarm unsupported", "model", s.req.Model.ID)
		if current {
			ka.suspendLocked()
		}
	default:
		// No retry: keep-alive stays off until the agent's next request.
		ev.Kind, ev.Err = KeepAlivePingError, err
		if current {
			ka.suspendLocked()
		}
	}
	ka.mu.Unlock()
	ka.emit(ev)
}

// keepAliveMatches reports whether s is still the prefix of the agent's next
// request: same settings, same tools and system prompt, and a conversation
// that has only grown since (no compaction, fresh, trim, restore or rewind).
func (a *Agent) keepAliveMatches(s *keepAliveSnapshot) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.configRevision != s.revision || !sameModel(a.config.Model, s.req.Model) ||
		a.config.SystemPrompt != s.req.System || a.config.Fast != s.req.Options.Fast ||
		a.config.CacheTTL != s.req.Options.CacheRetention ||
		a.state.CompactionEpoch != s.epoch || a.state.TrimWatermarkMsgID != s.trimMark {
		return false
	}
	msgs := a.state.Messages
	if len(msgs) < s.msgCount || (s.msgCount > 0 && msgs[s.msgCount-1].MsgID != s.lastMsgID) {
		return false
	}
	return toolsHash(a.tools.Specs()) == s.toolsHash
}

func cacheTTL(retention string) time.Duration {
	switch retention {
	case "1h":
		return time.Hour
	case "":
		return 5 * time.Minute
	}
	return 0 // core.CacheOff
}
