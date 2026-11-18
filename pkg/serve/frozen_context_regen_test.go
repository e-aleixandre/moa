package serve

// Every cut regenerates the date and the commit even when no file changed,
// and nothing else does. The frozen prompt is marked with a sentinel so a
// rebuild from the restored inputs cannot pass for a restore.

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

const frozenSentinel = "SENTINEL-built-by-an-older-binary"

func frozenManagerWithWindow(t *testing.T, ctx context.Context, prov core.Provider, sessionDir string, day time.Time, maxInput int) *Manager {
	t.Helper()
	moaCfg := core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"}
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return prov, nil },
		DefaultModel:    core.Model{ID: "claude-haiku-4-5-20251001", Provider: "anthropic", MaxInput: maxInput},
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

func sessionFile(t *testing.T, sessionDir, id string) string {
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
	return path
}

// rewriteKey rewrites one top-level key of a session file in place, keeping
// the key order (listing stops reading at "entries").
func rewriteKey(t *testing.T, path, key string, edit func(json.RawMessage) json.RawMessage) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	if _, err := dec.Token(); err != nil {
		t.Fatal(err)
	}
	out := []byte{'{'}
	first := true
	found := false
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			t.Fatal(err)
		}
		if k == key {
			found = true
			value = edit(value)
			if value == nil {
				continue
			}
		}
		if !first {
			out = append(out, ',')
		}
		first = false
		kb, _ := json.Marshal(k)
		out = append(append(append(out, kb...), ':'), value...)
	}
	if !found {
		t.Fatalf("%s has no %q", path, key)
	}
	out = append(out, '}')
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

// tamperFrozenSystem marks the frozen prompt the way a prompt built by an
// older binary differs from what this one builds: any rebuild drops the mark.
func tamperFrozenSystem(t *testing.T, sessionDir, id string) {
	t.Helper()
	rewriteKey(t, sessionFile(t, sessionDir, id), "prompt_context", func(v json.RawMessage) json.RawMessage {
		var pc map[string]any
		if err := json.Unmarshal(v, &pc); err != nil {
			t.Fatal(err)
		}
		pc["system"] = pc["system"].(string) + "\n" + frozenSentinel
		b, _ := json.Marshal(pc)
		return b
	})
}

// restartedFrozen builds a session on day 1, restarts on day 2 after a commit
// (no file changes) and returns the resumed session with its frozen prompt
// marked.
func restartedFrozen(t *testing.T, ctx context.Context, maxInput int, history func(*Manager, string) *ManagedSession) (*Manager, *ManagedSession, *recordingProvider) {
	t.Helper()
	sessionDir := t.TempDir()
	cwd := frozenRepo(t)
	prov1 := &recordingProvider{}
	mgr1 := frozenManagerWithWindow(t, ctx, prov1, sessionDir, frozenDay1, maxInput)
	sess := history(mgr1, cwd)
	stopFrozenTestManager(t, mgr1)
	tamperFrozenSystem(t, sessionDir, sess.ID)

	gitIn(t, cwd, "commit", "-q", "--allow-empty", "-m", "second commit")
	prov2 := &recordingProvider{}
	mgr2 := frozenManagerWithWindow(t, ctx, prov2, sessionDir, frozenDay2, maxInput)
	resumed, err := mgr2.ResumeSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	return mgr2, resumed, prov2
}

func assertRegenerated(t *testing.T, sys string) {
	t.Helper()
	if strings.Contains(sys, frozenSentinel) {
		t.Error("the cut kept the frozen prompt: it did not regenerate")
	}
	for _, want := range []string{"Thursday, November 19, 2026", "second commit"} {
		if !strings.Contains(sys, want) {
			t.Errorf("the regenerated prompt lacks %q", want)
		}
	}
}

func withHistory(t *testing.T) func(*Manager, string) *ManagedSession {
	return func(mgr *Manager, cwd string) *ManagedSession { return sessionWithHistory(t, mgr, cwd) }
}

func TestFrozenReview_ManualCompactionRegeneratesWithoutAFileChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr, sess, prov := restartedFrozen(t, ctx, 0, withHistory(t))
	sendAndWait(t, mgr, sess, "first turn after the restart")
	if !strings.Contains(prov.last().System, frozenSentinel) {
		t.Fatal("the restart did not restore the frozen prompt byte for byte")
	}
	compactAndSettle(t, mgr, sess, prov)
	sendAndWait(t, mgr, sess, "after compaction")
	assertRegenerated(t, prov.last().System)
}

func TestFrozenReview_StartFreshRegeneratesWithoutAFileChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr, sess, prov := restartedFrozen(t, ctx, 0, withHistory(t))
	sendAndWait(t, mgr, sess, "a turn to cut")
	res, err := mgr.ExecCommand(sess.ID, "/start-fresh", "")
	if err != nil || !res.OK {
		t.Fatalf("start-fresh = %+v, %v", res, err)
	}
	sendAndWait(t, mgr, sess, "after fresh")
	assertRegenerated(t, prov.last().System)
}

