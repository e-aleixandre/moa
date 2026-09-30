package session

import (
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func danglingCutMessage(id, role, text string) Entry {
	return Entry{Type: EntryMessage, Message: core.AgentMessage{Message: core.Message{MsgID: id, Role: role, Content: []core.Content{core.TextContent(text)}}}}
}

// danglingCutFixture is a compaction whose cut is target, followed by a
// tool pair, a trim, a persisted copy of the summary and two assistants whose
// Usage was measured against a prefix nobody can reconstruct.
func danglingCutFixture(target string) (*Tree, string) {
	tree := NewTree()
	tree.Append(danglingCutMessage("old", "user", "must not be resurrected"))
	boundary := tree.Append(Entry{Type: EntryCompaction, Compaction: CompactionData{Summary: "canonical summary", FirstKeptEntryID: target}})
	tree.Append(danglingCutMessage("post-user", "user", "continue"))
	call := danglingCutMessage("post-call", "assistant", "read now")
	call.Message.Content = append(call.Message.Content, core.ToolCallContent("call-1", "read", map[string]any{}))
	call.Message.Usage = &core.Usage{TotalTokens: 64718}
	call.Message.Custom = map[string]any{"compaction_epoch": 2}
	tree.Append(call)
	result := core.WrapMessage(core.NewToolResultMessage("call-1", "read", []core.Content{core.TextContent(strings.Repeat("synthetic output line\n", 400))}, false))
	result.MsgID = "post-result"
	tree.Append(Entry{Type: EntryMessage, Message: result})
	tree.Append(danglingCutMessage("watermark", "user", "after output"))
	tree.Append(Entry{Type: EntryTrim, Trim: TrimData{WatermarkEntryID: "watermark", ProjectionVersion: core.TrimProjectionVersion, Results: 1}})
	tree.Append(danglingCutMessage("duplicate-summary", "compaction_summary", "canonical summary"))
	answer := danglingCutMessage("post-answer", "assistant", "answer after duplicate")
	answer.Message.Usage = &core.Usage{TotalTokens: 70000}
	answer.Message.Custom = map[string]any{"compaction_epoch": 2}
	tree.Append(answer)
	tree.Append(danglingCutMessage("display-only", "session_event", "not model context"))
	return tree, boundary
}

func danglingCutIDs(messages []core.AgentMessage) []string {
	ids := make([]string, 0, len(messages))
	for _, m := range messages {
		ids = append(ids, m.MsgID)
	}
	return ids
}

// A compaction whose cut is empty or names no entry anywhere used to project
// the summary alone, dropping every later turn from the model's context while
// the transcript still showed them.
func TestBuildContext_DanglingCutRecoversPostBoundarySuffix(t *testing.T) {
	for _, target := range []string{"", "absent-whole-tree"} {
		t.Run("target="+target, func(t *testing.T) {
			tree, boundary := danglingCutFixture(target)
			before := tree.Entries()
			ctx, epoch := tree.BuildContext()
			wantIDs := []string{boundary, "post-user", "post-call", "post-result", "watermark", "post-answer"}
			if !reflect.DeepEqual(danglingCutIDs(ctx), wantIDs) {
				t.Errorf("context IDs=%v want canonical summary plus strictly post-boundary suffix %v", danglingCutIDs(ctx), wantIDs)
			}
			if epoch != 2 {
				t.Errorf("epoch=%d want 2 (compaction plus trim)", epoch)
			}
			if !strings.HasPrefix(contextText(ctx, "post-result"), "[Output elided") {
				t.Error("recovered suffix must replay the existing trim")
			}
			assistants := 0
			for _, m := range ctx {
				if m.Role == "assistant" {
					assistants++
					if m.Usage != nil {
						t.Errorf("recovered assistant %s kept a Usage anchor measured on an unknown prefix", m.MsgID)
					}
				}
			}
			if assistants != 2 {
				t.Errorf("recovered assistants=%d want 2", assistants)
			}
			if estimate := core.EstimateContextTokens(ctx, "", nil, epoch); estimate.UsageTokens != 0 {
				t.Errorf("recovered projection used a stale Usage anchor: %+v", estimate)
			}
			if !reflect.DeepEqual(before, tree.Entries()) {
				t.Error("recovery modified persisted messages, costs or boundaries")
			}
		})
	}
}

// Only a cut that exists nowhere is recovered. A target on a sibling branch is
// a different situation (the selected branch never held it) and keeps the
// current projection.
func TestBuildContext_DanglingCutOffPathTargetIsNotRecovered(t *testing.T) {
	tree := NewTree()
	root := tree.Append(danglingCutMessage("root", "user", "old branch"))
	boundary := tree.Append(Entry{Type: EntryCompaction, Compaction: CompactionData{Summary: "summary", FirstKeptEntryID: "future-retained"}})
	tree.Append(danglingCutMessage("future-retained", "user", "on original continuation"))
	if err := tree.Branch(boundary); err != nil {
		t.Fatal(err)
	}
	tree.Append(danglingCutMessage("other-branch", "user", "on sibling continuation"))
	ctx, epoch := tree.BuildContext()
	if !reflect.DeepEqual(danglingCutIDs(ctx), []string{boundary}) || epoch != 1 {
		t.Fatalf("off-path target changed the projection: IDs=%v epoch=%d", danglingCutIDs(ctx), epoch)
	}
	if err := tree.Branch(root); err != nil {
		t.Fatal(err)
	}
	ctx, epoch = tree.BuildContext()
	if !reflect.DeepEqual(danglingCutIDs(ctx), []string{root}) || epoch != 0 {
		t.Fatalf("branch before the boundary must stay uncut: IDs=%v epoch=%d", danglingCutIDs(ctx), epoch)
	}
}

func TestBuildContext_ExistingCutTargetsAreUnchanged(t *testing.T) {
	for _, target := range []string{"self-boundary", "descendant"} {
		t.Run(target, func(t *testing.T) {
			tree := NewTree()
			tree.Append(danglingCutMessage("old", "user", "old"))
			boundary := tree.Append(Entry{Type: EntryCompaction, Message: core.AgentMessage{Message: core.Message{MsgID: "self-boundary"}}, Compaction: CompactionData{Summary: "summary", FirstKeptEntryID: target}})
			reply := danglingCutMessage("descendant", "assistant", "retained after boundary")
			reply.Message.Usage = &core.Usage{TotalTokens: 10}
			tree.Append(reply)
			ctx, epoch := tree.BuildContext()
			if !reflect.DeepEqual(danglingCutIDs(ctx), []string{boundary, "descendant"}) || epoch != 1 {
				t.Fatalf("existing target activated the fallback: IDs=%v epoch=%d", danglingCutIDs(ctx), epoch)
			}
			if ctx[1].Usage == nil {
				t.Fatal("usage of a valid cut was invalidated")
			}
		})
	}
}

// Only the last boundary decides the context: a dangling cut already covered by
// a later valid compaction keeps today's projection.
func TestBuildContext_CoveredDanglingCutIsUnchanged(t *testing.T) {
	tree := NewTree()
	tree.Append(danglingCutMessage("old", "user", "old"))
	tree.Append(Entry{Type: EntryCompaction, Compaction: CompactionData{Summary: "first", FirstKeptEntryID: "missing"}})
	tree.Append(danglingCutMessage("between", "user", "summarised by the second compaction"))
	kept := tree.Append(danglingCutMessage("kept", "user", "kept by the second compaction"))
	second := tree.Append(Entry{Type: EntryCompaction, Compaction: CompactionData{Summary: "second", FirstKeptEntryID: kept}})
	tree.Append(danglingCutMessage("after", "user", "after"))
	ctx, epoch := tree.BuildContext()
	if !reflect.DeepEqual(danglingCutIDs(ctx), []string{second, "kept", "after"}) || epoch != 2 {
		t.Fatalf("covered dangling cut changed the projection: IDs=%v epoch=%d", danglingCutIDs(ctx), epoch)
	}
}

// Branch admission judges the same projection the model will get: a recovered
// suffix whose tool call has no result is not a valid branch point.
func TestBranch_DanglingCutAdmissionSeesRecoveredSuffix(t *testing.T) {
	tree := NewTree()
	tree.Append(danglingCutMessage("old", "user", "old"))
	tree.Append(Entry{Type: EntryCompaction, Compaction: CompactionData{Summary: "summary", FirstKeptEntryID: "missing"}})
	tree.Append(danglingCutMessage("post-user", "user", "go"))
	call := danglingCutMessage("post-call", "assistant", "calling")
	call.Message.Content = append(call.Message.Content, core.ToolCallContent("call-1", "read", map[string]any{}))
	callID := tree.Append(call)
	result := core.WrapMessage(core.NewToolResultMessage("call-1", "read", []core.Content{core.TextContent("ok")}, false))
	tree.Append(Entry{Type: EntryMessage, Message: result})
	if err := tree.ValidBranchTarget(callID); err == nil {
		t.Fatal("branching to a recovered tool call without its result was accepted")
	}
}

// The target's global presence and the path are read under the same lock:
// run with -race.
func TestBuildContext_DanglingCutConcurrentAppend(t *testing.T) {
	tree, _ := danglingCutFixture("absent-whole-tree")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			tree.Append(danglingCutMessage("", "user", "concurrent"))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			if ctx, _ := tree.BuildContext(); len(ctx) < 6 {
				t.Errorf("recovered context shrank to %d messages", len(ctx))
				return
			}
		}
	}()
	wg.Wait()
}
