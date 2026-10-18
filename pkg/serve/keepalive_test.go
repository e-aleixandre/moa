package serve

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/core"
)

// --- fake clock (the agent package keeps its own copy unexported) ---

type kaTimer struct {
	at             time.Time
	f              func()
	stopped, fired bool
}

type kaClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*kaTimer
}

// newKAClock starts at the real now: the session's cache clock is anchored on
// real time by MessageStarted, and a ping only moves it forward from there.
func newKAClock() *kaClock { return &kaClock{now: time.Now()} }

func (c *kaClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *kaClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &kaTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		was := !t.stopped && !t.fired
		t.stopped = true
		return was
	}
}

// advance moves the clock by d, firing due timers in order; settle runs after
// each one with no lock held.
func (c *kaClock) advance(d time.Duration, settle func()) {
	c.mu.Lock()
	target := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		sort.SliceStable(c.timers, func(i, j int) bool { return c.timers[i].at.Before(c.timers[j].at) })
		var next *kaTimer
		for _, t := range c.timers {
			if !t.stopped && !t.fired && !t.at.After(target) {
				next = t
				break
			}
		}
		if next == nil {
			c.now = target
			c.mu.Unlock()
			return
		}
		next.fired = true
		if next.at.After(c.now) {
			c.now = next.at
		}
		c.mu.Unlock()
		next.f()
		if settle != nil {
			settle()
		}
	}
}

// --- fake provider ---

const kaContextTokens = 250_000

type kaProvider struct {
	mu    sync.Mutex
	pings int
}

func (p *kaProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	if isTestAutoTitleRequest(req) {
		return simpleResponse("title"), nil
	}
	usage := core.Usage{Input: 10, CacheRead: kaContextTokens - 10, Output: 50}
	msg := core.Message{
		Role: "assistant", Provider: "anthropic", Content: []core.Content{core.TextContent("ping-test")}, StopReason: "end_turn",
		Usage: &usage, Timestamp: time.Now().Unix(),
		ProviderSource: &core.ProviderSource{Kind: "oauth", PrimaryGeneration: "g1", WireProfile: "oauth", Provider: "anthropic", Model: "claude-opus-5-5"},
	}
	ch := make(chan core.AssistantEvent, 3)
	ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
	close(ch)
	return ch, nil
}

func (p *kaProvider) Prewarm(ctx context.Context, req core.Request) (core.Usage, error) {
	p.mu.Lock()
	p.pings++
	p.mu.Unlock()
	return core.Usage{Input: 3, CacheRead: kaContextTokens - 3}, nil
}

func (p *kaProvider) pingCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pings
}

type kaServeHarness struct {
	t     *testing.T
	mgr   *Manager
	prov  *kaProvider
	clock *kaClock
	sess  *ManagedSession
	dir   string // the session base dir; keepalive.jsonl sits beside it
}

