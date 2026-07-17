package attention

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/bus"
)

// fakeClient is a test clientSink that records everything the loop sends it.
type fakeClient struct {
	mu   sync.Mutex
	cid  uint64
	msgs []ServerMsg
	dead bool
}

func (f *fakeClient) Send(m ServerMsg) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dead {
		return false
	}
	f.msgs = append(f.msgs, m)
	return true
}
func (f *fakeClient) ID() uint64 { return f.cid }

func (f *fakeClient) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dead = true
}

func (f *fakeClient) messages() []ServerMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ServerMsg, len(f.msgs))
	copy(out, f.msgs)
	return out
}

func (f *fakeClient) lastOfType(t string) (ServerMsg, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.msgs) - 1; i >= 0; i-- {
		if f.msgs[i].Type == t {
			return f.msgs[i], true
		}
	}
	return ServerMsg{}, false
}

// newTestService starts a Service and returns it with a cleanup.
func newTestService(t *testing.T) *Service {
	t.Helper()
	s := New(Config{Lang: "en"})
	s.Start()
	t.Cleanup(s.Close)
	return s
}

// eventually polls f until it returns true or the deadline passes. The loop is
// async, so tests must wait for it to process rather than reading directly.
func eventually(t *testing.T, why string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for: %s", why)
}

func TestPermissionRequestBecomesP0AndResolves(t *testing.T) {
	s := newTestService(t)
	b := bus.NewLocalBus()
	defer b.Close()

	detach := s.Attach(b, "sess1", "facturas", "Facturas")
	defer detach()

	client := &fakeClient{cid: 1}
	s.SetActiveClient(client)

	b.Publish(bus.PermissionRequested{
		SessionID: "sess1", ID: "perm_1", ToolName: "bash",
		Args: map[string]any{"command": "ls -la"},
	})

	eventually(t, "attention item delivered", func() bool {
		m, ok := client.lastOfType("attention")
		return ok && m.Item != nil && m.Item.Kind == KindPermission && m.Item.RefID == "perm_1"
	})

	// Status shows one unresolved P0.
	items := s.Status()
	if len(items) != 1 || items[0].Priority != P0Blocking {
		t.Fatalf("expected 1 unresolved P0, got %+v", items)
	}

	// Resolve via the bus (as the HTTP endpoint would).
	b.Publish(bus.PermissionResolved{SessionID: "sess1", ID: "perm_1"})

	eventually(t, "item resolved", func() bool {
		m, ok := client.lastOfType("item_update")
		return ok && m.Item != nil && m.Item.State == StateResolved
	})
	if items := s.Status(); len(items) != 0 {
		t.Fatalf("expected 0 unresolved after resolve, got %+v", items)
	}
}

func TestAskRequestAndResolve(t *testing.T) {
	s := newTestService(t)
	b := bus.NewLocalBus()
	defer b.Close()
	defer s.Attach(b, "s", "goikbot", "Goikbot")()

	client := &fakeClient{cid: 1}
	s.SetActiveClient(client)

	b.Publish(bus.AskUserRequested{
		SessionID: "s", ID: "ask_1",
		Questions: []bus.AskQuestion{{Text: "Deploy to prod now?"}},
	})

	eventually(t, "ask delivered", func() bool {
		m, ok := client.lastOfType("attention")
		return ok && m.Item != nil && m.Item.Kind == KindAsk &&
			contains(m.Item.Spoken, "Deploy to prod now?")
	})

	b.Publish(bus.AskUserResolved{SessionID: "s", ID: "ask_1"})
	eventually(t, "ask resolved", func() bool {
		return len(s.Status()) == 0
	})
}

func TestErrorStateBecomesP0(t *testing.T) {
	s := newTestService(t)
	b := bus.NewLocalBus()
	defer b.Close()
	defer s.Attach(b, "s", "ui", "UI")()

	client := &fakeClient{cid: 1}
	s.SetActiveClient(client)

	b.Publish(bus.StateChanged{SessionID: "s", State: "error", Error: "boom"})
	eventually(t, "error item", func() bool {
		m, ok := client.lastOfType("attention")
		return ok && m.Item != nil && m.Item.Kind == KindError && contains(m.Item.Spoken, "boom")
	})
}

func TestRunEndedErrorDedupedAgainstStateChange(t *testing.T) {
	s := newTestService(t)
	b := bus.NewLocalBus()
	defer b.Close()
	defer s.Attach(b, "s", "x", "X")()
	s.SetActiveClient(&fakeClient{cid: 1})

	b.Publish(bus.StateChanged{SessionID: "s", State: "error", Error: "kaput"})
	eventually(t, "one error item", func() bool { return len(s.Status()) == 1 })

	// A RunEnded with an error must not create a SECOND item.
	b.Publish(bus.RunEnded{SessionID: "s", Err: errors.New("kaput")})
	time.Sleep(50 * time.Millisecond)
	if items := s.Status(); len(items) != 1 {
		t.Fatalf("expected dedup to 1 error item, got %d", len(items))
	}
}

