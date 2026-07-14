package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

// --- unit tests for the queue primitives -----------------------------------

func steer(id, text string) core.SteerItem  { return core.SteerItem{ID: id, Text: text} }
func barrier(id, cmd string) core.SteerItem { return core.SteerItem{ID: id, Text: cmd, Command: cmd} }
func ids(items []core.SteerItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}
func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDrainUntilBarrier_StopsAtFirstBarrier(t *testing.T) {
	var q steerQueue
	q.push(steer("s1", "a"))
	q.push(steer("s2", "b"))
	q.push(barrier("c1", "/compact"))
	q.push(steer("s3", "c"))

	got := q.drainUntilBarrier()
	if !eq(ids(got), []string{"s1", "s2"}) {
		t.Fatalf("drainUntilBarrier = %v, want [s1 s2]", ids(got))
	}
	// The barrier and everything after it remain, in order.
	rest := q.snapshot()
	if !eq(ids(rest), []string{"c1", "s3"}) {
		t.Fatalf("remaining = %v, want [c1 s3]", ids(rest))
	}
}

func TestDrainUntilBarrier_HeadBarrierReturnsNil(t *testing.T) {
	var q steerQueue
	q.push(barrier("c1", "/clear"))
	q.push(steer("s1", "a"))

	if got := q.drainUntilBarrier(); got != nil {
		t.Fatalf("expected nil when head is a barrier, got %v", ids(got))
	}
	// Nothing consumed.
	if !eq(ids(q.snapshot()), []string{"c1", "s1"}) {
		t.Fatalf("queue mutated: %v", ids(q.snapshot()))
	}
}

func TestDrainUntilBarrier_NoBarrierDrainsAll(t *testing.T) {
	var q steerQueue
	q.push(steer("s1", "a"))
	q.push(steer("s2", "b"))
	got := q.drainUntilBarrier()
	if !eq(ids(got), []string{"s1", "s2"}) {
		t.Fatalf("got %v, want [s1 s2]", ids(got))
	}
	if q.snapshot() != nil {
		t.Fatalf("expected empty queue, got %v", ids(q.snapshot()))
	}
}

func TestPopBarrier_OnlyWhenHeadMatches(t *testing.T) {
	var q steerQueue
	q.push(barrier("c1", "/compact"))
	q.push(steer("s1", "a"))

	if q.popBarrier("wrong") {
		t.Fatal("popBarrier removed a non-matching head")
	}
	if !q.popBarrier("c1") {
		t.Fatal("popBarrier failed to remove the matching head")
	}
	if !eq(ids(q.snapshot()), []string{"s1"}) {
		t.Fatalf("after pop, queue = %v, want [s1]", ids(q.snapshot()))
	}
	// Head is now a steer, not a barrier.
	if q.popBarrier("s1") {
		t.Fatal("popBarrier removed a steer head")
	}
}

func TestPeekHead(t *testing.T) {
	var q steerQueue
	if _, ok := q.peekHead(); ok {
		t.Fatal("peekHead on empty queue returned ok")
	}
	q.push(steer("s1", "a"))
	h, ok := q.peekHead()
	if !ok || h.ID != "s1" {
		t.Fatalf("peekHead = %v,%v want s1,true", h.ID, ok)
	}
	// peek does not consume.
	if len(q.snapshot()) != 1 {
		t.Fatal("peekHead consumed the item")
	}
}

// --- behavioral tests ------------------------------------------------------

