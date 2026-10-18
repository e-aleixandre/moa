package core

import (
	"context"
	"errors"
	"time"
)

// CachePrewarmer is optionally implemented by providers that can refresh a
// prompt cache without generating output: Prewarm resends req as a
// zero-output request and returns its usage. It never retries.
type CachePrewarmer interface {
	Prewarm(ctx context.Context, req Request) (Usage, error)
}

// ErrPrewarmUnsupported is returned, before anything is sent, by a
// prewarm the provider cannot serve for this request or credential.
var ErrPrewarmUnsupported = errors.New("cache prewarm not supported for this request")

// ProviderPrewarmer returns p's prewarm capability, following decorators.
func ProviderPrewarmer(p Provider) (CachePrewarmer, bool) {
	for p != nil {
		if pw, ok := p.(CachePrewarmer); ok {
			return pw, true
		}
		unwrapper, ok := p.(ProviderUnwrapper)
		if !ok {
			return nil, false
		}
		p = unwrapper.Unwrap()
	}
	return nil, false
}

// Keep-alive roles: who the agent is decides how long its cache is worth
// keeping warm.
const (
	KeepAliveRoleOwner    = "owner"
	KeepAliveRoleSession  = "session"
	KeepAliveRoleSubagent = "subagent"
)

// Keep-alive states: what an agent that is not calling the model is waiting
// for.
const (
	KeepAliveIdle    = "idle"     // nothing pending
	KeepAliveAskUser = "ask_user" // blocked on a question to the user
	KeepAliveWaiting = "waiting"  // blocked on a tool, or idle with background work (bash job, async subagent)
)

// CacheKeepAliveConfig is the "cache_keepalive" setting. Each role is
// switched on on its own ("owner": {"enabled": true}); absent, every role is
// off and no ping is ever sent. A segment inside an enabled role is switched
// off with a "0" horizon. Every other field is optional and falls back to the
// preset (DefaultCacheKeepAlive).
type CacheKeepAliveConfig struct {
	// LargeContextTokens splits the "_large" horizons from the others.
	LargeContextTokens int `json:"large_context_tokens,omitempty"`
	// PingEvery1h and PingEvery5m are the ping periods for each cache TTL,
	// as Go durations: how long after a request the next ping goes.
	PingEvery1h string             `json:"ping_every_1h,omitempty"`
	PingEvery5m string             `json:"ping_every_5m,omitempty"`
	Owner       *KeepAliveHorizons `json:"owner,omitempty"`
	Session     *KeepAliveHorizons `json:"session,omitempty"`
	Subagent    *KeepAliveHorizons `json:"subagent,omitempty"`
}

// KeepAliveHorizons is how long after its last request an agent of one role
// is kept warm, per state, as Go durations ("0" = never). Agents whose last
// request was smaller than MinContextTokens are never kept warm.
type KeepAliveHorizons struct {
	Enabled          bool   `json:"enabled"`
	MinContextTokens *int   `json:"min_context_tokens,omitempty"`
	Idle             string `json:"idle,omitempty"`
	IdleLarge        string `json:"idle_large,omitempty"`
	AskUser          string `json:"ask_user,omitempty"`
	AskUserLarge     string `json:"ask_user_large,omitempty"`
	Waiting          string `json:"waiting,omitempty"`
	WaitingLarge     string `json:"waiting_large,omitempty"`
}

// KeepAlivePolicy is a resolved cache_keepalive setting.
type KeepAlivePolicy struct {
	LargeContextTokens int
	PingEvery1h        time.Duration
	PingEvery5m        time.Duration
	Roles              map[string]KeepAliveRolePolicy
}

// KeepAliveRolePolicy is the resolved horizons of one role.
type KeepAliveRolePolicy struct {
	Enabled                               bool
	MinContextTokens                      int
	Idle, AskUser, Waiting                time.Duration
	IdleLarge, AskUserLarge, WaitingLarge time.Duration
}