func TestErrorStateIsDedupedAndResolvedOnRecovery(t *testing.T) {
	s := newTestService(t)
	b := bus.NewLocalBus()
	defer b.Close()
	defer s.Attach(b, "s", "build", "Build")()

	b.Publish(bus.StateChanged{SessionID: "s", State: "error", Error: "boom"})
	b.Publish(bus.StateChanged{SessionID: "s", State: "error", Error: "boom"})
	eventually(t, "one live error", func() bool {
		items := s.Status()
		return len(items) == 1 && items[0].Kind == KindError
	})

	b.Publish(bus.StateChanged{SessionID: "s", State: "idle"})
	eventually(t, "error resolved after recovery", func() bool { return len(s.Status()) == 0 })
}

func TestDetachPurgesAndDropsLateEvents(t *testing.T) {
	s := newTestService(t)
	b := bus.NewLocalBus()
	defer b.Close()

	detach := s.Attach(b, "s", "a", "A")
	s.SetActiveClient(&fakeClient{cid: 1})

	b.Publish(bus.PermissionRequested{SessionID: "s", ID: "perm_1", ToolName: "bash", Args: map[string]any{"command": "ls"}})
	eventually(t, "item present", func() bool { return len(s.Status()) == 1 })

	detach()
	eventually(t, "purged", func() bool { return len(s.Status()) == 0 })

	// A late event with the (now-stale) generation must be ignored: publishing
	// after detach unsubscribed, but even a racing in-flight event is dropped by
	// the generation guard. Re-publish should not resurrect anything.
	b.Publish(bus.PermissionRequested{SessionID: "s", ID: "perm_2", ToolName: "bash", Args: map[string]any{"command": "rm x"}})
	time.Sleep(50 * time.Millisecond)
	if items := s.Status(); len(items) != 0 {
		t.Fatalf("late event resurrected state: %+v", items)
	}
}

func TestTwoSessionsDoNotMixRefIDs(t *testing.T) {
	s := newTestService(t)
	b1 := bus.NewLocalBus()
	b2 := bus.NewLocalBus()
	defer b1.Close()
	defer b2.Close()
	defer s.Attach(b1, "s1", "one", "One")()
	defer s.Attach(b2, "s2", "two", "Two")()
	s.SetActiveClient(&fakeClient{cid: 1})

	// Same refID string on both buses must stay independent per session.
	b1.Publish(bus.PermissionRequested{SessionID: "s1", ID: "perm_1", ToolName: "bash", Args: map[string]any{"command": "ls"}})
	b2.Publish(bus.PermissionRequested{SessionID: "s2", ID: "perm_1", ToolName: "bash", Args: map[string]any{"command": "ls"}})
	eventually(t, "two items", func() bool { return len(s.Status()) == 2 })

	// Resolving s1's perm_1 must not touch s2's.
	b1.Publish(bus.PermissionResolved{SessionID: "s1", ID: "perm_1"})
	eventually(t, "one remains", func() bool {
		items := s.Status()
		return len(items) == 1 && items[0].SessionID == "s2"
	})
}

func TestDuplicatePermissionEventDoesNotDouble(t *testing.T) {
	s := newTestService(t)
	b := bus.NewLocalBus()
	defer b.Close()
	defer s.Attach(b, "s", "a", "A")()
	s.SetActiveClient(&fakeClient{cid: 1})

	for i := 0; i < 3; i++ {
		b.Publish(bus.PermissionRequested{SessionID: "s", ID: "perm_1", ToolName: "bash", Args: map[string]any{"command": "ls"}})
	}
	eventually(t, "single item", func() bool { return len(s.Status()) >= 1 })
	time.Sleep(30 * time.Millisecond)
	if items := s.Status(); len(items) != 1 {
		t.Fatalf("dedup failed: got %d items", len(items))
	}
}

func TestAckDoesNotResolve(t *testing.T) {
	s := newTestService(t)
	b := bus.NewLocalBus()
	defer b.Close()
	defer s.Attach(b, "s", "a", "A")()
	client := &fakeClient{cid: 1}
	s.SetActiveClient(client)

	b.Publish(bus.AskUserRequested{SessionID: "s", ID: "ask_1", Questions: []bus.AskQuestion{{Text: "q?"}}})
	var id string
	eventually(t, "item present", func() bool {
		m, ok := client.lastOfType("attention")
		if ok && m.Item != nil {
			id = m.Item.ID
			return true
		}
		return false
	})

	s.Ack(id)
	time.Sleep(30 * time.Millisecond)
	// Still unresolved after ack.
	if items := s.Status(); len(items) != 1 {
		t.Fatalf("ack must not resolve; got %d unresolved", len(items))
	}
}

