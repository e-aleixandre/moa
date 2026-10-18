package agent

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// --- fake clock ---

type kaTimer struct {
	at      time.Time
	f       func()
	stopped bool
	fired   bool
}

type kaClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*kaTimer
}

func newKAClock() *kaClock { return &kaClock{now: time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)} }

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

// advance moves the clock by d, firing due timers in order; after each fire
// it lets the goroutines it started settle (settle is called with no lock).
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

// --- fake provider: streams text and answers prewarms ---

type kaProvider struct {
	mu        sync.Mutex
	streams   []core.Request
	pings     []core.Request
	pingAt    []time.Time
	clock     *kaClock
	usage     core.Usage
	source    *core.ProviderSource
	pingErr   error
	pingGate  chan struct{} // when set, a ping waits for it or its context
	pingDone  chan error
	streamFor time.Duration // the clock moves this much while a stream runs
	// streamGate, when set, holds every stream until closed; streamStarted
	// is told when one is waiting.
	streamGate    chan struct{}
	streamStarted chan struct{}
	toolCalls     []string // the next responses call these tools, one per response
}

func newKAProvider(clock *kaClock, contextTokens int) *kaProvider {
	return &kaProvider{
		clock:    clock,
		usage:    core.Usage{Input: 10, CacheRead: contextTokens - 10, Output: 50},
		source:   &core.ProviderSource{Kind: "oauth", PrimaryGeneration: "g1", WireProfile: "oauth", Provider: "anthropic", Model: "claude-opus-5-5"},
		pingDone: make(chan error, 100),
	}
}

func (p *kaProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	p.mu.Lock()
	p.streams = append(p.streams, req)
	var tool string
	if len(p.toolCalls) > 0 {
		tool, p.toolCalls = p.toolCalls[0], p.toolCalls[1:]
	}
	usage, src, d, gate, started := p.usage, p.source, p.streamFor, p.streamGate, p.streamStarted
	p.mu.Unlock()
	if gate != nil {
		started <- struct{}{}
		<-gate
	}
	if d > 0 {
		p.clock.mu.Lock()
		p.clock.now = p.clock.now.Add(d)
		p.clock.mu.Unlock()
	}
	msg := core.Message{Role: "assistant", Content: []core.Content{core.TextContent("ok")}, StopReason: "end_turn", Usage: &usage, ProviderSource: src}
	if tool != "" {
		msg.Content = append(msg.Content, core.ToolCallContent("call-"+tool, tool, map[string]any{}))
		msg.StopReason = "tool_use"
	}
	ch := make(chan core.AssistantEvent, 3)
	ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
	close(ch)
	return ch, nil
}

func (p *kaProvider) Prewarm(ctx context.Context, req core.Request) (core.Usage, error) {
	p.mu.Lock()
	p.pings = append(p.pings, req)
	p.pingAt = append(p.pingAt, p.clock.Now())
	gate, err, usage := p.pingGate, p.pingErr, p.usage
	p.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			p.pingDone <- ctx.Err()
			return core.Usage{}, ctx.Err()
		}
	}
	p.pingDone <- err
	if err != nil {
		return core.Usage{}, err
	}
	return core.Usage{Input: 3, CacheRead: usage.Input + usage.CacheRead + usage.CacheWrite - 3}, nil
}

func (p *kaProvider) pingCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.pings)
}

var kaModel = core.Model{ID: "claude-opus-5-5", Provider: "anthropic", MaxInput: 1_000_000,
	Pricing: &core.Pricing{Input: 5, Output: 25, CacheRead: 0.5, CacheWrite: 6.25, CacheWrite1h: 10}}

type kaHarness struct {
	t      *testing.T
	clock  *kaClock
	prov   *kaProvider
	agent  *Agent
	cfg    KeepAliveConfig
	mu     sync.Mutex
	events []KeepAliveEvent
	evCh   chan KeepAliveEvent
}

