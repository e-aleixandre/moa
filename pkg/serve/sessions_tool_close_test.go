package serve

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

func TestSessionsToolCloseKeepsAnOwnSessionSavedAndReopenable(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")
	child, err := mgr.CreateSession(CreateOpts{CWD: root, Origin: "owner", Title: "stuck on a provider error"})
	if err != nil {
		t.Fatal(err)
	}

	res := runSessionsTool(t, ownerSess, map[string]any{"action": "close", "session_id": child.ID})
	if res.IsError {
		t.Fatalf("close failed: %s", toolText(res))
	}
	if _, loaded := mgr.Get(child.ID); loaded {
		t.Fatal("session still loaded after close")
	}
	if _, _, err := session.FindSession(mgr.sessionBaseDir, child.ID); err != nil {
		t.Fatalf("closed session is not saved: %v", err)
	}
	var state SessionState
	for _, info := range mgr.List() {
		if info.ID == child.ID {
			state = info.State
		}
	}
	if state != StateSaved {
		t.Fatalf("listed state = %q, want saved", state)
	}
	if _, err := mgr.ResumeSession(child.ID); err != nil {
		t.Fatalf("closed session cannot be reopened: %v", err)
	}
	// Closing again, from disk, is the UI's idempotent no-op.
	_ = mgr.CloseSession(child.ID)
	if res := runSessionsTool(t, ownerSess, map[string]any{"action": "close", "session_id": child.ID}); res.IsError {
		t.Fatalf("close of a saved own session failed: %s", toolText(res))
	}
}

func TestSessionsToolCloseRefusesWhatIsNotOwnStopped(t *testing.T) {
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

	users, err := mgr.CreateSession(CreateOpts{CWD: root, Title: "the user's"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := mgr.CreateSession(CreateOpts{CWD: t.TempDir(), Origin: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	working, err := mgr.CreateSession(CreateOpts{CWD: root, Origin: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(working.ID, "go", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 2*time.Second, "running", func() bool { return sessState(working) == StateRunning })

	for name, tc := range map[string]struct{ id, want string }{
		"the user's":       {users.ID, "user's"},
		"another project":  {other.ID, "does not belong"},
		"working":          {working.ID, "is working"},
		"own conversation": {ownerSess.ID, "your own"},
		"unknown":          {"nope", "not found"},
	} {
		res := runSessionsTool(t, ownerSess, map[string]any{"action": "close", "session_id": tc.id})
		if !res.IsError || !strings.Contains(toolText(res), tc.want) {
			t.Errorf("%s: res=%v %q, want an error containing %q", name, res.IsError, toolText(res), tc.want)
		}
		if _, loaded := mgr.Get(tc.id); !loaded && name != "unknown" {
			t.Errorf("%s: session was closed anyway", name)
		}
	}
}

func TestSessionsToolCloseSaysWhenATaskLosesItsExecutor(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")
	task := liveOwnerTask(t, mgr, ownerSess, "export the invoices")

	res := runSessionsTool(t, ownerSess, map[string]any{"action": "new", "cwd": root, "text": "export", "title": "exporter", "task": float64(task.ID)})
	if res.IsError {
		t.Fatalf("new failed: %s", toolText(res))
	}
	child := liveSessionTitled(t, mgr, "exporter")
	pollUntil(t, 5*time.Second, "the run to end", func() bool { return sessState(child) == StateIdle })

	res = runSessionsTool(t, ownerSess, map[string]any{"action": "close", "session_id": child.ID})
	if res.IsError {
		t.Fatalf("close failed: %s", toolText(res))
	}
	if txt := toolText(res); !strings.Contains(txt, "no executor") || !strings.Contains(txt, "#") {
		t.Fatalf("close does not report the orphaned task: %s", txt)
	}
}

// ownerOnDaybreakManager is a daybreakManager (GPT-6 Sol, real FileStore)
// with an owner for root and the given catalog.
func ownerOnDaybreakManager(t *testing.T, root string, catalog func() core.DaybreakAvailability) (*Manager, *ManagedSession) {
	t.Helper()
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	mgr := daybreakManager(t, &daybreakRecorder{}, t.TempDir(), root, false)
	mgr.daybreakCatalog = catalog
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")
	return mgr, ownerSess
}

func TestSessionsToolNewDaybreakIsDurableWhenAvailable(t *testing.T) {
	root := t.TempDir()
	mgr, ownerSess := ownerOnDaybreakManager(t, root, daybreakCatalogKnown)
	res := runSessionsTool(t, ownerSess, map[string]any{"action": "new", "cwd": root, "text": "audit", "title": "audit", "task": "none", "model": "daybreak"})
	if res.IsError {
		t.Fatalf("new failed: %s", toolText(res))
	}
	child := liveSessionTitled(t, mgr, "audit")
	if !child.runtime.Context().Agent.Daybreak() {
		t.Fatal("session did not start with Daybreak")
	}
	saved, _, err := session.FindSession(mgr.sessionBaseDir, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.DaybreakMeta() || saved.Metadata[session.MetaModel] != "openai/gpt-6-sol" {
		t.Fatalf("not durable: model=%v daybreak=%v", saved.Metadata[session.MetaModel], saved.Metadata[session.MetaDaybreak])
	}
}

func TestSessionsToolNewDaybreakUnavailableCreatesNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		catalog func() core.DaybreakAvailability
		want    string
	}{
		"no plan login": {func() core.DaybreakAvailability { return core.DaybreakAvailability{} }, "plan login"},
		"catalog lacks it": {func() core.DaybreakAvailability {
			return core.DaybreakAvailability{Transport: true, Known: true, Models: map[string]bool{"gpt-6-sol": false}}
		}, "catalog does not offer"},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			mgr, ownerSess := ownerOnDaybreakManager(t, root, tc.catalog)
			before := len(mgr.List())
			res := runSessionsTool(t, ownerSess, map[string]any{"action": "new", "cwd": root, "text": "audit", "task": "none", "model": "daybreak"})
			if !res.IsError || !strings.Contains(toolText(res), tc.want) {
				t.Fatalf("res=%v %q, want an error containing %q", res.IsError, toolText(res), tc.want)
			}
			if got := len(mgr.List()); got != before {
				t.Fatalf("sessions = %d, want %d: a session was created", got, before)
			}
		})
	}
}
