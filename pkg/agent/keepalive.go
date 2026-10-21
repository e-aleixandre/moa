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

// pingSendMargin is how long before the cache expires a ping must already be
// on its way: past it, the request could reach the provider after the TTL and
// write the whole prefix cold instead of refreshing it. Pings are due 30 s
// before expiry, so this only refuses one delayed past its slot. It bounds the
// whole ping, credential and HTTP included: one that is not out by then is
// abandoned unsent.
const pingSendMargin = 15 * time.Second

// errPingStale is the dispatch gate's refusal: the ping stopped being worth
// sending between its admission and its POST.
var errPingStale = errors.New("cache keep-alive: ping no longer current")

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
	// Policy is read at every request and every ping, so a settings change
	// applies at once. A role it leaves off takes no snapshot at all.
	Policy func() core.KeepAlivePolicy
	Role   string // core.KeepAliveRole*
	// PendingWork reports background work that will wake the agent (an async
	// subagent, a bash job): an idle agent with some counts as waiting. nil =
	// never. Called with no agent lock held.
	PendingWork func() bool
	// Exempt reports that the agent is not to be kept warm right now (a
	// session whose next wake is a known scheduled task). nil = never.
	// Called with no agent lock held.
	Exempt func() bool
	// Group, when set, is told about this agent so a settings change can stop
	// it at once (see KeepAliveGroup).
	Group *KeepAliveGroup
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
	Kind string
	// At is when the ping was sent (the instant that refreshed the cache), or
	// when the wake's response arrived.
	At             time.Time
	Role           string
	State          string // ping: what the agent was waiting for
	Model          core.Model
	ContextTokens  int // the prefix the pings kept warm
	Usage          core.Usage
	CostUSD        float64
	Err            error
	ProviderSource *core.ProviderSource
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
	toolsCtx   context.Context // the running batch's run context
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
		if g := old.cfg.Group; g != nil {
			g.remove(old)
		}
	}
	if next != nil && next.cfg.Group != nil {
		next.cfg.Group.add(next)
	}
}

// KeepAliveGroup is the set of agents that share one live setting. When the
// setting switches a role off, Stop ends that role's keep-alive at once: the
// ping in flight is cancelled, and an evaluation already past the setting
// read cannot start a new one.
type KeepAliveGroup struct {
	mu     sync.Mutex
	agents map[*keepAlive]struct{}
}

func (g *KeepAliveGroup) add(ka *keepAlive) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.agents == nil {
		g.agents = make(map[*keepAlive]struct{})
	}
	g.agents[ka] = struct{}{}
}

func (g *KeepAliveGroup) remove(ka *keepAlive) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.agents, ka)
}

