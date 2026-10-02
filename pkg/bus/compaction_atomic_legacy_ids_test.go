package bus

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

// Load really migrates a v1 file. The only difference from the green control
// is whether its messages already have identities when the migration runs.
func TestCompactionAtomicMigratedLegacyMessageIDs(t *testing.T) {
	for _, mode := range []string{"manual", "automatic"} {
		for _, populated := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/nonempty_msgids=%t", mode, populated), func(t *testing.T) {
				store, err := session.NewFileStore(t.TempDir(), "")
				if err != nil {
					t.Fatal(err)
				}
				legacy := store.Create()
				legacy.Version = 1
				for i := 0; i < 4; i++ {
					role, text := "assistant", fmt.Sprintf("answer %d", i)
					if i%2 == 0 {
						// The first question alone exceeds the window; what is
						// retained after the cut fits it.
						n := 200
						if i == 0 {
							n = 800
						}
						role, text = "user", strings.Repeat(fmt.Sprintf("question %d ", i), n)
					}
					m := core.WrapMessage(core.Message{Role: role, Content: []core.Content{core.TextContent(text)}})
					if populated {
						m.MsgID = fmt.Sprintf("legacy-msg-%d", i)
					}
					legacy.Messages = append(legacy.Messages, m)
				}
				if err := store.Save(legacy); err != nil {
					t.Fatal(err)
				}
				loaded, err := store.Load(legacy.ID)
				if err != nil {
					t.Fatal(err)
				}
				if loaded.Version != session.SessionVersion || len(loaded.Entries) != 4 {
					t.Fatal("fixture did not migrate")
				}
				if err := session.ValidateEntries(loaded.Entries, loaded.LeafID); err != nil {
					t.Fatal(err)
				}
				for i, e := range loaded.Entries {
					if e.ID != fmt.Sprintf("migrated_%d", i) {
						t.Fatalf("unexpected migrated entry: %+v", e)
					}
					if populated && e.Message.MsgID != fmt.Sprintf("legacy-msg-%d", i) {
						t.Fatal("migration did not preserve message identity")
					}
				}
				ag, err := agent.New(agent.AgentConfig{Provider: &catomicSummaryProvider{}, Model: core.Model{ID: "review-legacy", MaxInput: 2048}, Tools: core.NewRegistry(), Compaction: &core.CompactionSettings{Enabled: true, ReserveTokens: 10, KeepRecent: 10}, MaxRunDuration: 10 * time.Second})
				if err != nil {
					t.Fatal(err)
				}
				p := &catomicPersister{store: store, sess: loaded, attempted: make(chan struct{})}
				rt, err := NewSessionRuntime(RuntimeConfig{SessionID: loaded.ID, Agent: ag, Persister: p, InitialEntries: loaded.Entries, InitialLeafID: loaded.LeafID})
				if err != nil {
					t.Fatal(err)
				}
				defer rt.Close()
				if len(ag.Messages()) != 4 {
					t.Fatal("valid legacy context did not reopen")
				}
				t.Logf("valid migrated target: Entry.ID=%q Message.MsgID=%q", loaded.Entries[2].ID, ag.Messages()[2].MsgID)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if mode == "manual" {
					ended := make(chan CompactionEnded, 1)
					rt.Bus.Subscribe(func(e CompactionEnded) { ended <- e })
					if err := rt.Bus.Execute(CompactSession{}); err != nil {
						t.Fatal(err)
					}
					select {
					case e := <-ended:
						if e.Err != nil {
							t.Fatalf("manual compact of valid migrated context failed: %v", e.Err)
						}
						if e.Payload == nil {
							t.Fatal("fixture did not compact")
						}
					case <-ctx.Done():
						t.Fatal("manual compact did not settle")
					}
				} else {
					ended := make(chan RunEnded, 1)
					rt.Bus.Subscribe(func(e RunEnded) { ended <- e })
					if err := rt.Bus.Execute(SendPrompt{Text: "continue"}); err != nil {
						t.Fatal(err)
					}
					select {
					case e := <-ended:
						if e.Err != nil {
							t.Fatalf("auto compact of valid migrated context failed: %v", e.Err)
						}
					case <-ctx.Done():
						t.Fatal("auto compact did not settle")
					}
				}
				if ag.CompactionEpoch() != 1 {
					t.Fatalf("fixture did not compact: epoch=%d", ag.CompactionEpoch())
				}
				rt.Bus.Drain(2 * time.Second)
				saved, err := store.Load(loaded.ID)
				if err != nil {
					t.Fatal(err)
				}
				cuts := 0
				for _, e := range saved.Entries {
					if e.Type != session.EntryCompaction {
						continue
					}
					cuts++
					if e.Compaction.FirstKeptEntryID != "migrated_2" {
						t.Fatalf("cut names %q, not the retained entry migrated_2", e.Compaction.FirstKeptEntryID)
					}
				}
				if cuts != 1 {
					t.Fatalf("cuts=%d, want one durable cut", cuts)
				}
			})
		}
	}
}