func TestFrozenReview_PrepareCompactRegeneratesWithoutAFileChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr, sess, prov := restartedFrozen(t, ctx, 0, withHistory(t))
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
	assertRegenerated(t, prov.last().System)
}

func TestFrozenReview_AutomaticCompactionRegeneratesWithoutAFileChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr, sess, prov := restartedFrozen(t, ctx, 60_000, func(mgr *Manager, cwd string) *ManagedSession {
		s, err := mgr.CreateSession(CreateOpts{CWD: cwd})
		if err != nil {
			t.Fatal(err)
		}
		sendAndWait(t, mgr, s, turn(0))
		return s
	})
	epoch := sess.runtime.Context().Agent.CompactionEpoch()
	for i := 1; sess.runtime.Context().Agent.CompactionEpoch() == epoch && i < 30; i++ {
		sendAndWait(t, mgr, sess, turn(i))
	}
	if sess.runtime.Context().Agent.CompactionEpoch() == epoch {
		t.Fatal("no automatic compaction happened")
	}
	assertRegenerated(t, sess.runtime.Context().Agent.SystemPrompt())
	sendAndWait(t, mgr, sess, "after the automatic compaction")
	assertRegenerated(t, prov.last().System)
}

// The operations that are not a regeneration keep the frozen prompt.
func TestFrozenReview_NonRegeneratingOperationsKeepTheFrozenPrompt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr, sess, prov := restartedFrozen(t, ctx, 0, withHistory(t))
	for _, cmd := range []string{"/reload", "/thinking high", "/model claude-sonnet-4-5", "/permissions"} {
		if _, err := mgr.ExecCommand(sess.ID, cmd, ""); err != nil {
			t.Fatalf("%s: %v", cmd, err)
		}
		sendAndWait(t, mgr, sess, "after "+cmd)
		if !strings.Contains(prov.last().System, frozenSentinel) {
			t.Errorf("%s rebuilt the frozen prompt", cmd)
		}
	}
}

// A child launched after a restart inherits the parent's frozen inputs: its
// date, its commit and its AGENTS.md, not what the disk has now.
func TestFrozenReview_NewChildAfterRestartInheritsTheFrozenInputs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sessionDir := t.TempDir()
	cwd := frozenRepo(t)
	prov1 := &recordingProvider{}
	mgr1 := frozenManagerWithWindow(t, ctx, prov1, sessionDir, frozenDay1, 0)
	sess := sessionWithHistory(t, mgr1, cwd)
	stopFrozenTestManager(t, mgr1)

	changeEverything(t, cwd)
	prov2 := &recordingProvider{}
	mgr2 := frozenManagerWithWindow(t, ctx, prov2, sessionDir, frozenDay2, 0)
	resumed, err := mgr2.ResumeSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	tl, ok := resumed.infra.toolReg.Get("subagent")
	if !ok {
		t.Fatal("no subagent tool")
	}
	if _, err := tl.Execute(ctx, map[string]any{"task": "say hi"}, func(core.Result) {}); err != nil {
		t.Fatalf("subagent: %v", err)
	}
	sys := prov2.last().System
	for _, want := range []string{"Wednesday, November 18, 2026", "first commit"} {
		if !strings.Contains(sys, want) {
			t.Errorf("the child lacks the parent's frozen %q", want)
		}
	}
	for _, unwanted := range []string{"ticket id", "second commit", "November 19"} {
		if strings.Contains(sys, unwanted) {
			t.Errorf("the child took %q from today instead of the parent's frozen context", unwanted)
		}
	}
}

// A child resumed after the parent restarted sends the prompt it ran with,
// through the real save and load of its transcript.
func TestFrozenReview_ResumedChildAfterRestartSendsItsOwnPrompt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sessionDir := t.TempDir()
	cwd := frozenRepo(t)
	prov1 := &recordingProvider{}
	mgr1 := frozenManagerWithWindow(t, ctx, prov1, sessionDir, frozenDay1, 0)
	sess := sessionWithHistory(t, mgr1, cwd)
	tl, _ := sess.infra.toolReg.Get("subagent")
	if _, err := tl.Execute(ctx, map[string]any{"task": "say hi"}, func(core.Result) {}); err != nil {
		t.Fatalf("subagent: %v", err)
	}
	childSystem := prov1.last().System
	var jobID string
	pollUntil(t, 5*time.Second, "child transcript saved", func() bool {
		list, _ := sess.persister.subagentStore(sess.ID).List()
		for _, tr := range list {
			jobID = tr.JobID
		}
		return jobID != ""
	})
	// The parent and the child are both on disk; a directly executed child can
	// keep the parent busy for a close, so the restart is a plain shutdown.
	mgr1.Shutdown()

	changeEverything(t, cwd)
	prov2 := &recordingProvider{}
	mgr2 := frozenManagerWithWindow(t, ctx, prov2, sessionDir, frozenDay2, 0)
	resumed, err := mgr2.ResumeSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The parent regenerates: the child must still not follow it.
	compactAndSettle(t, mgr2, resumed, prov2)
	tl2, _ := resumed.infra.toolReg.Get("subagent")
	res, err := tl2.Execute(ctx, map[string]any{"task": "again", "resume": jobID}, func(core.Result) {})
	if err != nil || res.IsError {
		t.Fatalf("resume: %v %+v", err, res)
	}
	if got := prov2.last().System; got != childSystem {
		t.Errorf("the resumed child got a new prompt:\n--- ran with\n%.400s\n--- resumed with\n%.400s", childSystem, got)
	}
}

