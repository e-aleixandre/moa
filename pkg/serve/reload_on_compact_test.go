package serve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/book"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/owner"
)

// newCompactPromptManager is newFreshTestManager with a chosen context window,
// so a test can push a session over its automatic-compaction threshold with a
// handful of turns.
func newCompactPromptManager(t *testing.T, ctx context.Context, provider core.Provider, maxInput int) *Manager {
	t.Helper()
	moaCfg := core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"}
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return provider, nil },
		DefaultModel:    core.Model{ID: "claude-haiku-4-5-20251001", Provider: "anthropic", MaxInput: maxInput},
		WorkspaceRoot:   t.TempDir(),
		MoaCfg:          moaCfg,
		ConfigLoader:    isolatedTestConfigLoader(t, moaCfg),
		SessionBaseDir:  t.TempDir(),
		SchedulePath:    filepath.Join(t.TempDir(), "schedules.json"),
	})
	t.Cleanup(func() {
		mgr.mu.RLock()
		ids := make([]string, 0, len(mgr.sessions))
		for id := range mgr.sessions {
			ids = append(ids, id)
		}
		mgr.mu.RUnlock()
		for _, id := range ids {
			_ = mgr.Delete(id)
		}
		mgr.Shutdown()
	})
	return mgr
}

const (
	oldRule = "- Commit messages in English."
	newRule = "- Start every commit with the ticket id."
)

// sessionWithHistory creates a session over cwd and gives it enough turns for a
// compaction or a fresh start to have something to cut.
func sessionWithHistory(t *testing.T, mgr *Manager, cwd string) *ManagedSession {
	t.Helper()
	sess, err := mgr.CreateSession(CreateOpts{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		sendAndWait(t, mgr, sess, turn(i))
	}
	return sess
}

func compactAndSettle(t *testing.T, mgr *Manager, sess *ManagedSession, prov *recordingProvider) {
	t.Helper()
	before := prov.count()
	res, err := mgr.ExecCommand(sess.ID, "/compact", "")
	if err != nil || !res.OK {
		t.Fatalf("compact = %+v, %v", res, err)
	}
	pollUntil(t, 10*time.Second, "compaction settled", func() bool {
		return prov.count() > before && sessState(sess) == StateIdle
	})
	sess.runtime.Bus.Drain(2 * time.Second)
}

func TestReloadOnCompact_ManualCompactionPicksUpEditedAgentsMD(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prov := &recordingProvider{}
	mgr := newFreshTestManager(t, ctx, prov)
	cwd := t.TempDir()
	writeAgentsMD(t, cwd, oldRule+"\n")
	sess := sessionWithHistory(t, mgr, cwd)
	if !strings.Contains(prov.last().System, "Commit messages in English") {
		t.Fatal("the session did not start with AGENTS.md in its prompt")
	}

	writeAgentsMD(t, cwd, oldRule+"\n"+newRule+"\n")
	// Editing the file alone must not change a running session.
	sendAndWait(t, mgr, sess, "still old")
	if strings.Contains(prov.last().System, "ticket id") {
		t.Fatal("the edit reached the prompt without a compaction or /reload")
	}

	compactAndSettle(t, mgr, sess, prov)
	sendAndWait(t, mgr, sess, "after compaction")
	if sys := prov.last().System; !strings.Contains(sys, "ticket id") {
		t.Errorf("the request after compaction lacks the edited AGENTS.md:\n%s", sys)
	}
}

func TestReloadOnCompact_StartFreshPicksUpEditedAgentsMD(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prov := &recordingProvider{}
	mgr := newFreshTestManager(t, ctx, prov)
	cwd := t.TempDir()
	writeAgentsMD(t, cwd, oldRule+"\n")
	sess := sessionWithHistory(t, mgr, cwd)

	writeAgentsMD(t, cwd, oldRule+"\n"+newRule+"\n")
	res, err := mgr.ExecCommand(sess.ID, "/start-fresh", "")
	if err != nil || !res.OK {
		t.Fatalf("start-fresh = %+v, %v", res, err)
	}
	sendAndWait(t, mgr, sess, "after fresh")
	if sys := prov.last().System; !strings.Contains(sys, "ticket id") {
		t.Errorf("the request after Start fresh lacks the edited AGENTS.md:\n%s", sys)
	}
}

func TestReloadOnCompact_StartFreshCannotOvertakeReload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prov := &recordingProvider{}
	mgr := newFreshTestManager(t, ctx, prov)
	cwd := t.TempDir()
	writeAgentsMD(t, cwd, oldRule+"\n")
	sess := sessionWithHistory(t, mgr, cwd)

	built := make(chan struct{})
	release := make(chan struct{})
	var blocked atomic.Bool
	build := sess.infra.buildBasePrompt
	sess.infra.buildBasePrompt = func(specs []core.ToolSpec) string {
		prompt := build(specs)
		if strings.Contains(prompt, newRule) && blocked.CompareAndSwap(false, true) {
			close(built)
			<-release
		}
		return prompt
	}
	writeAgentsMD(t, cwd, oldRule+"\n"+newRule+"\n")
	reloadDone := make(chan error, 1)
	go func() {
		_, err := mgr.ExecCommand(sess.ID, "/reload", "")
		reloadDone <- err
	}()
	select {
	case <-built:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("/reload did not reach the prompt builder")
	}
	writeAgentsMD(t, cwd, oldRule+"\n"+newRule+"\n- Newer rule after reload.\n")
	freshDone := make(chan error, 1)
	go func() {
		_, err := mgr.ExecCommand(sess.ID, "/start-fresh", "")
		freshDone <- err
	}()
	freshFinished := false
	select {
	case err := <-freshDone:
		if err != nil {
			t.Errorf("start fresh: %v", err)
		}
		freshFinished = true
		t.Error("Start fresh overtook an in-progress reload")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	waits := map[string]<-chan error{"reload": reloadDone}
	if !freshFinished {
		waits["start fresh"] = freshDone
	}
	for name, done := range waits {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not finish", name)
		}
	}
	if sys := sess.runtime.Context().Agent.SystemPrompt(); !strings.Contains(sys, "Newer rule after reload") {
		t.Error("the older reload overwrote the newer Start fresh prompt")
	}
	if changed := sess.reloadSession(); len(changed) != 0 {
		t.Errorf("prompt sources are still out of sync: %v", changed)
	}
}

