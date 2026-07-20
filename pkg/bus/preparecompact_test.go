package bus

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/session"
	"github.com/ealeixandre/moa/pkg/sessioncheckpoint"
)

func newPrepareTest(t *testing.T, fa *fakeAgent) (*LocalBus, *SessionContext, <-chan RunEnded) {
	t.Helper()
	b := NewLocalBus()
	sctx := newTestSessionContextWithState(b, fa)
	sctx.SessionCheckpoint = sessioncheckpoint.New()
	sctx.Tree = session.NewTree()
	RegisterHandlers(sctx)
	RegisterTreeSyncer(b, sctx)
	ended := make(chan RunEnded, 2)
	b.Subscribe(func(e RunEnded) { ended <- e })
	return b, sctx, ended
}

func waitRunEnd(t *testing.T, b EventBus, ch <-chan RunEnded) RunEnded {
	t.Helper()
	select {
	case e := <-ch:
		b.Drain(time.Second)
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("prepare run did not end")
	}
	return RunEnded{}
}

func TestPrepareCompactSessionSuccessIsEphemeral(t *testing.T) {
	original := core.AgentMessage{Message: core.Message{Role: "user", Content: []core.Content{core.TextContent("real")}, MsgID: "real"}}
	fa := &fakeAgent{messages: []core.AgentMessage{original}, sendResult: []core.AgentMessage{{Message: core.Message{Role: "assistant", Content: []core.Content{core.TextContent("internal")}}}}, compactPayload: &core.CompactionPayload{Summary: "summary", SummaryMsgID: "summary", FirstKeptMsgID: "real"}}
	b, sctx, ended := newPrepareTest(t, fa)
	defer b.Close()
	_ = sctx.SessionCheckpoint.Write("exact handoff")
	started := make(chan RunStarted, 1)
	commands := make(chan CommandExecuted, 1)
	b.Subscribe(func(e RunStarted) { started <- e })
	b.Subscribe(func(e CommandExecuted) { commands <- e })
	if err := b.Execute(PrepareCompactSession{}); err != nil {
		t.Fatal(err)
	}
	if e := waitRunEnd(t, b, ended); e.Err != nil {
		t.Fatal(e.Err)
	}
	select {
	case <-started:
	default:
		t.Fatal("missing RunStarted")
	}
	select {
	case c := <-commands:
		if c.Command != "prepare-compact" {
			t.Fatalf("command %q", c.Command)
		}
	default:
		t.Fatal("missing CommandExecuted")
	}
	if sctx.State.Current() != StateIdle {
		t.Fatalf("state = %s", sctx.State.Current())
	}
	fa.mu.Lock()
	got, passed := append([]core.AgentMessage(nil), fa.messages...), fa.checkpointPassed
	fa.mu.Unlock()
	if len(got) != 1 || got[0].MsgID != "real" {
		t.Fatalf("ephemeral transcript leaked: %#v", got)
	}
	if passed != "exact handoff" {
		t.Fatalf("checkpoint = %q", passed)
	}
	if text, _ := sctx.SessionCheckpoint.Read(); text != "" {
		t.Fatalf("slot not cleared: %q", text)
	}
	entries, _ := sctx.Tree.Snapshot()
	var compactions int
	for _, entry := range entries {
		if entry.Type == session.EntryCompaction {
			compactions++
		}
		if entry.Type == session.EntryMessage && len(entry.Message.Content) > 0 && entry.Message.Content[0].Text == "internal" {
			t.Fatal("ephemeral preparation leaked into session tree")
		}
	}
	if compactions != 1 {
		t.Fatalf("compaction entries = %d, want 1", compactions)
	}
	contextMessages, _ := sctx.Tree.BuildContext()
	if len(contextMessages) != 2 || contextMessages[0].Role != "compaction_summary" || contextMessages[1].MsgID != "real" {
		t.Fatalf("rebuilt context = %#v", contextMessages)
	}
}

func TestPrepareCompactSessionPreparationErrorAndCancel(t *testing.T) {
	t.Run("error", func(t *testing.T) {
		fa := &fakeAgent{sendErr: errors.New("prepare failed")}
		b, sctx, ended := newPrepareTest(t, fa)
		defer b.Close()
		_ = sctx.SessionCheckpoint.Write("keep")
		_ = b.Execute(PrepareCompactSession{})
		if e := waitRunEnd(t, b, ended); e.Err == nil {
			t.Fatal("missing error")
		}
		fa.mu.Lock()
		compact := fa.compactCalled
		fa.mu.Unlock()
		if compact {
			t.Fatal("compacted after preparation error")
		}
		if text, _ := sctx.SessionCheckpoint.Read(); text != "keep" {
			t.Fatal("slot lost")
		}
		if sctx.State.Current() != StateError {
			t.Fatalf("state %s", sctx.State.Current())
		}
	})
	t.Run("cancel", func(t *testing.T) {
		fa := &fakeAgent{sendDelay: time.Second}
		b, sctx, ended := newPrepareTest(t, fa)
		defer b.Close()
		_ = b.Execute(PrepareCompactSession{})
		time.Sleep(10 * time.Millisecond)
		_ = b.Execute(AbortRun{})
		if e := waitRunEnd(t, b, ended); e.Err != nil {
			t.Fatalf("cancel reported error: %v", e.Err)
		}
		if sctx.State.Current() != StateIdle {
			t.Fatalf("state %s", sctx.State.Current())
		}
		fa.mu.Lock()
		compact := fa.compactCalled
		fa.mu.Unlock()
		if compact {
			t.Fatal("compacted after cancellation")
		}
	})
}

