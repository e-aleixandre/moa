package bus

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/checkpoint"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/permission"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/sessioncheckpoint"
	"github.com/e-aleixandre/moa/pkg/tasks"
	"github.com/e-aleixandre/moa/pkg/tool"
)

func TestClearRunStartedAtCannotEraseNewGenerationAnchor(t *testing.T) {
	sctx := &SessionContext{}
	old := &runStartAnchor{gen: 1, at: time.Unix(1, 0)}
	newer := &runStartAnchor{gen: 2, at: time.Unix(2, 0)}
	sctx.runStartedAnchor.Store(old)

	// This is the formerly unsafe interleaving: the old run has loaded its
	// anchor, a new run has installed its anchor, then the old clear executes.
	// A generation check followed by Store(nil) loses newer; CAS must fail.
	loadedByOldRun := sctx.runStartedAnchor.Load()
	sctx.runStartedAnchor.Store(newer)
	if sctx.runStartedAnchor.CompareAndSwap(loadedByOldRun, nil) {
		t.Fatal("stale clear erased a newer run anchor")
	}

	sctx.clearRunStartedAt(1)
	if got := sctx.RunStartedAt(); !got.Equal(newer.at) {
		t.Fatalf("run start after stale clear = %v, want %v", got, newer.at)
	}
}

// ---------------------------------------------------------------------------
// fakeAgent — implements AgentController for handler tests
// Thread-safe: all fields protected by mu for SendPrompt goroutine tests.
// ---------------------------------------------------------------------------

type fakeAgent struct {
	mu sync.Mutex

	aborted          bool
	steered          string
	model            core.Model
	thinkingLevel    string
	messages         []core.AgentMessage
	compactionEpoch  int
	resetCalled      bool
	compactCalled    bool
	compactErr       error
	compactPayload   *core.CompactionPayload
	checkpointPassed string
	compactFocus     string
	compactHook      func()
	panicMessages    bool

	setModelProvider core.Provider
	setModelModel    core.Model
	setModelErr      error

	setThinkingErr error

	systemPrompt string
	compactAt    int
	maxBudget    float64

	// Send behavior
	sendCalled  bool
	sendPrompt  string
	sendResult  []core.AgentMessage
	sendErr     error
	sendDelay   time.Duration // simulates slow agent
	sendHook    func()
	sendContent []core.Content
	sendMsgID   string
	// announce, when set, is invoked by the announcing send entry points with
	// the message they appended, standing in for the real agent's
	// core.AgentEventUserMessage emission (which reaches the bus via Bridge).
	announce func(msgID string, text string, content []core.Content)
	// appendGate, when set, blocks an announcing send until the channel is
	// closed, holding open the window between accepting the send and its
	// message becoming visible in history.
	appendGate chan struct{}
	steerQueue []core.SteerItem
	steerFull  bool             // when true, Steer rejects (queue full)
	sentItems  []core.SteerItem // items delivered via SendItems (pump tests)
	inflight   int64            // reserved native bytes (Reserve/Release)

	// appendBusy > 0 makes AppendMessage fail (simulating a live run) and
	// decrements once per call, so a deferred append can succeed on retry.
	appendBusy int
}

func (f *fakeAgent) Abort() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.aborted = true
}

func (f *fakeAgent) Steer(it core.SteerItem) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.steerFull {
		return false
	}
	f.steered = it.Text
	f.steerQueue = append(f.steerQueue, it)
	return true
}

func (f *fakeAgent) CancelSteer() []core.SteerItem {
	f.mu.Lock()
	defer f.mu.Unlock()
	items := f.steerQueue
	f.steerQueue = nil
	return items
}

func (f *fakeAgent) DrainSteers() []core.SteerItem {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := f.steerQueue
	f.steerQueue = nil
	return q
}

func (f *fakeAgent) PushSteersFront(items []core.SteerItem) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steerQueue = append(append([]core.SteerItem{}, items...), f.steerQueue...)
}

// DrainUntilBarrier removes and returns the queued items up to (but not
// including) the first barrier, mirroring the real queue semantics so pump
// tests exercise the same control flow.
func (f *fakeAgent) DrainUntilBarrier() []core.SteerItem {
	f.mu.Lock()
	defer f.mu.Unlock()
	cut := 0
	for cut < len(f.steerQueue) && !f.steerQueue[cut].IsBarrier() {
		cut++
	}
	if cut == 0 {
		return nil
	}
	items := f.steerQueue[:cut]
	f.steerQueue = append([]core.SteerItem{}, f.steerQueue[cut:]...)
	return items
}

func (f *fakeAgent) PeekQueueHead() (core.SteerItem, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.steerQueue) == 0 {
		return core.SteerItem{}, false
	}
	return f.steerQueue[0], true
}

func (f *fakeAgent) PopQueueBarrier(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.steerQueue) == 0 || !f.steerQueue[0].IsBarrier() || f.steerQueue[0].ID != id {
		return false
	}
	f.steerQueue = append([]core.SteerItem{}, f.steerQueue[1:]...)
	return true
}

// SendItems records the delivered items and appends one user message per item
// (as the real agent does) before invoking announce, letting pump tests assert
// both which steers started a fresh run and that the announcement can never be
// observed ahead of the append.
func (f *fakeAgent) SendItems(ctx context.Context, items []core.SteerItem, msgIDs []string, announce func()) ([]core.AgentMessage, []string, error) {
	f.mu.Lock()
	ids := make([]string, len(items))
	for i, it := range items {
		if i < len(msgIDs) && msgIDs[i] != "" {
			ids[i] = msgIDs[i]
		} else {
			ids[i] = "msg-" + it.ID
		}
		f.sentItems = append(f.sentItems, it)
		m := core.WrapMessage(core.NewUserMessage(it.Text))
		m.MsgID = ids[i]
		f.messages = append(f.messages, m)
	}
	f.mu.Unlock()
	if announce != nil {
		announce()
	}
	return nil, ids, nil
}

func (f *fakeAgent) PendingSteers() []core.SteerItem {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]core.SteerItem, 0, len(f.steerQueue))
	for _, it := range f.steerQueue {
		if !it.Internal {
			out = append(out, it)
		}
	}
	return out
}

func (f *fakeAgent) QueueLen() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.steerQueue)
}

func (f *fakeAgent) NativeDocBytesUndelivered() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := f.inflight
	for _, it := range f.steerQueue {
		total += core.NativeDocBytes(it.Content)
	}
	return total
}

func (f *fakeAgent) ReserveNativeDocBytes(n int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inflight += n
}

func (f *fakeAgent) ReleaseNativeDocBytes(n int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inflight -= n
	if f.inflight < 0 {
		f.inflight = 0
	}
}

func (f *fakeAgent) Model() core.Model {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.model
}

func (f *fakeAgent) ThinkingLevel() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.thinkingLevel
}

func (f *fakeAgent) Messages() []core.AgentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.panicMessages {
		panic("messages panic")
	}
	// Return a copy to prevent races.
	cp := make([]core.AgentMessage, len(f.messages))
	copy(cp, f.messages)
	return cp
}

func (f *fakeAgent) CompactionEpoch() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.compactionEpoch
}

func (f *fakeAgent) IsRunning() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return false
}

func (f *fakeAgent) SetModel(provider core.Provider, model core.Model) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setModelProvider = provider
	f.setModelModel = model
	if f.setModelErr != nil {
		return f.setModelErr
	}
	f.model = model
	return nil
}

func (f *fakeAgent) SetThinkingLevel(level string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setThinkingErr != nil {
		return f.setThinkingErr
	}
	f.thinkingLevel = level
	return nil
}

func (f *fakeAgent) Reset() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resetCalled = true
	f.messages = nil
	return nil
}

func (f *fakeAgent) Compact(ctx context.Context, focus string) (*core.CompactionPayload, error) {
	f.mu.Lock()
	f.compactCalled = true
	f.compactFocus = focus
	hook := f.compactHook
	f.mu.Unlock()
	// Let a test observe state / queue a steer while compaction is "in flight",
	// mirroring a user message arriving mid-compaction.
	if hook != nil {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.compactPayload, f.compactErr
}

func (f *fakeAgent) CompactWithCheckpoint(ctx context.Context, checkpoint, focus string) (*core.CompactionPayload, error) {
	f.mu.Lock()
	f.checkpointPassed = checkpoint
	f.mu.Unlock()
	return f.Compact(ctx, focus)
}

func (f *fakeAgent) SnapshotConversation() ([]core.AgentMessage, int) {
	return f.Messages(), f.CompactionEpoch()
}

func (f *fakeAgent) RestoreConversation(msgs []core.AgentMessage, epoch int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append([]core.AgentMessage(nil), msgs...)
	f.compactionEpoch = epoch
	return nil
}

func (f *fakeAgent) Send(ctx context.Context, prompt string) ([]core.AgentMessage, error) {
	if f.sendHook != nil {
		f.sendHook()
	}
	if f.sendDelay > 0 {
		select {
		case <-time.After(f.sendDelay):
		case <-ctx.Done():
			return f.Messages(), ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendCalled = true
	f.sendPrompt = prompt
	// Append sendResult to messages to simulate agent behavior.
	if f.sendResult != nil {
		f.messages = append(f.messages, f.sendResult...)
	}
	return f.messages, f.sendErr
}

func (f *fakeAgent) SendWithCustom(ctx context.Context, prompt string, custom map[string]any) ([]core.AgentMessage, error) {
	return f.Send(ctx, prompt)
}

func (f *fakeAgent) SendWithCustomAnnounced(ctx context.Context, prompt string, custom map[string]any) ([]core.AgentMessage, error) {
	return f.Send(ctx, prompt)
}

func (f *fakeAgent) SendPrepareCompact(ctx context.Context, prompt string, _ *sessioncheckpoint.Slot, _ string) ([]core.AgentMessage, error) {
	return f.Send(ctx, prompt)
}

func (f *fakeAgent) SendWithMsgID(ctx context.Context, prompt, msgID string) ([]core.AgentMessage, error) {
	f.mu.Lock()
	f.sendMsgID = msgID
	announce := f.announce
	gate := f.appendGate
	f.mu.Unlock()
	if gate != nil {
		// Hold the append open so a test can keep the window between accepting
		// a send and its message reaching history wide open on purpose.
		<-gate
	}
	if msgID != "" {
		m := core.WrapMessage(core.NewUserMessage(prompt))
		m.MsgID = msgID
		f.mu.Lock()
		f.messages = append(f.messages, m)
		f.mu.Unlock()
	}
	if announce != nil {
		announce(msgID, prompt, nil)
	}
	return f.Send(ctx, prompt)
}

func (f *fakeAgent) AppendMessage(msg core.AgentMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.appendBusy > 0 {
		f.appendBusy--
		return fmt.Errorf("cannot append message while agent is running")
	}
	f.messages = append(f.messages, msg)
	return nil
}

func (f *fakeAgent) SetPermissionCheck(fn func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision) error {
	return nil
}

func (f *fakeAgent) SetSystemPrompt(prompt string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.systemPrompt = prompt
	return nil
}

func (f *fakeAgent) SystemPrompt() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.systemPrompt
}

func (f *fakeAgent) SetCompactAt(tokens int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.compactAt = tokens
	return nil
}

func (f *fakeAgent) CompactAt() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.compactAt
}

func (f *fakeAgent) CompactAtFloor() int {
	return core.DefaultCompactionSettings.MinCompactAt()
}

func (f *fakeAgent) SetMaxBudget(v float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.maxBudget = v
	return nil
}

func (f *fakeAgent) MaxBudget() float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxBudget
}

func (f *fakeAgent) LoadState(msgs []core.AgentMessage, compactionEpoch int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = msgs
	f.compactionEpoch = compactionEpoch
	return nil
}

func (f *fakeAgent) SendWithContent(ctx context.Context, content []core.Content) ([]core.AgentMessage, error) {
	if f.sendDelay > 0 {
		select {
		case <-time.After(f.sendDelay):
		case <-ctx.Done():
			return f.Messages(), ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendCalled = true
	f.sendContent = content
	if f.sendResult != nil {
		f.messages = append(f.messages, f.sendResult...)
	}
	return f.messages, f.sendErr
}

func (f *fakeAgent) SendWithContentMsgID(ctx context.Context, content []core.Content, msgID string) ([]core.AgentMessage, error) {
	f.mu.Lock()
	f.sendMsgID = msgID
	f.mu.Unlock()
	return f.SendWithContent(ctx, content)
}

func (f *fakeAgent) SendWithContentAnnounced(ctx context.Context, content []core.Content, msgID string) ([]core.AgentMessage, error) {
	f.mu.Lock()
	announce := f.announce
	f.mu.Unlock()
	if announce != nil {
		announce(msgID, "", content)
	}
	return f.SendWithContentMsgID(ctx, content, msgID)
}

// announceToBus wires the fake's announce hook to publish the same bus event
// the real agent produces through Bridge, so handler tests observe the live
// announcement without a real agent.
func (f *fakeAgent) announceToBus(b EventBus, sessionID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.announce = func(msgID, text string, content []core.Content) {
		b.Publish(UserMessageAppended{SessionID: sessionID, MsgID: msgID, Text: text, Content: content})
	}
}

// Thread-safe assertion helpers.

func (f *fakeAgent) wasSendCalled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sendCalled
}

func (f *fakeAgent) wasAborted() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.aborted
}