// DefaultCacheKeepAlive is the preset: the per-case policy of 18/10/2026
// (warm while the chance of a wake in the next hour pays for the ping).
var DefaultCacheKeepAlive = KeepAlivePolicy{
	LargeContextTokens: 200_000,
	PingEvery1h:        55 * time.Minute,
	PingEvery5m:        4*time.Minute + 30*time.Second,
	Roles: map[string]KeepAliveRolePolicy{
		KeepAliveRoleOwner: {
			MinContextTokens: 100_000,
			Idle:             3 * time.Hour, IdleLarge: 16 * time.Hour,
			AskUser: 12 * time.Hour, AskUserLarge: 24 * time.Hour,
			Waiting: 3 * time.Hour, WaitingLarge: 3 * time.Hour,
		},
		KeepAliveRoleSession: {
			MinContextTokens: 100_000,
			Idle:             time.Hour, IdleLarge: 3 * time.Hour,
			AskUser: 6 * time.Hour, AskUserLarge: 6 * time.Hour,
			Waiting: 3 * time.Hour, WaitingLarge: 3 * time.Hour,
		},
		// A child is only kept warm while its own run waits on a tool: once
		// the run ends nothing says its parent will come back.
		KeepAliveRoleSubagent: {
			AskUser: time.Hour, AskUserLarge: time.Hour,
			Waiting: time.Hour, WaitingLarge: time.Hour,
		},
	},
}

// GetCacheKeepAlive resolves the setting. ok is false when no role is
// enabled, which is the default. A malformed duration keeps the preset value:
// a hand-edited config must not stop sessions from loading.
func GetCacheKeepAlive(cfg MoaConfig) (KeepAlivePolicy, bool) {
	c := cfg.CacheKeepAlive
	if c == nil {
		return KeepAlivePolicy{}, false
	}
	p := DefaultCacheKeepAlive
	p.Roles = make(map[string]KeepAliveRolePolicy, len(DefaultCacheKeepAlive.Roles))
	for k, v := range DefaultCacheKeepAlive.Roles {
		p.Roles[k] = v
	}
	if c.LargeContextTokens > 0 {
		p.LargeContextTokens = c.LargeContextTokens
	}
	overrideDuration(&p.PingEvery1h, c.PingEvery1h, true)
	overrideDuration(&p.PingEvery5m, c.PingEvery5m, true)
	// A ping after the cache has expired writes it cold again.
	if p.PingEvery1h >= time.Hour {
		p.PingEvery1h = DefaultCacheKeepAlive.PingEvery1h
	}
	if p.PingEvery5m >= 5*time.Minute {
		p.PingEvery5m = DefaultCacheKeepAlive.PingEvery5m
	}
	for role, h := range map[string]*KeepAliveHorizons{KeepAliveRoleOwner: c.Owner, KeepAliveRoleSession: c.Session, KeepAliveRoleSubagent: c.Subagent} {
		if h == nil {
			continue
		}
		r := p.Roles[role]
		r.Enabled = h.Enabled
		if h.MinContextTokens != nil && *h.MinContextTokens >= 0 {
			r.MinContextTokens = *h.MinContextTokens
		}
		overrideDuration(&r.Idle, h.Idle, false)
		overrideDuration(&r.IdleLarge, h.IdleLarge, false)
		overrideDuration(&r.AskUser, h.AskUser, false)
		overrideDuration(&r.AskUserLarge, h.AskUserLarge, false)
		overrideDuration(&r.Waiting, h.Waiting, false)
		overrideDuration(&r.WaitingLarge, h.WaitingLarge, false)
		p.Roles[role] = r
	}
	for _, r := range p.Roles {
		if r.Enabled {
			return p, true
		}
	}
	return KeepAlivePolicy{}, false
}

// RoleEnabled reports whether agents of role are kept warm at all.
func (p KeepAlivePolicy) RoleEnabled(role string) bool {
	return p.Roles[role].Enabled
}

func overrideDuration(dst *time.Duration, s string, positive bool) {
	if s == "" {
		return
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 || (positive && d == 0) {
		return
	}
	*dst = d
}

// Horizon is how long after its last request an agent of role, in state,
// whose last request carried contextTokens, is kept warm. 0 = not at all.
func (p KeepAlivePolicy) Horizon(role, state string, contextTokens int) time.Duration {
	r, ok := p.Roles[role]
	if !ok || !r.Enabled || contextTokens < r.MinContextTokens {
		return 0
	}
	large := contextTokens >= p.LargeContextTokens
	pick := func(small, big time.Duration) time.Duration {
		if large {
			return big
		}
		return small
	}
	switch state {
	case KeepAliveIdle:
		return pick(r.Idle, r.IdleLarge)
	case KeepAliveAskUser:
		return pick(r.AskUser, r.AskUserLarge)
	case KeepAliveWaiting:
		return pick(r.Waiting, r.WaitingLarge)
	}
	return 0
}

// PingEvery is the ping period for a cache that lives ttl, or 0 when the
// TTL has no period (no cache).
func (p KeepAlivePolicy) PingEvery(ttl time.Duration) time.Duration {
	switch ttl {
	case time.Hour:
		return p.PingEvery1h
	case 5 * time.Minute:
		return p.PingEvery5m
	}
	return 0
}