func TestPrepareCompactSessionNoopAndUnsupportedCheckpoint(t *testing.T) {
	fa := &fakeAgent{}
	b, sctx, ended := newPrepareTest(t, fa)
	defer b.Close()
	_ = sctx.SessionCheckpoint.Write("keep")
	commands := make(chan CommandExecuted, 1)
	b.Subscribe(func(e CommandExecuted) { commands <- e })
	_ = b.Execute(PrepareCompactSession{})
	if e := waitRunEnd(t, b, ended); e.Err != nil {
		t.Fatal(e.Err)
	}
	select {
	case c := <-commands:
		if c.Command != "prepare-compact-noop" {
			t.Fatalf("%q", c.Command)
		}
	default:
		t.Fatal("missing noop")
	}
	if text, _ := sctx.SessionCheckpoint.Read(); text != "keep" {
		t.Fatal("noop cleared slot")
	}
	plain := struct{ AgentController }{&fakeAgent{}}
	unsupported := &SessionContext{Agent: plain}
	if _, err := compactWithCheckpoint(context.Background(), unsupported, "must preserve"); err == nil {
		t.Fatal("unsupported controller silently dropped checkpoint")
	}
	unsupported.SessionCheckpoint = sessioncheckpoint.New()
	if _, err := sendPrepareCompact(context.Background(), unsupported, "prepare"); err == nil {
		t.Fatal("incompatible controller fell back to ordinary SendWithCustom")
	}
}

func TestPrepareCompactSessionPanicSettlesState(t *testing.T) {
	fa := &fakeAgent{sendHook: func() { panic("prepare panic") }}
	b, sctx, ended := newPrepareTest(t, fa)
	defer b.Close()
	if err := b.Execute(PrepareCompactSession{}); err != nil {
		t.Fatal(err)
	}
	if e := waitRunEnd(t, b, ended); e.Err == nil {
		t.Fatal("panic was not reported")
	}
	if sctx.State.Current() != StateError {
		t.Fatalf("state = %s", sctx.State.Current())
	}
	if sctx.Compacting() {
		t.Fatal("compacting stuck after panic")
	}
}

func TestPrepareCompactSessionPersistenceFailureKeepsCheckpoint(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failAt  int
		panicAt int
	}{
		{name: "summary snapshot", failAt: 1},
		{name: "cleared metadata snapshot", failAt: 2},
		{name: "cleared metadata snapshot panic", panicAt: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fa := &fakeAgent{compactPayload: &core.CompactionPayload{Summary: "summary"}}
			b, sctx, ended := newPrepareTest(t, fa)
			defer b.Close()
			_ = sctx.SessionCheckpoint.Write("keep on failure")
			calls := 0
			sctx.PersistNow = func() error {
				calls++
				if calls == tc.panicAt {
					panic("save panic")
				}
				if calls == tc.failAt {
					return errors.New("save failed")
				}
				return nil
			}
			if err := b.Execute(PrepareCompactSession{}); err != nil {
				t.Fatal(err)
			}
			if e := waitRunEnd(t, b, ended); e.Err == nil {
				t.Fatal("persistence failure was not reported")
			}
			if text, _ := sctx.SessionCheckpoint.Read(); text != "keep on failure" {
				t.Fatalf("checkpoint after failure = %q", text)
			}
		})
	}
}

func TestPrepareCompactSessionClearAndBarrier(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	fa := &fakeAgent{compactPayload: &core.CompactionPayload{Summary: "s"}, compactHook: func() { close(entered); <-release }}
	b, sctx, ended := newPrepareTest(t, fa)
	defer b.Close()
	_ = sctx.SessionCheckpoint.Write("x")
	if err := b.Execute(PrepareCompactSession{}); err != nil {
		t.Fatal(err)
	}
	<-entered
	_ = b.Execute(SteerAgent{Text: "later"})
	fa.mu.Lock()
	q := append([]core.SteerItem(nil), fa.steerQueue...)
	fa.mu.Unlock()
	found := false
	for _, it := range q {
		if it.Text == "later" {
			found = true
		}
	}
	if !found {
		t.Fatal("steer was consumed by ephemeral preparation")
	}
	close(release)
	_ = waitRunEnd(t, b, ended)
	_ = sctx.SessionCheckpoint.Write("clear me")
	if err := b.Execute(ClearSession{}); err != nil {
		t.Fatal(err)
	}
	if text, _ := sctx.SessionCheckpoint.Read(); text != "" {
		t.Fatal("clear retained slot")
	}
}

func TestPrepareCompactSessionQueuePolicyAndBarrierExecution(t *testing.T) {
	if got := ClassifyCommand("/prepare-compact"); got != PolicyQueue {
		t.Fatalf("policy = %s", got)
	}
	fa := &fakeAgent{compactPayload: &core.CompactionPayload{Summary: "s"}}
	b, sctx, ended := newPrepareTest(t, fa)
	defer b.Close()
	if err := b.Execute(QueueCommand{Raw: "/prepare-compact"}); err != nil {
		t.Fatal(err)
	}
	requestPump(sctx)
	if e := waitRunEnd(t, b, ended); e.Err != nil {
		t.Fatal(e.Err)
	}
	fa.mu.Lock()
	called := fa.compactCalled
	fa.mu.Unlock()
	if !called {
		t.Fatal("queued prepare-compact did not compact")
	}
}