func (f *fakeAgent) wasResetCalled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resetCalled
}

func (f *fakeAgent) wasCompactCalled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.compactCalled
}

func (f *fakeAgent) focusPassed() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.compactFocus
}

func (f *fakeAgent) getSteered() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.steered
}

func (f *fakeAgent) getSendPrompt() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sendPrompt
}

func (f *fakeAgent) getSendMsgID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sendMsgID
}

// ---------------------------------------------------------------------------
// fakeSubscriber — implements AgentSubscriber for bridge integration tests
// ---------------------------------------------------------------------------

type fakeSubscriber struct {
	handler func(core.AgentEvent)
}

func (fs *fakeSubscriber) Subscribe(fn func(core.AgentEvent)) func() {
	fs.handler = fn
	return func() { fs.handler = nil }
}

func (fs *fakeSubscriber) emit(e core.AgentEvent) {
	if fs.handler != nil {
		fs.handler(e)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newTestSessionContext(b EventBus, agent AgentController) *SessionContext {
	return &SessionContext{
		SessionID:  "test-session",
		SessionCtx: context.Background(),
		Bus:        b,
		Agent:      agent,
	}
}

func newTestSessionContextWithState(b EventBus, agent AgentController) *SessionContext {
	sm := NewStateMachine(b, "test-session")
	return &SessionContext{
		SessionID:  "test-session",
		SessionCtx: context.Background(),
		Bus:        b,
		Agent:      agent,
		State:      sm,
	}
}

func drainChan[T any](ch <-chan T, b EventBus, t *testing.T) T {
	t.Helper()
	b.Drain(time.Second)
	select {
	case v := <-ch:
		return v
	case <-time.After(time.Second):
		var zero T
		t.Fatalf("timeout waiting for event of type %T", zero)
		return zero
	}
}

func expectNone[T any](ch <-chan T, b EventBus, t *testing.T) {
	t.Helper()
	b.Drain(time.Second)
	select {
	case v := <-ch:
		t.Fatalf("expected no event, got %+v", v)
	default:
		// good
	}
}

// waitForEvent waits for any typed event with drain + timeout. A compaction is
// asynchronous now (the command only accepts it), so tests observe its outcome
// through events instead of the Execute return.
func waitForEvent[T any](t *testing.T, b EventBus, ch <-chan T, name string) T {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		b.Drain(100 * time.Millisecond)
		select {
		case v := <-ch:
			return v
		case <-deadline:
			t.Fatalf("timeout waiting for %s", name)
			var zero T
			return zero
		}
	}
}

// sawErrorState reports whether an error transition carrying msg was published,
// draining whatever StateChanged events are already buffered.
func sawErrorState(ch <-chan StateChanged, msg string) bool {
	for {
		select {
		case e := <-ch:
			if e.State == string(StateError) && e.Error == msg {
				return true
			}
		default:
			return false
		}
	}
}

// waitForRunEnded waits for a RunEnded event with drain + timeout.
func waitForRunEnded(t *testing.T, ch <-chan RunEnded, b EventBus) RunEnded {
	t.Helper()
	// Runs are async — poll with drain until the event arrives.
	deadline := time.After(5 * time.Second)
	for {
		b.Drain(100 * time.Millisecond)
		select {
		case v := <-ch:
			return v
		case <-deadline:
			t.Fatal("timeout waiting for RunEnded")
			var zero RunEnded
			return zero
		}
	}
}

// ===========================================================================
// Bridge mapping tests (table-driven)
// ===========================================================================

func TestBridgeEvent_AgentStarted(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan AgentStarted, 1)
	b.Subscribe(func(e AgentStarted) { got <- e })

	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventStart})
	e := drainChan(got, b, t)
	if e.SessionID != "test-session" {
		t.Fatalf("SessionID = %q, want %q", e.SessionID, "test-session")
	}
}

func TestBridgeEvent_AgentEnded(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan AgentEnded, 1)
	b.Subscribe(func(e AgentEnded) { got <- e })

	msgs := []core.AgentMessage{{Message: core.Message{Role: "assistant"}}}
	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventEnd, Messages: msgs})
	e := drainChan(got, b, t)
	if len(e.Messages) != 1 || e.Messages[0].Role != "assistant" {
		t.Fatalf("unexpected Messages: %+v", e.Messages)
	}
}

func TestBridgeEvent_AgentError(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan AgentError, 1)
	b.Subscribe(func(e AgentError) { got <- e })

	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventError, Error: errors.New("boom")})
	e := drainChan(got, b, t)
	if e.Err == nil || e.Err.Error() != "boom" {
		t.Fatalf("Err = %v, want 'boom'", e.Err)
	}
}

func TestBridgeEvent_TurnStarted(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan TurnStarted, 1)
	b.Subscribe(func(e TurnStarted) { got <- e })

	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventTurnStart})
	e := drainChan(got, b, t)
	if e.SessionID != "test-session" {
		t.Fatalf("SessionID = %q", e.SessionID)
	}
}

func TestBridgeEvent_TurnEnded(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan TurnEnded, 1)
	b.Subscribe(func(e TurnEnded) { got <- e })

	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventTurnEnd})
	e := drainChan(got, b, t)
	if e.SessionID != "test-session" {
		t.Fatalf("SessionID = %q", e.SessionID)
	}
}

func TestBridgeEvent_MessageStarted(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan MessageStarted, 1)
	b.Subscribe(func(e MessageStarted) { got <- e })

	msg := core.AgentMessage{Message: core.Message{Role: "assistant"}}
	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventMessageStart, Message: msg})
	e := drainChan(got, b, t)
	if e.Message.Role != "assistant" {
		t.Fatalf("Message.Role = %q", e.Message.Role)
	}
}

func TestBridgeEvent_TextDelta(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan TextDelta, 1)
	b.Subscribe(func(e TextDelta) { got <- e })

	bridgeEvent(sctx, core.AgentEvent{
		Type: core.AgentEventMessageUpdate,
		AssistantEvent: &core.AssistantEvent{
			Type:  core.ProviderEventTextDelta,
			Delta: "hello",
		},
	})
	e := drainChan(got, b, t)
	if e.Delta != "hello" {
		t.Fatalf("Delta = %q, want %q", e.Delta, "hello")
	}
}

func TestBridgeEvent_ThinkingDelta(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan ThinkingDelta, 1)
	b.Subscribe(func(e ThinkingDelta) { got <- e })

	bridgeEvent(sctx, core.AgentEvent{
		Type: core.AgentEventMessageUpdate,
		AssistantEvent: &core.AssistantEvent{
			Type:  core.ProviderEventThinkingDelta,
			Delta: "thinking...",
		},
	})
	e := drainChan(got, b, t)
	if e.Delta != "thinking..." {
		t.Fatalf("Delta = %q", e.Delta)
	}
}

func TestBridgeEvent_MessageUpdate_NilAssistantEvent(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan TextDelta, 1)
	b.Subscribe(func(e TextDelta) { got <- e })

	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventMessageUpdate})
	expectNone(got, b, t)
}

// Regression for "reconnect renders the reply from mid-stream": the
// authoritative streaming aggregate must accumulate the partial text/thinking
// as deltas are published and clear once the message ends, so a snapshot taken
// mid-generation (GetStreamingAggregate) restores the whole streamed-so-far
// reply rather than only the deltas that land after the cut.
func TestBridgeEvent_StreamingAggregate(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)

	aggregate := func() StreamingAggregate {
		text, thinking, msgID := sctx.StreamingAggregate()
		return StreamingAggregate{Text: text, Thinking: thinking, MsgID: msgID}
	}

	if a := aggregate(); a.Text != "" || a.Thinking != "" || a.MsgID != "" {
		t.Fatalf("aggregate not empty before streaming: %+v", a)
	}

	bridgeEvent(sctx, core.AgentEvent{
		Type:    core.AgentEventMessageStart,
		Message: core.AgentMessage{Message: core.Message{Role: "assistant", MsgID: "m1"}},
	})
	bridgeEvent(sctx, core.AgentEvent{
		Type:           core.AgentEventMessageUpdate,
		AssistantEvent: &core.AssistantEvent{Type: core.ProviderEventThinkingDelta, Delta: "hmm "},
	})
	bridgeEvent(sctx, core.AgentEvent{
		Type:           core.AgentEventMessageUpdate,
		AssistantEvent: &core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: "hel"},
	})
	bridgeEvent(sctx, core.AgentEvent{
		Type:           core.AgentEventMessageUpdate,
		AssistantEvent: &core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: "lo"},
	})

	if a := aggregate(); a.Text != "hello" || a.Thinking != "hmm " || a.MsgID != "m1" {
		t.Fatalf("mid-stream aggregate = %+v, want text=hello thinking='hmm ' msgID=m1", a)
	}

	// MessageEnd clears the aggregate: the reply is now a real message in state.
	bridgeEvent(sctx, core.AgentEvent{
		Type:    core.AgentEventMessageEnd,
		Message: core.AgentMessage{Message: core.Message{Role: "assistant", MsgID: "m1"}},
	})
	if a := aggregate(); a.Text != "" || a.Thinking != "" || a.MsgID != "" {
		t.Fatalf("aggregate not cleared after MessageEnd: %+v", a)
	}

	// A new MessageStart resets the accumulated deltas for the next message.
	bridgeEvent(sctx, core.AgentEvent{
		Type:    core.AgentEventMessageStart,
		Message: core.AgentMessage{Message: core.Message{Role: "assistant", MsgID: "m2"}},
	})
	bridgeEvent(sctx, core.AgentEvent{
		Type:           core.AgentEventMessageUpdate,
		AssistantEvent: &core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: "next"},
	})
	if a := aggregate(); a.Text != "next" || a.MsgID != "m2" {
		t.Fatalf("second-message aggregate = %+v, want text=next msgID=m2", a)
	}

	// A run that dies without a MessageEnd must not leave a stale aggregate.
	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventEnd})
	if a := aggregate(); a.Text != "" || a.MsgID != "" {
		t.Fatalf("aggregate not cleared on run end: %+v", a)
	}
}

// Regression for the atomicity blocker: because the
// aggregate is accumulative (concatenated deltas), the snapshot cut and the
// aggregate text must be captured together under streamMu, or a delta folded
// into the snapshot text could ALSO carry a seq > cut and be replayed live,
// duplicating it. bridgeEvent publishes exactly one event per call in lock
// order, so seqs are deterministic: with L0 = LastSeq before MessageStart,
// MessageStart takes L0+1 and text delta i takes L0+2+i. A snapshot whose text
// holds k deltas is only consistent if cut == L0+1+k. This drives deltas from
// one goroutine while another snapshots concurrently and asserts exactly that —
// a non-atomic capture (text then cut read separately) makes cut outrun the
// text and fails.
func TestBridgeEvent_StreamingSnapshotCutIsAtomic(t *testing.T) {
	const nDeltas = 400
	deltaFor := func(i int) string { return fmt.Sprintf("%d.", i) }
	cumulative := make([]string, nDeltas+1)
	for i := 0; i < nDeltas; i++ {
		cumulative[i+1] = cumulative[i] + deltaFor(i)
	}

	for iter := 0; iter < 60; iter++ {
		b := NewLocalBus()
		sctx := newTestSessionContext(b, nil)

		l0 := b.LastSeq()
		bridgeEvent(sctx, core.AgentEvent{
			Type:    core.AgentEventMessageStart,
			Message: core.AgentMessage{Message: core.Message{Role: "assistant", MsgID: "m1"}},
		})

		done := make(chan struct{})
		go func() {
			defer close(done)
			for i := 0; i < nDeltas; i++ {
				bridgeEvent(sctx, core.AgentEvent{
					Type:           core.AgentEventMessageUpdate,
					AssistantEvent: &core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: deltaFor(i)},
				})
			}
		}()

		// Sample many (text, cut) pairs across the whole stream and assert each
		// one is internally consistent: cut implies exactly k deltas, so the
		// captured text must equal the k-delta prefix. A non-atomic capture lets
		// cut outrun the text for some sample.
		var samples int
		for {
			agg, _, cut := sctx.SnapshotInFlightWithCut()
			text := agg.Text
			k := int(cut) - int(l0) - 1
			if k >= 0 && k <= nDeltas {
				if text != cumulative[k] {
					t.Fatalf("iter %d: cut=%d implies %d deltas (len %d) but snapshot text len %d",
						iter, cut, k, len(cumulative[k]), len(text))
				}
			}
			if k >= 0 {
				samples++
			}
			select {
			case <-done:
				if text == cumulative[nDeltas] || samples > nDeltas {
					goto next
				}
			default:
			}
		}
	next:
		b.Close()
	}
}

func TestBridgeEvent_MessageEnded(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan MessageEnded, 1)
	b.Subscribe(func(e MessageEnded) { got <- e })

	msg := core.AgentMessage{Message: core.Message{
		Role: "assistant",
		Content: []core.Content{
			{Type: "text", Text: "part1"},
			{Type: "text", Text: "part2"},
			{Type: "image", Text: "ignored"},
		},
	}}
	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventMessageEnd, Message: msg})
	e := drainChan(got, b, t)
	if e.FullText != "part1part2" {
		t.Fatalf("FullText = %q, want %q", e.FullText, "part1part2")
	}
}

