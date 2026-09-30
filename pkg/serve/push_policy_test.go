package serve

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/events"
	"github.com/e-aleixandre/moa/pkg/push"
)

// pushProbe stands in for the transport and the clock: it records what the
// policy delivers and lets a test decide when a grace period ends.
type pushProbe struct {
	mu     sync.Mutex
	sent   []push.Notification
	timers []*probeTimer
}

type probeTimer struct {
	f       func()
	stopped bool
}

func (p *pushProbe) Notify(n push.Notification) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, n)
}

func (p *pushProbe) after(_ time.Duration, f func()) func() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	t := &probeTimer{f: f}
	p.timers = append(p.timers, t)
	return func() bool { p.mu.Lock(); defer p.mu.Unlock(); t.stopped = true; return true }
}

func (p *pushProbe) delivered() []push.Notification {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]push.Notification(nil), p.sent...)
}

// expire fires the grace timers that were not stopped, as a clock would.
func (p *pushProbe) expire() {
	p.mu.Lock()
	var live []func()
	for _, t := range p.timers {
		if !t.stopped {
			live = append(live, t.f)
		}
	}
	p.mu.Unlock()
	for _, f := range live {
		f()
	}
}

func (p *pushProbe) armed() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.timers)
}

// probePush installs the probe BEFORE sessions exist: subscribePush reads the
// policy when a session is created.
func probePush(mgr *Manager, summaries push.Summaries) *pushProbe {
	probe := &pushProbe{}
	mgr.pushPolicy = push.NewPolicy(probe, push.PolicyConfig{Summaries: summaries, After: probe.after})
	return probe
}

func watch(sess *ManagedSession) {
	sess.presence.join().apply([]byte(`{"type":"presence","visible":true}`))
}

// askingSession is a session whose first turn blocks on a real ask_user.
func askingSession(t *testing.T) (*ManagedSession, *pushProbe, func() bus.AskUserRequested) {
	t.Helper()
	mgr := newTestManagerWithConfig(t, context.Background(), newMockProvider(
		toolCallHandlerFor("tc-ask", "ask_user", map[string]any{
			"questions": []any{map[string]any{"question": "Continue?", "options": []any{"yes", "no"}}},
		}),
		func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
			return simpleResponse("ok"), nil
		},
	), t.TempDir(), core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"})
	probe := probePush(mgr, "")
	sess, err := mgr.CreateSession(CreateOpts{Title: "asker"})
	if err != nil {
		t.Fatal(err)
	}
	asks := make(chan bus.AskUserRequested, 1)
	unsub := sess.runtime.Bus.Subscribe(func(e bus.AskUserRequested) { asks <- e })
	t.Cleanup(unsub)
	send := func() bus.AskUserRequested {
		if _, _, _, err := mgr.Send(sess.ID, "ask me", nil, "", ""); err != nil {
			t.Fatal(err)
		}
		select {
		case e := <-asks:
			return e
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for ask_user")
			return bus.AskUserRequested{}
		}
	}
	return sess, probe, send
}

func TestQuestionNobodyWatchesPushesAtOnce(t *testing.T) {
	sess, probe, send := askingSession(t)
	send()
	sess.runtime.Bus.Drain(5 * time.Second)
	got := probe.delivered()
	if len(got) != 1 || got[0].Kind != push.KindAsk || got[0].Level != push.LevelUrgent || got[0].SessionID != sess.ID {
		t.Fatalf("delivered = %+v, want one urgent ask for the session", got)
	}
}

func TestQuestionSomeoneWatchesWaitsThenPushesIfStillOpen(t *testing.T) {
	sess, probe, send := askingSession(t)
	watch(sess)
	send()
	sess.runtime.Bus.Drain(5 * time.Second)
	if got := probe.delivered(); len(got) != 0 || probe.armed() != 1 {
		t.Fatalf("delivered %+v with %d timers armed, want nothing sent and one grace timer", got, probe.armed())
	}
	probe.expire() // still unanswered when the 60 s are up
	if got := probe.delivered(); len(got) != 1 || got[0].Kind != push.KindAsk {
		t.Fatalf("after the grace period delivered = %+v, want the question", got)
	}
}

