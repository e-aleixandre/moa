package serve

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// A session's prompt is part of its cached prefix, and Anthropic binds every
// signed thinking block to that prefix: rebuilding it on a restart with a new
// date, commit or AGENTS.md throws away the cache and all the session's
// thinking. These tests pin the owner's rule (18/11): the prompt is frozen with
// the session, a restart or a deploy rebuilds it byte for byte, and only a
// compaction, Start fresh or a reload regenerates it.

var (
	frozenDay1 = time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	frozenDay2 = time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	frozenDay3 = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
)

// newFrozenTestManager is a Manager over a session directory that survives it,
// dating prompts with day, so a test can restart the process on another day.
func newFrozenTestManager(t *testing.T, ctx context.Context, prov core.Provider, sessionDir string, day time.Time) *Manager {
	t.Helper()
	moaCfg := core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"}
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return prov, nil },
		DefaultModel:    core.Model{ID: "claude-haiku-4-5-20251001", Provider: "anthropic"},
		WorkspaceRoot:   t.TempDir(),
		MoaCfg:          moaCfg,
		ConfigLoader:    isolatedTestConfigLoader(t, moaCfg),
		SessionBaseDir:  sessionDir,
		SchedulePath:    filepath.Join(t.TempDir(), "schedules.json"),
		promptClock:     func() time.Time { return day },
	})
	t.Cleanup(mgr.Shutdown)
	return mgr
}

