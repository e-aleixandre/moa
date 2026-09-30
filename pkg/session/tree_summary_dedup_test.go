package session

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// summaryCopyEntry is what a reopened session persisted before the rebuilt
// summary had a stable identity: an ordinary message entry carrying the
// boundary's summary text.
func summaryCopyEntry(text string) Entry {
	return Entry{
		Type: EntryMessage,
		Message: core.AgentMessage{Message: core.Message{
			Role:    "compaction_summary",
			Content: []core.Content{core.TextContent(text)},
		}},
	}
}

func nonSummary(msgs []core.AgentMessage) []core.AgentMessage {
	var out []core.AgentMessage
	for _, m := range msgs {
		if m.Role != "compaction_summary" {
			out = append(out, m)
		}
	}
	return out
}

// The rebuilt summary carries its boundary's entry ID, which the tree already
// holds; a fresh random ID on every rebuild is what the syncer mistook for a
// new message.
func TestBuildContext_SummaryTakesBoundaryID(t *testing.T) {
	tree := NewTree()
	tree.Append(userEntry("old"))
	kept := tree.Append(userEntry("kept"))
	boundary := tree.Append(compactionEntry("summary", kept, 10_000))

	msgs, _ := tree.BuildContext()
	if msgs[0].Role != "compaction_summary" || msgs[0].MsgID != boundary {
		t.Fatalf("summary = %+v, want MsgID %q", msgs[0].Message, boundary)
	}
	again, _ := tree.BuildContext()
	if again[0].MsgID != msgs[0].MsgID {
		t.Fatal("rebuilt summary identity is not stable")
	}
}

// Real sessions hold chains of 2 to 8 copies, straight after the retained
// suffix (idle reopen + close) or interleaved with turns (reopen + run). The
// model gets the summary once; every other message keeps its place.
func TestBuildContext_DropsRepeatedSummaryCopies(t *testing.T) {
	for n := 2; n <= 8; n++ {
		for _, interleaved := range []bool{false, true} {
			t.Run(fmt.Sprintf("copies=%d/interleaved=%v", n, interleaved), func(t *testing.T) {
				tree := NewTree()
				tree.Append(userEntry("old"))
				tree.Append(assistantEntry("old'"))
				kept := tree.Append(userEntry("kept"))
				tree.Append(assistantEntry("kept'"))
				tree.Append(compactionEntry("the summary", kept, 50_000))
				var want []string
				want = append(want, "compaction_summary:the summary", "user:kept", "assistant:kept'")
				for i := 0; i < n; i++ {
					tree.Append(summaryCopyEntry("the summary"))
					if interleaved {
						tree.Append(userEntry(fmt.Sprintf("q%d", i)))
						tree.Append(assistantEntry(fmt.Sprintf("a%d", i)))
						want = append(want, fmt.Sprintf("user:q%d", i), fmt.Sprintf("assistant:a%d", i))
					}
				}
				entriesBefore := tree.Entries()

				msgs, epoch := tree.BuildContext()
				if got := roles(msgs); got != joinRoles(want) {
					t.Fatalf("context = %s\nwant      %s", got, joinRoles(want))
				}
				if epoch != 1 {
					t.Fatalf("epoch = %d, want 1", epoch)
				}
				if !reflect.DeepEqual(tree.Entries(), entriesBefore) {
					t.Fatal("building the context changed the history")
				}
				if got := len(tree.AllMessages()); got != len(entriesBefore) {
					t.Fatalf("display rows = %d, want every entry (%d)", got, len(entriesBefore))
				}
			})
		}
	}
}

// A fresh cut emits no summary of its own. Copies in its retained suffix are
// the only summary the model has, so the first stays, in place.
func TestBuildContext_FreshKeepsFirstRetainedSummaryCopy(t *testing.T) {
	tree := NewTree()
	kept := tree.Append(userEntry("kept"))
	tree.Append(compactionEntry("the summary", kept, 50_000))
	cut := tree.Append(summaryCopyEntry("the summary"))
	tree.Append(userEntry("q"))
	tree.Append(summaryCopyEntry("the summary"))
	tree.Append(assistantEntry("a"))
	tree.Append(freshEntry(cut))

	msgs, epoch := tree.BuildContext()
	if got := roles(msgs); got != "compaction_summary:the summary | user:q | assistant:a" {
		t.Fatalf("context = %s", got)
	}
	if msgs[0].MsgID != cut {
		t.Fatalf("kept copy = %q, want the first one %q", msgs[0].MsgID, cut)
	}
	if epoch != 2 {
		t.Fatalf("epoch = %d, want 2", epoch)
	}
}

