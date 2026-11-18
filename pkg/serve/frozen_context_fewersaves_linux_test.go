//go:build linux

package serve

import (
	"context"
	"strings"
	"testing"
)

// With unchanged-save skipping, a prompt change (here /reload with an edited
// AGENTS.md: the tree does not move) still reaches disk, and reopening and
// closing a frozen session writes nothing.
func TestFrozenReview_FewerSavesWritesAPromptChangeAndSkipsTheRest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sessionDir := t.TempDir()
	cwd := frozenRepo(t)
	prov := &recordingProvider{}
	mgr := frozenManagerWithWindow(t, ctx, prov, sessionDir, frozenDay1, 0)
	sess, err := mgr.CreateSession(CreateOpts{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	sendAndWait(t, mgr, sess, "hello")
	settleSaves(t, sess)
	dir := sess.persister.store.Dir()
	id := sess.ID
	if pc := loadSaved(t, dir, id).PromptContext; pc == nil || strings.Contains(pc.System, "ticket id") {
		t.Fatal("the first turn did not freeze the day-1 prompt")
	}
	w := watchTranscriptWrites(t, dir, id)

	if err := mgr.CloseSession(id); err != nil {
		t.Fatal(err)
	}
	resumed, err := mgr.ResumeSession(id)
	if err != nil {
		t.Fatal(err)
	}
	settleSaves(t, resumed)
	if n := w.take(); n != 0 {
		t.Errorf("closing and reopening a frozen session wrote %d times, want 0", n)
	}

	writeAgentsMD(t, cwd, oldRule+"\n"+newRule+"\n")
	if res, err := mgr.ExecCommand(id, "/reload", ""); err != nil || !res.OK {
		t.Fatalf("reload = %+v, %v", res, err)
	}
	if n := w.take(); n != 1 {
		t.Errorf("a reload that changed the prompt wrote %d times, want 1", n)
	}
	if pc := loadSaved(t, dir, id).PromptContext; pc == nil || !strings.Contains(pc.System, "ticket id") {
		t.Error("the reloaded prompt did not reach disk")
	}
}
