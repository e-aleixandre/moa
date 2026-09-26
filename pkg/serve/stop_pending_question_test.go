package serve

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

func TestStopPendingQuestionDoesNotCallProvider(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	nextRequests := make(chan core.Request, 1)
	provider := newMockProvider(
		simpleResponseHandler("one"), simpleResponseHandler("two"),
		simpleResponseHandler("three"), simpleResponseHandler("four"),
		simpleResponseHandler("five"), simpleResponseHandler("six"),
		toolCallHandlerFor("question-1", "ask_user", map[string]any{
			"questions": []any{map[string]any{"question": "Which option?"}},
		}),
		func(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
			nextRequests <- req
			return simpleResponse("after the stop"), nil
		},
	)
	mgr := newFreshTestManager(t, ctx, provider)
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		sendAndWait(t, mgr, sess, turn(i))
	}
	shortOwnerQuiescence(t, 50*time.Millisecond)
	outcomes := make(chan runOutcome, 3)
	sess.ownerObserver = newOwnerReportObserver(sess, func(out runOutcome) { outcomes <- out })
	asks := make(chan bus.AskUserRequested, 1)
	ends := make(chan bus.RunEnded, 1)
	unsubAsk := sess.runtime.Bus.Subscribe(func(e bus.AskUserRequested) { asks <- e })
	unsubEnd := sess.runtime.Bus.Subscribe(func(e bus.RunEnded) { ends <- e })
	defer unsubAsk()
	defer unsubEnd()
	if _, _, _, err := mgr.Send(sess.ID, "ask me", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-asks:
	case <-time.After(5 * time.Second):
		t.Fatal("ask_user never became pending")
	}
	if got := sessState(sess); got != StatePermission {
		t.Fatalf("pending question state = %s, want permission", got)
	}
	select {
	case out := <-outcomes:
		if out.Status != callbackStatusNeedsInput {
			t.Fatalf("owner status while asking = %s, want needs_input", out.Status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("owner did not hear the question")
	}
	// An expired cache must stay expired when Stop closes the run.
	sess.mu.Lock()
	sess.lastRunAt = time.Now().Add(-2 * time.Hour)
	sess.lastRunProvider = "anthropic"
	sess.mu.Unlock()
	if sess.info().CanStartFresh {
		t.Fatal("Start fresh offered before the run is idle")
	}
	before := provider.calls.Load()
	if err := mgr.Cancel(sess.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case ended := <-ends:
		if !ended.Cancelled || ended.Err != nil {
			t.Fatalf("run end = %+v, want cancelled without error", ended)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stopped run did not end")
	}
	sess.runtime.Bus.Drain(2 * time.Second)
	select {
	case out := <-outcomes:
		if out.Status != callbackStatusDone || out.Pending != nil {
			t.Fatalf("owner status after Stop = %+v, want done without pending input", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("owner did not hear the run ended")
	}
	if got := provider.calls.Load(); got != before {
		t.Fatalf("Stop called provider %d times, want 0", got-before)
	}
	info := sess.info()
	if info.State != StateIdle || !info.CanStartFresh || info.CacheExpiresAt.After(time.Now()) || info.PendingID != "" {
		t.Fatalf("after Stop: state=%s canStartFresh=%v cacheExpiresAt=%v pending=%q", info.State, info.CanStartFresh, info.CacheExpiresAt, info.PendingID)
	}
	assertStoppedQuestionResult(t, sess.History())
	res, err := mgr.ExecCommand(sess.ID, "/start-fresh", "")
	if err != nil || !res.OK {
		t.Fatalf("start fresh after Stop: %+v, %v", res, err)
	}
	if got := provider.calls.Load(); got != before {
		t.Fatalf("Start fresh called provider %d times, want 0", got-before)
	}

	// The displayed tree is persisted, not just the in-memory agent state.
	id := sess.ID
	mgr.mu.Lock()
	delete(mgr.sessions, id)
	mgr.mu.Unlock()
	sess.runtime.Close()
	resumed, err := mgr.ResumeSession(id)
	if err != nil {
		t.Fatal(err)
	}
	assertStoppedQuestionResult(t, resumed.History())
	if resumed.info().State != StateIdle {
		t.Fatalf("state after restart = %s", resumed.info().State)
	}
	if _, _, _, err := mgr.Send(id, "next message", nil, "", ""); err != nil {
		t.Fatalf("next message after restart: %v", err)
	}
	pollUntil(t, 5*time.Second, "next message completed", func() bool {
		return sessState(resumed) == StateIdle && provider.calls.Load() == before+1
	})
	req := <-nextRequests
	for i, msg := range req.Messages {
		if msg.Role == "assistant" && len(msg.Content) > 0 && msg.Content[0].ToolCallID == "question-1" {
			if i+1 >= len(req.Messages) || req.Messages[i+1].Role != "tool_result" ||
				req.Messages[i+1].ToolCallID != "question-1" {
				t.Fatalf("next provider request has an unpaired ask_user tool call at %d", i)
			}
			return
		}
	}
	t.Fatal("next provider request lost the stopped question after Start fresh and restart")
}

func assertStoppedQuestionResult(t *testing.T, msgs []core.AgentMessage) {
	t.Helper()
	for i, msg := range msgs {
		for _, content := range msg.Content {
			if content.ToolName != "ask_user" || content.ToolCallID != "question-1" || msg.Role != "assistant" {
				continue
			}
			if i+1 >= len(msgs) || msgs[i+1].Role != "tool_result" || len(msgs[i+1].Content) == 0 ||
				msgs[i+1].ToolCallID != "question-1" ||
				!strings.Contains(msgs[i+1].Content[0].Text, "not answered") ||
				!strings.Contains(msgs[i+1].Content[0].Text, "next user message") {
				t.Fatalf("ask_user tool use at %d not paired with a clear stopped result: %+v", i, msgs[min(i+1, len(msgs)-1)])
			}
			return
		}
	}
	t.Fatal("ask_user tool use not in transcript")
}
