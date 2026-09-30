package bus

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/extension"
	"github.com/e-aleixandre/moa/pkg/session"
)

// prepareDanglingProvider answers every request with a short text; failCall
// makes that call (1-based) fail to start.
type prepareDanglingProvider struct {
	mu       sync.Mutex
	calls    int
	failCall int
}

func (p *prepareDanglingProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()
	if call == p.failCall {
		return nil, fmt.Errorf("synthetic provider failure at call %d", call)
	}
	m := core.Message{Role: "assistant", Content: []core.Content{core.TextContent("synthetic summary or reply")}, StopReason: "end_turn", Timestamp: time.Now().Unix()}
	ch := make(chan core.AssistantEvent, 2)
	ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &m}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &m}
	close(ch)
	return ch, nil
}

type prepareDanglingPersister struct {
	mu    sync.Mutex
	store *session.FileStore
	sess  *session.Session
}

func (p *prepareDanglingPersister) Snapshot([]core.AgentMessage, int, map[string]any) error {
	panic("tree persistence expected")
}

func (p *prepareDanglingPersister) SnapshotTree(entries []session.Entry, leafID string, metadata map[string]any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sess.Entries, p.sess.LeafID, p.sess.Metadata = entries, leafID, metadata
	return p.store.Save(p.sess)
}

// The hook adds an ordinary assistant message to the preparation run, so the
// discarded conversation holds a cut target the transcript never syncs.
type prepareDanglingHook struct{}

func (prepareDanglingHook) Init(api extension.API) error {
	api.OnBeforeAgentStart(func(context.Context) ([]core.AgentMessage, error) {
		return []core.AgentMessage{core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{core.TextContent(strings.Repeat("synthetic hook content ", 100))}})}, nil
	})
	return nil
}

// /prepare-compact runs its preparation turn on a copy of the conversation and
// restores the original afterwards. With a small window the preparation turn
// compacts automatically; that boundary cut the discarded conversation, so its
// first kept message never reaches the tree. It used to be recorded anyway and,
// when the final compaction did nothing or failed, a reload rebuilt the context
// from that boundary: the summary of a throwaway conversation and nothing else.
func TestPrepareCompact_EphemeralAutoCompactionLeavesNoDanglingCut(t *testing.T) {
	cases := []struct {
		name     string
		messages int
		failCall int
		hook     bool
		// final compaction boundaries the tree must end with
		boundaries int
		context    int
	}{
		{name: "final-noop", messages: 1, boundaries: 0, context: 1},
		// Call 1 summarises inside the preparation, 2 is its turn, 3 the final summary.
		{name: "final-fails", messages: 2, failCall: 3, boundaries: 0, context: 2},
		{name: "final-succeeds", messages: 2, boundaries: 1, context: 2},
		{name: "hook-target-final-noop", messages: 1, hook: true, boundaries: 0, context: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tree := session.NewTree()
			var original []string
			for i := 0; i < tc.messages; i++ {
				m := core.WrapMessage(core.NewUserMessage(strings.Repeat(fmt.Sprintf("message %d ", i), 400)))
				m.EnsureMsgID()
				original = append(original, m.MsgID)
				tree.Append(session.Entry{Type: session.EntryMessage, Message: m})
			}
			entries, leaf := tree.Snapshot()
			provider := &prepareDanglingProvider{failCall: tc.failCall}
			cfg := agent.AgentConfig{
				Provider: provider, Model: core.Model{ID: "local", MaxInput: 512}, Tools: core.NewRegistry(),
				Compaction: &core.CompactionSettings{Enabled: true, ReserveTokens: 10, KeepRecent: 10},
				MaxTurns:   5, MaxRunDuration: 10 * time.Second,
			}
			if tc.hook {
				cfg.Extensions = []extension.Extension{prepareDanglingHook{}}
			}
			ag, err := agent.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			store, err := session.NewFileStore(t.TempDir(), "")
			if err != nil {
				t.Fatal(err)
			}
			persister := &prepareDanglingPersister{store: store, sess: store.Create()}
			rt, err := NewSessionRuntime(RuntimeConfig{SessionID: persister.sess.ID, Agent: ag, Persister: persister, InitialEntries: entries, InitialLeafID: leaf})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(rt.Close)

			var mu sync.Mutex
			ephemeral := 0
			rt.Bus.Subscribe(func(e CompactionEnded) {
				if e.RunGen != 0 && e.Payload != nil {
					mu.Lock()
					ephemeral++
					mu.Unlock()
				}
			})
			ended := make(chan RunEnded, 1)
			rt.Bus.Subscribe(func(e RunEnded) { ended <- e })
			if err := rt.Bus.Execute(PrepareCompactSession{}); err != nil {
				t.Fatal(err)
			}
			select {
			case e := <-ended:
				if (e.Err != nil) != (tc.failCall != 0) {
					t.Fatalf("run error=%v, want failure=%v", e.Err, tc.failCall != 0)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("prepare-compact did not finish")
			}
			rt.Bus.Drain(3 * time.Second)
			if err := rt.Flush(); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			if ephemeral == 0 {
				t.Fatal("fixture did not compact inside the preparation run")
			}
			mu.Unlock()

			saved, err := store.Load(persister.sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			reloaded, err := session.NewTreeFromEntries(saved.Entries, saved.LeafID)
			if err != nil {
				t.Fatal(err)
			}
			boundaries := 0
			for _, entry := range saved.Entries {
				if entry.Type != session.EntryCompaction {
					continue
				}
				boundaries++
				if _, ok := reloaded.Entry(entry.Compaction.FirstKeptEntryID); !ok {
					t.Errorf("persisted boundary %s cuts at %q, which is not in the tree", entry.ID, entry.Compaction.FirstKeptEntryID)
				}
			}
			if boundaries != tc.boundaries {
				t.Errorf("persisted compaction boundaries=%d want %d", boundaries, tc.boundaries)
			}
			ctx, epoch := reloaded.BuildContext()
			if len(ctx) != tc.context {
				t.Errorf("reloaded context has %d messages, want %d", len(ctx), tc.context)
			}
			if tc.boundaries == 0 {
				for i, m := range ctx {
					if i >= len(original) || m.MsgID != original[i] {
						t.Errorf("reloaded context %d is %s %q, want the restored original conversation", i, m.Role, m.MsgID)
					}
				}
			}
			if epoch != ag.CompactionEpoch() {
				t.Errorf("reloaded epoch=%d, live agent epoch=%d", epoch, ag.CompactionEpoch())
			}
		})
	}
}
