package serve

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

// A repeated provider ToolCall must not deliver twice even once the result
// cache is full: the delivery identity is the ToolCall's own.
func TestVoiceCall_ToolRetryAfterResultCacheFull(t *testing.T) {
	f := vpNewFixture(t, "grok-plan", vpBriefHandlers()...)
	c := f.call()
	p := f.up.peer(t, f.provider)
	vpAwaitBrief(t, f, p)
	for i := 0; i < voiceResultCache; i++ {
		id := fmt.Sprintf("fill-%03d", i)
		p.tool(t, id, "nonexistent-tool", map[string]any{})
		if !vpError(p.result(t, id)) {
			t.Fatal("control: unknown tool accepted")
		}
	}
	p.tool(t, "retried-pass", "pass_to_session", map[string]any{"summary": "CACHE-FULL-ONCE-ONLY"})
	if first := p.result(t, "retried-pass"); vpError(first) {
		t.Fatal("control: first pass failed", first)
	}
	f.waitRecords("relay", 1)
	p.tool(t, "retried-pass", "pass_to_session", map[string]any{"summary": "CACHE-FULL-ONCE-ONLY"})
	// A later tool result is a worker/network barrier: the retry was processed.
	p.tool(t, "after-retry", "read_session", map[string]any{})
	_ = p.result(t, "after-retry")
	ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
	defer cancel()
	if !f.sess.runtime.WaitSettled(ctx) {
		t.Fatal("session did not settle")
	}
	c.hangup()
	if n := len(f.records("relay")); n != 1 {
		t.Fatalf("same ToolCall ID was delivered %d times after the cache filled", n)
	}
	minutes := f.waitRecords("minutes", 1)[0]
	if passed := vpJSON(minutes.Custom["passed"]); strings.Count(passed, "delivery_id") != 1 {
		t.Fatalf("minutes Passed ledger = %v, want the one delivery", minutes.Custom["passed"])
	}
}

// A failed save of a voice record leaves no receipt: the same record can be
// retried and is saved once.
func TestVoiceRecordRetryAfterFailedSave(t *testing.T) {
	m := newTestManagerWithConfig(t, context.Background(), newMockProvider(), t.TempDir(), core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"})
	sess, err := m.CreateSession(CreateOpts{Title: "record write failure"})
	if err != nil {
		t.Fatal(err)
	}
	sess.runtime.Bus.Drain(2 * time.Second)
	if err := sess.runtime.Flush(); err != nil {
		t.Fatal(err)
	}
	_, store, err := session.FindSessionReadOnly(m.sessionBaseDir, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Dir(), sess.ID+".json")
	backup := path + ".before-test"
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	// A directory in place of the file makes the real FileStore rename fail.
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	msg := core.AgentMessage{Message: core.Message{Role: "session_event", Content: []core.Content{core.TextContent("MUST-BE-DURABLE")}}, Custom: map[string]any{"source": "voice_call", "kind": "minutes"}}
	if err := m.recordVoice(sess.ID, "failed/minutes", msg); err == nil {
		t.Fatal("real FileStore failure not reached")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, path); err != nil {
		t.Fatal(err)
	}
	if err := m.recordVoice(sess.ID, "failed/minutes", msg); err != nil {
		t.Fatal(err)
	}
	saved, _, err := session.FindSessionReadOnly(m.sessionBaseDir, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(vpSavedVoice(saved, "minutes")); n != 1 {
		t.Fatalf("retried record after the disk recovered: saved minutes=%d, want 1", n)
	}
}