func newKAHarness(t *testing.T, role string, contextTokens int, cacheTTL string, tools ...core.Tool) *kaHarness {
	t.Helper()
	h := &kaHarness{t: t, clock: newKAClock(), evCh: make(chan KeepAliveEvent, 100)}
	h.prov = newKAProvider(h.clock, contextTokens)
	reg := core.NewRegistry()
	for _, tool := range tools {
		if err := reg.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	a, err := New(AgentConfig{Provider: h.prov, Model: kaModel, SystemPrompt: "system", ThinkingLevel: "high", CacheTTL: cacheTTL, Tools: reg})
	if err != nil {
		t.Fatal(err)
	}
	h.agent = a
	h.cfg = KeepAliveConfig{Policy: allRolesPolicy, Role: role, Clock: h.clock, OnEvent: func(e KeepAliveEvent) {
		h.mu.Lock()
		h.events = append(h.events, e)
		h.mu.Unlock()
		h.evCh <- e
	}}
	a.SetKeepAlive(&h.cfg)
	t.Cleanup(func() { a.SetKeepAlive(nil) })
	return h
}

func allRolesPolicy() core.KeepAlivePolicy {
	p, ok := core.GetCacheKeepAlive(core.MoaConfig{CacheKeepAlive: &core.CacheKeepAliveConfig{
		Owner: &core.KeepAliveHorizons{Enabled: true}, Session: &core.KeepAliveHorizons{Enabled: true}, Subagent: &core.KeepAliveHorizons{Enabled: true}}})
	if !ok {
		panic("roles off")
	}
	return p
}

func (h *kaHarness) send(text string) {
	h.t.Helper()
	if _, err := h.agent.Send(context.Background(), text); err != nil {
		h.t.Fatal(err)
	}
}

// advance moves time forward, letting each ping a timer starts complete.
func (h *kaHarness) advance(d time.Duration) {
	h.t.Helper()
	h.clock.advance(d, h.waitIdlePing)
}

func (h *kaHarness) waitIdlePing() {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ka := h.agent.keepAlive.Load()
		if ka == nil {
			return
		}
		ka.mu.Lock()
		busy := ka.pingCancel != nil
		ka.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(time.Millisecond)
	}
	h.t.Fatal("ping did not finish")
}

func (h *kaHarness) eventsOf(kind string) []KeepAliveEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []KeepAliveEvent
	for _, e := range h.events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func stripCallbacks(o core.StreamOptions) core.StreamOptions {
	o.OnProviderDispatch, o.OnProviderPrepare, o.OnProviderRetry = nil, nil, nil
	o.OnFastUnavailable, o.OnRequestFingerprint, o.ProviderBinding = nil, nil, nil
	return o
}

// Scenario: Un owner de Anthropic inactivo sigue caliente.
func TestKeepAliveOwnerIdleIsPingedWithTheLastRequest(t *testing.T) {
	h := newKAHarness(t, core.KeepAliveRoleOwner, 250_000, "1h")
	h.send("hello")
	h.advance(54 * time.Minute)
	if n := h.prov.pingCount(); n != 0 {
		t.Fatalf("pinged before 55 min: %d", n)
	}
	h.advance(time.Minute)
	if n := h.prov.pingCount(); n != 1 {
		t.Fatalf("pings at 55 min = %d, want 1", n)
	}
	real, ping := h.prov.streams[0], h.prov.pings[0]
	if ping.System != real.System || !reflect.DeepEqual(ping.Messages, real.Messages) || !reflect.DeepEqual(ping.Tools, real.Tools) || ping.Model.ID != real.Model.ID {
		t.Fatal("the ping is not the last request")
	}
	if !reflect.DeepEqual(stripCallbacks(ping.Options), stripCallbacks(real.Options)) {
		t.Fatalf("options differ:\nreal %+v\nping %+v", stripCallbacks(real.Options), stripCallbacks(ping.Options))
	}
	if ping.Options.OnProviderDispatch != nil || ping.Options.OnRequestFingerprint != nil {
		t.Fatal("the ping carries the turn's callbacks")
	}
	if b := ping.Options.ProviderBinding; b == nil || b.Kind != "oauth" || b.PrimaryGeneration != "g1" {
		t.Fatalf("binding = %+v, want the source that served the request", b)
	}
	pings := h.eventsOf(KeepAlivePing)
	if len(pings) != 1 || pings[0].Usage.Output != 0 || pings[0].Usage.CacheRead != 250_000-3 || pings[0].CostUSD <= 0 || pings[0].State != core.KeepAliveIdle {
		t.Fatalf("ping events = %+v", pings)
	}
	if len(h.agent.Messages()) != 2 {
		t.Fatalf("the ping entered the history: %d messages", len(h.agent.Messages()))
	}
}