// newKAServe builds a manager whose global config is cfg and runs one prompt
// on an Opus 5.5 session, so the keep-alive timer is armed on a fake clock.
func newKAServe(t *testing.T, ka *core.CacheKeepAliveConfig) *kaServeHarness {
	t.Helper()
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	moaCfg := core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off", CacheKeepAlive: ka}
	prov := &kaProvider{}
	base := filepath.Join(t.TempDir(), "sessions")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	model, _ := core.ResolveModel("claude-opus-5-5")
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return prov, nil },
		DefaultModel:    model,
		WorkspaceRoot:   t.TempDir(),
		MoaCfg:          moaCfg,
		ConfigLoader:    isolatedTestConfigLoader(t, moaCfg),
		SessionBaseDir:  base,
		SchedulePath:    filepath.Join(t.TempDir(), "schedules.json"),
	})
	t.Cleanup(func() {
		mgr.mu.RLock()
		ids := make([]string, 0, len(mgr.sessions))
		for id := range mgr.sessions {
			ids = append(ids, id)
		}
		mgr.mu.RUnlock()
		for _, id := range ids {
			_ = mgr.Delete(id)
		}
		mgr.Shutdown()
	})
	h := &kaServeHarness{t: t, mgr: mgr, prov: prov, clock: newKAClock(), dir: base}
	mgr.keepAlive.clock = h.clock
	sess, err := mgr.CreateSession(CreateOpts{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	h.sess = sess
	sendAndWaitIdle(t, mgr, sess, "hello", "")
	return h
}

func (h *kaServeHarness) advance(d time.Duration) {
	h.t.Helper()
	h.clock.advance(d, func() { time.Sleep(50 * time.Millisecond) })
}

func (h *kaServeHarness) logLines() []map[string]any {
	h.t.Helper()
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(h.dir), "keepalive.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		h.t.Fatal(err)
	}
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			h.t.Fatalf("bad trace line %q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

func (h *kaServeHarness) kindCount(kind string) int {
	n := 0
	for _, l := range h.logLines() {
		if l["kind"] == kind {
			n++
		}
	}
	return n
}

func allKA() *core.CacheKeepAliveConfig {
	return &core.CacheKeepAliveConfig{
		Owner:    &core.KeepAliveHorizons{Enabled: true},
		Session:  &core.KeepAliveHorizons{Enabled: true},
		Subagent: &core.KeepAliveHorizons{Enabled: true},
	}
}

func TestKeepAlive_OffByDefaultChangesNothing(t *testing.T) {
	h := newKAServe(t, nil)
	before := sessionCostOf(t, h.sess)
	if before <= 0 {
		t.Fatalf("test setup: the prompt cost %v, want > 0", before)
	}
	h.advance(4 * time.Hour)
	if n := h.prov.pingCount(); n != 0 {
		t.Errorf("pings = %d with keep-alive unset, want 0", n)
	}
	if info := h.sess.info(); info.KeepAlive != nil {
		t.Errorf("SessionInfo.KeepAlive = %+v, want nil", info.KeepAlive)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(h.dir), "keepalive.jsonl")); !os.IsNotExist(err) {
		t.Errorf("keepalive.jsonl exists (err=%v), want no trace file", err)
	}
	if after := sessionCostOf(t, h.sess); after != before {
		t.Errorf("session cost moved %v -> %v", before, after)
	}
}

func TestKeepAlive_SessionPingIsCountedAndCharged(t *testing.T) {
	h := newKAServe(t, allKA())
	before := sessionCostOf(t, h.sess)
	expiry0 := h.sess.info().CacheExpiresAt
	if expiry0.IsZero() {
		t.Fatal("test setup: no cache expiry after the prompt")
	}

	h.advance(54 * time.Minute)
	if n := h.prov.pingCount(); n != 0 {
		t.Fatalf("pings at 54m = %d, want 0", n)
	}
	h.advance(2 * time.Minute) // 56m: the 55m ping has gone
	pollUntil(t, 2*time.Second, "ping accounted", func() bool {
		ka := h.sess.info().KeepAlive
		return ka != nil && ka.Pings == 1 && sessionCostOf(t, h.sess) > before
	})
	if n := h.prov.pingCount(); n != 1 {
		t.Errorf("pings = %d, want 1", n)
	}
	info := h.sess.info()
	if info.KeepAlive.CostUSD <= 0 {
		t.Errorf("KeepAlive.CostUSD = %v, want > 0", info.KeepAlive.CostUSD)
	}
	if got, want := sessionCostOf(t, h.sess)-before, info.KeepAlive.CostUSD; math.Abs(got-want) > 1e-9 {
		t.Errorf("session cost grew by %v, ping cost %v", got, want)
	}
	if !info.CacheExpiresAt.After(expiry0) {
		t.Errorf("CacheExpiresAt %v did not move past %v", info.CacheExpiresAt, expiry0)
	}
	if n := h.kindCount("ping"); n != 1 {
		t.Errorf("ping trace lines = %d, want 1", n)
	}
	if spent, _ := h.mgr.keepAlive.today(); math.Abs(spent-info.KeepAlive.CostUSD) > 1e-9 {
		t.Errorf("spent today = %v, want the ping's %v", spent, info.KeepAlive.CostUSD)
	}
}

func TestKeepAlive_OnlySubagentDoesNotPingTheSession(t *testing.T) {
	h := newKAServe(t, &core.CacheKeepAliveConfig{Subagent: &core.KeepAliveHorizons{Enabled: true}})
	h.advance(3 * time.Hour)
	if n := h.prov.pingCount(); n != 0 {
		t.Errorf("pings = %d with only subagent enabled, want 0", n)
	}
	if h.sess.info().KeepAlive != nil {
		t.Error("session reports keep-alive activity")
	}
}

// A wake that slept past the TTL and found the cache warm thanks to the
// pings is credited with the cold write it did not pay.
func TestKeepAlive_WarmWakeIsCreditedWithTheSaving(t *testing.T) {
	h := newKAServe(t, allKA())
	h.advance(2 * time.Hour) // pings at 55 and 110 min
	pollUntil(t, 2*time.Second, "two pings", func() bool { return h.kindCount("ping") == 2 })
	sendAndWaitIdle(t, h.mgr, h.sess, "back", "")
	pollUntil(t, 2*time.Second, "wake accounted", func() bool {
		ka := h.sess.info().KeepAlive
		return ka != nil && ka.WarmWakes == 1
	})
	ka := h.sess.info().KeepAlive
	model, _ := core.ResolveModel("claude-opus-5-5")
	n := kaContextTokens - 10
	want := model.Pricing.Cost(core.Usage{CacheWrite: n, CacheWrite1h: n}) - model.Pricing.Cost(core.Usage{CacheRead: n})
	if math.Abs(ka.SavedUSD-want) > 1e-9 || want <= 0 {
		t.Errorf("SavedUSD = %v, want %v", ka.SavedUSD, want)
	}
	if spent, saved := h.mgr.keepAlive.today(); math.Abs(saved-want) > 1e-9 || spent <= 0 || spent >= saved {
		t.Errorf("today spent=%v saved=%v, want saved %v above spent", spent, saved, want)
	}
	var wake map[string]any
	for _, l := range h.logLines() {
		if l["kind"] == "wake" {
			wake = l
		}
	}
	if wake == nil || wake["warm"] != true || wake["saved_usd"] == nil {
		t.Errorf("wake trace = %v", wake)
	}
}

func TestKeepAliveSavingNeedsASleepPastTheTTL(t *testing.T) {
	model, _ := core.ResolveModel("claude-opus-5-5")
	e := agent.KeepAliveEvent{Model: model, ContextTokens: 200_000, Usage: core.Usage{CacheRead: 200_000}, Slept: 58 * time.Minute}
	if got := keepAliveSaving(e, time.Hour); got != 0 {
		t.Errorf("a wake within the TTL saved %v", got)
	}
	e.Slept = 6 * time.Minute
	want := model.Pricing.Cost(core.Usage{CacheWrite: 200_000}) - model.Pricing.Cost(core.Usage{CacheRead: 200_000})
	if got := keepAliveSaving(e, 5*time.Minute); math.Abs(got-want) > 1e-9 {
		t.Errorf("5m saving = %v, want %v (base write rate)", got, want)
	}
}

func TestKeepAlive_SettingChangeAppliesToLiveSession(t *testing.T) {
	h := newKAServe(t, nil)
	h.advance(2 * time.Hour)
	if n := h.prov.pingCount(); n != 0 {
		t.Fatalf("pings while off = %d", n)
	}
	h.mgr.keepAlive.set(allKA())
	// The next request snapshots the conversation; the same session, not
	// a new one.
	sendAndWaitIdle(t, h.mgr, h.sess, "again", "")
	h.advance(56 * time.Minute)
	pollUntil(t, 2*time.Second, "ping after enabling", func() bool { return h.prov.pingCount() == 1 })

	h.mgr.keepAlive.set(nil)
	h.advance(2 * time.Hour)
	if n := h.prov.pingCount(); n != 1 {
		t.Errorf("pings after switching off = %d, want still 1", n)
	}
}