// Stop drops the snapshot of every agent whose role stop reports, until its
// next request.
func (g *KeepAliveGroup) Stop(stop func(role string) bool) {
	g.mu.Lock()
	var hit []*keepAlive
	for ka := range g.agents {
		if stop(ka.cfg.Role) {
			hit = append(hit, ka)
		}
	}
	g.mu.Unlock()
	for _, ka := range hit {
		ka.mu.Lock()
		ka.suspendLocked()
		ka.mu.Unlock()
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
	policy := ka.cfg.Policy()
	every := policy.PingEvery(ttl)
	// OpenAI and the rest: nothing (their OAuth backends refuse a prewarm).
	if req.Model.Provider == "anthropic" && every > 0 && policy.RoleEnabled(ka.cfg.Role) {
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
	prevTokens := 0
	var prevSource *core.ProviderSource
	if ka.snap != nil {
		prevSent, prevTokens = ka.snap.sentAt, ka.snap.contextTokens
		prevSource = ka.snap.req.Options.ProviderBinding
	}
	ka.suspendLocked()
	ka.snap = snap
	ka.inFlight = true
	if ka.pings > 0 {
		ka.wake = &KeepAliveEvent{Kind: KeepAliveWake, Role: ka.cfg.Role, Model: req.Model, ContextTokens: prevTokens, Pings: ka.pings, PingCostUSD: ka.pingCost, ProviderSource: prevSource}
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
		}
	}
	ka.wake = nil
	ka.mu.Unlock()
	ka.emit(wake)
}

// tools marks a tool batch of the run ctx running (names) or finished (nil).
func (ka *keepAlive) tools(ctx context.Context, names []string) {
	ka.mu.Lock()
	ka.toolsBusy = names != nil
	ka.toolsCtx = ctx
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
//
// What calls out (the setting, background work, schedules) is read first,
// with no lock held. The decision is then taken in one step under the
// agent's lock and keep-alive's, the same locks every change to the prefix,
// the setting or the run takes: the clock is read again there, so a ping
// starts only if, at that instant, it is still due, inside the cache's TTL,
// for an unchanged prefix and a role still on.
func (ka *keepAlive) evaluate() {
	ka.mu.Lock()
	s := ka.snap
	if s == nil || !s.settled || ka.inFlight || ka.pingCancel != nil || ka.ctx.Err() != nil {
		ka.mu.Unlock()
		return
	}
	if due := s.anchor.Add(s.every); ka.clock.Now().Before(due) {
		ka.armLocked(due.Sub(ka.clock.Now()))
		ka.mu.Unlock()
		return
	}
	gen := ka.gen
	ka.mu.Unlock()

	policy := ka.cfg.Policy()
	pending := ka.cfg.PendingWork != nil && ka.cfg.PendingWork()
	exempt := ka.cfg.Exempt != nil && ka.cfg.Exempt()

	a := ka.a
	a.mu.Lock()
	defer a.mu.Unlock()
	ka.mu.Lock()
	defer ka.mu.Unlock()
	if gen != ka.gen || ka.snap != s || ka.inFlight || ka.pingCancel != nil || ka.ctx.Err() != nil {
		return
	}
	var state string
	switch {
	case ka.toolsBusy && ka.askUser:
		state = core.KeepAliveAskUser
	case ka.toolsBusy:
		state = core.KeepAliveWaiting
	case a.cancel != nil:
		return // between steps of a run: the next transition re-evaluates
	case pending:
		state = core.KeepAliveWaiting
	default:
		state = core.KeepAliveIdle
	}
	if ka.toolsBusy && ka.toolsCtx.Err() != nil {
		// The run was cancelled and its tools are unwinding: nothing comes
		// back to this prefix.
		ka.suspendLocked()
		return
	}
	now := ka.clock.Now()
	if now.Sub(s.anchor) > s.ttl-pingSendMargin || exempt || !a.keepAliveMatchesLocked(s) ||
		now.Sub(s.sentAt) >= policy.Horizon(ka.cfg.Role, state, s.contextTokens) {
		// Too late for the cache, not wanted, a changed prefix, or past the
		// horizon: only a new request re-arms.
		ka.suspendLocked()
		return
	}
	parent := ka.ctx
	if ka.toolsBusy {
		// A run waiting on its tools owns the ping: cancelling the run
		// cancels it, even once admitted. Every other stop (a real request,
		// a setting, a shutdown) cancels it through pingCancel.
		parent = ka.toolsCtx
	}
	ctx, cancel := context.WithTimeout(parent, s.anchor.Add(s.ttl-pingSendMargin).Sub(now))
	ka.pingCancel = cancel
	ka.stopTimerLocked()
	go ka.ping(ctx, cancel, s, gen, now, state)
}

func (ka *keepAlive) ping(ctx context.Context, cancel context.CancelFunc, s *keepAliveSnapshot, gen uint64, sentAt time.Time, state string) {
	req := s.req
	req.Options.OnProviderDispatch = func(context.Context, core.ProviderSource) error {
		if !ka.stillCurrent(s, gen) {
			return errPingStale
		}
		return nil
	}
	usage, err := s.pw.Prewarm(ctx, req)
	// Cancelled or stale at the gate: nothing was sent. Out of time is not
	// silent: it may have hit after the POST left, with its usage lost, so it
	// is reported as a failed ping and leaves a trace.
	cancelled := (ctx.Err() != nil && errors.Is(err, context.Canceled)) || errors.Is(err, errPingStale)
	cancel()
	ev := &KeepAliveEvent{At: sentAt, Role: ka.cfg.Role, State: state, Model: s.req.Model, ContextTokens: s.contextTokens, ProviderSource: s.req.Options.ProviderBinding}

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
		// Its cost is spent either way, but only a ping of the snapshot still
		// kept warms the next wake: a late one belongs to an older prefix.
		if current {
			ka.pings++
			ka.pingCost += ev.CostUSD
			s.anchor = sentAt
			ka.armLocked(s.every - ka.clock.Now().Sub(sentAt))
		}
	case cancelled:
		ev = nil
		if current {
			ka.suspendLocked()
		}
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

// stillCurrent is the dispatch gate: whether, right now, the admitted ping is
// still the one to send. It re-checks under the same locks as the admission
// what any change since could have broken, so no mutator has to remember to
// cancel it: a changed prefix, a newer request or suspension, or a cancelled
// run whose tools are unwinding, or a cache too close to expiring.
func (ka *keepAlive) stillCurrent(s *keepAliveSnapshot, gen uint64) bool {
	a := ka.a
	a.mu.Lock()
	defer a.mu.Unlock()
	ka.mu.Lock()
	defer ka.mu.Unlock()
	if gen != ka.gen || ka.snap != s || ka.inFlight || (ka.toolsBusy && ka.toolsCtx.Err() != nil) ||
		ka.clock.Now().Sub(s.anchor) > s.ttl-pingSendMargin {
		return false
	}
	return a.keepAliveMatchesLocked(s)
}

// keepAliveMatches reports whether s is still the prefix of the agent's next
// request: same settings, same tools and system prompt, and a conversation
// that has only grown since (no compaction, fresh, trim, restore or rewind).
// Caller holds a.mu.
func (a *Agent) keepAliveMatchesLocked(s *keepAliveSnapshot) bool {
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