// Scenario: Un evento horas después no paga escritura fría — the wake after
// pings is reported with what kept it warm.
func TestKeepAliveWakeReportsThePings(t *testing.T) {
	h := newKAHarness(t, core.KeepAliveRoleOwner, 250_000, "1h")
	h.send("hello")
	h.advance(5 * time.Hour)
	if n := h.prov.pingCount(); n != 5 {
		t.Fatalf("pings in 5 h = %d, want 5", n)
	}
	h.send("event")
	wakes := h.eventsOf(KeepAliveWake)
	if len(wakes) != 1 || wakes[0].Pings != 5 || wakes[0].PingCostUSD <= 0 || wakes[0].Slept != 5*time.Hour {
		t.Fatalf("wake = %+v", wakes)
	}
	// The pings re-anchor on the new request.
	h.advance(54 * time.Minute)
	if n := h.prov.pingCount(); n != 5 {
		t.Fatalf("pinged before 55 min of the new request: %d", n)
	}
}

// Scenario: El horizonte depende del estado y del tamaño / se agota.
func TestKeepAliveSessionHorizonStopsPings(t *testing.T) {
	h := newKAHarness(t, core.KeepAliveRoleSession, 264_000, "1h")
	h.send("hello")
	h.advance(10 * time.Hour)
	if n := h.prov.pingCount(); n != 3 {
		t.Fatalf("pings = %d, want 3 (55, 110, 165 min)", n)
	}
	if last := h.prov.pingAt[2].Sub(h.prov.pingAt[0]); last != 110*time.Minute {
		t.Fatalf("ping spacing = %v", last)
	}
}

// Scenario: Agentes pequeños y OpenAI.
func TestKeepAliveSkipsSmallAgentsAndOpenAI(t *testing.T) {
	h := newKAHarness(t, core.KeepAliveRoleSession, 80_000, "1h")
	h.send("hello")
	h.advance(3 * time.Hour)
	if n := h.prov.pingCount(); n != 0 {
		t.Fatalf("80K session pinged %d times", n)
	}

	o := newKAHarness(t, core.KeepAliveRoleOwner, 250_000, "1h")
	sol := core.Model{ID: "gpt-6.1-sol", Provider: "openai", MaxInput: 1_000_000}
	if err := o.agent.SetModel(nil, sol); err != nil {
		t.Fatal(err)
	}
	o.send("hello")
	o.advance(3 * time.Hour)
	if n := o.prov.pingCount(); n != 0 {
		t.Fatalf("OpenAI session pinged %d times", n)
	}
}

// Scenario: El contexto cambió desde la última petición.
func TestKeepAliveStopsWhenTheContextChanges(t *testing.T) {
	for name, change := range map[string]func(a *Agent) error{
		"start fresh": func(a *Agent) error {
			p, err := a.StartFresh()
			if err == nil && p == nil {
				err = errors.New("nothing was cut")
			}
			return err
		},
		"model":    func(a *Agent) error { m := kaModel; m.ID = "claude-sonnet-5-5"; return a.SetModel(nil, m) },
		"thinking": func(a *Agent) error { return a.SetThinkingLevel("low") },
		"system":   func(a *Agent) error { return a.SetSystemPrompt("another") },
		"restore":  func(a *Agent) error { return a.RestoreConversation(a.Messages()[:1], 0) },
		"stop":     func(a *Agent) error { a.Abort(); return nil },
	} {
		t.Run(name, func(t *testing.T) {
			h := newKAHarness(t, core.KeepAliveRoleOwner, 250_000, "1h")
			h.send(strings.Repeat("old context ", 40_000))
			h.send(strings.Repeat("newer context ", 40_000))
			h.send("three")
			if err := change(h.agent); err != nil {
				t.Fatal(err)
			}
			h.advance(2 * time.Hour)
			if n := h.prov.pingCount(); n != 0 {
				t.Fatalf("pinged %d times after the change", n)
			}
		})
	}
}