// A barrier queued after a steer must NOT be injected into the current run: the
// steer before it is delivered, the barrier stops the drain, and the run ends
// with the barrier still at the head of the queue for the bus to execute.
func TestBarrierStopsRunAtIdle(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	blockTool := core.Tool{
		Name:       "block",
		Parameters: json.RawMessage(`{"type":"object"}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			close(started)
			<-release
			return core.TextResult("done"), nil
		},
	}
	provider := NewMockProvider(
		toolCallResponse("tc-1", "block", nil),
		simpleTextResponse("after steer"),
	)
	ag := newTestAgent(provider, blockTool)

	done := make(chan struct{})
	var msgs []core.AgentMessage
	var runErr error
	go func() {
		defer close(done)
		msgs, runErr = ag.Run(context.Background(), "go")
	}()

	<-started
	// Queue a steer, then a barrier command, then another steer.
	ag.Steer(steer("s1", "before barrier"))
	ag.Steer(barrier("c1", "/compact"))
	ag.Steer(steer("s2", "after barrier"))
	close(release)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("agent never finished")
	}
	if runErr != nil {
		t.Fatal(runErr)
	}

	// The steer before the barrier is injected; the barrier and the steer after
	// it are NOT. Messages: user(go), assistant(tc), tool_result, user(s1), assistant(after steer)
	if len(msgs) != 5 {
		t.Fatalf("expected 5 messages, got %d: %v", len(msgs), roles(msgs))
	}
	if firstText(msgs[3]) != "before barrier" {
		t.Fatalf("expected 'before barrier' at index 3, got %q", firstText(msgs[3]))
	}
	// The barrier and the trailing steer remain queued for the bus pump.
	pending := ag.PendingSteers()
	if !eq(ids(pending), []string{"c1", "s2"}) {
		t.Fatalf("pending after run = %v, want [c1 s2]", ids(pending))
	}
	head, ok := ag.PeekQueueHead()
	if !ok || !head.IsBarrier() || head.Command != "/compact" {
		t.Fatalf("queue head = %+v, want barrier /compact", head)
	}
}

// SendItems appends one user message per item, carrying content blocks.
func TestSendItems_PerItemMessagesWithContent(t *testing.T) {
	provider := NewMockProvider(simpleTextResponse("ok"))
	ag := newTestAgent(provider)

	items := []core.SteerItem{
		{ID: "s1", Text: "plain"},
		{ID: "s2", Content: []core.Content{core.TextContent("with"), core.ImageContent("AAAA", "image/png")}},
	}
	msgs, msgIDs, err := ag.SendItems(context.Background(), items, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgIDs) != 2 || msgIDs[0] == "" || msgIDs[1] == "" {
		t.Fatalf("expected 2 non-empty msgIDs, got %v", msgIDs)
	}
	// Messages: user(plain), user(with+image), assistant(ok)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d: %v", len(msgs), roles(msgs))
	}
	if firstText(msgs[0]) != "plain" {
		t.Fatalf("msg0 = %q, want plain", firstText(msgs[0]))
	}
	// The second message keeps its image block.
	var hasImage bool
	for _, c := range msgs[1].Content {
		if c.Type == "image" {
			hasImage = true
		}
	}
	if !hasImage {
		t.Fatalf("expected image content preserved in msg1: %+v", msgs[1].Content)
	}
}

// The agent must own the Content it enqueues: mutating the caller's slice after
// Steer/SendItems must not change the message recorded in state (no aliasing,
// no data race with the provider reading a.state.Messages). Covers both entry
// paths and the mutable Arguments map inside a content block.
func TestQueueOwnsContent_NoAliasing(t *testing.T) {
	t.Run("SendItems slice and Arguments map", func(t *testing.T) {
		provider := NewMockProvider(simpleTextResponse("ok"))
		ag := newTestAgent(provider)

		args := map[string]any{"path": "original", "opts": map[string]any{"deep": "original"}}
		content := []core.Content{
			core.TextContent("original"),
			core.ToolCallContent("tc", "edit", args),
		}
		item := core.SteerItem{ID: "s1", Content: content}
		msgs, _, err := ag.SendItems(context.Background(), []core.SteerItem{item}, nil)
		if err != nil {
			t.Fatal(err)
		}
		// Mutate the caller's backing array AND a nested value in the map.
		content[0] = core.TextContent("tampered")
		args["path"] = "tampered"
		args["opts"].(map[string]any)["deep"] = "tampered"

		if firstText(msgs[0]) != "original" {
			t.Fatalf("state message aliased the caller's slice: got %q", firstText(msgs[0]))
		}
		stored := msgs[0].Content[1].Arguments
		if stored["path"] != "original" {
			t.Fatalf("Arguments map aliased: path=%v, want original", stored["path"])
		}
		if stored["opts"].(map[string]any)["deep"] != "original" {
			t.Fatalf("nested Arguments aliased: deep=%v, want original", stored["opts"].(map[string]any)["deep"])
		}
	})

	t.Run("Steer path", func(t *testing.T) {
		// A steer queued mid-run, then the caller mutates its content. The
		// delivered message must reflect the value at enqueue time.
		started := make(chan struct{})
		release := make(chan struct{})
		blockTool := core.Tool{
			Name:       "block",
			Parameters: json.RawMessage(`{"type":"object"}`),
			Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
				close(started)
				<-release
				return core.TextResult("done"), nil
			},
		}
		provider := NewMockProvider(
			toolCallResponse("tc-1", "block", nil),
			simpleTextResponse("after steer"),
		)
		ag := newTestAgent(provider, blockTool)

		done := make(chan struct{})
		var msgs []core.AgentMessage
		go func() {
			defer close(done)
			msgs, _ = ag.Run(context.Background(), "go")
		}()

		<-started
		content := []core.Content{core.TextContent("steered")}
		ag.Steer(core.SteerItem{ID: "s1", Content: content})
		content[0] = core.TextContent("tampered") // mutate after handing over
		close(release)
		<-done

		// Messages: user(go), assistant(tc), tool_result, user(steered), assistant(after steer)
		if firstText(msgs[3]) != "steered" {
			t.Fatalf("Steer aliased the caller's slice: got %q, want 'steered'", firstText(msgs[3]))
		}
	})
}

// A barrier accidentally passed to SendItems is a command, not a message, and
// must never be injected into the conversation.
func TestSendItems_SkipsBarriers(t *testing.T) {
	provider := NewMockProvider(simpleTextResponse("ok"))
	ag := newTestAgent(provider)

	items := []core.SteerItem{
		{ID: "s1", Text: "real"},
		barrier("c1", "/compact"),
	}
	msgs, msgIDs, err := ag.SendItems(context.Background(), items, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Only the steer becomes a message: user(real), assistant(ok).
	if len(msgIDs) != 1 {
		t.Fatalf("expected 1 msgID (barrier skipped), got %v", msgIDs)
	}
	if len(msgs) != 2 || firstText(msgs[0]) != "real" {
		t.Fatalf("expected [user(real) assistant] got %v", roles(msgs))
	}
}

// Reset (the mechanism behind a queued /clear) must NOT drop the queue: items
// still queued belong, by FIFO, to the fresh conversation after the /clear.
func TestReset_PreservesQueue(t *testing.T) {
	provider := NewMockProvider(simpleTextResponse("ok"))
	ag := newTestAgent(provider)

	ag.Steer(steer("s1", "after clear"))
	ag.Steer(barrier("c1", "/model sonnet"))
	if err := ag.Reset(); err != nil {
		t.Fatal(err)
	}
	pending := ag.PendingSteers()
	if !eq(ids(pending), []string{"s1", "c1"}) {
		t.Fatalf("Reset dropped queued items: got %v, want [s1 c1]", ids(pending))
	}
	if ag.QueueLen() != 2 {
		t.Fatalf("QueueLen = %d, want 2", ag.QueueLen())
	}
}

// CancelSteer still drops the queue (explicit discard path).
func TestCancelSteer_DropsQueue(t *testing.T) {
	provider := NewMockProvider(simpleTextResponse("ok"))
	ag := newTestAgent(provider)
	ag.Steer(steer("s1", "x"))
	ag.CancelSteer()
	if ag.QueueLen() != 0 {
		t.Fatalf("CancelSteer left %d items", ag.QueueLen())
	}
}

// Abort clears the agent's own steer buffer (its low-level anti-stale-leak
// guarantee). The user-facing "keep my queued items" behavior is a frontend
// concern: on stop the client moves its own locally-tracked chips into the
// input. This asserts only the agent contract.
func TestAbortClearsQueue(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	blockTool := core.Tool{
		Name:       "block",
		Parameters: json.RawMessage(`{"type":"object"}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			close(started)
			<-release
			return core.TextResult("done"), nil
		},
	}
	provider := NewMockProvider(toolCallResponse("tc-1", "block", nil))
	ag := newTestAgent(provider, blockTool)

	done := make(chan struct{})
	go func() {
		defer close(done)
		ag.Run(context.Background(), "go") //nolint:errcheck
	}()

	<-started
	ag.Steer(barrier("c1", "/compact"))
	ag.Steer(steer("s2", "after barrier"))
	ag.Abort()
	close(release)
	<-done

	if pending := ag.PendingSteers(); len(pending) != 0 {
		t.Fatalf("expected agent queue cleared after abort, got %v", ids(pending))
	}
}