func TestBridgeEvent_ToolExecStarted(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan ToolExecStarted, 1)
	b.Subscribe(func(e ToolExecStarted) { got <- e })

	bridgeEvent(sctx, core.AgentEvent{
		Type:       core.AgentEventToolExecStart,
		ToolCallID: "tc-1",
		ToolName:   "read",
		Args:       map[string]any{"path": "foo.go"},
	})
	e := drainChan(got, b, t)
	if e.ToolCallID != "tc-1" || e.ToolName != "read" {
		t.Fatalf("unexpected: %+v", e)
	}
	if e.Args["path"] != "foo.go" {
		t.Fatalf("Args = %+v", e.Args)
	}
}

func TestBridgeEvent_ToolExecUpdate_WithDelta(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan ToolExecUpdate, 1)
	b.Subscribe(func(e ToolExecUpdate) { got <- e })

	bridgeEvent(sctx, core.AgentEvent{
		Type:       core.AgentEventToolExecUpdate,
		ToolCallID: "tc-1",
		Result: &core.Result{
			Content: []core.Content{{Type: "text", Text: "output"}},
		},
	})
	e := drainChan(got, b, t)
	if e.Delta != "output" {
		t.Fatalf("Delta = %q", e.Delta)
	}
}

func TestBridgeEvent_ToolExecUpdate_EmptyResult(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan ToolExecUpdate, 1)
	b.Subscribe(func(e ToolExecUpdate) { got <- e })

	bridgeEvent(sctx, core.AgentEvent{
		Type:       core.AgentEventToolExecUpdate,
		ToolCallID: "tc-1",
		Result:     nil,
	})
	expectNone(got, b, t)
}

func TestBridgeEvent_ToolExecEnded(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan ToolExecEnded, 1)
	b.Subscribe(func(e ToolExecEnded) { got <- e })

	bridgeEvent(sctx, core.AgentEvent{
		Type:       core.AgentEventToolExecEnd,
		ToolCallID: "tc-1",
		ToolName:   "write",
		IsError:    true,
		Rejected:   false,
		Result: &core.Result{
			Content: []core.Content{{Type: "text", Text: "error: denied"}},
		},
	})
	e := drainChan(got, b, t)
	if e.Result != "error: denied" || !e.IsError || e.Rejected {
		t.Fatalf("unexpected: %+v", e)
	}
}

func TestBridgeEvent_ToolExecEnd_EmitsTasksUpdated(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	store := tasks.NewStore()
	store.Create("task one", "", nil)
	sctx := newTestSessionContext(b, nil)
	sctx.TaskStore = store

	gotTool := make(chan ToolExecEnded, 1)
	gotTasks := make(chan TasksUpdated, 1)
	b.Subscribe(func(e ToolExecEnded) { gotTool <- e })
	b.Subscribe(func(e TasksUpdated) { gotTasks <- e })

	bridgeEvent(sctx, core.AgentEvent{
		Type:     core.AgentEventToolExecEnd,
		ToolName: "tasks",
	})

	drainChan(gotTool, b, t)
	tu := drainChan(gotTasks, b, t)
	if len(tu.Tasks) != 1 || tu.Tasks[0].Title != "task one" {
		t.Fatalf("unexpected tasks: %+v", tu.Tasks)
	}
}

func TestBridgeEvent_ToolExecEnd_NoTaskUpdate_WrongTool(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	store := tasks.NewStore()
	sctx := newTestSessionContext(b, nil)
	sctx.TaskStore = store

	gotTasks := make(chan TasksUpdated, 1)
	b.Subscribe(func(e TasksUpdated) { gotTasks <- e })

	bridgeEvent(sctx, core.AgentEvent{
		Type:     core.AgentEventToolExecEnd,
		ToolName: "read",
	})
	expectNone(gotTasks, b, t)
}

func TestBridgeEvent_Steered(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan Steered, 1)
	b.Subscribe(func(e Steered) { got <- e })

	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventSteer, Text: "focus on X"})
	e := drainChan(got, b, t)
	if e.Text != "focus on X" {
		t.Fatalf("Text = %q", e.Text)
	}
}

func TestProjectLiveCustomExposesOnlyFrontendFields(t *testing.T) {
	got := projectLiveCustom(map[string]any{
		"source":         "secret_batch",
		"secret_aliases": []string{"db"},
		"internal_only":  "must-not-leave-the-process",
	})
	if got["source"] != "secret_batch" {
		t.Fatalf("source = %#v", got)
	}
	if aliases, ok := got["secret_aliases"].([]string); !ok || len(aliases) != 1 || aliases[0] != "db" {
		t.Fatalf("aliases = %#v", got["secret_aliases"])
	}
	if _, ok := got["internal_only"]; ok {
		t.Fatalf("internal Custom field was exposed: %#v", got)
	}
	ordinary := projectLiveCustom(map[string]any{"source": "schedule", "schedule_id": "private"})
	if len(ordinary) != 1 || ordinary["source"] != "schedule" {
		t.Fatalf("ordinary source projection = %#v", ordinary)
	}
}

func TestBridgeEvent_Steered_Suppressed(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	sctx.SteerFilter = func(text string) bool { return text != "subagent" }
	got := make(chan Steered, 1)
	b.Subscribe(func(e Steered) { got <- e })

	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventSteer, Text: "subagent"})
	expectNone(got, b, t)

	// Non-suppressed steer should still work.
	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventSteer, Text: "user steer"})
	e := drainChan(got, b, t)
	if e.Text != "user steer" {
		t.Fatalf("Text = %q", e.Text)
	}
}

func TestBridgeEvent_CompactionStarted(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan CompactionStarted, 1)
	b.Subscribe(func(e CompactionStarted) { got <- e })

	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventCompactionStart})
	e := drainChan(got, b, t)
	if e.SessionID != "test-session" {
		t.Fatalf("SessionID = %q", e.SessionID)
	}
}

func TestBridgeEvent_CompactionEnded(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan CompactionEnded, 1)
	b.Subscribe(func(e CompactionEnded) { got <- e })

	payload := &core.CompactionPayload{Summary: "compacted"}
	bridgeEvent(sctx, core.AgentEvent{
		Type:       core.AgentEventCompactionEnd,
		Compaction: payload,
		Error:      errors.New("partial"),
	})
	e := drainChan(got, b, t)
	if e.Payload.Summary != "compacted" {
		t.Fatalf("Payload.Summary = %q", e.Payload.Summary)
	}
	if e.Err == nil || e.Err.Error() != "partial" {
		t.Fatalf("Err = %v", e.Err)
	}
	if !e.CostIncludedInRun {
		t.Fatal("bridge compaction cost must be included in RunEnded")
	}
}

// Regression: the automatic (bridge-driven) compaction path must
// toggle the authoritative compacting flag around the lifecycle events, and the
// run-end/error safety net must clear it if a run dies without a CompactionEnd.
func TestBridgeEvent_CompactingFlag(t *testing.T) {
	compacting := func(sctx *SessionContext) bool { return sctx.Compacting() }

	cases := []struct {
		name string
		end  string
	}{
		{"clean end via CompactionEnd", core.AgentEventCompactionEnd},
		{"safety net via AgentEnd", core.AgentEventEnd},
		{"safety net via AgentError", core.AgentEventError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := NewLocalBus()
			defer b.Close()
			sctx := newTestSessionContext(b, nil)

			if compacting(sctx) {
				t.Fatal("compacting flag set before start")
			}
			bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventCompactionStart})
			if !compacting(sctx) {
				t.Fatal("compacting flag not set after CompactionStart")
			}
			end := core.AgentEvent{Type: tc.end}
			if tc.end == core.AgentEventError {
				end.Error = errors.New("boom")
			}
			bridgeEvent(sctx, end)
			if compacting(sctx) {
				t.Fatalf("compacting flag still set after %v", tc.end)
			}
		})
	}
}

func TestRunStats_UsesLifecycleEventsForCostAndFinalText(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	pricing := &core.Pricing{Input: 1_000_000}
	fa := &fakeAgent{model: core.Model{Pricing: pricing}}
	sctx := newTestSessionContext(b, fa)
	sctx.RunGenAtomic.Store(7)
	sctx.runStats = runStats{gen: 7}

	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventMessageEnd,
		Message: core.AgentMessage{Message: core.Message{Role: "assistant", Content: []core.Content{core.TextContent("final")}, Usage: &core.Usage{Input: 2}}}})
	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventToolExecEnd, ToolName: "edit"})
	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventCompactionEnd,
		Compaction: &core.CompactionPayload{Usage: &core.Usage{Input: 3}}})

	stats := sctx.snapshotRunStats(7)
	if stats.finalText != "final" || !stats.hadEdits || stats.costUSD != 5 {
		t.Fatalf("stats = %#v, want final text, edits, and cost 5", stats)
	}
	sctx.RunGenAtomic.Store(8)
	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventMessageEnd,
		Message: core.AgentMessage{Message: core.Message{Role: "assistant", Content: []core.Content{core.TextContent("stale")}}}})
	if got := sctx.snapshotRunStats(7).finalText; got != "final" {
		t.Fatalf("stale generation changed final text to %q", got)
	}
}

// ===========================================================================
// Bridge integration test — subscribe/unsubscribe lifecycle
// ===========================================================================

func TestBridge_SubscribeAndUnsubscribe(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	sub := &fakeSubscriber{}

	got := make(chan AgentStarted, 2)
	b.Subscribe(func(e AgentStarted) { got <- e })

	unsub := Bridge(sctx, sub)

	// Emit via subscriber → should appear on bus.
	sub.emit(core.AgentEvent{Type: core.AgentEventStart})
	drainChan(got, b, t)

	// Unsubscribe.
	unsub()

	// Emit again → should NOT appear.
	sub.emit(core.AgentEvent{Type: core.AgentEventStart})
	expectNone(got, b, t)
}

// ===========================================================================
// Handler tests — commands
// ===========================================================================

func TestHandler_AbortRun(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	if err := b.Execute(AbortRun{SessionID: "test-session"}); err != nil {
		t.Fatal(err)
	}
	if !fa.wasAborted() {
		t.Fatal("Abort not called")
	}
}

func TestHandler_SteerAgent(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)
	// Steering targets an in-flight run; occupy the state so the queue pump
	// (kicked after enqueue to close the idle orphan-steer race) abstains and
	// leaves the steer queued for the running agent to drain.
	if err := sctx.State.Transition(StateRunning); err != nil {
		t.Fatal(err)
	}

	if err := b.Execute(SteerAgent{ID: "st1", Text: "focus here"}); err != nil {
		t.Fatal(err)
	}
	if fa.getSteered() != "focus here" {
		t.Fatalf("steered = %q", fa.getSteered())
	}
	// The queue must be inspectable with the authoritative ID so a reconnect
	// snapshot can reconcile the chip by ID.
	pending, err := QueryTyped[GetPendingSteers, []core.SteerItem](b, GetPendingSteers{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != "st1" || pending[0].Text != "focus here" {
		t.Fatalf("GetPendingSteers = %+v, want [{st1 focus here}]", pending)
	}
}

// A steer without an explicit ID must still get one (the handler mints it), so
// the API invariant "every queued steer has an ID" holds for all callers.
func TestHandler_SteerAgent_MintsMissingID(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)
	if err := sctx.State.Transition(StateRunning); err != nil {
		t.Fatal(err)
	}

	if err := b.Execute(SteerAgent{Text: "no id"}); err != nil {
		t.Fatal(err)
	}
	pending, _ := QueryTyped[GetPendingSteers, []core.SteerItem](b, GetPendingSteers{})
	if len(pending) != 1 || pending[0].ID == "" {
		t.Fatalf("GetPendingSteers = %+v, want one item with a non-empty ID", pending)
	}
}

// A full steer queue must surface ErrSteerQueueFull so the caller doesn't
// confirm a message that would never be delivered.
func TestHandler_SteerAgent_QueueFull(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{steerFull: true}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	err := b.Execute(SteerAgent{ID: "x", Text: "overflow"})
	if !errors.Is(err, ErrSteerQueueFull) {
		t.Fatalf("err = %v, want ErrSteerQueueFull", err)
	}
}

