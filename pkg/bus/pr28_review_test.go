package bus

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/sessioncheckpoint"
)

const pr28Ckpt = "PR28-CHECKPOINT-SENTINEL"

// newPR28CkptFix is newBGFix (soft 39k < estimate ~40k < hard 99k) with a real
// session checkpoint slot shared by the agent and the runtime, as in serve.
// onCompactionSave runs inside the commit's persist, right after the store
// accepted the first snapshot carrying a boundary: what is on disk then is
// exactly what a SIGKILL at that instant leaves behind.
func newPR28CkptFix(t *testing.T, slot *sessioncheckpoint.Slot, onCompactionSave func(*session.Session)) *bgFix {
	t.Helper()
	c := bgCfg{window: 100000, compactAt: 40000, reserve: 1000, keep: 8000, initial: bgPrefix()}
	f := &bgFix{
		ended: make(chan RunEnded, 16), cuts: make(chan CompactionEnded, 16),
		toolStarted: make(chan struct{}), toolRelease: make(chan struct{}),
		sum:     &bgSummary{entered: make(chan struct{}), release: make(chan struct{})},
		reg:     core.NewRegistry(),
		initial: c.initial,
	}
	f.prov = &bgProvider{f: f, reqs: make(chan bgReq, 32)}
	f.settings = core.CompactionSettings{Enabled: true, ReserveTokens: c.reserve, KeepRecent: c.keep, CompactAt: c.compactAt, TrimDisabled: true}
	f.window = f.settings.EffectiveWindow(c.window)
	settings := f.settings
	ag, err := agent.New(agent.AgentConfig{
		Provider: f.prov, Model: core.Model{ID: "bg", MaxInput: c.window}, Tools: f.reg, Compaction: &settings,
		MaxTurns: 12, MaxRunDuration: 20 * time.Second, SessionCheckpoint: slot,
		CompactSummarizer: func(m core.Model) (core.Provider, core.Model, string) { return f.sum, m, "" },
	})
	if err != nil {
		t.Fatal(err)
	}
	f.ag = ag
	tree := session.NewTree()
	for _, m := range c.initial {
		m.EnsureMsgID()
		tree.Append(session.Entry{Type: session.EntryMessage, Message: m})
	}
	entries, leaf := tree.Snapshot()
	store, err := session.NewFileStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	f.p = &catomicPersister{store: store, sess: store.Create(), attempted: make(chan struct{})}
	f.p.after = func(entries []session.Entry) {
		if !catomicHasCompaction(entries) {
			return
		}
		f.durable.Store(true)
		once.Do(func() {
			disk, err := store.Load(f.p.sess.ID)
			if err != nil {
				t.Errorf("load at the commit instant: %v", err)
				return
			}
			onCompactionSave(disk)
		})
	}
	rt, err := NewSessionRuntime(RuntimeConfig{SessionID: f.p.sess.ID, Agent: ag, Persister: f.p,
		InitialEntries: entries, InitialLeafID: leaf, SessionCheckpoint: slot})
	if err != nil {
		t.Fatal(err)
	}
	f.rt = rt
	rt.Bus.Subscribe(func(e RunEnded) { f.ended <- e })
	rt.Bus.Subscribe(func(e CompactionEnded) { f.cuts <- e })
	rt.Bus.Subscribe(func(RunStarted) { f.runs.Add(1) })
	t.Cleanup(func() {
		f.sum.open()
		ag.Abort()
		ctx, cancel := context.WithTimeout(context.Background(), bgWait)
		defer cancel()
		rt.WaitSettled(ctx)
		rt.Bus.Drain(bgWait)
		rt.Close()
	})
	return f
}

func pr28BoundarySummary(entries []session.Entry, leaf string) string {
	path, err := catomicRawPath(entries, leaf)
	if err != nil {
		return ""
	}
	for i := len(path) - 1; i >= 0; i-- {
		if path[i].Type == session.EntryCompaction {
			return path[i].Compaction.Summary
		}
	}
	return ""
}

// Durability: the boundary and the consumption of the checkpoint it embeds are
// one transition. The commit's snapshot is collected after Accept but before
// adopt() consumes the slot, so the very file that makes the boundary durable
// still carries the checkpoint as pending. A SIGKILL before the next ordinary
// save reopens a session whose summary already contains the checkpoint AND a
// slot that will append it again at the next compaction (and the agent is told
// to act on a handoff it already received). Before this PR the automatic path
// consumed the slot before the event that made the boundary durable.
func TestPR28Review_CheckpointConsumedWithBoundary(t *testing.T) {
	for _, path := range []string{"idle", "boundary"} {
		t.Run(path, func(t *testing.T) {
			slot := sessioncheckpoint.New()
			if err := slot.Write(pr28Ckpt); err != nil {
				t.Fatal(err)
			}
			var disk *session.Session
			var mu sync.Mutex
			f := newPR28CkptFix(t, slot, func(s *session.Session) {
				mu.Lock()
				disk = s
				mu.Unlock()
			})
			if path == "idle" {
				f.send(t, bgText("go ", 40))
				f.request(t, "ordinary request while the summary is held")
				f.waitSummaryEntered(t)
				f.waitEnded(t, "originating run")
				f.rt.Bus.Drain(bgWait)
				f.sum.open()
				f.waitCut(t)
			} else {
				// The summary is released while the run is still active: a
				// second turn makes the loop adopt at its next boundary.
				f.prov.script = func(call int, req core.Request) *core.Message {
					if call == 1 {
						f.waitSummaryEntered(t)
						f.sum.open()
						// Let the worker publish its outcome before the
						// next boundary.
						time.Sleep(200 * time.Millisecond)
						return bgToolCall("c1", map[string]any{}, "tool")
					}
					return nil
				}
				f.send(t, bgText("go ", 40))
				f.request(t, "first")
				f.request(t, "second, after the boundary adoption")
				f.waitEnded(t, "run")
				f.waitCut(t)
			}
			mu.Lock()
			s := disk
			mu.Unlock()
			if s == nil {
				t.Fatal("harness: no compaction snapshot observed")
			}
			if !strings.Contains(pr28BoundarySummary(s.Entries, s.LeafID), pr28Ckpt) {
				t.Fatal("harness: the durable boundary does not embed the checkpoint")
			}
			// Live state after the whole chain is consistent...
			if text, _ := slot.Read(); text != "" {
				t.Fatalf("harness: the slot was not consumed in memory: %q", text)
			}
			// ...but the file a SIGKILL leaves at the commit instant is not.
			reopened := sessioncheckpoint.New()
			reopened.Restore(s.Metadata)
			if text, _ := reopened.Read(); text != "" {
				t.Fatalf("the snapshot that made the boundary durable still carries the checkpoint it embeds (%q): a reopen re-appends it at the next compaction", text)
			}
		})
	}
}
