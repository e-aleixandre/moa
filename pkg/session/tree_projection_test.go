package session

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// Legacy projection: the implementation before the path stopped being copied
// as Entry values. Kept here so the new one is compared against what it
// replaced, not against itself.
func legacyPathTo(t *Tree, id string) []Entry {
	var stack []Entry
	for id != "" {
		idx, ok := t.index[id]
		if !ok {
			break
		}
		stack = append(stack, t.entries[idx])
		id = t.entries[idx].ParentID
	}
	for i, j := 0, len(stack)-1; i < j; i, j = i+1, j-1 {
		stack[i], stack[j] = stack[j], stack[i]
	}
	return stack
}

func legacyAllMessages(t *Tree) []core.AgentMessage {
	t.mu.RLock()
	var path []Entry
	if t.leafID != "" {
		path = legacyPathTo(t, t.leafID)
	}
	t.mu.RUnlock()
	return legacyDisplayMessages(path)
}

func legacyDisplayMessagesSince(t *Tree, entryID string) ([]core.AgentMessage, bool) {
	if entryID == "" {
		return nil, false
	}
	t.mu.RLock()
	if t.leafID == "" {
		t.mu.RUnlock()
		return nil, false
	}
	path := legacyPathTo(t, t.leafID)
	t.mu.RUnlock()
	for i, entry := range path {
		if entry.ID == entryID {
			return legacyDisplayMessages(path[i+1:]), true
		}
	}
	return nil, false
}

func legacyDisplayMessages(entries []Entry) []core.AgentMessage {
	// A fresh marker is drawn at the cut, right before the first message the
	// model still sees, not where the entry was appended: the line has to say
	// "the model's context starts here". When the cut point is not in this
	// slice (a resume suffix), it falls back to the entry's own position.
	freshAt := map[string][]Entry{}
	present := map[string]bool{}
	for _, e := range entries {
		if e.Type == EntryMessage {
			present[e.ID] = true
		}
	}
	for _, e := range entries {
		if e.Type == EntryFresh && !e.Fresh.IsEmpty() && present[e.Fresh.FirstKeptEntryID] {
			freshAt[e.Fresh.FirstKeptEntryID] = append(freshAt[e.Fresh.FirstKeptEntryID], e)
		}
	}
	var msgs []core.AgentMessage
	for _, e := range entries {
		switch e.Type {
		case EntryMessage:
			for _, f := range freshAt[e.ID] {
				msgs = append(msgs, freshMarker(f))
			}
			msgs = append(msgs, e.Message)
		case EntryCompaction:
			text := fmt.Sprintf("✂ Context compacted (%dK tokens summarized)", e.Compaction.TokensBefore/1000)
			msgs = append(msgs, core.AgentMessage{
				Message: core.Message{
					Role:      "session_event",
					MsgID:     e.ID,
					Content:   []core.Content{core.TextContent(text)},
					Timestamp: e.Timestamp.Unix(),
				},
				Custom: map[string]any{"type": "compaction_marker", "summary": e.Compaction.Summary, "tokens_before": e.Compaction.TokensBefore, "read_files": append([]string(nil), e.Compaction.ReadFiles...), "modified_files": append([]string(nil), e.Compaction.ModifiedFiles...)},
			})
		case EntryTrim:
			// The transcript keeps the original outputs: the reader can still
			// inspect what happened, and the marker is what makes explicit
			// that the model no longer sees them.
			msgs = append(msgs, core.AgentMessage{
				Message: core.Message{
					Role:      "session_event",
					MsgID:     e.ID,
					Content:   []core.Content{core.TextContent(TrimMarkerText(e.Trim.TokensRemoved))},
					Timestamp: e.Timestamp.Unix(),
				},
				Custom: map[string]any{"type": "trim_marker", "results": e.Trim.Results, "tokens_removed": e.Trim.TokensRemoved},
			})
		case EntryFresh:
			if e.Fresh.IsEmpty() || present[e.Fresh.FirstKeptEntryID] {
				continue
			}
			msgs = append(msgs, freshMarker(e))
		}
	}
	return msgs
}

