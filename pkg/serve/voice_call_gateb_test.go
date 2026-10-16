package serve

import (
	"context"
	"fmt"
	"strings"
	"testing"
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
