package serve

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

// KeepAliveInfo is a session's cache keep-alive activity since it was loaded,
// kept apart from the rest of its usage: what the pings cost, and how many
// wakes found the cache they kept warm.
type KeepAliveInfo struct {
	Pings     int     `json:"pings"`
	CostUSD   float64 `json:"cost_usd"`
	Errors    int     `json:"errors,omitempty"`
	Wakes     int     `json:"wakes"`
	WarmWakes int     `json:"warm_wakes"`
	// SavedUSD estimates what the warm wakes did not pay: writing the prefix
	// they read cold, minus reading it (see keepAliveSaving).
	SavedUSD float64 `json:"saved_usd"`
}

// warmWakeShare is how much of the kept prefix a wake must read from the
// cache to count as warm.
const warmWakeShare = 0.95

// keepAliveRecord is one line of keepalive.jsonl, the durable trace the net
// saving is measured from: every ping, failed ping and wake.
type keepAliveRecord struct {
	At            time.Time `json:"at"`
	Kind          string    `json:"kind"`
	SessionID     string    `json:"session_id,omitempty"`
	JobID         string    `json:"job_id,omitempty"`
	Role          string    `json:"role,omitempty"`
	State         string    `json:"state,omitempty"`
	Model         string    `json:"model,omitempty"`
	ContextTokens int       `json:"context_tokens,omitempty"`
	Input         int       `json:"input,omitempty"`
	CacheRead     int       `json:"cache_read,omitempty"`
	CacheWrite    int       `json:"cache_write,omitempty"`
	Output        int       `json:"output,omitempty"`
	CostUSD       float64   `json:"cost_usd,omitempty"`
	Pings         int       `json:"pings,omitempty"`
	PingCostUSD   float64   `json:"ping_cost_usd,omitempty"`
	SleptSeconds  int64     `json:"slept_s,omitempty"`
	Warm          bool      `json:"warm,omitempty"`
	SavedUSD      float64   `json:"saved_usd,omitempty"`
	Error         string    `json:"error,omitempty"`
}

// keepAliveService is the server-wide part of keep-alive: the setting in
// force, today's totals and the trace file.
type keepAliveService struct {
	// setting is the global cache_keepalive setting in force, replaced
	// when Settings saves it (nil = everything off).
	setting atomic.Pointer[core.CacheKeepAliveConfig]
	group   agent.KeepAliveGroup
	logPath string
	clock   agent.KeepAliveClock // nil = system; tests only
	mu      sync.Mutex
	// Today's totals across every session, for Settings. In memory: a
	// restart starts the day over, and no ping survives a restart anyway.
	day        string
	spentToday float64
	savedToday float64
}

func (k *keepAliveService) now() time.Time {
	if k.clock != nil {
		return k.clock.Now()
	}
	return time.Now()
}

// policy is the setting in force, resolved; every role off when unset.
func (k *keepAliveService) policy() core.KeepAlivePolicy {
	p, _ := core.GetCacheKeepAlive(core.MoaConfig{CacheKeepAlive: k.setting.Load()})
	return p
}

// set replaces the setting in force; live agents read it at their next
// request or ping.
func (k *keepAliveService) set(c *core.CacheKeepAliveConfig) {
	if c != nil {
		cp := *c
		c = &cp
	}
	k.setting.Store(c)
	// A role switched off stops now, ping in flight included, not at its
	// next timer.
	p, _ := core.GetCacheKeepAlive(core.MoaConfig{CacheKeepAlive: c})
	k.group.Stop(func(role string) bool { return !p.RoleEnabled(role) })
}

// today returns what pings cost today and the estimated saving of the
// warm wakes they bought, across every session.
func (k *keepAliveService) today() (spentUSD, savedUSD float64) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.rollLocked()
	return k.spentToday, k.savedToday
}

func (k *keepAliveService) add(spentUSD, savedUSD float64) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.rollLocked()
	k.spentToday += spentUSD
	k.savedToday += savedUSD
}

func (k *keepAliveService) rollLocked() {
	if day := k.now().Format("2006-01-02"); day != k.day {
		k.day, k.spentToday, k.savedToday = day, 0, 0
	}
}