// Internal steers (subagent/bash completions) are delivered but must not appear
// in the user-visible queue snapshot.
func TestHandler_SteerAgent_InternalExcludedFromSnapshot(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)
	if err := sctx.State.Transition(StateRunning); err != nil {
		t.Fatal(err)
	}

	if err := b.Execute(SteerAgent{ID: "u1", Text: "user msg"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Execute(SteerAgent{ID: "i1", Text: "subagent done", Internal: true}); err != nil {
		t.Fatal(err)
	}
	pending, _ := QueryTyped[GetPendingSteers, []core.SteerItem](b, GetPendingSteers{})
	if len(pending) != 1 || pending[0].ID != "u1" {
		t.Fatalf("GetPendingSteers = %+v, want only the user steer", pending)
	}
}

// Canceling the queue must broadcast SteersCanceled so every client of the
// shared queue clears its chips.
func TestHandler_CancelSteer_PublishesEvent(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{steerQueue: []core.SteerItem{{
		ID: "image-steer",
		Content: []core.Content{{
			Type: "image", AttachmentID: "att_0123456789abcdef01234567",
		}},
	}}}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	got := make(chan SteersCanceled, 1)
	b.Subscribe(func(e SteersCanceled) { got <- e })

	if err := b.Execute(CancelSteer{}); err != nil {
		t.Fatal(err)
	}
	e := drainChan(got, b, t)
	if got, want := e.AttachmentIDs, []string{"att_0123456789abcdef01234567"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("AttachmentIDs = %v, want %v", got, want)
	}
}

func TestHandler_SetThinking(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{thinkingLevel: "low"}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	got := make(chan ConfigChanged, 1)
	b.Subscribe(func(e ConfigChanged) { got <- e })

	if err := b.Execute(SetThinking{Level: "high"}); err != nil {
		t.Fatal(err)
	}
	if fa.ThinkingLevel() != "high" {
		t.Fatalf("thinkingLevel = %q", fa.ThinkingLevel())
	}

	e := drainChan(got, b, t)
	if e.Thinking != "high" {
		t.Fatalf("ConfigChanged.Thinking = %q", e.Thinking)
	}
}

func TestHandler_ClearSession(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	got := make(chan CommandExecuted, 1)
	b.Subscribe(func(e CommandExecuted) { got <- e })

	if err := b.Execute(ClearSession{}); err != nil {
		t.Fatal(err)
	}
	if !fa.wasResetCalled() {
		t.Fatal("Reset not called")
	}
	e := drainChan(got, b, t)
	if e.Command != "clear" {
		t.Fatalf("Command = %q", e.Command)
	}
}

func TestHandler_CompactSession(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{
		messages: []core.AgentMessage{{Message: core.Message{Role: "user"}}},
	}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	got := make(chan CommandExecuted, 1)
	b.Subscribe(func(e CommandExecuted) { got <- e })

	if err := b.Execute(CompactSession{}); err != nil {
		t.Fatal(err)
	}
	e := drainChan(got, b, t)
	// Execute only ACCEPTS the compaction now, so the agent call is observed
	// after its terminal event, not on return.
	if !fa.wasCompactCalled() {
		t.Fatal("Compact not called")
	}
	if e.Command != "compact" {
		t.Fatalf("Command = %q", e.Command)
	}
	if len(e.Messages) != 1 {
		t.Fatalf("Messages len = %d", len(e.Messages))
	}
}

// The one-shot focus from `/compact <focus>` reaches the agent unchanged.
func TestHandler_CompactSession_ForwardsFocus(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{messages: []core.AgentMessage{{Message: core.Message{Role: "user"}}}}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)
	ended := make(chan CompactionEnded, 1)
	b.Subscribe(func(e CompactionEnded) { ended <- e })

	if err := b.Execute(CompactSession{Focus: "keep phase 3"}); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, b, ended, "CompactionEnded")
	if got := fa.focusPassed(); got != "keep phase 3" {
		t.Fatalf("focus passed to agent = %q, want %q", got, "keep phase 3")
	}
}

// A compaction failure is no longer the return value of Execute (the model call
// is asynchronous): the session settles to StateError carrying the message, and
// the terminal CompactionEnded carries the error too.
func TestHandler_CompactSession_Error(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{compactErr: errors.New("no context")}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)
	ended := make(chan CompactionEnded, 1)
	states := make(chan StateChanged, 8)
	// Observe both events on one ordered subscriber. Separate typed subscribers
	// run on independent goroutines, so receiving CompactionEnded would not prove
	// that the earlier StateChanged callback has run yet.
	b.SubscribeAll(func(event any) {
		switch e := event.(type) {
		case CompactionEnded:
			ended <- e
		case StateChanged:
			states <- e
		}
	})

	if err := b.Execute(CompactSession{}); err != nil {
		t.Fatalf("Execute must accept the compaction, got %v", err)
	}
	e := waitForEvent(t, b, ended, "CompactionEnded")
	if e.Err == nil || e.Err.Error() != "no context" {
		t.Fatalf("CompactionEnded.Err = %v", e.Err)
	}
	// The failure reaches frontends through the state machine (StateChanged →
	// state_change), which is why no dedicated error event is needed.
	if !sawErrorState(states, "no context") {
		t.Fatal("no StateChanged(error) carrying the compaction error")
	}
}

// A manual compact must occupy the session (StateRunning) for its whole
// duration so frontends switch the input to queue mode and Manager.Send steers
// instead of racing a concurrent run. It must settle back to idle afterwards.
func TestHandler_CompactSession_OccupiesSession(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	during := make(chan SessionState, 1)
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	fa.compactHook = func() { during <- sctx.State.Current() }
	RegisterHandlers(sctx)
	ended := make(chan CompactionEnded, 1)
	b.Subscribe(func(e CompactionEnded) { ended <- e })

	if got := sctx.State.Current(); got != StateIdle {
		t.Fatalf("pre-compact state = %q, want idle", got)
	}
	if err := b.Execute(CompactSession{}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-during:
		if got != StateRunning {
			t.Fatalf("state during compaction = %q, want running", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for the agent to enter Compact")
	}
	waitForEvent(t, b, ended, "CompactionEnded")
	if got := sctx.State.Current(); got != StateIdle {
		t.Fatalf("post-compact state = %q, want idle", got)
	}
}

// On a compaction error the session must settle to error (not stay stuck in
// running), so the input becomes usable again.
func TestHandler_CompactSession_ErrorSettlesState(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{compactErr: errors.New("boom")}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)
	ended := make(chan CompactionEnded, 1)
	b.Subscribe(func(e CompactionEnded) { ended <- e })

	_ = b.Execute(CompactSession{})
	waitForEvent(t, b, ended, "CompactionEnded")
	if got := sctx.State.Current(); got != StateError {
		t.Fatalf("post-error state = %q, want error", got)
	}
}

// Regression for the ghost compacting spinner: the authoritative
// compacting flag must be true while a compaction runs and cleared once it
// finishes — on both the success and error paths — so a reconnect snapshot
// (GetCompacting) never restores a stale spinner.
func TestHandler_CompactSession_CompactingFlag(t *testing.T) {
	compactingNow := func(b EventBus) bool {
		v, _ := QueryTyped[GetCompacting, bool](b, GetCompacting{})
		return v
	}

	t.Run("success", func(t *testing.T) {
		b := NewLocalBus()
		defer b.Close()
		during := make(chan bool, 1)
		fa := &fakeAgent{}
		sctx := newTestSessionContextWithState(b, fa)
		fa.compactHook = func() { during <- compactingNow(b) }
		RegisterHandlers(sctx)
		ended := make(chan CompactionEnded, 1)
		b.Subscribe(func(e CompactionEnded) { ended <- e })

		if compactingNow(b) {
			t.Fatal("compacting flag set before compaction")
		}
		if err := b.Execute(CompactSession{}); err != nil {
			t.Fatal(err)
		}
		if got := <-during; !got {
			t.Fatal("compacting flag not set during compaction")
		}
		waitForEvent(t, b, ended, "CompactionEnded")
		if compactingNow(b) {
			t.Fatal("compacting flag still set after successful compaction")
		}
	})

	t.Run("error", func(t *testing.T) {
		b := NewLocalBus()
		defer b.Close()
		fa := &fakeAgent{compactErr: errors.New("boom")}
		sctx := newTestSessionContextWithState(b, fa)
		RegisterHandlers(sctx)
		ended := make(chan CompactionEnded, 1)
		b.Subscribe(func(e CompactionEnded) { ended <- e })

		_ = b.Execute(CompactSession{})
		waitForEvent(t, b, ended, "CompactionEnded")
		if compactingNow(b) {
			t.Fatal("compacting flag still set after failed compaction")
		}
	})

	t.Run("panic", func(t *testing.T) {
		b := NewLocalBus()
		defer b.Close()
		fa := &fakeAgent{}
		sctx := newTestSessionContextWithState(b, fa)
		fa.compactHook = func() { panic("kaboom") }
		RegisterHandlers(sctx)
		ended := make(chan CompactionEnded, 1)
		b.Subscribe(func(e CompactionEnded) { ended <- e })

		// The goroutine recovers the panic into an error and still clears the
		// flag: LocalBus.Execute's own recover no longer covers this code.
		_ = b.Execute(CompactSession{})
		waitForEvent(t, b, ended, "CompactionEnded")
		if compactingNow(b) {
			t.Fatal("compacting flag still set after panicking compaction")
		}
	})
}

// The reconnect snapshot must distinguish a finished auto-verify from one
// still in flight. The counter permits overlapping verifies, so one ending
// cannot clear the indicator while another remains.
func TestHandler_GetAutoVerifying(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContextWithState(b, &fakeAgent{})
	RegisterHandlers(sctx)

	autoVerifying := func() bool {
		v, err := QueryTyped[GetAutoVerifying, bool](b, GetAutoVerifying{})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	if autoVerifying() {
		t.Fatal("reconnect snapshot reports auto-verify after it finished")
	}

	sctx.beginAutoVerify()
	sctx.beginAutoVerify()
	if !autoVerifying() {
		t.Fatal("reconnect snapshot does not report an auto-verify still in progress")
	}

	sctx.endAutoVerify()
	if !autoVerifying() {
		t.Fatal("reconnect snapshot cleared while another auto-verify is still in progress")
	}
	sctx.endAutoVerify()
	if autoVerifying() {
		t.Fatal("reconnect snapshot reports auto-verify after it finished")
	}
}

// The whole point of the asynchronous handler: Bus.Execute returns an
// ACCEPTANCE, not a completion. A `/compact` POST that blocked for the model
// call left the web composer read-only for tens of seconds, and a suspended PWA
// saw the aborted fetch as a failed command. On return the session must already
// be claimed (running + compacting + CompactionStarted published) so a
// concurrent close or run cannot slip in.
func TestHandler_CompactSession_ReturnsOnAcceptance(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	release := make(chan struct{})
	entered := make(chan struct{})
	fa := &fakeAgent{}
	fa.compactHook = func() {
		close(entered)
		<-release
	}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)
	started := make(chan CompactionStarted, 1)
	b.Subscribe(func(e CompactionStarted) { started <- e })
	ended := make(chan CompactionEnded, 1)
	b.Subscribe(func(e CompactionEnded) { ended <- e })

	if err := b.Execute(CompactSession{}); err != nil {
		t.Fatalf("Execute = %v, want acceptance", err)
	}
	// Blocked inside Agent.Compact while the command has already returned.
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for the agent to enter Compact")
	}
	select {
	case <-ended:
		t.Fatal("Execute returned only after the compaction completed")
	default:
	}
	if got := sctx.State.Current(); got != StateRunning {
		t.Fatalf("state on acceptance = %q, want running", got)
	}
	if v, _ := QueryTyped[GetCompacting, bool](b, GetCompacting{}); !v {
		t.Fatal("compacting flag not set on acceptance")
	}
	waitForEvent(t, b, started, "CompactionStarted")

	close(release)
	waitForEvent(t, b, ended, "CompactionEnded")
	if got := sctx.State.Current(); got != StateIdle {
		t.Fatalf("post-compact state = %q, want idle", got)
	}
}

// The terminal ordering the frontends depend on: the state settles to idle
// BEFORE the result is published, so a reactor seeing CompactionEnded never
// observes a running session.
func TestHandler_CompactSession_SettlesStateBeforeTerminalEvent(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{
		messages:       []core.AgentMessage{{Message: core.Message{Role: "user"}}},
		compactPayload: &core.CompactionPayload{Summary: "gist", TokensBefore: 2000, TokensAfter: 500},
	}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)
	// One ordered subscription: separate typed subscribers each run on their own
	// goroutine, so only the sequenced stream states the publication order.
	var mu sync.Mutex
	var order []string
	ended := make(chan CompactionEnded, 1)
	executed := make(chan CommandExecuted, 1)
	b.SubscribeAllSeq(func(_ uint64, event any) {
		switch e := event.(type) {
		case StateChanged:
			mu.Lock()
			order = append(order, "state:"+e.State)
			mu.Unlock()
		case CompactionEnded:
			mu.Lock()
			order = append(order, "compaction_ended")
			mu.Unlock()
			ended <- e
		case CommandExecuted:
			executed <- e
		}
	})

	if err := b.Execute(CompactSession{}); err != nil {
		t.Fatal(err)
	}
	e := waitForEvent(t, b, ended, "CompactionEnded")
	if e.Err != nil {
		t.Fatalf("CompactionEnded.Err = %v", e.Err)
	}
	if e.Marker == nil {
		t.Fatal("CompactionEnded carries no marker")
	}
	if cmd := waitForEvent(t, b, executed, "CommandExecuted"); cmd.Command != "compact" {
		t.Fatalf("CommandExecuted.Command = %q", cmd.Command)
	}
	if v, _ := QueryTyped[GetCompacting, bool](b, GetCompacting{}); v {
		t.Fatal("compacting flag still set after success")
	}
	b.Drain(time.Second)
	mu.Lock()
	defer mu.Unlock()
	idleAt, endedAt := -1, -1
	for i, s := range order {
		if s == "state:idle" && idleAt < 0 {
			idleAt = i
		}
		if s == "compaction_ended" && endedAt < 0 {
			endedAt = i
		}
	}
	if idleAt < 0 || endedAt < 0 || idleAt > endedAt {
		t.Fatalf("state must settle before the terminal event, order = %v", order)
	}
}

