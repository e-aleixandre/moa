package serve

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestSessionsToolStop(t *testing.T) {
	ctx := context.Background()
	blocking := func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 1)
		go func() {
			defer close(ch)
			<-ctx.Done()
			ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: ctx.Err()}
		}()
		return ch, nil
	}
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	mgr := newTestManager(t, ctx, newMockProvider(blocking))
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")
	stop := func(id string) core.Result {
		return runSessionsTool(t, ownerSess, map[string]any{"action": "stop", "session_id": id})
	}

	working, err := mgr.CreateSession(CreateOpts{CWD: root, Origin: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(working.ID, "go", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 2*time.Second, "running", func() bool { return sessState(working) == StateRunning })

	if res := runSessionsTool(t, ownerSess, map[string]any{"action": "send", "session_id": working.ID, "text": "queued correction"}); res.IsError {
		t.Fatalf("send while running: %s", toolText(res))
	}
	res := stop(working.ID)
	if res.IsError || !strings.Contains(toolText(res), "Stopped") || !strings.Contains(toolText(res), "«queued correction»") {
		t.Fatalf("stop of a working session: err=%v %q", res.IsError, toolText(res))
	}
	pollUntil(t, 5*time.Second, "idle", func() bool { return sessState(working) == StateIdle })
	if _, loaded := mgr.Get(working.ID); !loaded {
		t.Fatal("stop closed the session")
	}

	if res := stop(working.ID); res.IsError || !strings.Contains(toolText(res), "already idle") {
		t.Fatalf("stop of an idle session: err=%v %q", res.IsError, toolText(res))
	}

	// A later send resumes it.
	if _, _, _, err := mgr.Send(working.ID, "again", nil, "", ""); err != nil {
		t.Fatalf("send after stop: %v", err)
	}
	pollUntil(t, 2*time.Second, "running again", func() bool { return sessState(working) == StateRunning })
	stop(working.ID)

	// Saved but not loaded: nothing is running, so it is idle, not "not found".
	saved, err := mgr.CreateSession(CreateOpts{CWD: root, Origin: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.CloseSession(saved.ID); err != nil {
		t.Fatal(err)
	}
	if res := stop(saved.ID); res.IsError || !strings.Contains(toolText(res), "already idle") {
		t.Fatalf("stop of a saved session: err=%v %q", res.IsError, toolText(res))
	}

	users, err := mgr.CreateSession(CreateOpts{CWD: root, Title: "the user's"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := mgr.CreateSession(CreateOpts{CWD: t.TempDir(), Origin: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct{ id, want string }{
		"the user's":       {users.ID, "belongs to the user"},
		"another project":  {other.ID, "does not belong"},
		"own conversation": {ownerSess.ID, "your own"},
		"unknown":          {"nope", "not found"},
	} {
		if res := stop(tc.id); !res.IsError || !strings.Contains(toolText(res), tc.want) {
			t.Errorf("%s: res=%v %q, want an error containing %q", name, res.IsError, toolText(res), tc.want)
		}
	}
}