func TestQuestionAnsweredDuringGraceNeverPushes(t *testing.T) {
	sess, probe, send := askingSession(t)
	watch(sess)
	ask := send()
	if err := sess.runtime.Bus.Execute(bus.ResolveAskUser{SessionID: sess.ID, AskID: ask.ID, Answers: []string{"yes"}}); err != nil {
		t.Fatal(err)
	}
	sess.runtime.Bus.Drain(5 * time.Second)
	probe.expire()
	if got := probe.delivered(); len(got) != 0 {
		t.Fatalf("an answered question pushed %+v", got)
	}
}

// Presence that has lapsed (the phone was put away without a last message)
// must not keep a question silent.
func TestQuestionWithLapsedPresencePushesAtOnce(t *testing.T) {
	sess, probe, send := askingSession(t)
	v := sess.presence.join()
	v.apply([]byte(`{"type":"presence","visible":true}`))
	v.mu.Lock()
	v.visibleUntil = time.Now().Add(-time.Second)
	v.mu.Unlock()
	send()
	sess.runtime.Bus.Drain(5 * time.Second)
	if got := probe.delivered(); len(got) != 1 {
		t.Fatalf("delivered = %+v, want the question at once", got)
	}
}

func TestOnlyVisiblePresenceCounts(t *testing.T) {
	var set presenceSet
	if set.watched() {
		t.Fatal("no viewers: nobody is watching")
	}
	a, b := set.join(), set.join()
	a.apply([]byte(`{"type":"presence","visible":true}`))
	if !set.watched() {
		t.Fatal("a visible viewer must count")
	}
	b.apply([]byte(`{"type":"presence","visible":true}`))
	a.apply([]byte(`{"type":"presence","visible":false}`))
	if !set.watched() {
		t.Fatal("one viewer hiding must not hide the other")
	}
	set.leave(b)
	if set.watched() {
		t.Fatal("a viewer that left must stop counting")
	}
	a.apply([]byte(`not json`))
	a.apply([]byte(`{"type":"other","visible":true}`))
	if set.watched() {
		t.Fatal("anything but a presence report must be ignored")
	}
}