// A panic inside Agent.Compact is no longer covered by LocalBus.Execute's
// recover, so the goroutine must recover it itself: a session stuck in running
// would be unusable and unclosable until a restart.
func TestHandler_CompactSession_PanicSettlesSession(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	fa.compactHook = func() { panic("kaboom") }
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)
	ended := make(chan CompactionEnded, 1)
	b.Subscribe(func(e CompactionEnded) { ended <- e })
	states := make(chan StateChanged, 8)
	b.Subscribe(func(e StateChanged) { states <- e })
	// A message queued while the compact held the session must still be drained
	// after the panic: the queue is pumped on every exit path.
	fa.Steer(core.SteerItem{ID: "s1", Text: "after the panic"})

	if err := b.Execute(CompactSession{}); err != nil {
		t.Fatalf("Execute = %v, want acceptance", err)
	}
	e := waitForEvent(t, b, ended, "CompactionEnded")
	if e.Err == nil {
		t.Fatal("panic did not surface as a compaction error")
	}
	// The session must have LEFT running: it settled to error. (It may be
	// running again right after, because the pump starts the queued message —
	// which is itself the proof it was never stuck.)
	b.Drain(time.Second)
	if !sawErrorState(states, e.Err.Error()) {
		t.Fatal("session did not settle to error after a panicking compaction")
	}
	if v, _ := QueryTyped[GetCompacting, bool](b, GetCompacting{}); v {
		t.Fatal("compacting flag still set after a panicking compaction")
	}
	deadline := time.After(2 * time.Second)
	for {
		fa.mu.Lock()
		n := len(fa.sentItems)
		fa.mu.Unlock()
		if n >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the queue was not pumped after the panicking compaction")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// Two concurrent /compact: the state machine is the mutual exclusion. The
// second fails to claim idle→running and never reaches the agent, so the model
// call happens exactly once.
func TestHandler_CompactSession_ConcurrentSecondRejected(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	release := make(chan struct{})
	var calls atomic.Int32
	fa := &fakeAgent{}
	fa.compactHook = func() {
		calls.Add(1)
		<-release
	}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)
	ended := make(chan CompactionEnded, 1)
	b.Subscribe(func(e CompactionEnded) { ended <- e })

	if err := b.Execute(CompactSession{}); err != nil {
		t.Fatalf("first compact = %v, want acceptance", err)
	}
	if err := b.Execute(CompactSession{}); err == nil {
		t.Fatal("second concurrent compact was accepted; it must fail to claim the session")
	}
	close(release)
	waitForEvent(t, b, ended, "CompactionEnded")
	if got := calls.Load(); got != 1 {
		t.Fatalf("Agent.Compact entered %d times, want 1", got)
	}
}

// The first compact settles state before it publishes its terminal event. That
// state change must not let a second compact start until the first terminal
// event has been published, or the first cleanup would clear the second
// compact's authoritative flag.
func TestHandler_CompactSession_TerminalPublicationPrecedesNextStart(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	secondEntered := make(chan struct{})
	releaseSecond := make(chan struct{})
	var calls atomic.Int32
	fa := &fakeAgent{}
	fa.compactHook = func() {
		if calls.Add(1) == 2 {
			close(secondEntered)
			<-releaseSecond
		}
	}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	var launchSecond sync.Once
	secondResult := make(chan error, 1)
	b.Subscribe(func(e StateChanged) {
		if e.State == string(StateIdle) {
			launchSecond.Do(func() { secondResult <- b.Execute(CompactSession{}) })
		}
	})

	var orderMu sync.Mutex
	var order []string
	ended := make(chan CompactionEnded, 2)
	b.SubscribeAllSeq(func(_ uint64, event any) {
		switch e := event.(type) {
		case CompactionStarted:
			orderMu.Lock()
			order = append(order, "start")
			orderMu.Unlock()
		case CompactionEnded:
			orderMu.Lock()
			order = append(order, "end")
			orderMu.Unlock()
			ended <- e
		}
	})

	if err := b.Execute(CompactSession{}); err != nil {
		t.Fatalf("first compact = %v", err)
	}
	select {
	case <-secondEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("second compact never started from the idle transition")
	}
	if err := <-secondResult; err != nil {
		t.Fatalf("second compact = %v", err)
	}
	close(releaseSecond)
	waitForEvent(t, b, ended, "first CompactionEnded")
	waitForEvent(t, b, ended, "second CompactionEnded")
	b.Drain(time.Second)

	orderMu.Lock()
	defer orderMu.Unlock()
	firstEnd, secondStart := -1, -1
	for i, event := range order {
		if event == "end" && firstEnd < 0 {
			firstEnd = i
		}
		if event == "start" && i > 0 {
			secondStart = i
			break
		}
	}
	if firstEnd < 0 || secondStart < 0 || firstEnd > secondStart {
		t.Fatalf("first terminal event must precede the second start, order = %v", order)
	}
}

// The deferred finalizer must also recover panics after Agent.Compact returns.
// An extension controller can panic while producing the success snapshot; that
// must not strand the state in running or skip the queue pump.
func TestHandler_CompactSession_RecoveryCoversWholeGoroutine(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{panicMessages: true}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)
	ended := make(chan CompactionEnded, 1)
	b.Subscribe(func(e CompactionEnded) { ended <- e })

	if err := b.Execute(CompactSession{}); err != nil {
		t.Fatalf("Execute = %v, want acceptance", err)
	}
	e := waitForEvent(t, b, ended, "CompactionEnded")
	if e.Err == nil || e.Err.Error() != "compaction panic: messages panic" {
		t.Fatalf("CompactionEnded.Err = %v, want messages panic", e.Err)
	}
	if got := sctx.State.Current(); got != StateError {
		t.Fatalf("post-panic state = %q, want error", got)
	}
	if v, _ := QueryTyped[GetCompacting, bool](b, GetCompacting{}); v {
		t.Fatal("compacting flag still set after a post-compact panic")
	}
}

// A message sent while a compact holds the session busy is queued as a steer;
// since the compact never runs the agent loop, the queue pump must drain the
// queued steers and start a run to deliver them when compaction finishes. With
// the unified queue rail each steer becomes its own message (SendItems) and its
// own Steered announcement.
func TestHandler_CompactSession_DeliversQueuedSteers(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	// Simulate two user messages arriving mid-compaction: each queued as a steer.
	fa.compactHook = func() {
		fa.Steer(core.SteerItem{ID: "s1", Text: "first while compacting"})
		fa.Steer(core.SteerItem{ID: "s2", Text: "second while compacting"})
	}
	// Capture the per-item announcements the pump publishes.
	got := make(chan Steered, 8)
	b.Subscribe(func(e Steered) { got <- e })
	RegisterHandlers(sctx)

	if err := b.Execute(CompactSession{}); err != nil {
		t.Fatal(err)
	}
	// The pump starts a run via SendItems, which startRun runs asynchronously —
	// poll until both queued items were delivered as messages.
	deadline := time.After(2 * time.Second)
	for {
		fa.mu.Lock()
		n := len(fa.sentItems)
		fa.mu.Unlock()
		if n >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("queued steers were never delivered as a run after compaction")
		case <-time.After(5 * time.Millisecond):
		}
	}
	fa.mu.Lock()
	if fa.sentItems[0].Text != "first while compacting" || fa.sentItems[1].Text != "second while compacting" {
		fa.mu.Unlock()
		t.Fatalf("delivered items = %+v", fa.sentItems)
	}
	fa.mu.Unlock()

	// One Steered per item, each carrying its own chip ID and a non-empty MsgID.
	seen := map[string]string{}
	deadline = time.After(2 * time.Second)
	for len(seen) < 2 {
		select {
		case e := <-got:
			if e.MsgID == "" {
				t.Fatalf("Steered for %q has empty MsgID", e.ID)
			}
			seen[e.ID] = e.MsgID
		case <-deadline:
			t.Fatalf("expected 2 Steered events, got %d: %v", len(seen), seen)
		}
	}
	if _, ok := seen["s1"]; !ok {
		t.Fatalf("missing Steered for s1: %v", seen)
	}
	if _, ok := seen["s2"]; !ok {
		t.Fatalf("missing Steered for s2: %v", seen)
	}
}

func TestHandler_UndoLastChange(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)

	// Create a temp file, checkpoint it, modify it, then undo.
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(filePath, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}

	store := checkpoint.New(5)
	store.Begin("turn 1")
	if err := store.Capture(filePath); err != nil {
		t.Fatal(err)
	}

	// Overwrite the file to simulate the agent's write, then commit — matching
	// the real Capture-then-write-then-Commit order used by the write/edit tools.
	if err := os.WriteFile(filePath, []byte("modified"), 0644); err != nil {
		t.Fatal(err)
	}
	store.Commit()

	sctx.Checkpoints = store
	RegisterHandlers(sctx)

	if err := b.Execute(UndoLastChange{}); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "original" {
		t.Fatalf("content = %q, want %q", content, "original")
	}
}

func TestHandler_UndoLastChange_NoCheckpoints(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	err := b.Execute(UndoLastChange{})
	if err == nil || err.Error() != "checkpoints not available" {
		t.Fatalf("err = %v", err)
	}
}

func TestHandler_UndoLastChange_EmptyStore(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	sctx.Checkpoints = checkpoint.New(5)
	RegisterHandlers(sctx)

	err := b.Execute(UndoLastChange{})
	if err == nil {
		t.Fatal("expected error for empty checkpoint store")
	}
}

func TestHandler_MarkTaskDone(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)

	store := tasks.NewStore()
	store.Create("my task", "", nil)
	sctx.TaskStore = store
	RegisterHandlers(sctx)

	got := make(chan TasksUpdated, 1)
	b.Subscribe(func(e TasksUpdated) { got <- e })

	if err := b.Execute(MarkTaskDone{TaskID: 1}); err != nil {
		t.Fatal(err)
	}

	e := drainChan(got, b, t)
	if len(e.Tasks) != 1 || e.Tasks[0].Status != "done" {
		t.Fatalf("unexpected tasks: %+v", e.Tasks)
	}
}

func TestHandler_MarkTaskDone_NotFound(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	sctx.TaskStore = tasks.NewStore()
	RegisterHandlers(sctx)

	err := b.Execute(MarkTaskDone{TaskID: 999})
	if err == nil {
		t.Fatal("expected error for nonexistent task")
	}
}

func TestHandler_MarkTaskDone_NoStore(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	err := b.Execute(MarkTaskDone{TaskID: 1})
	if err == nil || err.Error() != "task store not available" {
		t.Fatalf("err = %v", err)
	}
}

func TestHandler_ResetTasks(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)

	store := tasks.NewStore()
	store.Create("task A", "", nil)
	store.Create("task B", "", nil)
	sctx.TaskStore = store
	RegisterHandlers(sctx)

	got := make(chan TasksUpdated, 1)
	b.Subscribe(func(e TasksUpdated) { got <- e })

	if err := b.Execute(ResetTasks{}); err != nil {
		t.Fatal(err)
	}

	e := drainChan(got, b, t)
	if len(e.Tasks) != 0 {
		t.Fatalf("expected 0 tasks after reset, got %d", len(e.Tasks))
	}
}

func TestHandler_ResetTasks_NoStore(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	err := b.Execute(ResetTasks{})
	if err == nil || err.Error() != "task store not available" {
		t.Fatalf("err = %v", err)
	}
}

// ===========================================================================
// Handler tests — queries
// ===========================================================================

