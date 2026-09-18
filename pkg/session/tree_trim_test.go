package session

import (
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func trimEntry(watermarkID string, results, removed int) Entry {
	return Entry{
		Type: EntryTrim,
		Trim: TrimData{
			WatermarkEntryID:  watermarkID,
			ProjectionVersion: core.TrimProjectionVersion,
			TokensBefore:      300_000,
			TokensRemoved:     removed,
			Results:           results,
		},
	}
}

func contextText(msgs []core.AgentMessage, msgID string) string {
	for _, m := range msgs {
		if m.MsgID == msgID {
			var b strings.Builder
			for _, c := range m.Content {
				b.WriteString(c.Text)
			}
			return b.String()
		}
	}
	return ""
}

// The point of persisting the watermark: rebuilding the tree must hand the
// provider the same conversation it already saw. Restoring the originals would
// mean a cold prefix cache and a different context after every restart.
func TestBuildContext_TrimIsReplayedOnRebuild(t *testing.T) {
	big := strings.Repeat("output line\n", 400)
	tree := NewTree()
	tree.Append(userEntry("go"))
	tree.Append(assistantToolCallEntry("call-1"))
	r1 := tree.Append(toolResultEntry("call-1", "read", big))
	wm := tree.Append(userEntry("carry on"))
	tree.Append(trimEntry(wm, 1, 1200))

	msgs, epoch := tree.BuildContext()
	if epoch != 1 {
		t.Fatalf("epoch = %d, want 1: a trim invalidates anchored usage exactly as a compaction does", epoch)
	}
	got := contextText(msgs, r1)
	if got == big {
		t.Fatal("the trimmed result came back whole from the tree")
	}
	if !strings.HasPrefix(got, "[Output elided") {
		t.Fatalf("rebuilt context = %q, want the placeholder", got)
	}
}

// The message survives the elision precisely so this stays true: an unanswered
// tool_call is rejected by providers, and reloading one makes the tree inject a
// synthetic error result.
func TestBuildContext_TrimKeepsToolCallsAnswered(t *testing.T) {
	big := strings.Repeat("output line\n", 400)
	entries := []Entry{}
	tree := NewTree()
	tree.Append(userEntry("go"))
	tree.Append(assistantToolCallEntry("call-1"))
	tree.Append(toolResultEntry("call-1", "read", big))
	wm := tree.Append(userEntry("carry on"))
	tree.Append(trimEntry(wm, 1, 1200))
	entries, leaf := tree.Snapshot()

	reloaded, err := NewTreeFromEntries(entries, leaf)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	msgs, _ := reloaded.BuildContext()
	for _, m := range msgs {
		for _, c := range m.Content {
			if strings.Contains(c.Text, "Tool result unavailable") {
				t.Fatal("reload injected a synthetic error result: the trim broke a tool_call pair")
			}
		}
	}
}

// Trimming twice must leave the first region byte-identical. That is what keeps
// the provider's prefix cache warm across trims — the whole reason the
// watermark is monotonic rather than a set of message IDs.
func TestBuildContext_SuccessiveTrimsKeepEarlierRegionIdentical(t *testing.T) {
	big := strings.Repeat("output line\n", 400)
	tree := NewTree()
	tree.Append(userEntry("go"))
	tree.Append(assistantToolCallEntry("call-1"))
	r1 := tree.Append(toolResultEntry("call-1", "read", big))
	wm1 := tree.Append(userEntry("carry on"))
	tree.Append(trimEntry(wm1, 1, 1200))

	afterFirst, _ := tree.BuildContext()
	firstRegion := contextText(afterFirst, r1)

	tree.Append(assistantToolCallEntry("call-2"))
	r2 := tree.Append(toolResultEntry("call-2", "read", big))
	wm2 := tree.Append(userEntry("and again"))
	tree.Append(trimEntry(wm2, 1, 1200))

	afterSecond, _ := tree.BuildContext()
	if contextText(afterSecond, r1) != firstRegion {
		t.Fatal("the second trim rewrote the first region")
	}
	if !strings.HasPrefix(contextText(afterSecond, r2), "[Output elided") {
		t.Fatal("the second region was not elided")
	}
}

// A compaction keeps its suffix as it stood in memory — placeholders included.
// A rebuild that restored those originals would send the provider a context it
// has never seen, so trims stay applicable to whatever the compaction retained.
func TestBuildContext_TrimStillAppliesToACompactionSuffix(t *testing.T) {
	big := strings.Repeat("output line\n", 400)
	tree := NewTree()
	tree.Append(userEntry("go"))
	tree.Append(assistantToolCallEntry("call-1"))
	r1 := tree.Append(toolResultEntry("call-1", "read", big))
	wm := tree.Append(userEntry("carry on"))
	tree.Append(trimEntry(wm, 1, 1200))
	// The compaction retains a suffix that starts BEFORE the trimmed result.
	tree.Append(compactionEntry("summary", r1, 250_000))
	tree.Append(userEntry("after the compaction"))

	msgs, epoch := tree.BuildContext()
	if epoch != 2 {
		t.Fatalf("epoch = %d, want 2 (one trim + one compaction)", epoch)
	}
	if got := contextText(msgs, r1); got == big {
		t.Fatal("a compaction that retained a trimmed result restored its original")
	}
}

// Branching before a trim is how the user gets the full outputs back: the trim
// entry is not on the new path, so nothing projects it.
func TestBuildContext_BranchBeforeTrimRestoresOriginals(t *testing.T) {
	big := strings.Repeat("output line\n", 400)
	tree := NewTree()
	tree.Append(userEntry("go"))
	tree.Append(assistantToolCallEntry("call-1"))
	r1 := tree.Append(toolResultEntry("call-1", "read", big))
	wm := tree.Append(userEntry("carry on"))
	tree.Append(trimEntry(wm, 1, 1200))

	if err := tree.Branch(wm); err != nil {
		t.Fatalf("branch: %v", err)
	}
	msgs, epoch := tree.BuildContext()
	if epoch != 0 {
		t.Fatalf("epoch = %d, want 0 after branching before the trim", epoch)
	}
	if contextText(msgs, r1) != big {
		t.Fatal("branching before the trim did not restore the original output")
	}
	if got := tree.TrimWatermark(); got != "" {
		t.Fatalf("watermark = %q, want empty on a branch with no trim", got)
	}
}

// The watermark the agent carries has to come from the branch, not from
// whatever the last trim on any branch happened to be.
func TestTrimWatermarkFollowsThePath(t *testing.T) {
	tree := NewTree()
	tree.Append(userEntry("go"))
	wm1 := tree.Append(userEntry("one"))
	tree.Append(trimEntry(wm1, 1, 1200))
	wm2 := tree.Append(userEntry("two"))
	tree.Append(trimEntry(wm2, 1, 1200))

	if got := tree.TrimWatermark(); got != wm2 {
		t.Fatalf("watermark = %q, want the latest on the path (%q)", got, wm2)
	}
}

// Attachments must survive a trim untouched: the tree's copy is what keeps the
// blob reachable, and a non-textual result is not eligible in the first place.
func TestBuildContext_TrimLeavesAttachmentsReachable(t *testing.T) {
	shot := Entry{
		Type: EntryMessage,
		Message: core.AgentMessage{Message: core.NewToolResultMessage("call-i", "screenshot", []core.Content{
			core.TextContent(strings.Repeat("pixels\n", 400)),
			{Type: "image", AttachmentID: "att-1", MimeType: "image/png", AttachmentSize: 4096},
		}, false)},
	}
	tree := NewTree()
	tree.Append(userEntry("go"))
	tree.Append(assistantToolCallEntry("call-i"))
	shotID := tree.Append(shot)
	wm := tree.Append(userEntry("carry on"))
	tree.Append(trimEntry(wm, 1, 1200))

	msgs, _ := tree.BuildContext()
	for _, m := range msgs {
		if m.MsgID != shotID {
			continue
		}
		for _, c := range m.Content {
			if c.Type == "image" && c.AttachmentID == "att-1" {
				return
			}
		}
	}
	t.Fatal("the attachment reference did not survive the trim projection")
}

// The snapshot handed to a subagent as evidence keeps the original outputs, so
// it has to say the parent stopped seeing them — otherwise it reads as if the
// parent still had all of it in context.
func TestFormatTranscript_RecordsTheTrim(t *testing.T) {
	tree := NewTree()
	tree.Append(userEntry("go"))
	wm := tree.Append(userEntry("carry on"))
	tree.Append(trimEntry(wm, 4, 90_000))

	out := FormatTranscript(tree.Path())
	if !strings.Contains(out, "--- trim") {
		t.Fatalf("transcript has no trim heading:\n%s", out)
	}
	if !strings.Contains(out, "removed from the parent's model context") {
		t.Fatalf("transcript does not explain the trim:\n%s", out)
	}
}
