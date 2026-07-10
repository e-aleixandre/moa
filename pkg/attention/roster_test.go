package attention

import (
	"testing"

	"github.com/ealeixandre/moa/pkg/bus"
)

// rosterOf returns the sessions from the most recent init/roster message.
func rosterOf(f *fakeClient) []SessionBrief {
	for _, m := range reverse(f.messages()) {
		if m.Type == "init" || m.Type == "roster" {
			return m.Sessions
		}
	}
	return nil
}

func reverse(in []ServerMsg) []ServerMsg {
	out := make([]ServerMsg, len(in))
	for i, m := range in {
		out[len(in)-1-i] = m
	}
	return out
}

func TestRosterOnInitListsSessions(t *testing.T) {
	s := newTestService(t)
	b1 := bus.NewLocalBus()
	b2 := bus.NewLocalBus()
	defer b1.Close()
	defer b2.Close()
	defer s.Attach(b1, "s1", "facturas", "Facturas API")()
	defer s.Attach(b2, "s2", "goikbot", "Goikbot")()

	client := &fakeClient{cid: 1}
	s.SetActiveClient(client)

	eventually(t, "roster has both sessions", func() bool {
		r := rosterOf(client)
		if len(r) != 2 {
			return false
		}
		// Sorted by session id: s1 then s2.
		return r[0].SessionID == "s1" && r[0].Alias == "facturas" &&
			r[1].SessionID == "s2" && r[1].Title == "Goikbot"
	})
}

func TestRosterUpdatesOnStateChange(t *testing.T) {
	s := newTestService(t)
	b := bus.NewLocalBus()
	defer b.Close()
	defer s.Attach(b, "s", "x", "X")()
	client := &fakeClient{cid: 1}
	s.SetActiveClient(client)

	b.Publish(bus.StateChanged{SessionID: "s", State: "running"})
	eventually(t, "roster reflects running", func() bool {
		r := rosterOf(client)
		return len(r) == 1 && r[0].State == "running"
	})
}

func TestRosterPendingCounts(t *testing.T) {
	s := newTestService(t)
	b := bus.NewLocalBus()
	defer b.Close()
	defer s.Attach(b, "s", "x", "X")()
	client := &fakeClient{cid: 1}
	s.SetActiveClient(client)

	b.Publish(bus.PermissionRequested{SessionID: "s", ID: "perm_1", ToolName: "bash", Args: map[string]any{"command": "ls"}})
	eventually(t, "roster shows 1 pending perm", func() bool {
		r := rosterOf(client)
		return len(r) == 1 && r[0].PendingPerm == 1
	})

	b.Publish(bus.PermissionResolved{SessionID: "s", ID: "perm_1"})
	eventually(t, "roster shows 0 pending perm", func() bool {
		r := rosterOf(client)
		return len(r) == 1 && r[0].PendingPerm == 0
	})
}

func TestRosterDropsDetachedSession(t *testing.T) {
	s := newTestService(t)
	b := bus.NewLocalBus()
	defer b.Close()
	detach := s.Attach(b, "s", "x", "X")
	client := &fakeClient{cid: 1}
	s.SetActiveClient(client)

	eventually(t, "roster has the session", func() bool { return len(rosterOf(client)) == 1 })
	detach()
	eventually(t, "roster empty after detach", func() bool { return len(rosterOf(client)) == 0 })
}

func TestUpdateMetaRefreshesAlias(t *testing.T) {
	s := newTestService(t)
	b := bus.NewLocalBus()
	defer b.Close()
	// Attach with an empty title (pre-auto-title): alias falls back.
	defer s.Attach(b, "s", "a session", "")()
	client := &fakeClient{cid: 1}
	s.SetActiveClient(client)

	eventually(t, "initial roster", func() bool { return len(rosterOf(client)) == 1 })

	// Auto-title arrives later.
	s.UpdateMeta("s", "invoices api", "Invoices API")
	eventually(t, "alias updated", func() bool {
		r := rosterOf(client)
		return len(r) == 1 && r[0].Alias == "invoices api" && r[0].Title == "Invoices API"
	})

	// Unknown session is a no-op (must not panic or create anything).
	s.UpdateMeta("ghost", "x", "X")
	eventually(t, "still one session", func() bool { return len(rosterOf(client)) == 1 })
}
