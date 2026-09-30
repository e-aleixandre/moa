package session

import (
	"reflect"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// The fixture's provider reports exact chars/4 counts for the old, polluted
// prefix. That anchor is valid only while the same prefix reaches the model.
// Recovery must not keep treating it as current after removing summary copies.
func TestCompactionReview_DedupInvalidatesUsageOfChangedPrefix(t *testing.T) {
	tree := NewTree()
	const keptText = "retained question"
	summary := strings.Repeat("s", 40_000)
	kept := tree.Append(userEntry(keptText))
	tree.Append(compactionEntry(summary, kept, 50_000))
	tree.Append(summaryCopyEntry(summary))
	after := userEntry("question after the old copied summary")
	tree.Append(after)
	reply := assistantEntry("answer")
	canonical := summaryCopyEntry(summary)
	// Include both summaries: this is the request the older binary sent.
	total := core.EstimateTokens(canonical.Message.Message)*2 +
		core.EstimateTokens(userEntry(keptText).Message.Message) +
		core.EstimateTokens(after.Message.Message) +
		core.EstimateTokens(reply.Message.Message)
	reply.Message.Usage = &core.Usage{TotalTokens: total}
	reply.Message.Custom = map[string]any{"compaction_epoch": 1}
	tree.Append(reply)
	entriesBefore, leaf := tree.Snapshot()

	msgs, epoch := tree.BuildContext()
	expected := 0
	for _, msg := range msgs {
		expected += core.EstimateTokens(msg.Message)
	}
	estimate := core.EstimateContextTokens(msgs, "", nil, epoch)
	if estimate.UsageTokens != 0 || epoch != 1 {
		t.Fatalf("changed-prefix anchor or epoch retained: estimate %+v, epoch %d", estimate, epoch)
	}
	if estimate.Tokens != expected {
		t.Fatalf("projection has %d summaries and estimates %d tokens, but retains old-prefix usage %d (epoch %d), producing %d tokens",
			compactionSummaries(msgs), expected, estimate.UsageTokens, epoch, estimate.Tokens)
	}
	// Invalidation is a context projection, not a rewrite of historical billing.
	entry, ok := tree.Entry(leaf)
	if !ok || entry.Message.Usage == nil || entry.Message.Usage.TotalTokens != total {
		t.Fatal("recovery changed the historical provider usage")
	}
	entriesAfter, leafAfter := tree.Snapshot()
	if leafAfter != leaf || !reflect.DeepEqual(entriesBefore, entriesAfter) {
		t.Fatal("recovery mutated the persisted tree")
	}
}

func usageTestTree(withCopy bool) (tree *Tree, reply1Total int) {
	tree = NewTree()
	summary := strings.Repeat("s", 4_000)
	kept := tree.Append(userEntry("retained question"))
	tree.Append(compactionEntry(summary, kept, 5_000))
	r1 := assistantEntry("first answer")
	r1.Message.Usage = &core.Usage{TotalTokens: 1234}
	r1.Message.Custom = map[string]any{"compaction_epoch": 1}
	tree.Append(r1)
	if withCopy {
		tree.Append(summaryCopyEntry(summary))
	}
	tree.Append(userEntry("next question"))
	return tree, 1234
}

// No copy removed: anchors are untouched.
func TestCompactionReview_NoRemovalKeepsUsageAnchor(t *testing.T) {
	tree, total := usageTestTree(false)
	msgs, epoch := tree.BuildContext()
	est := core.EstimateContextTokens(msgs, "", nil, epoch)
	if est.UsageTokens != total {
		t.Fatalf("anchor lost without removal: %+v", est)
	}
}

// An anchor before the removed copy still describes the projected prefix.
func TestCompactionReview_AnchorBeforeRemovedCopyStaysValid(t *testing.T) {
	tree, total := usageTestTree(true)
	msgs, epoch := tree.BuildContext()
	if compactionSummaries(msgs) != 1 {
		t.Fatalf("copy not removed: %d summaries", compactionSummaries(msgs))
	}
	est := core.EstimateContextTokens(msgs, "", nil, epoch)
	if est.UsageTokens != total || est.TrailingTokens != core.EstimateTokens(userEntry("next question").Message.Message) {
		t.Fatalf("earlier anchor or trailing estimate changed: %+v", est)
	}
}

// Clearing anchors is a projection detail: stored history, display and cost
// keep the provider's numbers.
func TestCompactionReview_ProjectionKeepsHistoricalUsage(t *testing.T) {
	tree := NewTree()
	summary := "sum"
	kept := tree.Append(userEntry("q"))
	tree.Append(compactionEntry(summary, kept, 10))
	tree.Append(summaryCopyEntry(summary))
	r := assistantEntry("a")
	r.Message.Usage = &core.Usage{TotalTokens: 99, Input: 40, CacheRead: 30, CacheWrite: 5}
	r.Message.Custom = map[string]any{"compaction_epoch": 1}
	tree.Append(r)
	before, leaf := tree.Snapshot()
	displayBefore := tree.AllMessages()
	cacheBefore := core.SummarizeCacheUsage(displayBefore)
	model, ok := core.ResolveModel("luna")
	if !ok || model.Pricing == nil {
		t.Fatal("fixture model has no pricing")
	}
	costBefore := model.Pricing.Cost(*displayBefore[len(displayBefore)-1].Usage)
	msgs, epoch := tree.BuildContext()
	for _, m := range msgs {
		if m.Role == "assistant" && m.Usage != nil {
			t.Fatal("downstream anchor not cleared in projection")
		}
	}
	after, leaf2 := tree.Snapshot()
	if epoch != 1 || leaf != leaf2 || !reflect.DeepEqual(before, after) {
		t.Fatal("tree or boundary epoch changed")
	}
	displayAfter := tree.AllMessages()
	if !reflect.DeepEqual(displayBefore, displayAfter) {
		t.Fatal("display lost historical usage")
	}
	if !reflect.DeepEqual(cacheBefore, core.SummarizeCacheUsage(displayAfter)) {
		t.Fatal("historical cache totals changed")
	}
	if costBefore != model.Pricing.Cost(*displayAfter[len(displayAfter)-1].Usage) {
		t.Fatal("historical billing changed")
	}
}