func TestSingleActiveClientSupersedes(t *testing.T) {
	s := newTestService(t)
	b := bus.NewLocalBus()
	defer b.Close()
	defer s.Attach(b, "s", "a", "A")()

	c1 := &fakeClient{cid: 1}
	c2 := &fakeClient{cid: 2}
	s.SetActiveClient(c1)
	s.SetActiveClient(c2)

	eventually(t, "c1 told inactive", func() bool {
		_, ok := c1.lastOfType("inactive")
		return ok
	})
	eventually(t, "c2 got init", func() bool {
		_, ok := c2.lastOfType("init")
		return ok
	})

	// New events go only to c2.
	b.Publish(bus.StateChanged{SessionID: "s", State: "error", Error: "e"})
	eventually(t, "c2 receives", func() bool {
		_, ok := c2.lastOfType("attention")
		return ok
	})
}

func TestSupersededClientCannotAcknowledge(t *testing.T) {
	s := newTestService(t)
	b := bus.NewLocalBus()
	defer b.Close()
	defer s.Attach(b, "s", "a", "A")()
	b.Publish(bus.PermissionRequested{SessionID: "s", ID: "perm_1", ToolName: "bash", Args: map[string]any{"command": "ls"}})
	eventually(t, "pending item", func() bool { return len(s.Status()) == 1 })

	c1 := &fakeClient{cid: 1}
	c2 := &fakeClient{cid: 2}
	s.SetActiveClient(c1)
	s.SetActiveClient(c2)
	eventually(t, "first client closed", func() bool {
		c1.mu.Lock()
		defer c1.mu.Unlock()
		return c1.dead
	})
	itemID := s.Status()[0].ID
	if s.AckForClient(c1, itemID) {
		t.Fatal("superseded client acknowledgement was accepted")
	}
	if got := s.Status()[0].State; got != StateAnnounced {
		t.Fatalf("superseded acknowledgement changed item state to %q", got)
	}
}

func TestInitIsAuthoritativeOnConnect(t *testing.T) {
	s := newTestService(t)
	b := bus.NewLocalBus()
	defer b.Close()
	defer s.Attach(b, "s", "a", "A")()

	// Item exists BEFORE any client connects.
	b.Publish(bus.PermissionRequested{SessionID: "s", ID: "perm_1", ToolName: "bash", Args: map[string]any{"command": "ls"}})
	eventually(t, "item present", func() bool { return len(s.Status()) == 1 })

	// A newly connected client must learn it via init.
	c := &fakeClient{cid: 1}
	s.SetActiveClient(c)
	eventually(t, "init carries the item", func() bool {
		m, ok := c.lastOfType("init")
		return ok && len(m.Items) == 1 && m.Items[0].RefID == "perm_1"
	})
}

func TestSlowClientDoesNotBlockService(t *testing.T) {
	s := newTestService(t)
	b := bus.NewLocalBus()
	defer b.Close()
	defer s.Attach(b, "s", "a", "A")()

	// A dead client (send returns false) must be forgotten, not block the loop.
	dead := &fakeClient{cid: 1, dead: true}
	s.SetActiveClient(dead)
	b.Publish(bus.StateChanged{SessionID: "s", State: "error", Error: "e"})

	// The loop is still responsive: Status returns promptly.
	eventually(t, "service still responsive", func() bool { return len(s.Status()) == 1 })
}

func TestUndeliveredHookOnlyFiresForP0WithoutClient(t *testing.T) {
	called := make(chan AttentionItem, 1)
	s := New(Config{Lang: "en", OnUndelivered: func(item AttentionItem) { called <- item }})
	s.Start()
	t.Cleanup(s.Close)
	b := bus.NewLocalBus()
	defer b.Close()
	defer s.Attach(b, "s", "a", "A")()

	b.Publish(bus.PermissionRequested{SessionID: "s", ID: "perm_1", ToolName: "bash", Args: map[string]any{"command": "ls"}})
	select {
	case item := <-called:
		if item.Kind != KindPermission || item.RefID != "perm_1" {
			t.Fatalf("hook item = %+v", item)
		}
	case <-time.After(time.Second):
		t.Fatal("undelivered hook was not called")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (indexOf(haystack, needle) >= 0)
}
func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