// keepAliveSaving estimates what a warm wake after pings did not pay: the
// prefix it read from the cache, written cold at the window's rate, minus
// reading it. Only a wake that slept past the TTL counts: a shorter sleep
// would have found the cache warm without any ping.
func keepAliveSaving(e agent.KeepAliveEvent, ttl time.Duration) float64 {
	p := e.Model.Pricing
	if p == nil || ttl <= 0 || e.Slept < ttl || e.Usage.CacheRead <= 0 {
		return 0
	}
	n := e.Usage.CacheRead
	cold := core.Usage{CacheWrite: n}
	if ttl >= time.Hour {
		cold.CacheWrite1h = n
	}
	if saved := p.Cost(cold) - p.Cost(core.Usage{CacheRead: n}); saved > 0 {
		return saved
	}
	return 0
}

func (k *keepAliveService) record(r keepAliveRecord) {
	if k.logPath == "" {
		return
	}
	line, err := json.Marshal(r)
	if err != nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	f, err := os.OpenFile(k.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		slog.Warn("cache keep-alive: trace not written", "error", err)
		return
	}
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
}

// newKeepAliveService starts from the global setting saved on disk. The
// setting is global-only, so the empty directory reads exactly that.
func newKeepAliveService(loader func(string) core.MoaConfig, sessionBaseDir string) *keepAliveService {
	k := &keepAliveService{logPath: keepAliveLogPath(sessionBaseDir)}
	if loader == nil {
		k.set(core.LoadGlobalConfig().CacheKeepAlive)
	} else {
		k.set(loader("").CacheKeepAlive)
	}
	return k
}

// keepAliveLogPath puts the trace beside the session stores.
func keepAliveLogPath(sessionBaseDir string) string {
	if sessionBaseDir == "" {
		sessionBaseDir = core.ConfigSubdir("sessions")
	}
	if sessionBaseDir == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(sessionBaseDir), "keepalive.jsonl")
}

// keepAliveConfig is the keep-alive of one agent of sess (jobID set for a
// subagent). It follows the setting live: while the role is off (the
// default) the agent takes no snapshot and sends nothing.
func (m *Manager) keepAliveConfig(sess *ManagedSession, role, jobID string) *agent.KeepAliveConfig {
	if m.keepAlive == nil {
		return nil
	}
	k := m.keepAlive
	cfg := &agent.KeepAliveConfig{
		Policy: k.policy,
		Group:  &k.group,
		Role:   role,
		Clock:  m.keepAlive.clock,
		OnEvent: func(e agent.KeepAliveEvent) {
			m.onKeepAliveEvent(sess, jobID, e)
		},
	}
	if role != core.KeepAliveRoleSubagent {
		cfg.PendingWork = func() bool { return sess.runtime.BackgroundWork() > 0 }
	}
	if role == core.KeepAliveRoleSession && m.tasks != nil {
		// A session whose next wake is a known scheduled task pays it cold:
		// at the usual distance of one, the pings cost as much as the write.
		cfg.Exempt = func() bool {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			scheduled, err := m.sessionScheduled(ctx, sess.ID)
			return err != nil || len(scheduled) > 0
		}
	}
	return cfg
}

// subscribeKeepAlive turns keep-alive on for the session's own agent, and
// off again when the session closes or is deleted.
func (m *Manager) subscribeKeepAlive(sess *ManagedSession, owner bool) {
	role := core.KeepAliveRoleSession
	if owner {
		role = core.KeepAliveRoleOwner
	}
	cfg := m.keepAliveConfig(sess, role, "")
	ag, ok := sess.runtime.Context().Agent.(interface {
		SetKeepAlive(*agent.KeepAliveConfig)
	})
	if cfg == nil || !ok {
		return
	}
	ag.SetKeepAlive(cfg)
	sess.pushUnsubs = append(sess.pushUnsubs, func() { ag.SetKeepAlive(nil) })
}