// Scenario: Un run arranca mientras toca un ping.
func TestKeepAliveRealRequestCancelsThePing(t *testing.T) {
	h := newKAHarness(t, core.KeepAliveRoleOwner, 250_000, "1h")
	h.prov.pingGate = make(chan struct{})
	h.send("hello")
	h.clock.advance(55*time.Minute, nil)
	deadline := time.Now().Add(5 * time.Second)
	for h.prov.pingCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if h.prov.pingCount() != 1 {
		t.Fatal("no ping in flight")
	}
	done := make(chan struct{})
	go func() { h.send("now"); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the run waited for the ping")
	}
	if err := <-h.prov.pingDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("ping ended with %v, want cancelled", err)
	}
	if len(h.eventsOf(KeepAlivePing))+len(h.eventsOf(KeepAlivePingError)) != 0 {
		t.Fatal("a cancelled ping was reported")
	}
}

func blockingTool(name string, release <-chan struct{}, started chan<- struct{}) core.Tool {
	return core.Tool{Name: name, Parameters: []byte(`{"type":"object"}`), Effect: core.EffectShell,
		Execute: func(ctx context.Context, _ map[string]any, _ func(core.Result)) (core.Result, error) {
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
			return core.TextResult("done"), nil
		}}
}

func (h *kaHarness) sendAsync(text string) chan error {
	errc := make(chan error, 1)
	go func() { _, err := h.agent.Send(context.Background(), text); errc <- err }()
	return errc
}

// Scenario: Un owner esperando una respuesta a ask_user.
func TestKeepAliveOwnerOnAskUser(t *testing.T) {
	release, started := make(chan struct{}), make(chan struct{}, 1)
	h := newKAHarness(t, core.KeepAliveRoleOwner, 250_000, "1h", blockingTool("ask_user", release, started))
	h.prov.toolCalls = []string{"ask_user"}
	errc := h.sendAsync("ask me")
	<-started
	h.advance(23 * time.Hour)
	if n := h.prov.pingCount(); n != 25 {
		t.Fatalf("pings over 23 h on ask_user = %d, want 25 (every 55 min)", n)
	}
	if st := h.eventsOf(KeepAlivePing)[0].State; st != core.KeepAliveAskUser {
		t.Fatalf("state = %q", st)
	}
	h.advance(2 * time.Hour)
	if n := h.prov.pingCount(); n != 26 {
		t.Fatalf("pings past 24 h = %d, want 26 (the last at 23 h 50)", n)
	}
	close(release)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if n := len(h.prov.streams); n != 2 {
		t.Fatalf("streams = %d", n)
	}
}

// Scenario: Hijo esperando un bash_wait largo.
func TestKeepAliveChildOnALongToolWait(t *testing.T) {
	release, started := make(chan struct{}), make(chan struct{}, 1)
	h := newKAHarness(t, core.KeepAliveRoleSubagent, 59_000, "", blockingTool("bash_wait", release, started))
	h.prov.toolCalls = []string{"bash_wait"}
	errc := h.sendAsync("task")
	<-started
	h.advance(20 * time.Minute)
	if n := h.prov.pingCount(); n != 4 {
		t.Fatalf("pings in 20 min = %d, want 4 (every 4m30s)", n)
	}
	if st := h.eventsOf(KeepAlivePing)[0].State; st != core.KeepAliveWaiting {
		t.Fatalf("state = %q", st)
	}
	close(release)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	// Run over: a child is not kept warm idle.
	h.advance(time.Hour)
	if n := h.prov.pingCount(); n != 4 {
		t.Fatalf("idle child pinged: %d", n)
	}
}