// stopFrozenTestManager closes every loaded session (flushing it) and shuts the
// Manager down, as a restart does.
func stopFrozenTestManager(t *testing.T, mgr *Manager) {
	t.Helper()
	mgr.mu.RLock()
	ids := make([]string, 0, len(mgr.sessions))
	for id := range mgr.sessions {
		ids = append(ids, id)
	}
	mgr.mu.RUnlock()
	for _, id := range ids {
		if err := mgr.CloseSession(id); err != nil {
			t.Fatal(err)
		}
	}
	mgr.Shutdown()
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// frozenRepo is a git repository with AGENTS.md and one commit.
func frozenRepo(t *testing.T) string {
	t.Helper()
	cwd := t.TempDir()
	gitIn(t, cwd, "init", "-q", "-b", "main")
	writeAgentsMD(t, cwd, oldRule+"\n")
	gitIn(t, cwd, "add", "-A")
	gitIn(t, cwd, "commit", "-q", "-m", "first commit")
	return cwd
}

// changeEverything is what a restart a day later finds: another AGENTS.md and
// another last commit.
func changeEverything(t *testing.T, cwd string) {
	t.Helper()
	writeAgentsMD(t, cwd, oldRule+"\n"+newRule+"\n")
	gitIn(t, cwd, "commit", "-q", "-am", "second commit")
}

// toolsKey is the tool list as the provider receives it, in order.
func toolsKey(t *testing.T, req core.Request) string {
	t.Helper()
	b, err := json.Marshal(req.Tools)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func assertFreshContext(t *testing.T, sys, day string) {
	t.Helper()
	for _, want := range []string{"ticket id", "second commit", day} {
		if !strings.Contains(sys, want) {
			t.Errorf("regenerated prompt lacks %q:\n%s", want, sys)
		}
	}
}

func TestFrozenContext_RestartRebuildsTheSamePrefixUntilCompaction(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sessionDir := t.TempDir()
	cwd := frozenRepo(t)

	prov1 := &recordingProvider{}
	mgr1 := newFrozenTestManager(t, ctx, prov1, sessionDir, frozenDay1)
	sess := sessionWithHistory(t, mgr1, cwd)
	before := prov1.last()
	if !strings.Contains(before.System, "Wednesday, November 18, 2026") || !strings.Contains(before.System, "first commit") {
		t.Fatalf("the session did not start with the day-1 context:\n%s", before.System)
	}
	stopFrozenTestManager(t, mgr1)

	changeEverything(t, cwd)
	prov2 := &recordingProvider{}
	mgr2 := newFrozenTestManager(t, ctx, prov2, sessionDir, frozenDay2)
	resumed, err := mgr2.ResumeSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	sendAndWait(t, mgr2, resumed, "after the restart")
	after := prov2.last()
	if after.System != before.System {
		t.Errorf("a restart rebuilt the prompt:\n--- before\n%s\n--- after\n%s", before.System, after.System)
	}
	if toolsKey(t, after) != toolsKey(t, before) {
		t.Error("a restart changed the tool list")
	}

	// A second restart, with nothing new in between, still rebuilds it.
	stopFrozenTestManager(t, mgr2)
	prov3 := &recordingProvider{}
	mgr3 := newFrozenTestManager(t, ctx, prov3, sessionDir, frozenDay3)
	resumed, err = mgr3.ResumeSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	sendAndWait(t, mgr3, resumed, "after the second restart")
	if got := prov3.last().System; got != before.System {
		t.Errorf("the second restart rebuilt the prompt:\n%s", got)
	}

	// A compaction regenerates everything: the disk, the date and the commit.
	compactAndSettle(t, mgr3, resumed, prov3)
	sendAndWait(t, mgr3, resumed, "after compaction")
	regenerated := prov3.last().System
	assertFreshContext(t, regenerated, "Friday, November 20, 2026")

	// And what the compaction produced is the new frozen prompt.
	stopFrozenTestManager(t, mgr3)
	prov4 := &recordingProvider{}
	mgr4 := newFrozenTestManager(t, ctx, prov4, sessionDir, frozenDay3.AddDate(0, 0, 5))
	resumed, err = mgr4.ResumeSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	sendAndWait(t, mgr4, resumed, "after the third restart")
	if got := prov4.last().System; got != regenerated {
		t.Errorf("the restart after a compaction did not keep its prompt:\n--- want\n%s\n--- got\n%s", regenerated, got)
	}
}

func TestFrozenContext_StartFreshRegenerates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sessionDir := t.TempDir()
	cwd := frozenRepo(t)

	prov1 := &recordingProvider{}
	mgr1 := newFrozenTestManager(t, ctx, prov1, sessionDir, frozenDay1)
	sess := sessionWithHistory(t, mgr1, cwd)
	stopFrozenTestManager(t, mgr1)

	// Only the day and the commit move: a reload would keep the prompt, so
	// this tells regenerating from reloading.
	gitIn(t, cwd, "commit", "-q", "--allow-empty", "-m", "second commit")
	prov2 := &recordingProvider{}
	mgr2 := newFrozenTestManager(t, ctx, prov2, sessionDir, frozenDay2)
	resumed, err := mgr2.ResumeSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	sendAndWait(t, mgr2, resumed, "a turn to cut")
	res, err := mgr2.ExecCommand(resumed.ID, "/start-fresh", "")
	if err != nil || !res.OK {
		t.Fatalf("start-fresh = %+v, %v", res, err)
	}
	sendAndWait(t, mgr2, resumed, "after fresh")
	sys := prov2.last().System
	for _, want := range []string{"Thursday, November 19, 2026", "second commit"} {
		if !strings.Contains(sys, want) {
			t.Errorf("start fresh did not regenerate %q:\n%s", want, sys)
		}
	}
}

// A session saved by a binary that did not freeze its prompt has none: it is
// built with today's context once, and that build is what it keeps.
func TestFrozenContext_SessionWithoutSnapshotFreezesOnFirstTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sessionDir := t.TempDir()
	cwd := frozenRepo(t)

	prov1 := &recordingProvider{}
	mgr1 := newFrozenTestManager(t, ctx, prov1, sessionDir, frozenDay1)
	sess := sessionWithHistory(t, mgr1, cwd)
	stopFrozenTestManager(t, mgr1)
	stripPromptContext(t, sessionDir, sess.ID)

	changeEverything(t, cwd)
	prov2 := &recordingProvider{}
	mgr2 := newFrozenTestManager(t, ctx, prov2, sessionDir, frozenDay2)
	resumed, err := mgr2.ResumeSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	sendAndWait(t, mgr2, resumed, "first turn on the new binary")
	frozen := prov2.last().System
	assertFreshContext(t, frozen, "Thursday, November 19, 2026")
	stopFrozenTestManager(t, mgr2)

	writeAgentsMD(t, cwd, "- A third rule nobody reloaded.\n")
	prov3 := &recordingProvider{}
	mgr3 := newFrozenTestManager(t, ctx, prov3, sessionDir, frozenDay3)
	resumed, err = mgr3.ResumeSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	sendAndWait(t, mgr3, resumed, "after another restart")
	if got := prov3.last().System; got != frozen {
		t.Errorf("the session did not keep the prompt it froze on its first turn:\n%s", got)
	}
}