func (m *Manager) onKeepAliveEvent(sess *ManagedSession, jobID string, e agent.KeepAliveEvent) {
	rec := keepAliveRecord{
		At: e.At, Kind: e.Kind, SessionID: sess.ID, JobID: jobID, Role: e.Role, State: e.State, Model: e.Model.ID,
		ContextTokens: e.ContextTokens, Input: e.Usage.Input, CacheRead: e.Usage.CacheRead, CacheWrite: e.Usage.CacheWrite,
		Output: e.Usage.Output, CostUSD: e.CostUSD, Pings: e.Pings, PingCostUSD: e.PingCostUSD, SleptSeconds: int64(e.Slept / time.Second),
	}
	if e.Err != nil {
		rec.Error = e.Err.Error()
	}
	sess.mu.Lock()
	switch e.Kind {
	case agent.KeepAlivePing:
		sess.keepAlive.Pings++
		sess.keepAlive.CostUSD += e.CostUSD
		// The ping refreshed the cache: the warm/cold indicator follows it.
		// Only the session's own agent: a child's cache is not the session's.
		if jobID == "" && sess.lastRunProvider == e.Model.Provider && e.At.After(sess.lastRunAt) {
			sess.lastRunAt = e.At
		}
	case agent.KeepAlivePingError:
		sess.keepAlive.Errors++
	case agent.KeepAliveWake:
		sess.keepAlive.Wakes++
		rec.Warm = e.ContextTokens > 0 && float64(e.Usage.CacheRead) >= warmWakeShare*float64(e.ContextTokens)
		if rec.Warm {
			sess.keepAlive.WarmWakes++
			// A child runs on the provider's 5-minute cache; the session's
			// own agent on the configured window.
			ttl := sess.cacheTTL
			if jobID != "" {
				ttl = 5 * time.Minute
			}
			rec.SavedUSD = keepAliveSaving(e, ttl)
			sess.keepAlive.SavedUSD += rec.SavedUSD
		}
	}
	totals := bus.KeepAliveUpdated{SessionID: sess.ID, Pings: sess.keepAlive.Pings, CostUSD: sess.keepAlive.CostUSD, SavedUSD: sess.keepAlive.SavedUSD}
	sess.mu.Unlock()
	if e.Kind == agent.KeepAlivePing || totals.SavedUSD > 0 {
		sess.runtime.Bus.Publish(totals)
	}
	switch e.Kind {
	case agent.KeepAlivePing:
		m.keepAlive.add(e.CostUSD, 0)
	case agent.KeepAliveWake:
		m.keepAlive.add(0, rec.SavedUSD)
	}
	if e.Kind == agent.KeepAlivePing && e.CostUSD > 0 {
		m.chargeKeepAlive(sess.ID, bus.KeepAliveUsage{SessionID: sess.ID, JobID: jobID, Usage: e.Usage, CostUSD: e.CostUSD})
	}
	m.keepAlive.record(rec)
}

// chargeKeepAlive adds a ping's cost to the session total wherever the
// session is now. A ping cancelled by a close can still have been served:
// then the session is no longer live and the cost goes to its saved file,
// under the same reservation resume, close and delete honor.
func (m *Manager) chargeKeepAlive(id string, usage bus.KeepAliveUsage) {
	for wait := time.Duration(0); ; wait += 50 * time.Millisecond {
		if wait > 30*time.Second {
			slog.Warn("cache keep-alive: session busy, ping cost not added to it", "session", id, "cost_usd", usage.CostUSD)
			return
		}
		if wait > 0 {
			time.Sleep(50 * time.Millisecond)
		}
		m.mu.Lock()
		if _, busy := m.resuming[id]; busy {
			m.mu.Unlock()
			continue // closing or reopening: decide once it settles
		}
		live, loaded := m.sessions[id]
		if loaded && live == nil {
			m.mu.Unlock()
			continue
		}
		if loaded {
			m.mu.Unlock()
			if _, err := live.runtime.Bus.Query(bus.GetSessionCost{}); !errors.Is(err, bus.ErrClosed) {
				live.runtime.Bus.Publish(usage)
				return
			}
			// Shut down: its runtime is closed but still listed; nothing
			// else writes the file any more.
		} else {
			m.resuming[id] = struct{}{}
			m.mu.Unlock()
			defer func() {
				m.mu.Lock()
				delete(m.resuming, id)
				m.mu.Unlock()
			}()
		}
		if err := m.addSavedSessionCost(id, usage.CostUSD); err != nil {
			slog.Warn("cache keep-alive: ping cost not saved", "session", id, "error", err)
		}
		return
	}
}

func (m *Manager) addSavedSessionCost(id string, usd float64) error {
	saved, store, err := session.FindSession(m.sessionBaseDir, id)
	if err != nil {
		return err
	}
	if saved.Metadata == nil {
		saved.Metadata = map[string]any{}
	}
	prev, _ := saved.Metadata[session.MetaSessionCostUSD].(float64)
	if prev < 0 || math.IsNaN(prev) || math.IsInf(prev, 0) {
		prev = 0
	}
	saved.Metadata[session.MetaSessionCostUSD] = prev + usd
	if err := store.Save(saved); err != nil {
		return err
	}
	m.invalidateSavedCache()
	return nil
}