// Only byte-identical repeats of a compaction's summary go. A summary with
// other text, or one no compaction on the path produced, is left alone even
// when it repeats; a copy of an older compaction's summary kept in a newer
// suffix stays once, next to the newer canonical summary.
func TestBuildContext_KeepsSummariesThatAreNotRepeats(t *testing.T) {
	tree := NewTree()
	a := tree.Append(userEntry("a"))
	tree.Append(compactionEntry("first summary", a, 10_000))
	b := tree.Append(summaryCopyEntry("first summary"))
	tree.Append(summaryCopyEntry("first summary"))
	tree.Append(summaryCopyEntry("first summary with other text"))
	tree.Append(summaryCopyEntry("not from any compaction"))
	tree.Append(summaryCopyEntry("not from any compaction"))
	tree.Append(userEntry("c"))
	tree.Append(compactionEntry("second summary", b, 20_000))
	tree.Append(summaryCopyEntry("second summary"))
	tree.Append(summaryCopyEntry("first summary"))

	msgs, _ := tree.BuildContext()
	want := "compaction_summary:second summary | compaction_summary:first summary | " +
		"compaction_summary:first summary with other text | compaction_summary:not from any compaction | " +
		"compaction_summary:not from any compaction | user:c"
	if got := roles(msgs); got != want {
		t.Fatalf("context = %s\nwant      %s", got, want)
	}
}

// Copies before the retained suffix were already outside the context; the
// recovery does not change what the cut point selects.
func TestBuildContext_SummaryCopyBeforeCutStaysOut(t *testing.T) {
	tree := NewTree()
	tree.Append(userEntry("a"))
	tree.Append(summaryCopyEntry("the summary"))
	kept := tree.Append(userEntry("kept"))
	tree.Append(compactionEntry("the summary", kept, 10_000))

	msgs, _ := tree.BuildContext()
	if got := roles(msgs); got != "compaction_summary:the summary | user:kept" {
		t.Fatalf("context = %s", got)
	}
}

// Each branch is projected on its own path: copies on one branch do not affect
// the other, and switching back and forth yields the same contexts.
func TestBuildContext_SummaryCopiesPerBranch(t *testing.T) {
	tree := NewTree()
	kept := tree.Append(userEntry("kept"))
	boundary := tree.Append(compactionEntry("the summary", kept, 10_000))
	tree.Append(summaryCopyEntry("the summary"))
	tree.Append(summaryCopyEntry("the summary"))
	leafA := tree.Append(userEntry("on A"))
	if err := tree.Branch(boundary); err != nil {
		t.Fatal(err)
	}
	leafB := tree.Append(userEntry("on B"))

	for i := 0; i < 2; i++ {
		if err := tree.Branch(leafA); err != nil {
			t.Fatal(err)
		}
		msgs, _ := tree.BuildContext()
		if got := roles(msgs); got != "compaction_summary:the summary | user:kept | user:on A" {
			t.Fatalf("branch A context = %s", got)
		}
		if err := tree.Branch(leafB); err != nil {
			t.Fatal(err)
		}
		msgs, _ = tree.BuildContext()
		if got := roles(msgs); got != "compaction_summary:the summary | user:kept | user:on B" {
			t.Fatalf("branch B context = %s", got)
		}
	}
}

// Trims replay over the messages they were planned on, copies included, and
// only then are the copies dropped: the elision is the same with or without
// them.
func TestBuildContext_SummaryCopiesAfterTrims(t *testing.T) {
	big := make([]byte, 20_000)
	for i := range big {
		big[i] = 'x'
	}
	tree := NewTree()
	kept := tree.Append(userEntry("kept"))
	tree.Append(compactionEntry("the summary", kept, 10_000))
	tree.Append(summaryCopyEntry("the summary"))
	tree.Append(assistantToolCallEntry("c1"))
	tree.Append(toolResultEntry("c1", "bash", string(big)))
	watermark := tree.Append(userEntry("after"))
	tree.Append(Entry{Type: EntryTrim, Trim: TrimData{WatermarkEntryID: watermark, ProjectionVersion: core.TrimProjectionVersion}})

	msgs, _ := tree.BuildContext()
	if compactionSummaries(msgs) != 1 {
		t.Fatalf("context = %s, want one summary", roles(msgs))
	}
	rest := nonSummary(msgs)
	if len(rest) != 4 || rest[2].Role != "tool_result" || len(rest[2].Content[0].Text) >= len(big) {
		t.Fatalf("trim not replayed over the retained tool result: %s", roles(rest))
	}
}

func compactionSummaries(msgs []core.AgentMessage) int {
	n := 0
	for _, m := range msgs {
		if m.Role == "compaction_summary" {
			n++
		}
	}
	return n
}

func joinRoles(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " | "
		}
		out += p
	}
	return out
}