// Scenario: Un stream largo no rompe el reloj.
func TestKeepAliveAnchorsOnTheSend(t *testing.T) {
	release, started := make(chan struct{}), make(chan struct{}, 1)
	h := newKAHarness(t, core.KeepAliveRoleSubagent, 59_000, "", blockingTool("bash_wait", release, started))
	h.prov.toolCalls = []string{"bash_wait"}
	h.prov.streamFor = 4*time.Minute + 40*time.Second
	errc := h.sendAsync("task")
	<-started
	h.waitIdlePing()
	// Due 4m30s after the send: the tool start finds it overdue and pings at
	// once, inside the TTL. Anchored on the response it would wait until 9m10s.
	if n := h.prov.pingCount(); n != 1 || !h.prov.pingAt[0].Equal(newKAClock().now.Add(h.prov.streamFor)) {
		t.Fatalf("pings = %d at %v", n, h.prov.pingAt)
	}
	close(release)
	<-errc

	// A stream longer than the TTL left nothing to keep warm.
	release2, started2 := make(chan struct{}), make(chan struct{}, 1)
	h2 := newKAHarness(t, core.KeepAliveRoleSubagent, 59_000, "", blockingTool("bash_wait", release2, started2))
	h2.prov.toolCalls = []string{"bash_wait"}
	h2.prov.streamFor = 6 * time.Minute
	errc2 := h2.sendAsync("task")
	<-started2
	h2.advance(20 * time.Minute)
	if n := h2.prov.pingCount(); n != 0 {
		t.Fatalf("pinged an expired cache %d times", n)
	}
	close(release2)
	<-errc2
}

// Scenario: No ping while a request is in flight.
func TestKeepAliveNeverPingsDuringARequest(t *testing.T) {
	h := newKAHarness(t, core.KeepAliveRoleOwner, 250_000, "1h")
	h.send("one")
	gate := make(chan struct{})
	h.prov.mu.Lock()
	h.prov.streamGate, h.prov.streamStarted = gate, make(chan struct{}, 1)
	h.prov.mu.Unlock()
	errc := h.sendAsync("two")
	<-h.prov.streamStarted
	h.advance(50 * time.Minute)
	h.agent.keepAlive.Load().evaluate()
	if n := h.prov.pingCount(); n != 0 {
		t.Fatalf("pinged during the request: %d", n)
	}
	close(gate)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	// Anchored on the second request's send, 50 min before its response
	// arrived: the next ping is 5 min away, not 55.
	h.advance(4 * time.Minute)
	if n := h.prov.pingCount(); n != 0 {
		t.Fatalf("pinged before 55 min from the send: %d", n)
	}
	h.advance(time.Minute)
	if n := h.prov.pingCount(); n != 1 {
		t.Fatalf("pings 55 min after the send = %d, want 1", n)
	}
}

// Scenario: El proveedor rechaza el ping.
func TestKeepAlivePingErrorStopsUntilTheNextRun(t *testing.T) {
	h := newKAHarness(t, core.KeepAliveRoleOwner, 250_000, "1h")
	h.prov.pingErr = errors.New("anthropic: prewarm HTTP 400")
	h.send("hello")
	h.advance(3 * time.Hour)
	if n := h.prov.pingCount(); n != 1 {
		t.Fatalf("pings = %d, want 1 and no retry", n)
	}
	if errs := h.eventsOf(KeepAlivePingError); len(errs) != 1 || errs[0].Err == nil {
		t.Fatalf("errors = %+v", errs)
	}
	h.prov.mu.Lock()
	h.prov.pingErr = nil
	h.prov.mu.Unlock()
	h.send("again")
	h.advance(55 * time.Minute)
	if n := h.prov.pingCount(); n != 2 {
		t.Fatalf("not re-armed by the next run: %d", n)
	}
}