func TestReloadOnCompact_ReloadLostAdmissionQueuesCallerAndUpdatesOthers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newFreshTestManager(t, ctx, &recordingProvider{})
	cwd := t.TempDir()
	writeAgentsMD(t, cwd, oldRule+"\n")
	caller, err := mgr.CreateSession(CreateOpts{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	other, err := mgr.CreateSession(CreateOpts{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	writeAgentsMD(t, cwd, oldRule+"\n"+newRule+"\n")
	if err := caller.runtime.State.Transition(bus.StateRunning); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := caller.runtime.State.Transition(bus.StateIdle); err != nil {
			t.Error(err)
		}
	}()
	res, err := reloadAfterIdleCheck(mgr, caller, "client-id")
	if err != nil || !res.OK || !res.Queued || res.ID != "client-id" {
		t.Fatalf("raced reload = %+v, %v", res, err)
	}
	if strings.Contains(caller.runtime.Context().Agent.SystemPrompt(), newRule) {
		t.Error("caller prompt was changed mid-run")
	}
	if !strings.Contains(other.runtime.Context().Agent.SystemPrompt(), newRule) {
		t.Error("the caller's lost slot prevented reloading the other session")
	}
}

func TestReloadOnCompact_ReloadLostAdmissionReportsQueueFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newFreshTestManager(t, ctx, &recordingProvider{})
	cwd := t.TempDir()
	writeAgentsMD(t, cwd, oldRule+"\n")
	caller, err := mgr.CreateSession(CreateOpts{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	writeAgentsMD(t, cwd, oldRule+"\n"+newRule+"\n")
	if err := caller.runtime.State.Transition(bus.StateRunning); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := caller.runtime.State.Transition(bus.StateIdle); err != nil {
			t.Error(err)
		}
	}()
	full := false
	for i := 0; i < 64; i++ {
		if err := caller.runtime.Bus.Execute(bus.QueueCommand{ID: core.NewSteerID(), Raw: "/reload"}); err != nil {
			full = true
			break
		}
	}
	if !full {
		t.Fatal("the reload queue did not fill")
	}
	res, err := reloadAfterIdleCheck(mgr, caller, "client-id")
	if err == nil && (res == nil || res.OK) {
		t.Fatalf("failed enqueue was reported as success: %+v", res)
	}
}

func TestReloadOnCompact_OwnerFreshReloadsBookAndRole(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prov := &recordingProvider{}
	mgr := newFreshTestManager(t, ctx, prov)
	info, err := mgr.CreateOwner(CreateOwnerOpts{Root: t.TempDir(), Name: "Winerim"})
	if err != nil {
		t.Fatal(err)
	}
	sess, ok := mgr.Get(info.SessionID)
	if !ok {
		t.Fatal("owner session missing")
	}
	for i := range 6 {
		sendAndWait(t, mgr, sess, turn(i))
	}
	store, err := owner.Default()
	if err != nil {
		t.Fatal(err)
	}
	writeBookIndex(t, info.CodebaseKey, "# Project\n\nUpdated book content.\n")
	if err := os.WriteFile(filepath.Join(store.BookDir(info.CodebaseKey), book.OwnerFile), []byte("Updated owner preference.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := mgr.ExecCommand(sess.ID, "/start-fresh", "")
	if err != nil || !res.OK {
		t.Fatalf("start fresh = %+v, %v", res, err)
	}
	sendAndWait(t, mgr, sess, "after fresh")
	sys := prov.last().System
	for _, want := range []string{"Updated book content", "Updated owner preference", "You are the owner of this project"} {
		if !strings.Contains(sys, want) {
			t.Errorf("owner prompt after Start fresh lacks %q", want)
		}
	}
}

// An automatic compaction lands in the middle of a run, where the agent refuses
// SetSystemPrompt: the loop has to pick the prompt up itself.
func TestReloadOnCompact_AutomaticCompactionPicksUpEditedAgentsMD(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prov := &recordingProvider{}
	mgr := newCompactPromptManager(t, ctx, prov, 60_000)
	cwd := t.TempDir()
	writeAgentsMD(t, cwd, oldRule+"\n")
	sess, err := mgr.CreateSession(CreateOpts{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	sendAndWait(t, mgr, sess, turn(0))
	writeAgentsMD(t, cwd, oldRule+"\n"+newRule+"\n")

	epoch := sess.runtime.Context().Agent.CompactionEpoch()
	for i := 1; sess.runtime.Context().Agent.CompactionEpoch() == epoch && i < 12; i++ {
		sendAndWait(t, mgr, sess, turn(i))
	}
	if sess.runtime.Context().Agent.CompactionEpoch() == epoch {
		t.Fatal("no automatic compaction happened; the test does not exercise it")
	}
	if sys := prov.last().System; !strings.Contains(sys, "ticket id") {
		t.Errorf("the request after the automatic compaction lacks the edited AGENTS.md")
	}
	// The stored prompt follows too, so the next run starts from it.
	if !strings.Contains(sess.runtime.Context().Agent.SystemPrompt(), "ticket id") {
		t.Error("the agent's stored prompt is still the old one")
	}
}

func TestReloadOnCompact_OnlyTheCompactingSessionChanges(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prov := &recordingProvider{}
	mgr := newFreshTestManager(t, ctx, prov)
	cwd := t.TempDir()
	writeAgentsMD(t, cwd, oldRule+"\n")
	a := sessionWithHistory(t, mgr, cwd)
	b, err := mgr.CreateSession(CreateOpts{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	bBefore := b.runtime.Context().Agent.SystemPrompt()

	writeAgentsMD(t, cwd, oldRule+"\n"+newRule+"\n")
	compactAndSettle(t, mgr, a, prov)

	if !strings.Contains(a.runtime.Context().Agent.SystemPrompt(), "ticket id") {
		t.Error("the compacting session did not reload")
	}
	if got := b.runtime.Context().Agent.SystemPrompt(); got != bBefore {
		t.Error("another session's prompt changed when this one compacted")
	}
}

func TestReloadOnCompact_UnchangedSourcesLeaveThePromptByteIdentical(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prov := &recordingProvider{}
	mgr := newFreshTestManager(t, ctx, prov)
	cwd := t.TempDir()
	writeAgentsMD(t, cwd, oldRule+"\n")
	sess := sessionWithHistory(t, mgr, cwd)
	before := prov.last().System

	compactAndSettle(t, mgr, sess, prov)
	sendAndWait(t, mgr, sess, "after compaction")
	if after := prov.last().System; after != before {
		t.Error("the prompt changed although no source did")
	}
}

func TestReloadOnCompact_SubagentAfterCompactionSeesTheNewAgentsMD(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prov := &recordingProvider{}
	mgr := newFreshTestManager(t, ctx, prov)
	cwd := t.TempDir()
	writeAgentsMD(t, cwd, oldRule+"\n")
	sess := sessionWithHistory(t, mgr, cwd)

	writeAgentsMD(t, cwd, oldRule+"\n"+newRule+"\n")
	compactAndSettle(t, mgr, sess, prov)

	tl, ok := sess.infra.toolReg.Get("subagent")
	if !ok {
		t.Fatal("no subagent tool")
	}
	if _, err := tl.Execute(ctx, map[string]any{"task": "say hi"}, func(core.Result) {}); err != nil {
		t.Fatalf("subagent: %v", err)
	}
	if sys := prov.last().System; !strings.Contains(sys, "ticket id") {
		t.Errorf("the subagent started after the compaction did not get the new AGENTS.md")
	}
}

func TestReloadOnCompact_PrepareCompactPicksUpEditedAgentsMD(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prov := &recordingProvider{}
	mgr := newFreshTestManager(t, ctx, prov)
	cwd := t.TempDir()
	writeAgentsMD(t, cwd, oldRule+"\n")
	sess := sessionWithHistory(t, mgr, cwd)

	writeAgentsMD(t, cwd, oldRule+"\n"+newRule+"\n")
	epoch := sess.runtime.Context().Agent.CompactionEpoch()
	res, err := mgr.ExecCommand(sess.ID, "/prepare-compact", "")
	if err != nil || !res.OK {
		t.Fatalf("prepare-compact = %+v, %v", res, err)
	}
	pollUntil(t, 10*time.Second, "prepare-compact settled", func() bool {
		return sess.runtime.Context().Agent.CompactionEpoch() > epoch && sessState(sess) == StateIdle
	})
	sess.runtime.Bus.Drain(2 * time.Second)
	sendAndWait(t, mgr, sess, "after prepare-compact")
	if sys := prov.last().System; !strings.Contains(sys, "ticket id") {
		t.Errorf("the request after prepare-compact lacks the edited AGENTS.md")
	}
}
