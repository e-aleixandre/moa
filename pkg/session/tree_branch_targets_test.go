package session

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// legacyBranchTargets is the algorithm BranchTargets replaced: one
// ValidBranchTarget (a full path copy and context rebuild) and one Children
// scan per entry. It is the oracle the one-pass version must agree with.
func legacyBranchTargets(t *Tree) []BranchTarget {
	var out []BranchTarget
	for _, e := range t.Entries() {
		if e.Type != EntryMessage {
			continue
		}
		if err := t.ValidBranchTarget(e.ID); err != nil {
			continue
		}
		out = append(out, BranchTarget{Entry: e, Children: len(t.Children(e.ID))})
	}
	return out
}

func assertSameTargets(t *testing.T, label string, tree *Tree) {
	t.Helper()
	want := legacyBranchTargets(tree)
	got, err := tree.BranchTargets(context.Background())
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	if len(got) != len(want) {
		t.Fatalf("%s: %d targets, legacy %d", label, len(got), len(want))
	}
	for i := range want {
		if got[i].Entry.ID != want[i].Entry.ID || got[i].Children != want[i].Children {
			t.Fatalf("%s: target %d = %s/%d, legacy %s/%d", label, i,
				got[i].Entry.ID, got[i].Children, want[i].Entry.ID, want[i].Children)
		}
	}
}

// randomTree builds a log with branches, forests, repeated and orphaned call
// IDs, results that precede their call, compactions and fresh cuts whose
// kept entry lies above, below, on another branch or nowhere, and trims.
func randomTree(rng *rand.Rand, n int) *Tree {
	callPool := 1 + rng.Intn(6)
	callID := func() string { return fmt.Sprintf("c%d", rng.Intn(callPool)) }
	entries := make([]Entry, 0, n)
	id := func(i int) string { return fmt.Sprintf("e%d", i) }
	for i := 0; i < n; i++ {
		e := Entry{ID: id(i), Timestamp: time.Unix(int64(i), 0)}
		switch {
		case i == 0 || rng.Intn(25) == 0:
		case rng.Intn(3) == 0:
			e.ParentID = id(i - 1)
		default:
			e.ParentID = id(rng.Intn(i))
		}
		kept := func() string {
			switch rng.Intn(6) {
			case 0:
				return ""
			case 1:
				return "nowhere"
			case 2:
				return id(rng.Intn(n))
			default:
				return id(rng.Intn(i + 1))
			}
		}
		switch r := rng.Intn(20); {
		case r < 5:
			e.Type = EntryMessage
			e.Message = core.AgentMessage{Message: core.NewUserMessage("u")}
		case r < 10:
			e.Type = EntryMessage
			content := []core.Content{core.TextContent("a")}
			for k := rng.Intn(4); k > 0; k-- {
				content = append(content, core.ToolCallContent(callID(), "bash", nil))
			}
			if rng.Intn(10) == 0 {
				content = append(content, core.ToolCallContent("", "bash", nil))
			}
			e.Message = core.AgentMessage{Message: core.Message{Role: "assistant", Content: content}}
		case r < 15:
			e.Type = EntryMessage
			e.Message = core.AgentMessage{Message: core.NewToolResultMessage(callID(), "bash", []core.Content{core.TextContent("r")}, false)}
		case r < 16:
			e.Type = EntryMessage
			e.Message = core.AgentMessage{Message: core.Message{Role: "compaction_summary", Content: []core.Content{core.TextContent("s")}}}
		case r < 17:
			e.Type = EntryCompaction
			e.Compaction = CompactionData{Summary: "sum", FirstKeptEntryID: kept()}
		case r < 18:
			e.Type = EntryFresh
			e.Fresh = FreshData{FirstKeptEntryID: kept()}
		case r < 19:
			e.Type = EntryTrim
			e.Trim = TrimData{WatermarkEntryID: id(rng.Intn(i + 1))}
		default:
			e.Type = EntryConfig
		}
		entries = append(entries, e)
	}
	index := make(map[string]int, n)
	for i, e := range entries {
		index[e.ID] = i
	}
	return &Tree{entries: entries, index: index, leafID: id(n - 1)}
}

func TestBranchTargets_MatchesPerEntryValidation(t *testing.T) {
	for seed := int64(0); seed < 3000; seed++ {
		rng := rand.New(rand.NewSource(seed))
		tree := randomTree(rng, 1+rng.Intn(60))
		assertSameTargets(t, fmt.Sprintf("seed %d", seed), tree)
		if t.Failed() {
			return
		}
	}
}

func TestBranchTargets_MatchesOnHandBuiltTree(t *testing.T) {
	tree := NewTree()
	tree.Append(userEntry("start"))
	tree.Append(assistantToolCallEntry("tc-old"))
	keepID := tree.Append(userEntry("kept"))
	tree.Append(compactionEntry("summary", keepID, 5000))
	tree.Append(assistantToolCallEntry("tc-new"))
	tree.Append(toolResultEntry("tc-new", "bash", "ok"))
	branchPoint := tree.Append(assistantEntry("answer"))
	tree.Append(assistantEntry("tail"))
	if err := tree.Branch(branchPoint); err != nil {
		t.Fatal(err)
	}
	tree.Append(assistantToolCallEntry("tc-dangling"))
	tree.Append(userEntry("after"))
	tree.Append(freshEntry(branchPoint))
	tree.Append(assistantEntry("post-fresh"))
	assertSameTargets(t, "hand built", tree)

	targets, err := tree.BranchTargets(context.Background())
	if err != nil || len(targets) == 0 {
		t.Fatalf("expected targets, got %d, %v", len(targets), err)
	}
}

func TestBranchTargets_EmptyTree(t *testing.T) {
	got, err := NewTree().BranchTargets(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("empty tree: %v, %v", got, err)
	}
}

func TestBranchTargets_StopsWhenCancelled(t *testing.T) {
	tree := NewTree()
	for i := 0; i < 5*branchTargetCtxEvery; i++ {
		tree.Append(userEntry("m"))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tree.BranchTargets(ctx); err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// The per-entry algorithm copied the path to the root for every entry; on a
// deep chain that took minutes at the sizes real owner sessions reach.
func TestBranchTargets_ScalesLinearly(t *testing.T) {
	tree := NewTree()
	for i := 0; i < 40000; i++ {
		if i%2 == 0 {
			tree.Append(userEntry("m"))
		} else {
			tree.Append(assistantEntry("a"))
		}
	}
	start := time.Now()
	got, err := tree.BranchTargets(context.Background())
	if err != nil || len(got) != 40000 {
		t.Fatalf("got %d targets, %v", len(got), err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %s for 40000 entries", d)
	}
}