func TestQuery_GetMessages(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{messages: []core.AgentMessage{
		{Message: core.Message{Role: "user"}},
		{Message: core.Message{Role: "assistant"}},
	}}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	msgs, err := QueryTyped[GetMessages, []core.AgentMessage](b, GetMessages{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("len = %d", len(msgs))
	}
}

func TestQuery_GetModel(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{model: core.Model{ID: "claude-4", Name: "Claude 4"}}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	m, err := QueryTyped[GetModel, core.Model](b, GetModel{})
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "claude-4" {
		t.Fatalf("Model.ID = %q", m.ID)
	}
}

func TestQuery_GetThinkingLevel(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{thinkingLevel: "medium"}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	level, err := QueryTyped[GetThinkingLevel, string](b, GetThinkingLevel{})
	if err != nil {
		t.Fatal(err)
	}
	if level != "medium" {
		t.Fatalf("level = %q", level)
	}
}

func TestQuery_GetContextUsage(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{
		model: core.Model{MaxInput: 1000},
		messages: []core.AgentMessage{
			{Message: core.Message{Role: "user", Content: []core.Content{{Type: "text", Text: "hello"}}}},
		},
	}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	pct, err := QueryTyped[GetContextUsage, int](b, GetContextUsage{})
	if err != nil {
		t.Fatal(err)
	}
	// We can't predict exact token estimation, but it should be >= 0 and <= 100.
	if pct < 0 || pct > 100 {
		t.Fatalf("pct = %d, want [0,100]", pct)
	}
}

func TestQuery_GetContextUsage_NoMaxInput(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{model: core.Model{MaxInput: 0}}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	pct, err := QueryTyped[GetContextUsage, int](b, GetContextUsage{})
	if err != nil {
		t.Fatal(err)
	}
	if pct != -1 {
		t.Fatalf("pct = %d, want -1", pct)
	}
}

func TestQuery_GetTasks(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	store := tasks.NewStore()
	store.Create("task A", "", nil)
	sctx.TaskStore = store
	RegisterHandlers(sctx)

	result, err := QueryTyped[GetTasks, []tasks.Task](b, GetTasks{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0].Title != "task A" {
		t.Fatalf("unexpected tasks: %+v", result)
	}
}

func TestQuery_GetTasks_NilStore(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	result, err := QueryTyped[GetTasks, []tasks.Task](b, GetTasks{})
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatalf("expected nil, got %+v", result)
	}
}

func TestQuery_GetCompactionEpoch(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{compactionEpoch: 3}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	epoch, err := QueryTyped[GetCompactionEpoch, int](b, GetCompactionEpoch{})
	if err != nil {
		t.Fatal(err)
	}
	if epoch != 3 {
		t.Fatalf("epoch = %d, want 3", epoch)
	}
}

func TestQuery_GetPermissionMode_NoGate(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	mode, err := QueryTyped[GetPermissionMode, string](b, GetPermissionMode{})
	if err != nil {
		t.Fatal(err)
	}
	if mode != "yolo" {
		t.Fatalf("mode = %q, want %q", mode, "yolo")
	}
}

func TestQuery_GetPathPolicy_Nil(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	info, err := QueryTyped[GetPathPolicy, PathPolicyInfo](b, GetPathPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if info.WorkspaceRoot != "" || info.Scope != "" || info.AllowedPaths != nil {
		t.Fatalf("expected empty PathPolicyInfo, got %+v", info)
	}
}

// ===========================================================================
// GetSessionState
// ===========================================================================

func TestQuery_GetSessionState_NilState(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	state, err := QueryTyped[GetSessionState, string](b, GetSessionState{})
	if err != nil {
		t.Fatal(err)
	}
	if state != "idle" {
		t.Fatalf("state = %q, want idle", state)
	}
}

func TestQuery_GetSessionState_WithState(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	state, err := QueryTyped[GetSessionState, string](b, GetSessionState{})
	if err != nil {
		t.Fatal(err)
	}
	if state != "idle" {
		t.Fatalf("state = %q, want idle", state)
	}

	// Force to error and check again.
	sctx.State.ForceState(StateError)
	state, err = QueryTyped[GetSessionState, string](b, GetSessionState{})
	if err != nil {
		t.Fatal(err)
	}
	if state != "error" {
		t.Fatalf("state = %q, want error", state)
	}
}

// ===========================================================================
// SwitchModel — requires model registry so tested with error case only
// ===========================================================================

func TestHandler_SwitchModel_NilFactory(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	// ProviderFactory is nil by default.
	RegisterHandlers(sctx)

	err := b.Execute(SwitchModel{ModelSpec: "claude-4"})
	if err == nil {
		t.Fatal("expected error for nil ProviderFactory")
	}
	if err.Error() != "model switching unavailable: provider factory not configured" {
		t.Fatalf("err = %v", err)
	}
}

func TestHandler_SwitchModel_Unknown(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	sctx.ProviderFactory = func(m core.Model) (core.Provider, error) {
		return nil, fmt.Errorf("no provider")
	}
	RegisterHandlers(sctx)

	err := b.Execute(SwitchModel{ModelSpec: "nonexistent-model-xyz"})
	if err == nil {
		t.Fatal("expected error for unknown model")
	}
}

func TestHandler_SwitchModel_CustomProviderModel(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{model: core.Model{ID: "grok-4.5", Provider: "xai"}, thinkingLevel: "low"}
	sctx := newTestSessionContext(b, fa)
	sctx.ProviderFactory = func(m core.Model) (core.Provider, error) {
		if m.Provider != "xai" || m.ID != "future-grok" {
			t.Fatalf("factory model = %+v", m)
		}
		return errProvider{}, nil
	}
	RegisterHandlers(sctx)

	if err := b.Execute(SwitchModel{ModelSpec: "xai/future-grok"}); err != nil {
		t.Fatal(err)
	}
	if fa.setModelModel.Provider != "xai" || fa.setModelModel.ID != "future-grok" {
		t.Fatalf("switched model = %+v", fa.setModelModel)
	}
}

// ===========================================================================
// SendPrompt handler tests
// ===========================================================================

func TestHandler_SendPrompt(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{
		sendResult: []core.AgentMessage{
			{Message: core.Message{Role: "assistant", Content: []core.Content{
				{Type: "text", Text: "hello world"},
			}}},
		},
	}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	// Subscribe to events.
	gotRunEnded := make(chan RunEnded, 1)
	b.Subscribe(func(e RunEnded) { gotRunEnded <- e })

	gotStates := make(chan StateChanged, 10)
	b.Subscribe(func(e StateChanged) { gotStates <- e })

	// Execute.
	if err := b.Execute(SendPrompt{Text: "say hello"}); err != nil {
		t.Fatal(err)
	}

	// Wait for RunEnded.
	re := waitForRunEnded(t, gotRunEnded, b)
	if re.FinalText != "hello world" {
		t.Fatalf("FinalText = %q", re.FinalText)
	}
	if re.Err != nil {
		t.Fatalf("Err = %v", re.Err)
	}

	// Verify state transitions: idle→running, running→idle.
	b.Drain(time.Second)
	var states []string
	for {
		select {
		case s := <-gotStates:
			states = append(states, s.State)
		default:
			goto done
		}
	}
done:
	if len(states) != 2 || states[0] != "running" || states[1] != "idle" {
		t.Fatalf("states = %v, want [running, idle]", states)
	}

	if !fa.wasSendCalled() {
		t.Fatal("Send not called")
	}
	if fa.getSendPrompt() != "say hello" {
		t.Fatalf("sendPrompt = %q", fa.getSendPrompt())
	}
}

func TestHandler_SendPrompt_Error(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{
		sendErr: errors.New("provider timeout"),
	}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	gotRunEnded := make(chan RunEnded, 1)
	b.Subscribe(func(e RunEnded) { gotRunEnded <- e })

	if err := b.Execute(SendPrompt{Text: "fail"}); err != nil {
		t.Fatal(err)
	}

	re := waitForRunEnded(t, gotRunEnded, b)
	if re.Err == nil || re.Err.Error() != "provider timeout" {
		t.Fatalf("Err = %v", re.Err)
	}
	if sctx.State.Current() != StateError {
		t.Fatalf("state = %q, want error", sctx.State.Current())
	}
}

func TestHandler_SendPrompt_Abort(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{
		sendDelay: 5 * time.Second, // long enough to abort
	}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	gotRunEnded := make(chan RunEnded, 1)
	b.Subscribe(func(e RunEnded) { gotRunEnded <- e })

	if err := b.Execute(SendPrompt{Text: "long task"}); err != nil {
		t.Fatal(err)
	}

	// Give the goroutine time to start.
	time.Sleep(50 * time.Millisecond)

	// Abort.
	if err := b.Execute(AbortRun{}); err != nil {
		t.Fatal(err)
	}

	re := waitForRunEnded(t, gotRunEnded, b)
	// On abort: Err should be nil (cancelled, not a real error).
	if re.Err != nil {
		t.Fatalf("Err = %v, want nil on abort", re.Err)
	}
	// State should be idle (not error).
	if sctx.State.Current() != StateIdle {
		t.Fatalf("state = %q, want idle after abort", sctx.State.Current())
	}
}

func TestHandler_SendPrompt_WhenRunning(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	// Force state to running.
	sctx.State.ForceState(StateRunning)

	err := b.Execute(SendPrompt{Text: "should fail"})
	if err == nil {
		t.Fatal("expected error when sending while running")
	}
}

func TestHandler_SendPrompt_WithCheckpoints(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(filePath, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}

	fa := &fakeAgent{
		sendResult: []core.AgentMessage{
			{Message: core.Message{Role: "assistant", Content: []core.Content{
				{Type: "text", Text: "done"},
			}}},
		},
	}
	sctx := newTestSessionContextWithState(b, fa)
	store := checkpoint.New(5)
	sctx.Checkpoints = store
	RegisterHandlers(sctx)

	// Simulate a file capture happening during the run (normally the tool does this).
	// We capture before executing so the checkpoint has content.
	gotRunEnded := make(chan RunEnded, 1)
	b.Subscribe(func(e RunEnded) { gotRunEnded <- e })

	if err := b.Execute(SendPrompt{Text: "with checkpoint"}); err != nil {
		t.Fatal(err)
	}

	// Capture a file while the run is active (before Send returns).
	// Since fakeAgent.Send is instant, the checkpoint Begin has already been called.
	// We can't capture mid-run with a fake, so verify the lifecycle works
	// by checking state returns to idle and no errors.
	waitForRunEnded(t, gotRunEnded, b)

	if sctx.State.Current() != StateIdle {
		t.Fatalf("state = %q, want idle", sctx.State.Current())
	}
}

func TestHandler_SendPrompt_NoStaleText(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	// Pre-existing messages.
	fa := &fakeAgent{
		messages: []core.AgentMessage{
			{Message: core.Message{Role: "user", Content: []core.Content{{Type: "text", Text: "old prompt"}}}},
			{Message: core.Message{Role: "assistant", Content: []core.Content{{Type: "text", Text: "old response"}}}},
		},
		sendResult: []core.AgentMessage{
			{Message: core.Message{Role: "assistant", Content: []core.Content{
				{Type: "text", Text: "new response"},
			}}},
		},
	}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	gotRunEnded := make(chan RunEnded, 1)
	b.Subscribe(func(e RunEnded) { gotRunEnded <- e })

	if err := b.Execute(SendPrompt{Text: "new prompt"}); err != nil {
		t.Fatal(err)
	}

	re := waitForRunEnded(t, gotRunEnded, b)
	// FinalText should be "new response", NOT "old response".
	if re.FinalText != "new response" {
		t.Fatalf("FinalText = %q, want %q", re.FinalText, "new response")
	}
}

func TestHandler_SendPromptWithContent(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{
		sendResult: []core.AgentMessage{
			{Message: core.Message{Role: "assistant", Content: []core.Content{
				{Type: "text", Text: "image analyzed"},
			}}},
		},
	}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	gotRunEnded := make(chan RunEnded, 1)
	b.Subscribe(func(e RunEnded) { gotRunEnded <- e })

	content := []core.Content{{Type: "image", Text: "base64data"}}
	if err := b.Execute(SendPromptWithContent{Content: content}); err != nil {
		t.Fatal(err)
	}

	re := waitForRunEnded(t, gotRunEnded, b)
	if re.FinalText != "image analyzed" {
		t.Fatalf("FinalText = %q", re.FinalText)
	}
}

func TestHandler_SendPrompt_AnnouncesUserMessage(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	fa.announceToBus(b, "test-session")
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	got := make(chan UserMessageAppended, 4)
	b.Subscribe(func(e UserMessageAppended) { got <- e })
	gotRunEnded := make(chan RunEnded, 1)
	b.Subscribe(func(e RunEnded) { gotRunEnded <- e })

	if err := b.Execute(SendPrompt{Text: "hola desde el móvil"}); err != nil {
		t.Fatal(err)
	}

	ev := drainChan(got, b, t)
	if ev.Text != "hola desde el móvil" {
		t.Fatalf("Text = %q", ev.Text)
	}
	if ev.MsgID == "" {
		t.Fatal("MsgID is empty; clients cannot dedup the message")
	}
	// The announced ID must be the one the message lands in history with.
	// The run goroutine calls the agent asynchronously, so wait for it to end.
	waitForRunEnded(t, gotRunEnded, b)
	if fa.getSendMsgID() != ev.MsgID {
		t.Fatalf("agent MsgID = %q, announced %q", fa.getSendMsgID(), ev.MsgID)
	}
}

func TestHandler_SendPromptWithContent_AnnouncesUserMessage(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	fa.announceToBus(b, "test-session")
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	got := make(chan UserMessageAppended, 4)
	b.Subscribe(func(e UserMessageAppended) { got <- e })

	content := []core.Content{{Type: "image", Text: "base64data"}, core.TextContent("mira")}
	if err := b.Execute(SendPromptWithContent{Content: content, MsgID: "client-1"}); err != nil {
		t.Fatal(err)
	}

	ev := drainChan(got, b, t)
	if ev.MsgID != "client-1" {
		t.Fatalf("MsgID = %q, want the client-minted ID", ev.MsgID)
	}
	if len(ev.Content) != 2 {
		t.Fatalf("Content = %+v, want the full block list", ev.Content)
	}
}

func TestHandler_SendPrompt_NoAnnounceOnRejectedOrInternal(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	fa.announceToBus(b, "test-session")
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	got := make(chan UserMessageAppended, 4)
	b.Subscribe(func(e UserMessageAppended) { got <- e })
	gotRunEnded := make(chan RunEnded, 1)
	b.Subscribe(func(e RunEnded) { gotRunEnded <- e })

	// Internal producer: no user-visible message to announce.
	if err := b.Execute(SendPrompt{Text: "verify", Custom: map[string]any{"source": "goal"}}); err != nil {
		t.Fatal(err)
	}
	waitForRunEnded(t, gotRunEnded, b) // settle before forcing the state below
	expectNone(got, b, t)

	// Rejected prompt: the session never accepted it, so announcing would
	// leave a phantom message on every client.
	sctx.State.ForceState(StateRunning)
	if err := b.Execute(SendPrompt{Text: "too late"}); err == nil {
		t.Fatal("expected a rejected send while running")
	}
	expectNone(got, b, t)
}

// A prompt that arrives with a non-empty queue rail is converted into a queued
// steer. The two rails have distinct identities, so it must be reported on the
// STEER rail: reporting a chip ID as a message ID makes the caller reconcile
// the wrong rail (chip dropped, phantom message kept).
func TestHandler_SendPrompt_QueuedReportsSteerRail(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{steerQueue: []core.SteerItem{{ID: "ahead", Text: "first"}}}
	sctx := newTestSessionContextWithState(b, fa)
	sctx.State.ForceState(StateRunning) // keep the pump from draining the rail
	RegisterHandlers(sctx)

	acceptedMsg, acceptedSteer := "c-msg", ""
	if err := b.Execute(SendPrompt{
		Text: "hola", MsgID: "c-msg", AcceptedMsgID: &acceptedMsg,
		SteerID: "c-steer", AcceptedSteerID: &acceptedSteer,
	}); err != nil {
		t.Fatal(err)
	}
	if acceptedSteer != "c-steer" {
		t.Fatalf("accepted steer ID = %q, want the client-supplied chip ID", acceptedSteer)
	}
	if acceptedMsg != "" {
		t.Fatalf("accepted msg ID = %q, want empty: the prompt was queued, not sent", acceptedMsg)
	}
	q := fa.PendingSteers()
	if len(q) != 2 || q[1].ID != "c-steer" || q[1].Text != "hola" {
		t.Fatalf("queue = %+v, want the prompt queued under its chip ID", q)
	}
}

// Same conversion for a content send: the queued chip must carry the text of
// the content blocks, or the Steered event (and every chip rendering it) shows
// an empty message.
func TestHandler_SendPromptWithContent_QueuedKeepsTextAndSteerRail(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{steerQueue: []core.SteerItem{{ID: "ahead", Text: "first"}}}
	sctx := newTestSessionContextWithState(b, fa)
	sctx.State.ForceState(StateRunning)
	RegisterHandlers(sctx)

	acceptedMsg, acceptedSteer := "c-msg", ""
	content := []core.Content{{Type: "image", Data: "base64data"}, core.TextContent("mira esto")}
	if err := b.Execute(SendPromptWithContent{
		Content: content, MsgID: "c-msg", AcceptedMsgID: &acceptedMsg,
		SteerID: "c-steer", AcceptedSteerID: &acceptedSteer,
	}); err != nil {
		t.Fatal(err)
	}
	if acceptedSteer != "c-steer" || acceptedMsg != "" {
		t.Fatalf("accepted (msg=%q, steer=%q), want the steer rail only", acceptedMsg, acceptedSteer)
	}
	q := fa.PendingSteers()
	if len(q) != 2 {
		t.Fatalf("queue = %+v, want the content send queued", q)
	}
	if q[1].Text != "mira esto" {
		t.Fatalf("queued Text = %q, want the text of the content blocks", q[1].Text)
	}
	if len(q[1].Content) != 2 {
		t.Fatalf("queued Content = %+v, want the full block list", q[1].Content)
	}
}

// The queued content send's Steered event must carry that text end to end: the
// pump publishes Steered from the item, and clients render data.text only.
func TestHandler_SendPromptWithContent_QueuedSteeredCarriesText(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{steerQueue: []core.SteerItem{{ID: "ahead", Text: "first"}}}
	sctx := newTestSessionContextWithState(b, fa)
	sctx.State.ForceState(StateRunning)
	RegisterHandlers(sctx)

	got := make(chan Steered, 4)
	b.Subscribe(func(e Steered) { got <- e })

	content := []core.Content{{Type: "image", Data: "base64data"}, core.TextContent("mira esto")}
	if err := b.Execute(SendPromptWithContent{Content: content, SteerID: "c-steer"}); err != nil {
		t.Fatal(err)
	}
	// Let the pump drain the rail now that the fake run is over.
	sctx.State.ForceState(StateIdle)
	requestPump(sctx)

	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-got:
			if ev.ID != "c-steer" {
				continue
			}
			if ev.Text != "mira esto" {
				t.Fatalf("Steered.Text = %q, want the queued content's text", ev.Text)
			}
			return
		case <-deadline:
			t.Fatal("no Steered event for the queued content send")
		}
	}
}

