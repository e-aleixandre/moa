package push

import (
	"sync"
	"time"
)

// The policy decides WHAT deserves a notification, how loud it is and when it
// leaves; a transport (Sender) only delivers. Web Push is today's transport;
// another one (APNs through a relay) plugs in behind the same Sender without
// touching a single rule here.

// Kind says why a notification exists.
type Kind string

const (
	KindAsk        Kind = "ask"        // ask_user: the agent is waiting for an answer
	KindPermission Kind = "permission" // a tool waits for approval
	KindDone       Kind = "done"       // a run the user started finished
	KindFailed     Kind = "failed"     // a run errored
	KindDigest     Kind = "digest"     // an owner finished digesting a report
	KindEvent      Kind = "event"      // a wake-on-event source delivered something
)

// Level is how hard a notification should interrupt.
type Level string

const (
	// LevelUrgent is for what blocks the agent on you: sound, alerts again when
	// a newer one replaces it.
	LevelUrgent Level = "urgent"
	// LevelActive is an ordinary notification, with sound.
	LevelActive Level = "active"
	// LevelPassive arrives without sound and replaces the previous one of its
	// group quietly.
	LevelPassive Level = "passive"
)

// Summaries is how owner digests and events are announced. It is the one knob
// of the policy that is provisional: the owner will revisit it after using it.
type Summaries string

const (
	SummariesPassive Summaries = "passive" // default: silent, one per group, replaced by the next
	SummariesActive  Summaries = "active"  // as loud as a finished run
	SummariesOff     Summaries = "off"     // never notify
)

// ParseSummaries maps a config value to a mode; anything unknown is the default.
func ParseSummaries(s string) Summaries {
	switch Summaries(s) {
	case SummariesActive, SummariesOff:
		return Summaries(s)
	}
	return SummariesPassive
}

// DefaultGrace is how long a question or permission someone is looking at on
// another device waits before it is sent anyway.
const DefaultGrace = 60 * time.Second

// Sender is a transport: it delivers a notification the policy already decided
// on. The Web Push Dispatcher is one.
type Sender interface {
	Notify(Notification)
}

// Signal is something that happened, as the policy needs to know it.
type Signal struct {
	Kind Kind
	// SessionID is what tapping the notification opens; empty with Inbox.
	SessionID string
	Inbox     bool
	// Title is the session's title: the "which one", never the specifics.
	Title string
	// Headline replaces the default headline of the kind (events name their source).
	Headline string
	// Project groups digests: one notification per project, the next replacing
	// the previous. Source does the same for events.
	Project string
	Source  string
	// RequestID identifies an ask or a permission for the grace period.
	RequestID string
	// Watched: somebody has this session visible right now.
	Watched bool
	// StillPending is asked when the grace period ends; false drops the
	// notification (answered, cancelled, session gone). Nil means yes.
	StillPending func() bool
}

// PolicyConfig tunes a Policy.
type PolicyConfig struct {
	Summaries Summaries
	// Grace overrides DefaultGrace.
	Grace time.Duration
	// After schedules f after d and returns a stop func. Tests replace it.
	After func(d time.Duration, f func()) (stop func() bool)
}

// Policy turns signals into notifications for a Sender.
type Policy struct {
	sender    Sender
	summaries Summaries
	grace     time.Duration
	after     func(d time.Duration, f func()) func() bool

	mu      sync.Mutex
	pending map[pendingKey]func() bool // grace timers, by request
}

type pendingKey struct {
	kind      Kind
	sessionID string
	requestID string
}

// NewPolicy builds a policy delivering through sender.
func NewPolicy(sender Sender, cfg PolicyConfig) *Policy {
	p := &Policy{
		sender:    sender,
		summaries: cfg.Summaries,
		grace:     cfg.Grace,
		after:     cfg.After,
		pending:   make(map[pendingKey]func() bool),
	}
	if p.summaries == "" {
		p.summaries = SummariesPassive
	}
	if p.grace <= 0 {
		p.grace = DefaultGrace
	}
	if p.after == nil {
		p.after = func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }
	}
	return p
}

// decision is what the rules say about one signal.
type decision struct {
	send  bool
	delay time.Duration
	n     Notification
}

// decide holds every rule of the policy. Tags are what replace a notification
// on the device, so they are per group, and never shared between a question and
// anything that would hide it.
func (p *Policy) decide(s Signal) decision {
	n := Notification{
		Kind:      s.Kind,
		Title:     s.Headline,
		Body:      s.Title,
		SessionID: s.SessionID,
		Inbox:     s.Inbox,
	}
	setTitle := func(def string) {
		if n.Title == "" {
			n.Title = def
		}
	}
	switch s.Kind {
	case KindAsk, KindPermission:
		// Blocking: urgent. A question being looked at elsewhere waits out the
		// grace period and goes only if it is still open; if the viewer leaves
		// sooner it still goes at the original deadline, not earlier.
		if s.Kind == KindAsk {
			setTitle("moa necesita tu decisión")
		} else {
			setTitle("moa espera tu aprobación")
		}
		n.Level = LevelUrgent
		n.Tag = "req:" + s.SessionID
		d := decision{send: true, n: n}
		if s.Watched {
			d.delay = p.grace
		}
		return d
	case KindDone:
		if s.Watched {
			return decision{}
		}
		setTitle("moa terminó")
		n.Level, n.Tag = LevelActive, "run:"+s.SessionID
		return decision{send: true, n: n}
	case KindFailed:
		if s.Watched {
			return decision{}
		}
		setTitle("moa falló")
		n.Level, n.Tag = LevelActive, "run:"+s.SessionID
		return decision{send: true, n: n}
	case KindDigest:
		if s.Watched || p.summaries == SummariesOff {
			return decision{}
		}
		setTitle("moa terminó")
		n.Level = p.summaryLevel()
		n.Tag = "project:" + s.Project
		return decision{send: true, n: n}
	case KindEvent:
		if p.summaries == SummariesOff {
			return decision{}
		}
		setTitle("moa: evento")
		n.Level = p.summaryLevel()
		n.Tag = "event:" + s.Source
		return decision{send: true, n: n}
	}
	return decision{}
}

func (p *Policy) summaryLevel() Level {
	if p.summaries == SummariesActive {
		return LevelActive
	}
	return LevelPassive
}

// Handle applies the policy to a signal.
func (p *Policy) Handle(s Signal) {
	d := p.decide(s)
	if !d.send {
		return
	}
	if d.delay <= 0 {
		p.sender.Notify(d.n)
		return
	}
	key := pendingKey{s.Kind, s.SessionID, s.RequestID}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, dup := p.pending[key]; dup {
		return
	}
	p.pending[key] = p.after(d.delay, func() {
		p.mu.Lock()
		delete(p.pending, key)
		p.mu.Unlock()
		if s.StillPending == nil || s.StillPending() {
			p.sender.Notify(d.n)
		}
	})
}

// Resolved drops the grace timer of a request that was answered.
func (p *Policy) Resolved(kind Kind, sessionID, requestID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := pendingKey{kind, sessionID, requestID}
	if stop, ok := p.pending[key]; ok {
		stop()
		delete(p.pending, key)
	}
}

// CancelSession drops every grace timer of a session that is going away.
func (p *Policy) CancelSession(sessionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, stop := range p.pending {
		if key.sessionID == sessionID {
			stop()
			delete(p.pending, key)
		}
	}
}