// Off is the default: an agent without SetKeepAlive never prewarms.
func TestKeepAliveOffSendsNothing(t *testing.T) {
	clock := newKAClock()
	prov := newKAProvider(clock, 250_000)
	a, err := New(AgentConfig{Provider: prov, Model: kaModel, SystemPrompt: "system", CacheTTL: "1h", Tools: core.NewRegistry()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Send(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if a.keepAlive.Load() != nil {
		t.Fatal("keep-alive on by default")
	}
	time.Sleep(10 * time.Millisecond)
	if n := prov.pingCount(); n != 0 {
		t.Fatalf("pinged %d times", n)
	}
}

// Scenario: Activación por segmento — a role left off is never pinged.
func TestKeepAliveDisabledRoleIsNotPinged(t *testing.T) {
	h := newKAHarness(t, core.KeepAliveRoleOwner, 250_000, "1h")
	p, _ := core.GetCacheKeepAlive(core.MoaConfig{CacheKeepAlive: &core.CacheKeepAliveConfig{Subagent: &core.KeepAliveHorizons{Enabled: true}}})
	h.cfg.Policy = func() core.KeepAlivePolicy { return p }
	h.agent.SetKeepAlive(&h.cfg)
	h.send("hello")
	h.advance(5 * time.Hour)
	if n := h.prov.pingCount(); n != 0 {
		t.Fatalf("owner pinged %d times with only subagents on", n)
	}
}

// Scenario: Tope diario de gasto en pings.
func TestKeepAliveDailyBudgetStopsPingsUntilTheNextDay(t *testing.T) {
	h := newKAHarness(t, core.KeepAliveRoleOwner, 250_000, "1h")
	// One ping reads 250K at $0.5/M: $0.125. The cap lets two through.
	exhausted := make(chan string, 4)
	h.cfg.Budget = NewKeepAliveBudget(0.3, h.clock.Now, func(day string, spent, capUSD float64) { exhausted <- day })
	h.agent.SetKeepAlive(&h.cfg)
	h.send("hello")
	h.advance(5 * time.Hour)
	if n := h.prov.pingCount(); n != 2 {
		t.Fatalf("pings under a $0.30 cap = %d, want 2", n)
	}
	select {
	case day := <-exhausted:
		if day != "2026-10-18" {
			t.Fatalf("exhausted day = %q", day)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the cap was not recorded")
	}
	if _, spent := h.cfg.Budget.Spent(); spent < 0.24 || spent > 0.26 {
		t.Fatalf("spent = %v, want the two pings' real cost", spent)
	}
	// Same day, new request: still capped, and recorded only once.
	h.send("again")
	h.advance(2 * time.Hour)
	if n := h.prov.pingCount(); n != 2 {
		t.Fatalf("pinged past the cap: %d", n)
	}
	if len(exhausted) != 0 {
		t.Fatal("the cap was recorded twice in a day")
	}
	// The next day the count starts over.
	h.advance(16 * time.Hour)
	h.send("tomorrow")
	h.advance(55 * time.Minute)
	if n := h.prov.pingCount(); n != 3 {
		t.Fatalf("pings the next day = %d, want 3", n)
	}
}

// Settings apply at once: a role switched off mid-sleep stops being pinged,
// and switched on again it is warmed from its next request.
func TestKeepAlivePolicyChangesApplyAtOnce(t *testing.T) {
	h := newKAHarness(t, core.KeepAliveRoleOwner, 250_000, "1h")
	var mu sync.Mutex
	on := true
	h.cfg.Policy = func() core.KeepAlivePolicy {
		mu.Lock()
		defer mu.Unlock()
		if on {
			return allRolesPolicy()
		}
		return core.KeepAlivePolicy{}
	}
	h.agent.SetKeepAlive(&h.cfg)
	h.send("hello")
	h.advance(55 * time.Minute)
	mu.Lock()
	on = false
	mu.Unlock()
	h.advance(3 * time.Hour)
	if n := h.prov.pingCount(); n != 1 {
		t.Fatalf("pings after switching off = %d, want 1", n)
	}
	h.send("off")
	if h.agent.keepAlive.Load().snap != nil {
		t.Fatal("a role that is off took a snapshot")
	}
	mu.Lock()
	on = true
	mu.Unlock()
	h.send("on")
	h.advance(55 * time.Minute)
	if n := h.prov.pingCount(); n != 2 {
		t.Fatalf("pings after switching on = %d, want 2", n)
	}
}