// A snapshot that is not usable falls back to a fresh build; one whose JSON
// does not match the type must not cost the user the whole session.
func TestFrozenReview_UnusableSnapshotFallsBack(t *testing.T) {
	cases := map[string]string{
		"future-version": `{"version":99,"cwd":"CWD","system":"from the future"}`,
		"other-cwd":      `{"version":1,"cwd":"/elsewhere","system":"elsewhere"}`,
		"empty-system":   `{"version":1,"cwd":"CWD","system":""}`,
		"wrong-type":     `{"version":"1","cwd":"CWD","system":"typed wrong"}`,
		"null":           `null`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sessionDir := t.TempDir()
			cwd := frozenRepo(t)
			prov1 := &recordingProvider{}
			mgr1 := frozenManagerWithWindow(t, ctx, prov1, sessionDir, frozenDay1, 0)
			sess, err := mgr1.CreateSession(CreateOpts{CWD: cwd})
			if err != nil {
				t.Fatal(err)
			}
			sendAndWait(t, mgr1, sess, "hello")
			stopFrozenTestManager(t, mgr1)
			rewriteKey(t, sessionFile(t, sessionDir, sess.ID), "prompt_context", func(json.RawMessage) json.RawMessage {
				return json.RawMessage(strings.ReplaceAll(raw, "CWD", cwd))
			})

			prov2 := &recordingProvider{}
			mgr2 := frozenManagerWithWindow(t, ctx, prov2, sessionDir, frozenDay2, 0)
			resumed, err := mgr2.ResumeSession(sess.ID)
			if err != nil {
				t.Fatalf("the session no longer opens: %v", err)
			}
			sendAndWait(t, mgr2, resumed, "after")
			if sys := prov2.last().System; !strings.Contains(sys, "Thursday, November 19, 2026") {
				t.Errorf("an unusable snapshot was not rebuilt:\n%.300s", sys)
			}
		})
	}
}

// An old binary drops the snapshot and keeps every entry; this binary then
// freezes the session again on its next save.
func TestFrozenReview_DowngradeRoundTrip(t *testing.T) {
	// A tool built from a binary that predates the snapshot, loading and
	// saving the session (args: session dir, id). Manual: not run in CI.
	old := os.Getenv("MOA_OLD_BINARY_TOOL")
	if old == "" {
		t.Skip("MOA_OLD_BINARY_TOOL not set")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sessionDir := t.TempDir()
	cwd := frozenRepo(t)
	prov1 := &recordingProvider{}
	mgr1 := frozenManagerWithWindow(t, ctx, prov1, sessionDir, frozenDay1, 0)
	sess := sessionWithHistory(t, mgr1, cwd)
	stopFrozenTestManager(t, mgr1)
	path := sessionFile(t, sessionDir, sess.ID)
	countEntries := func() (int, bool) {
		var s struct {
			Entries       []json.RawMessage `json:"entries"`
			PromptContext json.RawMessage   `json:"prompt_context"`
		}
		data, _ := os.ReadFile(path)
		if err := json.Unmarshal(data, &s); err != nil {
			t.Fatal(err)
		}
		return len(s.Entries), len(s.PromptContext) > 0
	}
	before, frozen := countEntries()
	if !frozen {
		t.Fatal("the branch did not freeze the session")
	}
	out, err := exec.Command(old, filepath.Dir(path), sess.ID).CombinedOutput()
	if err != nil {
		t.Fatalf("old binary: %v\n%s", err, out)
	}
	after, frozen := countEntries()
	if after != before || frozen {
		t.Fatalf("after the old binary: entries %d→%d, snapshot kept=%v", before, after, frozen)
	}

	prov2 := &recordingProvider{}
	mgr2 := frozenManagerWithWindow(t, ctx, prov2, sessionDir, frozenDay2, 0)
	resumed, err := mgr2.ResumeSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	sendAndWait(t, mgr2, resumed, "back on the new binary")
	if !strings.Contains(prov2.last().System, "Thursday, November 19, 2026") {
		t.Error("the upgraded session did not get a fresh prompt")
	}
	stopFrozenTestManager(t, mgr2)
	final, frozen := countEntries()
	if !frozen || final < before {
		t.Errorf("after the upgrade: entries %d (were %d), frozen again=%v", final, before, frozen)
	}
}