func TestWebSocketPresenceReachesTheSession(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	sess, err := mgr.CreateSession(CreateOpts{Title: "presence"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	conn, _, err := websocket.Dial(ctx, srv.URL+"/api/sessions/"+sess.ID+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow() //nolint:errcheck
	var init Event
	if err := wsjson.Read(ctx, conn, &init); err != nil {
		t.Fatal(err)
	}
	if sess.presence.watched() {
		t.Fatal("a connected socket that said nothing is not a viewer")
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"presence","visible":true}`)); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 3*time.Second, "visible presence", sess.presence.watched)
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"presence","visible":false}`)); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 3*time.Second, "hidden presence", func() bool { return !sess.presence.watched() })
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"presence","visible":true}`)); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 3*time.Second, "visible again", sess.presence.watched)
	conn.CloseNow() //nolint:errcheck
	pollUntil(t, 3*time.Second, "closed socket", func() bool { return !sess.presence.watched() })
}

// How a run that ended is announced depends on what started it.
func TestRunEndedPolicy(t *testing.T) {
	old := minRunForPush
	minRunForPush = 0
	t.Cleanup(func() { minRunForPush = old })

	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	probe := probePush(mgr, "")
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")
	userSess := ownerChild(t, mgr, root, "opened by the user")

	end := func(sess *ManagedSession, gen uint64, origin bus.RunOrigin, cancelled bool) []push.Notification {
		before := len(probe.delivered())
		publishRunStart(sess, gen, origin)
		sess.runtime.Bus.Drain(5 * time.Second)
		sess.runtime.Bus.Publish(bus.RunEnded{SessionID: sess.ID, RunGen: gen, Cancelled: cancelled})
		sess.runtime.Bus.Drain(5 * time.Second)
		return probe.delivered()[before:]
	}

	got := end(ownerSess, 1, bus.RunOrigin{Explicit: true, Source: reportSource}, false)
	if len(got) != 1 || got[0].Kind != push.KindDigest || got[0].Level != push.LevelPassive || got[0].Tag != "project:"+ownerSess.CWD {
		t.Fatalf("an owner digesting a report: %+v, want one passive digest tagged by project", got)
	}
	got = end(ownerSess, 2, bus.RunOrigin{Explicit: true}, false)
	if len(got) != 1 || got[0].Kind != push.KindDone || got[0].Level != push.LevelActive {
		t.Fatalf("a turn the owner's user asked for: %+v, want one active 'done'", got)
	}
	got = end(userSess, 3, bus.RunOrigin{Explicit: true}, false)
	if len(got) != 1 || got[0].Kind != push.KindDone || got[0].Level != push.LevelActive {
		t.Fatalf("the user's own session: %+v, want one active 'done'", got)
	}
	if got = end(userSess, 4, bus.RunOrigin{Explicit: true}, true); len(got) != 0 {
		t.Fatalf("a cancelled run pushed %+v", got)
	}
	watch(userSess)
	if got = end(userSess, 5, bus.RunOrigin{Explicit: true}, false); len(got) != 0 {
		t.Fatalf("a run somebody is watching pushed %+v", got)
	}
}

func TestSessionsTheOwnerLaunchedNeverPushUnderThePolicy(t *testing.T) {
	old := minRunForPush
	minRunForPush = 0
	t.Cleanup(func() { minRunForPush = old })

	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	probe := probePush(mgr, "")
	root := t.TempDir()
	ownerWithSession(t, mgr, root, "Winerim")
	launched, err := mgr.CreateSession(CreateOpts{CWD: root, Title: "launched by the owner", Origin: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	bus_ := launched.runtime.Bus
	bus_.Publish(bus.AskUserRequested{SessionID: launched.ID, RunGen: 1, ID: "a"})
	bus_.Publish(bus.PermissionRequested{SessionID: launched.ID, RunGen: 1, ID: "p"})
	publishRunStart(launched, 1, bus.RunOrigin{Explicit: true})
	bus_.Drain(5 * time.Second)
	bus_.Publish(bus.RunEnded{SessionID: launched.ID, RunGen: 1})
	bus_.Publish(bus.StateChanged{SessionID: launched.ID, State: string(bus.StateError)})
	bus_.Drain(5 * time.Second)
	// Its owner does get a report and digests it (a real, separate notification
	// for the owner's session); nothing may be about the launched session.
	for _, n := range probe.delivered() {
		if n.SessionID == launched.ID {
			t.Fatalf("a session the owner launched pushed %+v", n)
		}
	}
}

func TestEventsAreQuietAndOnePerSource(t *testing.T) {
	mgr := newOwnerTestManager(t, context.Background())
	probe := probePush(mgr, "")
	mgr.notifyEvent(events.Event{ID: "e1", Source: "grokbot"})
	mgr.notifyEvent(events.Event{ID: "e2", Source: "grokbot"})
	mgr.notifyEvent(events.Event{ID: "e3", Source: "ci"})
	got := probe.delivered()
	if len(got) != 3 {
		t.Fatalf("delivered %d, want 3 (the device replaces by tag)", len(got))
	}
	for _, n := range got {
		if n.Level != push.LevelPassive || !n.Inbox {
			t.Fatalf("event = %+v, want passive and opening the inbox", n)
		}
	}
	if got[0].Tag != got[1].Tag || got[0].Tag == got[2].Tag {
		t.Fatalf("tags %q %q %q: one per source", got[0].Tag, got[1].Tag, got[2].Tag)
	}
}
