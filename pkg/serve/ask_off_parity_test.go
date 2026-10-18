package serve

import (
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/bus"
)

// The owner's decision: with ask_user_async off (the production default) the
// build behaves as it did before the pilot existed. These tests pin the places
// that sit on paths every deployment runs.

func TestOffParityNothingEngagesTheQuestionMachinery(t *testing.T) {
	pinSyncAskUser(t)
	env := newRSEnv(t, nil)
	sess := env.session(env.root)
	env.mention(sess, "one", sess.ID)
	env.mention(sess, "two", sess.ID)

	snapshots := 0
	env.mgr.questions.hooks.afterBranchSnapshot = func() { snapshots++ }

	if env.mgr.questions.engaged() {
		t.Fatal("a database no question was ever written to is engaged")
	}
	env.rewind(sess, rsUserEntryIDs(sess)[1])
	if snapshots != 0 {
		t.Fatal("a rewind saved and settled questions although the pilot never ran")
	}
	if err := env.mgr.questions.Fence(env.mgr.baseCtx, sess.ID, true); err != nil {
		t.Fatalf("stop fence: %v", err)
	}
	env.mgr.notices.slotFreed()
	if trig := env.mgr.notices.takeTriggers(); trig.retryWaiting {
		t.Fatal("a freed slot made the dispatcher retry held notices")
	}
	id := sess.ID
	if err := env.mgr.Delete(id); err != nil {
		t.Fatal(err)
	}
	if _, gone := env.mgr.questions.gone.Load(id); gone {
		t.Fatal("deleting a session of a blocking-ask build marked it for the question machinery")
	}
	if env.mgr.questions.engaged() {
		t.Fatal("the flows above engaged the question machinery")
	}
}

func TestOffParityWireShapes(t *testing.T) {
	ev, ok := wsEventFromBus(bus.AskUserRequested{ID: "ask_1", RunGen: 1, Questions: []bus.AskQuestion{{Text: "q"}}})
	if !ok {
		t.Fatal("no event")
	}
	data := ev.Data.(map[string]any)
	if _, has := data["async"]; has {
		t.Fatalf("a blocking ask event carries an async key: %v", data)
	}
	ev, _ = wsEventFromBus(bus.AskUserRequested{ID: "q_1", Async: true})
	if ev.Data.(map[string]any)["async"] != true {
		t.Fatal("an async ask event lost its marker")
	}
}

func TestOffParityToolDescriptionsDoNotAnnounceTheAsyncContract(t *testing.T) {
	const marker = "asynchronous"
	m := &Manager{}
	if d := m.newRequestCallTool("s", nil, false).Description; strings.Contains(d, marker) {
		t.Fatalf("request_call tells a blocking session about asynchronous questions: %s", d)
	}
	if d := m.newRequestCallTool("s", nil, true).Description; !strings.Contains(d, marker) {
		t.Fatal("request_call does not tell an async session its fallback is acknowledged")
	}
	if p := string(newSessionsTool(m, "k", false).Parameters); strings.Contains(p, marker) {
		t.Fatalf("the sessions tool tells a blocking session about asynchronous questions: %s", p)
	}
	if p := string(newSessionsTool(m, "k", true).Parameters); !strings.Contains(p, marker) {
		t.Fatal("the sessions tool does not tell an async session what send does")
	}
}
