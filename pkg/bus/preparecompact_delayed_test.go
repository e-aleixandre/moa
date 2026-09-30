package bus

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

type prepareDelayedProvider struct {
	mu       sync.Mutex
	calls    int
	gateCall int
	entered  chan struct{}
	release  chan struct{}
}

func (p *prepareDelayedProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()
	if call == p.gateCall {
		close(p.entered)
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	m := core.Message{Role: "assistant", Content: []core.Content{core.TextContent(fmt.Sprintf("delayed response %d", call))}, StopReason: "end_turn", Timestamp: time.Now().Unix()}
	ch := make(chan core.AssistantEvent, 2)
	ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &m}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &m}
	close(ch)
	return ch, nil
}

func prepareDelayedRuntime(t *testing.T, count int, provider *prepareDelayedProvider) (*SessionRuntime, *agent.Agent, *prepareDanglingPersister) {
	t.Helper()
	tree := session.NewTree()
	for i := 0; i < count; i++ {
		m := core.WrapMessage(core.NewUserMessage(strings.Repeat(fmt.Sprintf("original message %d ", i), 400)))
		m.EnsureMsgID()
		tree.Append(session.Entry{Type: session.EntryMessage, Message: m})
	}
	entries, leaf := tree.Snapshot()
	ag, err := agent.New(agent.AgentConfig{Provider: provider, Model: core.Model{ID: "delayed", MaxInput: 512}, Tools: core.NewRegistry(), Compaction: &core.CompactionSettings{Enabled: true, ReserveTokens: 10, KeepRecent: 10}, MaxTurns: 5, MaxRunDuration: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	store, err := session.NewFileStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	p := &prepareDanglingPersister{store: store, sess: store.Create()}
	rt, err := NewSessionRuntime(RuntimeConfig{SessionID: p.sess.ID, Agent: ag, Persister: p, InitialEntries: entries, InitialLeafID: leaf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	return rt, ag, p
}

func prepareDelayedWait(t *testing.T, rt *SessionRuntime, ended <-chan RunEnded) RunEnded {
	t.Helper()
	select {
	case e := <-ended:
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if !rt.WaitSettled(ctx) {
			t.Fatal("run did not settle")
		}
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("run did not end")
	}
	return RunEnded{}
}

// The syncer is delayed on its own real mutex, not by stopping the bus or
// constructing compaction events. Both runs use the actual Agent and runtime.
func TestPrepareCompact_DelayedSyncKeepsPreparationEphemeral(t *testing.T) {
	provider := &prepareDelayedProvider{gateCall: 2, entered: make(chan struct{}), release: make(chan struct{})}
	rt, ag, p := prepareDelayedRuntime(t, 1, provider)
	original := ag.Messages()[0].MsgID
	ended := make(chan RunEnded, 4)
	rt.Bus.Subscribe(func(e RunEnded) { ended <- e })
	var eventsMu sync.Mutex
	var autoGens []uint64
	rt.Bus.Subscribe(func(e CompactionEnded) {
		if e.RunGen != 0 && e.Payload != nil {
			eventsMu.Lock()
			autoGens = append(autoGens, e.RunGen)
			eventsMu.Unlock()
		}
	})

	ts := rt.Context().treeSyncer
	ts.mu.Lock()
	locked := true
	defer func() {
		if locked {
			ts.mu.Unlock()
		}
	}()
	// FIFO keeps every later CompactionEnded behind this ordinary sync event.
	rt.Bus.Publish(CommandExecuted{SessionID: rt.ID, Command: "delayed-sync"})
	if err := rt.Bus.Execute(PrepareCompactSession{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first preparation provider gate not reached")
	}
	// The RunEnded pump admits the queued second preparation even though
	// TreeSyncer's ordered subscriber has not processed the first one yet.
	if err := rt.Bus.Execute(QueueCommand{Raw: "/prepare-compact"}); err != nil {
		t.Fatal(err)
	}
	close(provider.release)
	for i := 0; i < 2; i++ {
		e := prepareDelayedWait(t, rt, ended)
		if e.Err != nil {
			t.Fatal(e.Err)
		}
		t.Logf("preparation %d finished: gen=%d, live messages=%d epoch=%d", i+1, e.RunGen, len(ag.Messages()), ag.CompactionEpoch())
	}
	ts.mu.Unlock()
	locked = false
	rt.Bus.Drain(3 * time.Second)
	eventsMu.Lock()
	gens := append([]uint64(nil), autoGens...)
	eventsMu.Unlock()
	if len(gens) != 2 || gens[0] == gens[1] {
		t.Fatalf("fixture requires two ephemeral generations, got %v", gens)
	}
	t.Logf("actual automatic compaction generations=%v", gens)
	if err := rt.Flush(); err != nil {
		t.Fatal(err)
	}
	saved, err := p.store.Load(p.sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := session.NewTreeFromEntries(saved.Entries, saved.LeafID)
	if err != nil {
		t.Fatal(err)
	}
	var cuts int
	for _, e := range saved.Entries {
		if e.Type == session.EntryCompaction {
			cuts++
			_, exists := reloaded.Entry(e.Compaction.FirstKeptEntryID)
			t.Logf("saved boundary=%s cut=%s targetExists=%v summary=%q", e.ID, e.Compaction.FirstKeptEntryID, exists, e.Compaction.Summary)
		}
	}
	if cuts != 0 {
		t.Errorf("discarded preparations persisted %d boundaries, want 0", cuts)
	}
	ctx, epoch := reloaded.BuildContext()
	if len(ctx) != 1 || ctx[0].MsgID != original || epoch != ag.CompactionEpoch() {
		t.Errorf("reload context roles/IDs=%v epoch=%d; want original %q only, epoch=%d", prepareDelayedIDs(ctx), epoch, original, ag.CompactionEpoch())
	}
}

func prepareDelayedIDs(msgs []core.AgentMessage) []string {
	var out []string
	for _, m := range msgs {
		out = append(out, m.Role+":"+m.MsgID)
	}
	return out
}

type prepareDelayedBridgeProvider struct {
	mu      sync.Mutex
	calls   int
	agent   *agent.Agent
	entered chan struct{}
	release chan struct{}
}

func (p *prepareDelayedBridgeProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()
	m := core.Message{Role: "assistant", Content: []core.Content{core.TextContent(fmt.Sprintf("bridge reply %d", call))}, StopReason: "end_turn", Timestamp: time.Now().Unix()}
	if call == 1 {
		// The first turn enters without compaction; shrinking the window now
		// makes the next iteration compact after the bridge's MessageStart.
		if err := p.agent.Reconfigure(nil, core.Model{ID: "delayed", MaxInput: 512}, "", 0); err != nil {
			return nil, err
		}
		m.Content = []core.Content{core.TextContent(strings.Repeat("working ", 40)), core.ToolCallContent("delayed-tool-call", "delayed", map[string]any{})}
		m.StopReason = "tool_use"
	}
	if call == 4 {
		close(p.entered)
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ch := make(chan core.AssistantEvent, 2)
	ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &m}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &m}
	close(ch)
	return ch, nil
}

func TestPrepareCompact_DelayedRealCompactionRemainsDurable(t *testing.T) {
	provider := &prepareDelayedBridgeProvider{entered: make(chan struct{}), release: make(chan struct{})}
	tools := core.NewRegistry()
	if err := tools.Register(core.Tool{Name: "delayed", Parameters: []byte(`{"type":"object","properties":{}}`), Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
		return core.TextResult("ok"), nil
	}}); err != nil {
		t.Fatal(err)
	}
	ag, err := agent.New(agent.AgentConfig{Provider: provider, Model: core.Model{ID: "delayed", MaxInput: 32768}, Tools: tools, Compaction: &core.CompactionSettings{Enabled: true, ReserveTokens: 10, KeepRecent: 10}, MaxTurns: 5, MaxRunDuration: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	provider.agent = ag
	tree := session.NewTree()
	m := core.WrapMessage(core.NewUserMessage(strings.Repeat("original message ", 400)))
	m.EnsureMsgID()
	tree.Append(session.Entry{Type: session.EntryMessage, Message: m})
	entries, leaf := tree.Snapshot()
	fs, err := session.NewFileStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	persister := &prepareDanglingPersister{store: fs, sess: fs.Create()}
	rt, err := NewSessionRuntime(RuntimeConfig{SessionID: persister.sess.ID, Agent: ag, Persister: persister, InitialEntries: entries, InitialLeafID: leaf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	ended := make(chan RunEnded, 4)
	rt.Bus.Subscribe(func(e RunEnded) { ended <- e })
	compacted := make(chan CompactionEnded, 4)
	rt.Bus.Subscribe(func(e CompactionEnded) {
		if e.Payload != nil {
			compacted <- e
		}
	})

	// Use the actual production bridge and its actual snapshot mutex. Its
	// MessageStarted handler blocks here while later real compaction events
	// remain queued in the Agent's real emitter. No subscriber is replaced.
	rt.Context().streamMu.Lock()
	locked := true
	defer func() {
		if locked {
			rt.Context().streamMu.Unlock()
		}
	}()
	if err := rt.Bus.Execute(SendPrompt{Text: strings.Repeat("actual normal turn ", 40)}); err != nil {
		t.Fatal(err)
	}
	// The default 2s emitter DrainTimeout expires, so gen 1 can settle before
	// its real compaction has been bridged.
	first := prepareDelayedWait(t, rt, ended)
	if first.Err != nil {
		t.Fatal(first.Err)
	}
	if ag.CompactionEpoch() != 1 {
		t.Fatalf("fixture did not compact real conversation: epoch=%d", ag.CompactionEpoch())
	}
	// The real compacted conversation and the preparation both fit entirely
	// in this larger window. The preparation's final compact is also a noop.
	if err := ag.Reconfigure(nil, core.Model{ID: "delayed", MaxInput: 32768}, "", 0); err != nil {
		t.Fatal(err)
	}
	if err := rt.Bus.Execute(PrepareCompactSession{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("preparation provider gate not reached")
	}
	current := rt.Context().RunGenAtomic.Load()
	rt.Context().streamMu.Unlock()
	locked = false
	ag.Drain(2 * time.Second)
	close(provider.release)
	second := prepareDelayedWait(t, rt, ended)
	if second.Err != nil {
		t.Fatal(second.Err)
	}
	rt.Bus.Drain(2 * time.Second)
	select {
	case e := <-compacted:
		t.Logf("real summary=%q original generation=%d, stamped RunGen=%d during preparation generation=%d", e.Payload.Summary, first.RunGen, e.RunGen, current)
		if e.RunGen != current || e.Payload.Summary != "bridge reply 2" {
			t.Fatal("fixture did not delay the real compaction into next generation")
		}
	default:
		t.Fatal("real compaction event was not delivered")
	}
	prepareDelayedSaveAndCompare(t, rt, ag, persister, 1)
}

func prepareDelayedSaveAndCompare(t *testing.T, rt *SessionRuntime, ag *agent.Agent, p *prepareDanglingPersister, wantCuts int) {
	t.Helper()
	rt.Bus.Drain(2 * time.Second)
	if err := rt.Flush(); err != nil {
		t.Fatal(err)
	}
	saved, err := p.store.Load(p.sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := session.NewTreeFromEntries(saved.Entries, saved.LeafID)
	if err != nil {
		t.Fatal(err)
	}
	cuts := 0
	for _, e := range saved.Entries {
		if e.Type != session.EntryCompaction {
			continue
		}
		cuts++
		if _, ok := tree.Entry(e.Compaction.FirstKeptEntryID); !ok {
			t.Errorf("saved dangling cut=%s", e.Compaction.FirstKeptEntryID)
		}
	}
	if cuts != wantCuts {
		t.Errorf("saved boundaries=%d, want %d", cuts, wantCuts)
	}
	ctx, epoch := tree.BuildContext()
	live := ag.Messages()
	projection := func(msgs []core.AgentMessage) []string {
		var out []string
		for _, m := range msgs {
			id := m.MsgID
			if m.Role == "compaction_summary" {
				id = "boundary"
			}
			out = append(out, fmt.Sprintf("%s:%s:%v", m.Role, id, m.Content))
		}
		return out
	}
	if !reflect.DeepEqual(projection(ctx), projection(live)) || epoch != ag.CompactionEpoch() {
		t.Errorf("reload IDs=%v epoch=%d, live IDs=%v epoch=%d", prepareDelayedIDs(ctx), epoch, prepareDelayedIDs(live), ag.CompactionEpoch())
	}
}
