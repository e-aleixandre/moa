package serve

import (
	"context"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// The open-questions block is the parent's: a subagent it spawns is a
// separate conversation with no ask_user, and never receives the block.
func TestAskAsyncInventoryIsNotInheritedBySubagents(t *testing.T) {
	f1Isolate(t)
	base := f1Script(false)
	script := func(req core.Request) <-chan core.AssistantEvent {
		switch {
		case strings.Contains(req.System, "You are a focused subagent"):
			return f1Text("child done")
		case strings.HasPrefix(f1LatestFixtureInput(req), "F1-SPAWN"):
			for _, m := range req.Messages {
				if m.Role == "tool_result" && m.ToolCallID == "tc-spawn" {
					return f1Text("F1-AFTER-SPAWN")
				}
			}
			ch, _ := toolCallHandlerFor("tc-spawn", "subagent", map[string]any{"task": "look around"})(context.Background(), core.Request{})
			return ch
		}
		return base(req)
	}
	prov := &f1Provider{script: script}
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), prov)
	sess := e.session(f1KindHuman)
	e.send(sess, "F1-TRIGGER please ask")
	waitPendingAsk(t, sess)
	f1Settle(t, sess)

	e.send(sess, "F1-SPAWN a helper")
	if !f1Eventually(f1Guard, func() bool {
		return prov.countWhere(func(r core.Request) bool { return strings.Contains(r.System, "You are a focused subagent") }) > 0 &&
			sessState(sess) == StateIdle && !sess.runtime.Context().Agent.IsRunning()
	}) {
		t.Fatal("the subagent never ran")
	}

	has := func(r core.Request) bool { return f1RequestMentions(r, "open_questions") }
	child := func(r core.Request) bool { return strings.Contains(r.System, "You are a focused subagent") }
	if n := prov.countWhere(func(r core.Request) bool { return child(r) && has(r) }); n != 0 {
		t.Fatalf("%d subagent request(s) carried the parent's open-questions block", n)
	}
	if n := prov.countWhere(func(r core.Request) bool { return !child(r) && has(r) }); n == 0 {
		t.Fatal("the parent's own requests lost the block: the control is not observing it")
	}
}

func f1RequestMentions(r core.Request, sub string) bool {
	for _, m := range r.Messages {
		for _, c := range m.Content {
			if strings.Contains(c.Text, sub) {
				return true
			}
		}
	}
	return false
}
