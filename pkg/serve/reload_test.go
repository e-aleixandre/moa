package serve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

func writeAgentsMD(t *testing.T, cwd, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The point of /reload: a rule added to AGENTS.md while the session is open has
// to reach the model, which means it has to be in the system prompt.
func TestReload_PicksUpAnEditedAgentsMD(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("ok")))

	cwd := t.TempDir()
	writeAgentsMD(t, cwd, "- Commit messages in English.\n")
	sess, err := mgr.CreateSession(CreateOpts{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}

	before := sess.runtime.Context().Agent.SystemPrompt()
	if !strings.Contains(before, "Commit messages in English") {
		t.Fatalf("the session did not start with AGENTS.md in its prompt")
	}

	writeAgentsMD(t, cwd, "- Commit messages in English.\n- Start every commit with the ticket id.\n")

	res, err := mgr.ExecCommand(sess.ID, "/reload", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("reload failed: %s", res.Message)
	}

	after := sess.runtime.Context().Agent.SystemPrompt()
	if !strings.Contains(after, "ticket id") {
		t.Errorf("the edited AGENTS.md never reached the system prompt:\n%s", after)
	}
	if !strings.Contains(res.Message, "AGENTS.md") {
		t.Errorf("the report should name what changed, got: %s", res.Message)
	}
}

// A rule deleted from AGENTS.md must disappear. This is why the prompt is
// rebuilt rather than a notice appended: an appended message can add an
// instruction but cannot retract one that is still in the prompt.
func TestReload_DropsADeletedRule(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("ok")))

	cwd := t.TempDir()
	writeAgentsMD(t, cwd, "- Never run tests.\n")
	sess, err := mgr.CreateSession(CreateOpts{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}

	writeAgentsMD(t, cwd, "- Run the tests before reporting.\n")
	if _, err := mgr.ExecCommand(sess.ID, "/reload", ""); err != nil {
		t.Fatal(err)
	}

	after := sess.runtime.Context().Agent.SystemPrompt()
	if strings.Contains(after, "Never run tests") {
		t.Error("a rule deleted from AGENTS.md survived the reload")
	}
}

// Nothing changed is the common case — a reload run just in case — and it must
// leave the prompt byte-identical so the cached prefix stays valid.
func TestReload_LeavesThePromptAloneWhenNothingChanged(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("ok")))

	cwd := t.TempDir()
	writeAgentsMD(t, cwd, "- Commit messages in English.\n")
	sess, err := mgr.CreateSession(CreateOpts{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}

	before := sess.runtime.Context().Agent.SystemPrompt()
	res, err := mgr.ExecCommand(sess.ID, "/reload", "")
	if err != nil {
		t.Fatal(err)
	}
	if sess.runtime.Context().Agent.SystemPrompt() != before {
		t.Error("a no-op reload rewrote the system prompt, invalidating the cache for nothing")
	}
	if !strings.Contains(res.Message, "Nothing changed") {
		t.Errorf("a no-op reload should say so, got: %s", res.Message)
	}
}

// A reload is silent: the model gets the new instructions, not an announcement
// about them.
func TestReload_AddsNothingToTheConversation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("ok")))

	cwd := t.TempDir()
	writeAgentsMD(t, cwd, "- One.\n")
	sess, err := mgr.CreateSession(CreateOpts{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}

	writeAgentsMD(t, cwd, "- One.\n- Two.\n")
	if _, err := mgr.ExecCommand(sess.ID, "/reload", ""); err != nil {
		t.Fatal(err)
	}

	if msgs := sess.runtime.Context().Agent.Messages(); len(msgs) != 0 {
		t.Errorf("reload spoke to the model instead of just reloading: %+v", msgs)
	}
}

// The three inputs are global to the workspace, so a reload has to reach every
// live session — not just the one that asked.
func TestReload_AppliesToEveryLiveSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("ok")))

	cwd := t.TempDir()
	writeAgentsMD(t, cwd, "- One.\n")
	first, err := mgr.CreateSession(CreateOpts{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	second, err := mgr.CreateSession(CreateOpts{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}

	writeAgentsMD(t, cwd, "- One.\n- Two.\n")
	if _, err := mgr.ExecCommand(first.ID, "/reload", ""); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(second.runtime.Context().Agent.SystemPrompt(), "Two") {
		t.Error("a session that did not ask for the reload kept the stale instructions")
	}
}

// The user edited a file and asked for it to take effect: a busy session must
// not lose the request. It queues as a barrier and runs at the next idle point.
func TestReload_QueuesForABusySession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	release := make(chan struct{})
	provider := newMockProvider(func(_ context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		<-release
		return simpleResponse("done"), nil
	})
	mgr := newTestManager(t, ctx, provider)

	cwd := t.TempDir()
	writeAgentsMD(t, cwd, "- One.\n")
	busy, err := mgr.CreateSession(CreateOpts{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	idle, err := mgr.CreateSession(CreateOpts{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := mgr.Send(busy.ID, "work", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 2*time.Second, "running", func() bool {
		return sessState(busy) == StateRunning
	})

	writeAgentsMD(t, cwd, "- One.\n- Two.\n")
	res, err := mgr.ExecCommand(idle.ID, "/reload", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("a reload must never fail because a session is busy: %s", res.Message)
	}
	if !strings.Contains(res.Message, "Queued") {
		t.Errorf("the report should say the busy session is pending, got: %s", res.Message)
	}

	close(release)
	pollUntil(t, 3*time.Second, "idle", func() bool {
		return sessState(busy) == StateIdle
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(busy.runtime.Context().Agent.SystemPrompt(), "Two") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("the queued reload never applied after the session settled")
}