// bigFixtureTree builds a long session: text turns with usage, compactions,
// trims, empty and effective fresh cuts, config/label entries, and an
// abandoned branch so the current path is not the whole log.
func bigFixtureTree(tb testing.TB, n int) *Tree {
	tb.Helper()
	tree := NewTree()
	var ids []string
	branchPoint := ""
	for i := 0; len(ids) < n; i++ {
		u := userEntry(fmt.Sprintf("question %d", i))
		ids = append(ids, tree.Append(u))
		a := assistantEntry(fmt.Sprintf("answer %d", i))
		a.Message.Usage = &core.Usage{Input: 100 + i%7, CacheRead: (i % 5) * 900, CacheWrite: (i % 3) * 400}
		if i%50 == 7 {
			a.Message.StopReason = "error"
		}
		aid := tree.Append(a)
		ids = append(ids, aid)
		if i == n/6 {
			branchPoint = aid
		}
		switch {
		case i%400 == 399:
			tree.Append(compactionEntry("summary", ids[len(ids)-20], 50_000+i))
		case i%500 == 250:
			tree.Append(Entry{Type: EntryTrim, Trim: TrimData{WatermarkEntryID: aid, ProjectionVersion: 1, TokensRemoved: 10, Results: 2}})
		case i%700 == 100:
			tree.Append(freshEntry(ids[len(ids)-10]))
		case i%900 == 300:
			tree.Append(Entry{Type: EntryFresh})
		case i%333 == 5:
			tree.Append(Entry{Type: EntryConfig, Config: ConfigChangeData{Model: "m"}})
		case i%444 == 6:
			tree.Append(Entry{Type: EntryLabel, Label: "l"})
		}
	}
	// Abandoned branch: fork off early, add a few entries, come back to the tip.
	tip := tree.LeafID()
	if err := tree.Branch(branchPoint); err != nil {
		tb.Fatal(err)
	}
	tree.Append(userEntry("abandoned"))
	tree.Append(assistantEntry("abandoned answer"))
	if err := tree.Branch(tip); err != nil {
		tb.Fatal(err)
	}
	return tree
}

func TestProjectionMatchesLegacyOnLargeSession(t *testing.T) {
	tree := bigFixtureTree(t, 15000)

	want := legacyAllMessages(tree)
	got := tree.AllMessages()
	if len(want) < 15000 {
		t.Fatalf("fixture too small: %d messages", len(want))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("AllMessages differs from the legacy projection")
	}

	// Cache aggregate: streamed over the tree == summarised over the projection.
	var acc core.CacheUsageAccumulator
	tree.VisitDisplayMessages(acc.Add)
	if g, w := acc.Summary(), core.SummarizeCacheUsage(want); !reflect.DeepEqual(g, w) {
		t.Fatalf("cache usage = %+v, want %+v", g, w)
	}

	// Every anchor class: early, mid, the last entry, the leaf's parent, unknown, off-path.
	path := tree.Path()
	if !reflect.DeepEqual(path, legacyPathTo(tree, tree.LeafID())) {
		t.Fatal("Path differs from the legacy path")
	}
	for _, id := range []string{path[0].ID, path[len(path)/2].ID, path[len(path)-2].ID, path[len(path)-1].ID, "nope", ""} {
		g, gok := tree.DisplayMessagesSince(id)
		w, wok := legacyDisplayMessagesSince(tree, id)
		if gok != wok || !reflect.DeepEqual(g, w) {
			t.Fatalf("DisplayMessagesSince(%q) = (%d,%v), want (%d,%v)", id, len(g), gok, len(w), wok)
		}
	}
}

func TestProjectionEmptyTreeIsNil(t *testing.T) {
	tree := NewTree()
	if msgs := tree.AllMessages(); msgs != nil {
		t.Fatalf("empty tree: %#v, want nil", msgs)
	}
	tree.Append(Entry{Type: EntryLabel, Label: "only a label"})
	if msgs := tree.AllMessages(); msgs != nil {
		t.Fatalf("no display rows: %#v, want nil", msgs)
	}
}

func BenchmarkInitProjection(b *testing.B) {
	tree := bigFixtureTree(b, 15000)
	b.Run("legacy", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			core.SummarizeCacheUsage(legacyAllMessages(tree)) // GetCacheUsage
			legacyAllMessages(tree)                           // GetDisplayMessages
		}
	})
	b.Run("current", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			var acc core.CacheUsageAccumulator
			tree.VisitDisplayMessages(acc.Add) // GetCacheUsage
			_ = acc.Summary()
			tree.AllMessages() // GetDisplayMessages
		}
	})
}