// ===========================================================================
// ClearSession — state-aware
// ===========================================================================

func TestHandler_ClearSession_FromError(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	// Force error state.
	sctx.State.ForceState(StateError)

	got := make(chan CommandExecuted, 1)
	b.Subscribe(func(e CommandExecuted) { got <- e })

	if err := b.Execute(ClearSession{}); err != nil {
		t.Fatal(err)
	}

	drainChan(got, b, t)

	// State should be back to idle.
	if sctx.State.Current() != StateIdle {
		t.Fatalf("state = %q, want idle after clear", sctx.State.Current())
	}
}

// ===========================================================================
// New handler tests — SendPrompt with Custom, AppendToConversation,
// SetPermissionMode, ResolvePermission, ResolveAskUser, queries
// ===========================================================================

func TestHandler_SendPrompt_WithCustom(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{
		sendResult: []core.AgentMessage{
			{Message: core.Message{Role: "assistant", Content: []core.Content{
				{Type: "text", Text: "custom response"},
			}}},
		},
	}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	gotRunEnded := make(chan RunEnded, 1)
	b.Subscribe(func(e RunEnded) { gotRunEnded <- e })

	if err := b.Execute(SendPrompt{
		Text:   "hello",
		Custom: map[string]any{"source": "subagent"},
	}); err != nil {
		t.Fatal(err)
	}

	re := waitForRunEnded(t, gotRunEnded, b)
	if re.FinalText != "custom response" {
		t.Fatalf("FinalText = %q", re.FinalText)
	}
}

func TestHandler_AppendToConversation(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	msg := core.AgentMessage{
		Message: core.Message{
			Role:    "user",
			Content: []core.Content{core.TextContent("shell output")},
		},
	}
	if err := b.Execute(AppendToConversation{Message: msg}); err != nil {
		t.Fatal(err)
	}

	msgs := fa.Messages()
	if len(msgs) != 1 || msgs[0].Role != "user" {
		t.Fatalf("messages = %+v", msgs)
	}
}

func TestHandler_SetPermissionMode_YoloToAsk(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	sctx.Approvals = NewApprovalManager(b, sctx.State, "test-session")
	RegisterHandlers(sctx)

	gotConfig := make(chan ConfigChanged, 1)
	b.Subscribe(func(e ConfigChanged) { gotConfig <- e })

	// Initially no gate (yolo).
	if sctx.GetGate() != nil {
		t.Fatal("expected nil gate initially")
	}

	// Switch to ask.
	if err := b.Execute(SetPermissionMode{Mode: "ask"}); err != nil {
		t.Fatal(err)
	}

	e := drainChan(gotConfig, b, t)
	if e.PermissionMode != "ask" {
		t.Fatalf("PermissionMode = %q", e.PermissionMode)
	}
	if sctx.GetGate() == nil {
		t.Fatal("expected gate to be created")
	}

	// Query should return ask.
	mode, err := QueryTyped[GetPermissionMode, string](b, GetPermissionMode{})
	if err != nil {
		t.Fatal(err)
	}
	if mode != "ask" {
		t.Fatalf("mode = %q", mode)
	}
}

func TestHandler_SetPermissionMode_AskToYolo(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	sctx.Approvals = NewApprovalManager(b, sctx.State, "test-session")
	RegisterHandlers(sctx)

	// Set up ask mode first.
	_ = b.Execute(SetPermissionMode{Mode: "ask"})
	b.Drain(100 * time.Millisecond)

	gotConfig := make(chan ConfigChanged, 2)
	b.Subscribe(func(e ConfigChanged) { gotConfig <- e })

	// Switch to yolo.
	if err := b.Execute(SetPermissionMode{Mode: "yolo"}); err != nil {
		t.Fatal(err)
	}

	e := drainChan(gotConfig, b, t)
	if e.PermissionMode != "yolo" {
		t.Fatalf("PermissionMode = %q", e.PermissionMode)
	}
	if sctx.GetGate() == nil || sctx.GetGate().Mode() != permission.ModeYolo {
		t.Fatal("expected yolo gate to remain active for hard-coded safety checks")
	}
}

func TestHandler_SetPermissionMode_Invalid(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	err := b.Execute(SetPermissionMode{Mode: "invalid"})
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
}

