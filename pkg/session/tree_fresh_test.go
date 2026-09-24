package session

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func freshEntry(firstKeptID string) Entry {
	return Entry{Type: EntryFresh, Fresh: FreshData{FirstKeptEntryID: firstKeptID, TokensBefore: 100_000, TokensAfter: 20_000}}
}

func roles(msgs []core.AgentMessage) string {
	var out []string
	for _, m := range msgs {
		text := ""
		for _, c := range m.Content {
			text += c.Text
		}
		out = append(out, m.Role+":"+text)
	}
	return strings.Join(out, " | ")
}

// The model's context starts at the cut with no summary in front; the display
// keeps everything and draws the marker right before the first kept message.
func TestFresh_ContextStartsAtCutDisplayKeepsAll(t *testing.T) {
	tree := NewTree()
	tree.Append(userEntry("old question"))
	tree.Append(assistantEntry("old answer"))
	kept := tree.Append(userEntry("recent question"))
	tree.Append(assistantEntry("recent answer"))
	freshID := tree.Append(freshEntry(kept))

	msgs, epoch := tree.BuildContext()
	if got := roles(msgs); got != "user:recent question | assistant:recent answer" {
		t.Fatalf("context = %s", got)
	}
	if epoch != 1 {
		t.Fatalf("epoch = %d, want 1", epoch)
	}

	shown := tree.AllMessages()
	if len(shown) != 5 {
		t.Fatalf("display has %d rows, want 4 messages + marker: %s", len(shown), roles(shown))
	}
	if shown[2].MsgID != freshID || shown[2].Custom["type"] != "fresh_marker" || shown[2].Custom["first_kept_msg_id"] != kept {
		t.Fatalf("row 2 = %+v, want the fresh marker right before the cut", shown[2])
	}
	if shown[3].MsgID != kept {
		t.Fatalf("row 3 = %s, want the first kept message", shown[3].MsgID)
	}

	// A resume suffix that no longer contains the cut still carries the
	// marker, at its own position, so a client can place it.
	since, ok := tree.DisplayMessagesSince(kept)
	if !ok || len(since) != 2 || since[1].Custom["type"] != "fresh_marker" {
		t.Fatalf("suffix = %s", roles(since))
	}
}

// Whichever boundary came last wins: a compaction after a cut summarizes from
// the cut on; a cut after a compaction drops the summary too.
func TestFresh_InteractsWithCompaction(t *testing.T) {
	tree := NewTree()
	tree.Append(userEntry("a"))
	tree.Append(assistantEntry("a'"))
	b := tree.Append(userEntry("b"))
	tree.Append(assistantEntry("b'"))
	tree.Append(freshEntry(b))
	c := tree.Append(userEntry("c"))
	tree.Append(assistantEntry("c'"))
	tree.Append(compactionEntry("summary of b", c, 50_000))

	msgs, epoch := tree.BuildContext()
	if got := roles(msgs); got != "compaction_summary:summary of b | user:c | assistant:c'" {
		t.Fatalf("compaction after cut: context = %s", got)
	}
	if epoch != 2 {
		t.Fatalf("epoch = %d, want 2", epoch)
	}

	d := tree.Append(userEntry("d"))
	tree.Append(assistantEntry("d'"))
	tree.Append(freshEntry(d))
	msgs, epoch = tree.BuildContext()
	if got := roles(msgs); got != "user:d | assistant:d'" {
		t.Fatalf("cut after compaction: context = %s", got)
	}
	if epoch != 3 {
		t.Fatalf("epoch = %d, want 3", epoch)
	}
}

// Branching back to before the cut leaves the cut off the path: the full
// context comes back.
func TestFresh_BranchBeforeCutRestoresFullContext(t *testing.T) {
	tree := NewTree()
	tree.Append(userEntry("a"))
	a2 := tree.Append(assistantEntry("a'"))
	b := tree.Append(userEntry("b"))
	tree.Append(assistantEntry("b'"))
	tree.Append(freshEntry(b))
	if err := tree.Branch(a2); err != nil {
		t.Fatal(err)
	}
	msgs, _ := tree.BuildContext()
	if got := roles(msgs); got != "user:a | assistant:a'" {
		t.Fatalf("context = %s", got)
	}
}

// Compatibility: a version that predates the entry keeps its type but drops
// its data when it rewrites the session. Such an entry is ignored and the full
// context comes back — the documented cost of a downgrade.
func TestFresh_EmptyEntryIsIgnored(t *testing.T) {
	tree := NewTree()
	tree.Append(userEntry("a"))
	tree.Append(assistantEntry("a'"))
	tree.Append(Entry{Type: EntryFresh})
	msgs, epoch := tree.BuildContext()
	if got := roles(msgs); got != "user:a | assistant:a'" || epoch != 0 {
		t.Fatalf("context = %s, epoch %d", got, epoch)
	}
	for _, m := range tree.AllMessages() {
		if m.Role == "session_event" {
			t.Fatal("empty fresh entry drew a marker")
		}
	}
}

// The cut survives the real file store.
func TestFresh_SurvivesFileStoreRoundTrip(t *testing.T) {
	store, err := NewFileStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	tree := NewTree()
	tree.Append(userEntry("a"))
	tree.Append(assistantEntry("a'"))
	b := tree.Append(userEntry("b"))
	tree.Append(assistantEntry("b'"))
	tree.Append(freshEntry(b))

	sess := store.Create()
	sess.Entries, sess.LeafID = tree.Snapshot()
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewTreeFromEntries(loaded.Entries, loaded.LeafID)
	if err != nil {
		t.Fatal(err)
	}
	msgs, epoch := reloaded.BuildContext()
	if got := roles(msgs); got != "user:b | assistant:b'" || epoch != 1 {
		t.Fatalf("reloaded context = %s, epoch %d", got, epoch)
	}

	// What an older build does on rewrite: unknown fields are dropped.
	raw, _ := json.Marshal(loaded.Entries[len(loaded.Entries)-1])
	var generic map[string]any
	_ = json.Unmarshal(raw, &generic)
	delete(generic, "fresh")
	raw, _ = json.Marshal(generic)
	var stripped Entry
	if err := json.Unmarshal(raw, &stripped); err != nil {
		t.Fatal(err)
	}
	if !stripped.Fresh.IsEmpty() {
		t.Fatal("stripped entry still has data")
	}
}

func TestFresh_SnapshotSaysTheModelStoppedSeeingAbove(t *testing.T) {
	tree := NewTree()
	tree.Append(userEntry("a"))
	b := tree.Append(userEntry("b"))
	tree.Append(freshEntry(b))
	text := FormatTranscript(tree.Path())
	if !strings.Contains(text, "started fresh") {
		t.Fatalf("snapshot = %s", text)
	}
}
