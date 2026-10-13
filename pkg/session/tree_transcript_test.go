package session

import (
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// The transcript a history search reads is the current branch with every
// original message, the ones a compaction or a fresh start keeps away from
// the model included, plus a marker where each boundary sits.
func TestVisitTranscript_OriginalsAndBoundariesOfTheCurrentBranch(t *testing.T) {
	tree := NewTree()
	tree.Append(userEntry("old question"))
	fork := tree.Append(assistantEntry("old answer"))
	kept := tree.Append(userEntry("recent question"))
	tree.Append(Entry{Type: EntryCompaction, Compaction: CompactionData{Summary: "sum", FirstKeptEntryID: kept, TokensBefore: 300_000}})
	tree.Append(assistantEntry("recent answer"))
	tree.Append(freshEntry(kept))

	var got []core.AgentMessage
	tree.VisitTranscript(func(m *core.AgentMessage) { got = append(got, *m) })
	want := "user:old question | assistant:old answer | user:recent question | session_event:✂ Context compacted (300K tokens summarized) | assistant:recent answer | session_event:" + FreshMarkerText
	if r := roles(got); r != want {
		t.Fatalf("transcript =\n%s\nwant\n%s", r, want)
	}

	// An abandoned branch is not part of it.
	if err := tree.Branch(fork); err != nil {
		t.Fatal(err)
	}
	tree.Append(userEntry("edited question"))
	got = nil
	tree.VisitTranscript(func(m *core.AgentMessage) { got = append(got, *m) })
	if r := roles(got); r != "user:old question | assistant:old answer | user:edited question" {
		t.Fatalf("after branch = %s", r)
	}
}
