package serve

import (
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// The event command for a task notice carries only that notice, so a client
// appends it without pulling older context or a concurrent send's row.
func TestInjectEventTaskNoticeCommandCarriesOnlyTheNotice(t *testing.T) {
	mgr, sess := newInjectSession(t)
	old := core.AgentMessage{Message: core.NewUserMessage("old history")}
	if err := sess.runtime.Context().Agent.AppendMessage(old); err != nil {
		t.Fatal(err)
	}
	got := make(chan bus.CommandExecuted, 4)
	unsub := sess.runtime.Bus.Subscribe(func(e bus.CommandExecuted) {
		if e.Command == "event" {
			got <- e
		}
	})
	defer unsub()

	steered, err := mgr.injectEvent(sess.ID, eventInjection{
		Text: func() string { return "task queued" },
		Custom: func(bool) map[string]any {
			return map[string]any{"source": "event", "source_name": tasks.NoticeSourceName, "id": "tn_1", "autorun": false}
		},
	})
	if err != nil || steered {
		t.Fatalf("injectEvent = steered %v, err %v; want an append", steered, err)
	}

	var ev bus.CommandExecuted
	select {
	case ev = <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("no event command published")
	}
	if len(ev.Messages) != 1 {
		t.Fatalf("command carries %d messages, want exactly the notice", len(ev.Messages))
	}
	msgs := sess.runtime.Context().Agent.Messages()
	last := msgs[len(msgs)-1]
	if ev.Messages[0].MsgID == "" || ev.Messages[0].MsgID != last.MsgID {
		t.Fatalf("command MsgID %q, history MsgID %q; want equal and non-empty", ev.Messages[0].MsgID, last.MsgID)
	}
	if ev.Messages[0].Custom["id"] != "tn_1" {
		t.Fatalf("command carries %v, want the notice", ev.Messages[0].Custom)
	}
}
