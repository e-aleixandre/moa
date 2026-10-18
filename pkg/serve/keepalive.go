package serve

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
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
}

// warmWakeShare is how much of the kept prefix a wake must read from the
// cache to count as warm.
const warmWakeShare = 0.95

// keepAliveRecord is one line of keepalive.jsonl, the durable trace the net
// saving is measured from: every ping, failed ping, wake and daily cap.
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
	Error         string    `json:"error,omitempty"`
	BudgetUSD     float64   `json:"budget_usd,omitempty"`
}

// keepAliveService is the server-wide part of keep-alive: the daily budget
// every agent shares and the trace file.
type keepAliveService struct {
	// setting is the global cache_keepalive setting in force, replaced
	// when Settings saves it (nil = everything off).
	setting atomic.Pointer[core.CacheKeepAliveConfig]
	logPath string
	clock   agent.KeepAliveClock // nil = system; tests only
	mu      sync.Mutex
	budget  *agent.KeepAliveBudget
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
	if p.DailyBudgetUSD > 0 {
		k.budgetFor(p.DailyBudgetUSD)
	}
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
}

// spentToday is what pings have cost today, across every session.
func (k *keepAliveService) spentToday() float64 {
	k.mu.Lock()
	b := k.budget
	k.mu.Unlock()
	if b == nil {
		return 0
	}
	_, spent := b.Spent()
	return spent
}

// budgetFor returns the shared budget, following the configured cap.
func (k *keepAliveService) budgetFor(capUSD float64) *agent.KeepAliveBudget {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.budget == nil {
		k.budget = agent.NewKeepAliveBudget(capUSD, k.now, func(day string, spent, capUSD float64) {
			slog.Warn("cache keep-alive: daily budget reached, no pings until tomorrow", "day", day, "spent_usd", spent, "budget_usd", capUSD)
			k.record(keepAliveRecord{At: k.now(), Kind: "budget_reached", CostUSD: spent, BudgetUSD: capUSD})
		})
	} else {
		k.budget.SetCap(capUSD)
	}
	return k.budget
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
		Role:   role,
		Budget: k.budgetFor(core.DefaultCacheKeepAlive.DailyBudgetUSD),
		Clock:  m.keepAlive.clock,
		OnEvent: func(e agent.KeepAliveEvent) {
			m.onKeepAliveEvent(sess, jobID, e)
		},
	}
	if role != core.KeepAliveRoleSubagent {
		cfg.PendingWork = func() bool { return sess.runtime.BackgroundWork() > 0 }
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
		}
	}
	sess.mu.Unlock()
	if e.Kind == agent.KeepAlivePing && e.CostUSD > 0 {
		sess.runtime.Bus.Publish(bus.KeepAliveUsage{SessionID: sess.ID, JobID: jobID, Usage: e.Usage, CostUSD: e.CostUSD})
	}
	m.keepAlive.record(rec)
}
