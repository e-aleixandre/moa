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

// After hangup only end_call and reads run: a tool with an effect sent in
// the ending window is refused and delivers nothing.
func TestVoiceCall_NoEffectAfterHangup(t *testing.T) {
	limits := vpDefaultLimits()
	limits.minutes = 2 * time.Second
	f := vpNewFixtureWithLimits(t, "grok-plan", limits, vpBriefHandlers()...)
	c := f.call()
	p := f.up.peer(t, f.provider)
	vpAwaitBrief(t, f, p)
	finished := make(chan int, 1)
	go func() {
		status, _, _, _ := f.request("POST", "/api/voice/calls/"+c.c.ID+"/hangup", map[string]any{}, f.owner())
		finished <- status
	}()
	c.await("ended", func(v map[string]any) bool { return v["type"] == "ended" })
	p.await(t, "minutes request", func(v map[string]any) bool {
		return v["type"] == "conversation.item.create" && strings.Contains(vpJSON(v), "Call end_call with what was decided")
	})
	late := map[string]map[string]any{
		"late-pass":     {"name": "pass_to_session", "args": map[string]any{"summary": "WORK-AFTER-USER-HUNG-UP"}},
		"late-answer":   {"name": "answer_question", "args": map[string]any{"ask_id": "x", "answer": "y"}},
		"late-tap":      {"name": "request_tap", "args": map[string]any{"item_id": "x"}},
		"late-question": {"name": "ask_session", "args": map[string]any{"question": "LATE-QUESTION"}},
	}
	for id, tc := range late {
		p.tool(t, id, tc["name"].(string), tc["args"].(map[string]any))
		if r := p.result(t, id); !vpError(r) || !strings.Contains(vpJSON(r), "call has ended") {
			t.Errorf("%s after hangup: %s", tc["name"], vpJSON(r))
		}
	}
	p.tool(t, "late-read", "read_session", map[string]any{})
	if r := p.result(t, "late-read"); vpError(r) {
		t.Errorf("read_session after hangup refused: %s", vpJSON(r))
	}
	p.tool(t, "real-end", "end_call", map[string]any{"decided": []string{}, "passed": []string{}, "open": []string{}})
	if status := <-finished; status != 204 {
		t.Errorf("hangup status=%d", status)
	}
	if n := len(f.records("relay")) + len(f.records("question")); n != 0 {
		t.Errorf("deliveries after hangup: %d", n)
	}
}