func catomicLegacyTree(t *testing.T) (*TreeSyncer, []session.Entry) {
	t.Helper()
	msg := func(id, msgID, role string) session.Entry {
		m := core.WrapMessage(core.Message{Role: role, Content: []core.Content{core.TextContent(id)}})
		m.MsgID = msgID
		return session.Entry{ID: id, Type: session.EntryMessage, Message: m}
	}
	entries := []session.Entry{
		msg("migrated_0", "legacy-msg-0", "user"),
		msg("migrated_1", "legacy-msg-1", "assistant"),
		msg("migrated_2", "legacy-msg-2", "user"),
		msg("migrated_3", "legacy-msg-3", "assistant"),
		msg("side_0", "legacy-side", "user"),
	}
	for i := 1; i < 4; i++ {
		entries[i].ParentID = entries[i-1].ID
	}
	entries[4].ParentID = "migrated_0"
	tree, err := session.NewTreeFromEntries(entries, "migrated_3")
	if err != nil {
		t.Fatal(err)
	}
	return &TreeSyncer{tree: tree, synced: map[string]struct{}{}}, entries
}

func catomicLegacyPayload(cut string) *core.CompactionPayload {
	return &core.CompactionPayload{Summary: catomicSummary, SummaryMsgID: core.NewMsgID(), BoundaryID: core.NewMsgID(), FirstKeptMsgID: cut}
}

// A MsgID that only an inactive branch holds is not a cut on the active path.
func TestCompactionAtomic_CommitRejectsAliasOutsideActiveBranch(t *testing.T) {
	ts, _ := catomicLegacyTree(t)
	called := false
	err := ts.commitCompaction(core.CompactionCommit{Payload: catomicLegacyPayload("legacy-side")}, func([]session.Entry, string) error {
		called = true
		return nil
	})
	if err == nil || called {
		t.Fatalf("off-branch alias accepted: err=%v persisted=%v", err, called)
	}
}

// The boundary names the entry that holds the retained message, and the
// reopened projection is the summary followed by exactly the retained suffix.
func TestCompactionAtomic_CommitStampsEntryIDOfMigratedAlias(t *testing.T) {
	ts, _ := catomicLegacyTree(t)
	p := catomicLegacyPayload("legacy-msg-2")
	var saved []session.Entry
	var leaf string
	if err := ts.commitCompaction(core.CompactionCommit{Payload: p}, func(e []session.Entry, l string) error {
		saved, leaf = e, l
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var cut *session.Entry
	for i := range saved {
		if saved[i].Type == session.EntryCompaction {
			cut = &saved[i]
		}
	}
	if cut == nil || cut.Compaction.FirstKeptEntryID != "migrated_2" {
		t.Fatalf("boundary = %+v, want first kept migrated_2", cut)
	}
	tree, err := session.NewTreeFromEntries(saved, leaf)
	if err != nil {
		t.Fatal(err)
	}
	msgs, _ := tree.BuildContext()
	if len(msgs) != 3 || msgs[0].Role != "compaction_summary" || msgs[1].MsgID != "legacy-msg-2" || msgs[2].MsgID != "legacy-msg-3" {
		var got []string
		for _, m := range msgs {
			got = append(got, m.Role+":"+m.MsgID)
		}
		t.Fatalf("projection = %v, want [summary legacy-msg-2 legacy-msg-3]", got)
	}
}