// The steerQueue inflight ledger keeps native-content bytes counted through the
// drain→settle window: undelivered = queued items + drained-but-unsettled.
func TestSteerQueue_InflightLedger(t *testing.T) {
	img := core.ImageContent("0123456789ABCDEF", "image/png") // DecodedLen 12
	imgSteer := func(id string) core.SteerItem {
		return core.SteerItem{ID: id, Content: []core.Content{img}}
	}
	var q steerQueue

	q.push(imgSteer("s1"))
	q.push(imgSteer("s2"))
	if got := q.undeliveredNativeDocBytes(); got != 24 {
		t.Fatalf("queued: undelivered = %d, want 24", got)
	}

	// Drain removes them from the queue but they are still undelivered (in
	// flight to history) until settled.
	drained := q.drainUntilBarrier()
	if len(drained) != 2 {
		t.Fatalf("drained %d items, want 2", len(drained))
	}
	if got := q.undeliveredNativeDocBytes(); got != 24 {
		t.Fatalf("in flight: undelivered = %d, want 24 (queue empty but inflight)", got)
	}

	// Settling the batch (as delivery to history does) clears the ledger.
	q.settle(drained)
	if got := q.undeliveredNativeDocBytes(); got != 0 {
		t.Fatalf("after settle: undelivered = %d, want 0", got)
	}
}