func TestHandler_ResolvePermission(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	am := NewApprovalManager(b, sctx.State, "test-session")
	sctx.Approvals = am
	RegisterHandlers(sctx)

	// Add pending permission.
	respCh := make(chan permission.Response, 1)
	am.mu.Lock()
	am.perms["p1"] = &PendingPermission{
		ID: "p1", ToolName: "write", response: respCh,
	}
	am.mu.Unlock()
	sctx.State.ForceState(StatePermission)

	if err := b.Execute(ResolvePermission{
		PermissionID: "p1", Approved: true, Feedback: "ok",
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case resp := <-respCh:
		if !resp.Approved {
			t.Fatal("expected approved")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestHandler_ResolvePermission_PersistsAllow(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	sctx.CWD = t.TempDir()
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	am := NewApprovalManager(b, sctx.State, "test-session")
	sctx.Approvals = am
	RegisterHandlers(sctx)

	resolve := func(id, allow string) {
		respCh := make(chan permission.Response, 1)
		am.mu.Lock()
		am.perms[id] = &PendingPermission{ID: id, ToolName: "bash", response: respCh}
		am.mu.Unlock()
		sctx.State.ForceState(StatePermission)
		if err := b.Execute(ResolvePermission{
			PermissionID: id, Approved: true, AllowPattern: allow,
		}); err != nil {
			t.Fatalf("resolve %s: %v", id, err)
		}
		<-respCh
	}

	// The approval is the user's, so it is persisted with their config and never
	// inside the project — which is committed and shared.
	resolve("p1", "Bash(git:*)")
	if allow := loadProjectAllow(t, sctx.CWD); !contains(allow, "Bash(git:*)") {
		t.Fatalf("Permissions.Allow = %v, want to contain Bash(git:*)", allow)
	}
	if _, err := os.Stat(filepath.Join(sctx.CWD, ".moa", "config.json")); !os.IsNotExist(err) {
		t.Fatalf("approving must not write into the project, stat err = %v", err)
	}

	// Resolving again with the same pattern does not duplicate it.
	resolve("p2", "Bash(git:*)")
	allow := loadProjectAllow(t, sctx.CWD)
	if n := countOccurrences(allow, "Bash(git:*)"); n != 1 {
		t.Fatalf("Bash(git:*) appears %d times, want 1: %v", n, allow)
	}
}

func TestHandler_ResolvePermission_NoAllowNoFile(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	sctx.CWD = t.TempDir()
	am := NewApprovalManager(b, sctx.State, "test-session")
	sctx.Approvals = am
	RegisterHandlers(sctx)

	respCh := make(chan permission.Response, 1)
	am.mu.Lock()
	am.perms["p1"] = &PendingPermission{ID: "p1", ToolName: "bash", response: respCh}
	am.mu.Unlock()
	sctx.State.ForceState(StatePermission)

	// Approved but with no allow pattern → must not write a config file.
	if err := b.Execute(ResolvePermission{PermissionID: "p1", Approved: true}); err != nil {
		t.Fatal(err)
	}
	<-respCh

	cfgPath := filepath.Join(sctx.CWD, ".moa", "config.json")
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Fatalf("expected no config file, stat err = %v", err)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func countOccurrences(s []string, v string) int {
	n := 0
	for _, x := range s {
		if x == v {
			n++
		}
	}
	return n
}

// loadProjectAllow reads the allow patterns saved in the user's project state.
func loadProjectAllow(t *testing.T, cwd string) []string {
	t.Helper()
	st, err := core.LoadProjectState(cwd)
	if err != nil {
		t.Fatalf("load project state: %v", err)
	}
	return st.PermissionAllow
}

func TestHandler_ResolveAskUser(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	am := NewApprovalManager(b, sctx.State, "test-session")
	sctx.Approvals = am
	RegisterHandlers(sctx)

	respCh := make(chan []string, 1)
	am.mu.Lock()
	am.asks["a1"] = &PendingAsk{
		ID: "a1", Questions: []AskQuestion{{Text: "Name?"}}, response: respCh,
	}
	am.mu.Unlock()

	if err := b.Execute(ResolveAskUser{
		AskID: "a1", Answers: []string{"Bob"},
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case answers := <-respCh:
		if len(answers) != 1 || answers[0] != "Bob" {
			t.Fatalf("answers = %v", answers)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestQuery_GetSessionError(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	// Initially empty.
	errStr, err := QueryTyped[GetSessionError, string](b, GetSessionError{})
	if err != nil {
		t.Fatal(err)
	}
	if errStr != "" {
		t.Fatalf("initial error = %q", errStr)
	}

	// Set error state.
	sctx.State.ForceState(StateRunning)
	_ = sctx.State.TransitionWithError(StateError, "boom")

	errStr, _ = QueryTyped[GetSessionError, string](b, GetSessionError{})
	if errStr != "boom" {
		t.Fatalf("error = %q, want boom", errStr)
	}
}

func TestQuery_GetPendingApproval(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	am := NewApprovalManager(b, sctx.State, "test-session")
	sctx.Approvals = am
	RegisterHandlers(sctx)

	// Empty initially.
	info, err := QueryTyped[GetPendingApproval, PendingApprovalInfo](b, GetPendingApproval{})
	if err != nil {
		t.Fatal(err)
	}
	if info.Permission != nil || info.Ask != nil {
		t.Fatal("expected empty")
	}

	// Add pending permission.
	respCh := make(chan permission.Response, 1)
	am.mu.Lock()
	am.perms["p1"] = &PendingPermission{
		ID: "p1", ToolName: "write", AllowPattern: "write(*)", response: respCh,
	}
	am.mu.Unlock()

	info, _ = QueryTyped[GetPendingApproval, PendingApprovalInfo](b, GetPendingApproval{})
	if info.Permission == nil || info.Permission.ID != "p1" {
		t.Fatal("expected permission p1")
	}
	if info.Permission.AllowPattern != "write(*)" {
		t.Fatalf("AllowPattern = %q", info.Permission.AllowPattern)
	}
}

// ---------------------------------------------------------------------------
// SetThinking validation tests
// ---------------------------------------------------------------------------

func TestHandler_SetThinking_InvalidLevel(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{thinkingLevel: "low"}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	err := b.Execute(SetThinking{Level: "invalid"})
	if err == nil {
		t.Fatal("expected error for invalid thinking level")
	}
	// Agent thinking should remain unchanged.
	if fa.ThinkingLevel() != "low" {
		t.Fatalf("thinkingLevel = %q, want low", fa.ThinkingLevel())
	}
}

func TestHandler_SetThinking_ValidLevels(t *testing.T) {
	for _, level := range core.ThinkingLevels {
		t.Run(level, func(t *testing.T) {
			b := NewLocalBus()
			defer b.Close()
			fa := &fakeAgent{}
			sctx := newTestSessionContext(b, fa)
			RegisterHandlers(sctx)

			if err := b.Execute(SetThinking{Level: level}); err != nil {
				t.Fatalf("SetThinking(%q) = %v", level, err)
			}
			if fa.ThinkingLevel() != level {
				t.Fatalf("thinkingLevel = %q, want %q", fa.ThinkingLevel(), level)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SetPathScope normalization and validation tests
// ---------------------------------------------------------------------------

func TestHandler_SetPathScope_Workspace(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	sctx.PathPolicy = tool.NewPathPolicy(t.TempDir(), nil, true) // start unrestricted
	RegisterHandlers(sctx)

	if err := b.Execute(SetPathScope{Scope: "workspace"}); err != nil {
		t.Fatal(err)
	}
	if sctx.PathPolicy.Scope() != "workspace" {
		t.Fatalf("scope = %q, want workspace", sctx.PathPolicy.Scope())
	}
}

func TestHandler_SetPathScope_Unrestricted(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	sctx.PathPolicy = tool.NewPathPolicy(t.TempDir(), nil, false) // start restricted
	RegisterHandlers(sctx)

	if err := b.Execute(SetPathScope{Scope: "unrestricted"}); err != nil {
		t.Fatal(err)
	}
	if sctx.PathPolicy.Scope() != "unrestricted" {
		t.Fatalf("scope = %q, want unrestricted", sctx.PathPolicy.Scope())
	}
}

func TestHandler_SetPathScope_WsPlusN_Normalized(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	sctx.PathPolicy = tool.NewPathPolicy(t.TempDir(), nil, true) // start unrestricted
	RegisterHandlers(sctx)

	// ws+3 should be normalized to workspace.
	if err := b.Execute(SetPathScope{Scope: "ws+3"}); err != nil {
		t.Fatalf("SetPathScope(ws+3) = %v", err)
	}
	if sctx.PathPolicy.Scope() != "workspace" {
		t.Fatalf("scope = %q, want workspace", sctx.PathPolicy.Scope())
	}
}

func TestHandler_SetPathScope_Invalid(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	sctx.PathPolicy = tool.NewPathPolicy(t.TempDir(), nil, false)
	RegisterHandlers(sctx)

	err := b.Execute(SetPathScope{Scope: "bogus"})
	if err == nil {
		t.Fatal("expected error for invalid scope")
	}
}

func TestHandler_SetPathScope_NilPolicy(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	// PathPolicy is nil.
	RegisterHandlers(sctx)

	err := b.Execute(SetPathScope{Scope: "workspace"})
	if err == nil {
		t.Fatal("expected error when PathPolicy is nil")
	}
}

func TestHandler_AddAllowedPath(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	sctx.PathPolicy = tool.NewPathPolicy(t.TempDir(), nil, false)
	RegisterHandlers(sctx)

	extra := t.TempDir()
	if err := b.Execute(AddAllowedPath{Path: extra}); err != nil {
		t.Fatal(err)
	}
	paths := sctx.PathPolicy.AllowedPaths()
	if len(paths) != 1 || paths[0] != extra {
		t.Fatalf("AllowedPaths = %v, want [%s]", paths, extra)
	}
	if sctx.PathPolicy.Scope() != "ws+1" {
		t.Fatalf("scope = %q, want ws+1", sctx.PathPolicy.Scope())
	}
}

// ---------------------------------------------------------------------------
// Restore flow integration tests (simulates CLI session restore via bus)
// ---------------------------------------------------------------------------

func TestRestoreFlow_ThinkingPermissionsPath(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	sctx.Approvals = NewApprovalManager(b, sctx.State, "test-session")
	sctx.PathPolicy = tool.NewPathPolicy(t.TempDir(), nil, false)
	RegisterHandlers(sctx)

	// Restore thinking.
	if err := b.Execute(SetThinking{Level: "high"}); err != nil {
		t.Fatalf("SetThinking: %v", err)
	}

	// Restore permission mode.
	if err := b.Execute(SetPermissionMode{Mode: "ask"}); err != nil {
		t.Fatalf("SetPermissionMode: %v", err)
	}

	// Restore path scope (ws+2 → normalized to workspace).
	if err := b.Execute(SetPathScope{Scope: "ws+2"}); err != nil {
		t.Fatalf("SetPathScope: %v", err)
	}

	// Restore allowed path.
	extra := t.TempDir()
	if err := b.Execute(AddAllowedPath{Path: extra}); err != nil {
		t.Fatalf("AddAllowedPath: %v", err)
	}

	// Verify state.
	if fa.ThinkingLevel() != "high" {
		t.Errorf("thinking = %q, want high", fa.ThinkingLevel())
	}
	if sctx.GetGate() == nil {
		t.Error("gate should exist after SetPermissionMode(ask)")
	}
	// Path scope is "ws+1" because we set workspace + added 1 allowed path.
	if scope := sctx.PathPolicy.Scope(); scope != "ws+1" {
		t.Errorf("scope = %q, want ws+1", scope)
	}
}

func TestRestoreFlow_InvalidThinking_Error(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	err := b.Execute(SetThinking{Level: "invalid"})
	if err == nil {
		t.Error("expected error for invalid thinking level")
	}
}

// ===========================================================================
// msg_id uniqueness — the claim must be atomic with accepting the run
// ===========================================================================

// A client-supplied msg_id is claimed the instant the send is accepted, not
// when its message reaches history: the append happens later, in the run
// goroutine, so a uniqueness check made against history alone would still
// report the ID as free. The query must already see the claim, otherwise a
// second send would be confirmed under an identity it is about to lose.
func TestHandler_SendPrompt_MsgIDClaimedBeforeAppend(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{appendGate: make(chan struct{})}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	accepted := "c-dup"
	if err := b.Execute(SendPrompt{Text: "hola", MsgID: "c-dup", AcceptedMsgID: &accepted}); err != nil {
		t.Fatal(err)
	}
	if accepted != "c-dup" {
		t.Fatalf("accepted msg ID = %q, want the client-supplied one", accepted)
	}
	// The run is parked before the append: history cannot know the ID yet.
	if msgs := fa.Messages(); len(msgs) != 0 {
		t.Fatalf("history is not empty yet: %+v", msgs)
	}
	if inUse, _ := QueryTyped[MsgIDInUse, bool](b, MsgIDInUse{MsgID: "c-dup"}); !inUse {
		t.Fatal("MsgIDInUse(c-dup) = false while a send accepted under it has not appended yet")
	}
	close(fa.appendGate)
}

// Two sends carrying the SAME client msg_id, racing on purpose: both goroutines
// wait on one barrier, and the fake agent parks every append behind a gate, so
// their claims overlap exactly the window a history-only check leaves open.
// Exactly one may keep the ID; the others must be re-minted, and history must
// end up with a single message under it — a duplicate is deduped live but
// reappears doubled after a reload.
func TestHandler_SendPrompt_ConcurrentSameMsgID(t *testing.T) {
	const senders = 8
	b := NewLocalBus()
	defer b.Close()
	// Park every append: while the gate is shut, no send's message is visible in
	// history, which is precisely the state a history-only uniqueness check
	// misreads as "the ID is free".
	fa := &fakeAgent{appendGate: make(chan struct{})}
	// No state machine: every send is accepted, so all claims genuinely race
	// instead of being serialized by the run slot.
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	accepted := make([]string, senders)
	errs := make([]error, senders)
	for i := range senders {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			errs[i] = b.Execute(SendPrompt{Text: "hola", MsgID: "c-dup", AcceptedMsgID: &accepted[i]})
		}()
	}
	start.Done()
	done.Wait()
	// Every send has been accepted (and answered with an ID) with nothing in
	// history yet. Only now let the appends through, then wait for all of them.
	close(fa.appendGate)
	deadline := time.After(2 * time.Second)
	for len(fa.Messages()) < senders {
		select {
		case <-deadline:
			t.Fatalf("only %d of %d messages reached history", len(fa.Messages()), senders)
		case <-time.After(time.Millisecond):
		}
	}
	b.Drain(2 * time.Second)

	seen := map[string]int{}
	for i, id := range accepted {
		if errs[i] != nil {
			t.Fatalf("send %d failed: %v", i, errs[i])
		}
		if id == "" {
			t.Fatalf("send %d was accepted without an ID", i)
		}
		seen[id]++
	}
	if len(seen) != senders {
		t.Fatalf("accepted IDs collide: %v", seen)
	}
	if seen["c-dup"] != 1 {
		t.Fatalf("the client ID was handed to %d sends, want exactly 1", seen["c-dup"])
	}

	inHistory := 0
	for _, m := range fa.Messages() {
		if m.MsgID == "c-dup" {
			inHistory++
		}
	}
	if inHistory != 1 {
		t.Fatalf("history holds %d messages under c-dup, want 1", inHistory)
	}
}

// A message the user branched away from still exists in the tree, one /branch
// away from being on screen again. Uniqueness must therefore be checked against
// the whole tree, not the current branch's projection: reusing that ID would
// give the client two different messages under one identity as soon as it
// navigates back.
func TestHandler_SendPrompt_MsgIDTakenOnAnotherBranch(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	fa.announceToBus(b, "test-session")
	sctx := newTestSessionContextWithState(b, fa)
	sctx.Tree = session.NewTree()
	RegisterHandlers(sctx)
	RegisterTreeSyncer(b, sctx)

	fa.mu.Lock()
	fa.messages = []core.AgentMessage{msgWithID("user", "hi", "m-1"), msgWithID("assistant", "hello", "m-2")}
	fa.mu.Unlock()
	b.Publish(RunEnded{SessionID: "test-session"})
	b.Drain(time.Second)

	// Branch back to the first message: m-2 leaves the current path but stays
	// in the tree.
	if err := b.Execute(BranchTo{EntryID: "m-1"}); err != nil {
		t.Fatalf("BranchTo: %v", err)
	}
	b.Drain(time.Second)
	for _, m := range sctx.treeSyncer.DisplayMessages() {
		if m.MsgID == "m-2" {
			t.Fatal("m-2 is still on the current branch; the test would not exercise branch-away")
		}
	}

	gotRunEnded := make(chan RunEnded, 1)
	b.Subscribe(func(e RunEnded) { gotRunEnded <- e })
	accepted := "m-2"
	if err := b.Execute(SendPrompt{Text: "again", MsgID: "m-2", AcceptedMsgID: &accepted}); err != nil {
		t.Fatal(err)
	}
	if accepted == "m-2" {
		t.Fatal("accepted an ID that another branch of the tree already holds")
	}
	waitForRunEnded(t, gotRunEnded, b)
}

// A claim exists only while the send is in flight: once the message is in
// history, history is what keeps the ID taken. Otherwise the claim map would
// grow for the whole life of the session, and its contents would be lost on
// restart anyway.
func TestHandler_SendPrompt_ClaimReleasedOnceAppended(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	fa.announceToBus(b, "test-session")
	sctx := newTestSessionContextWithState(b, fa)
	sctx.Tree = session.NewTree()
	RegisterHandlers(sctx)
	RegisterTreeSyncer(b, sctx)

	gotRunEnded := make(chan RunEnded, 8)
	b.Subscribe(func(e RunEnded) { gotRunEnded <- e })

	for i := range 5 {
		id := fmt.Sprintf("c-%d", i)
		accepted := ""
		if err := b.Execute(SendPrompt{Text: "hola", MsgID: id, AcceptedMsgID: &accepted}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
		if accepted != id {
			t.Fatalf("send %d accepted %q, want the client ID %q", i, accepted, id)
		}
		waitForRunEnded(t, gotRunEnded, b)
	}
	b.Drain(time.Second)

	if n := sctx.reservedMsgIDCount(); n != 0 {
		t.Fatalf("claims still held after every message landed: %d", n)
	}
	// The IDs are still taken — now by history, which is what survives a
	// restart.
	for i := range 5 {
		id := fmt.Sprintf("c-%d", i)
		if inUse, _ := QueryTyped[MsgIDInUse, bool](b, MsgIDInUse{MsgID: id}); !inUse {
			t.Fatalf("MsgIDInUse(%s) = false, want true (the message is in history)", id)
		}
	}
}
