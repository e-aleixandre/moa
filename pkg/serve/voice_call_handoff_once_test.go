package serve

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

type handoffOnceProvider func(context.Context, core.Request) (<-chan core.AssistantEvent, error)

func (f handoffOnceProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	return f(ctx, req)
}

// handoffOnceFixture makes the backend mint a fresh ToolCallID on every
// request, so only the provider's delegation id can identify a handoff.
func handoffOnceFixture(t *testing.T, note string) (*vpFixture, *vpCall) {
	f := vpNewFixture(t, "chatgpt-plan")
	var seq atomic.Int32
	f.voiceText.fallback = handoffOnceProvider(func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		backend, minutes := vpBackendRequest(req)
		if !backend || minutes {
			return f.prov.Stream(ctx, req)
		}
		last := req.Messages[len(req.Messages)-1]
		if last.Role == "tool_result" {
			return simpleResponse(last.Content[0].Text), nil
		}
		id := fmt.Sprintf("backend-tool-%d", seq.Add(1))
		return toolCallHandlerFor(id, "pass_to_session", map[string]any{"summary": note})(ctx, req)
	})
	return f, f.call()
}

func settle(t *testing.T, f *vpFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
	defer cancel()
	if !f.sess.runtime.WaitSettled(ctx) {
		t.Fatal("session did not settle")
	}
}

func countAppends(p *vpPeer, item string) int {
	n := 0
	for _, fr := range p.snapshot() {
		if fr["type"] == "delegation.context.append" && fr["delegation_item_id"] == item {
			n++
		}
	}
	return n
}

func TestVoiceHandoff_SameDelegationItemDeliversOnce(t *testing.T) {
	f, c := handoffOnceFixture(t, "HANDOFF-ONCE-NOTE")
	p := f.up.peer(t, f.provider)
	p.user(t, "u-confirm", "Yes, pass it")
	p.delegate(t, "same-item", "Yes, pass it")
	if first := p.result(t, "same-item"); vpError(first) || first["status"] != "delivered" {
		t.Fatalf("control: %v", first)
	}
	f.waitRecords("relay", 1)
	p.delegate(t, "same-item", "Yes, pass it")
	pollUntil(t, vpDeadline, "second response to same delegation", func() bool { return countAppends(p, "same-item") == 2 })
	settle(t, f)
	c.hangup()
	if n := len(f.records("relay")); n != 1 {
		t.Fatalf("same delegation delivered %d times; want 1", n)
	}
}

func TestVoiceHandoff_DistinctDelegationItemsDeliverEach(t *testing.T) {
	f, c := handoffOnceFixture(t, "HANDOFF-ONCE-NOTE")
	p := f.up.peer(t, f.provider)
	p.delegate(t, "item-a", "Yes, pass it")
	p.result(t, "item-a")
	f.waitRecords("relay", 1)
	p.delegate(t, "item-b", "Yes, pass it")
	p.result(t, "item-b")
	f.waitRecords("relay", 2)
	settle(t, f)
	c.hangup()
	if n := len(f.records("relay")); n != 2 {
		t.Fatalf("distinct delegations delivered %d times; want 2", n)
	}
}