// pushFront (a drained batch that lost the run slot) settles the inflight bytes
// as it returns the items to the queue, so they are counted once, not twice.
func TestSteerQueue_InflightLedger_PushFrontSettles(t *testing.T) {
	img := core.ImageContent("0123456789ABCDEF", "image/png") // DecodedLen 12
	var q steerQueue
	q.push(core.SteerItem{ID: "s1", Content: []core.Content{img}})

	drained := q.drainUntilBarrier() // inflight += 12
	q.pushFront(drained)             // back in queue, inflight -= 12
	if got := q.undeliveredNativeDocBytes(); got != 12 {
		t.Fatalf("after pushFront: undelivered = %d, want 12 (counted once, in queue)", got)
	}
}

// A direct SendWithContent keeps its native bytes counted from the handler's
// reservation until the message is appended to history, so a concurrent quota
// read never undercounts an accepted-but-not-yet-delivered content send.
func TestSendWithContent_ReservationCountedUntilAppended(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	blockTool := core.Tool{
		Name:       "block",
		Parameters: json.RawMessage(`{"type":"object"}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			close(started)
			<-release
			return core.TextResult("done"), nil
		},
	}
	provider := NewMockProvider(toolCallResponse("tc-1", "block", nil), simpleTextResponse("ok"))
	ag := newTestAgent(provider, blockTool)

	content := []core.Content{core.ImageContent("0123456789ABCDEF", "image/png")} // DecodedLen 12
	want := core.NativeDocBytes(content)

	// The bus handler reserves before starting the run; emulate that here.
	ag.ReserveNativeDocBytes(want)
	if got := ag.NativeDocBytesUndelivered(); got != want {
		t.Fatalf("after reserve: undelivered = %d, want %d", got, want)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		ag.SendWithContent(context.Background(), content) //nolint:errcheck
	}()

	<-started
	// The message has been appended to history (append happens before the tool
	// runs), so SendWithContent's settle has fired: undelivered is back to 0
	// while the bytes now live in history.
	if got := ag.NativeDocBytesUndelivered(); got != 0 {
		t.Fatalf("while running: undelivered = %d, want 0 (settled into history)", got)
	}
	close(release)
	<-done
	if got := ag.NativeDocBytesUndelivered(); got != 0 {
		t.Fatalf("after run: undelivered = %d, want 0", got)
	}
}

// A reservation for a send that never runs is released (no permanent ledger
// leak): ReleaseNativeDocBytes undoes it.
func TestReserveNativeDocBytes_ReleaseNoLeak(t *testing.T) {
	provider := NewMockProvider(simpleTextResponse("ok"))
	ag := newTestAgent(provider)
	ag.ReserveNativeDocBytes(100)
	if got := ag.NativeDocBytesUndelivered(); got != 100 {
		t.Fatalf("after reserve: undelivered = %d, want 100", got)
	}
	ag.ReleaseNativeDocBytes(100)
	if got := ag.NativeDocBytesUndelivered(); got != 0 {
		t.Fatalf("after release: undelivered = %d, want 0", got)
	}
}