// stripPromptContext removes the frozen prompt from a saved session, leaving
// the file as a binary that predates it wrote it.
func stripPromptContext(t *testing.T, sessionDir, id string) {
	t.Helper()
	var path string
	_ = filepath.Walk(sessionDir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && filepath.Base(p) == id+".json" {
			path = p
		}
		return nil
	})
	if path == "" {
		t.Fatalf("session file %s not found", id)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Rewrite key by key: the file's key order matters (listing stops reading
	// at "entries").
	dec := json.NewDecoder(strings.NewReader(string(data)))
	if _, err := dec.Token(); err != nil {
		t.Fatal(err)
	}
	var out []byte
	out = append(out, '{')
	first := true
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			t.Fatal(err)
		}
		if key == "prompt_context" {
			continue
		}
		if !first {
			out = append(out, ',')
		}
		first = false
		k, _ := json.Marshal(key)
		out = append(append(append(out, k...), ':'), value...)
	}
	out = append(out, '}')
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

// An owner's conversation freezes its role and its book like any other input:
// the book is edited all the time, and a restart must not rewrite the prompt.
func TestFrozenContext_OwnerKeepsItsBookAcrossARestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	sessionDir := t.TempDir()
	root := frozenRepo(t)

	prov1 := &recordingProvider{}
	mgr1 := newFrozenTestManager(t, ctx, prov1, sessionDir, frozenDay1)
	info, err := mgr1.CreateOwner(CreateOwnerOpts{Root: root, Name: "Winerim"})
	if err != nil {
		t.Fatal(err)
	}
	writeBookIndex(t, info.CodebaseKey, "# Project\n\nbook v1\n")
	if _, err := mgr1.ExecCommand(info.SessionID, "/reload", ""); err != nil {
		t.Fatal(err)
	}
	ownerSess, _ := mgr1.Get(info.SessionID)
	sendAndWait(t, mgr1, ownerSess, "hello owner")
	before := prov1.last().System
	if !strings.Contains(before, "book v1") || !strings.Contains(before, "You are the owner of this project") {
		t.Fatalf("the owner did not start with its role and book:\n%s", before)
	}
	stopFrozenTestManager(t, mgr1)

	writeBookIndex(t, info.CodebaseKey, "# Project\n\nbook v2\n")
	changeEverything(t, root)
	prov2 := &recordingProvider{}
	mgr2 := newFrozenTestManager(t, ctx, prov2, sessionDir, frozenDay2)
	resumed, err := mgr2.ResumeSession(info.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	sendAndWait(t, mgr2, resumed, "after the restart")
	if got := prov2.last().System; got != before {
		t.Errorf("a restart rebuilt the owner's prompt:\n%s", got)
	}
}

// /reload rebuilds only a session whose files changed, and then takes today's
// date and commit too. A new day or commit alone leaves the prompt, and its
// cache, alone. What it installs is saved at once: a restart before the next
// turn keeps it.
func TestFrozenContext_ReloadRegeneratesOnlyOnAChangedFile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sessionDir := t.TempDir()
	cwd := frozenRepo(t)

	prov1 := &recordingProvider{}
	mgr1 := newFrozenTestManager(t, ctx, prov1, sessionDir, frozenDay1)
	sess := sessionWithHistory(t, mgr1, cwd)
	before := prov1.last().System
	stopFrozenTestManager(t, mgr1)

	gitIn(t, cwd, "commit", "-q", "--allow-empty", "-m", "second commit")
	prov2 := &recordingProvider{}
	mgr2 := newFrozenTestManager(t, ctx, prov2, sessionDir, frozenDay2)
	resumed, err := mgr2.ResumeSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := mgr2.ExecCommand(resumed.ID, "/reload", ""); err != nil || !res.OK {
		t.Fatalf("reload = %+v, %v", res, err)
	}
	sendAndWait(t, mgr2, resumed, "after a reload with no file changed")
	if got := prov2.last().System; got != before {
		t.Errorf("a reload with no file changed rebuilt the prompt:\n%s", got)
	}

	writeAgentsMD(t, cwd, oldRule+"\n"+newRule+"\n")
	if res, err := mgr2.ExecCommand(resumed.ID, "/reload", ""); err != nil || !res.OK {
		t.Fatalf("reload = %+v, %v", res, err)
	}
	reloaded := systemPromptOf(t, resumed)
	assertFreshContext(t, reloaded, "Thursday, November 19, 2026")

	// Restart before any turn: the reload must already be on disk.
	stopFrozenTestManager(t, mgr2)
	prov3 := &recordingProvider{}
	mgr3 := newFrozenTestManager(t, ctx, prov3, sessionDir, frozenDay3)
	resumed, err = mgr3.ResumeSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	sendAndWait(t, mgr3, resumed, "after the restart")
	if got := prov3.last().System; got != reloaded {
		t.Errorf("the restart lost the reloaded prompt:\n--- want\n%s\n--- got\n%s", reloaded, got)
	}
}
