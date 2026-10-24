package compaction

import (
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func forYouSnap(text string) core.AgentMessage {
	m := core.WrapMessage(core.NewUserMessage(text))
	m.Custom = map[string]any{"source": core.SourceForYouSnapshot}
	return m
}

// The same ordinary conversation keeps the same first message however many
// For you snapshots ride in it: they are not charged to the kept suffix.
func TestFindCutPointDoesNotChargeSnapshotsToTheTail(t *testing.T) {
	settings := core.CompactionSettings{Enabled: true, ReserveTokens: 1000, KeepRecent: 2000}
	turn := func() []core.AgentMessage {
		return []core.AgentMessage{
			core.WrapMessage(core.NewUserMessage(strings.Repeat("u", 2000))),
			core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{core.TextContent(strings.Repeat("a", 2000))}}),
		}
	}
	// summarized counts the ordinary messages before the cut.
	summarized := func(snapsPerTurn int) int {
		var msgs []core.AgentMessage
		for i := 0; i < 10; i++ {
			msgs = append(msgs, turn()...)
			for j := 0; j < snapsPerTurn; j++ {
				msgs = append(msgs, forYouSnap(strings.Repeat("s", 3000)))
			}
		}
		n := 0
		for _, m := range msgs[:FindCutPoint(msgs, 0, 20_000, settings)] {
			if !core.IsForYouSnapshot(m) {
				n++
			}
		}
		return n
	}
	without, with := summarized(0), summarized(2)
	if without == 0 {
		t.Fatal("the fixture must cut something")
	}
	if without != with {
		t.Fatalf("ordinary messages summarized: %d without snapshots, %d with", without, with)
	}
}

// The summarizer never reads a snapshot as words of the user.
func TestSerializeForSummarySkipsSnapshots(t *testing.T) {
	out := SerializeForSummary([]core.AgentMessage{
		core.WrapMessage(core.NewUserMessage("hello")),
		forYouSnap("<foryou_snapshot>Q1 open</foryou_snapshot>"),
	}, 0)
	if strings.Contains(out, "foryou_snapshot") || !strings.Contains(out, "hello") {
		t.Fatalf("serialized = %q", out)
	}
}

// A snapshot right after a large tool batch is not a cut boundary: cutting
// there would summarize the batch and keep only the snapshot.
func TestFindCutPointNeverCutsAtASnapshot(t *testing.T) {
	settings := core.CompactionSettings{Enabled: true, ReserveTokens: 1000, KeepRecent: 2000}
	msgs := []core.AgentMessage{
		core.WrapMessage(core.NewUserMessage(strings.Repeat("u", 4000))),
		core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{core.ToolCallContent("c1", "read", nil)}}),
		core.WrapMessage(core.Message{Role: "tool_result", ToolCallID: "c1", Content: []core.Content{core.TextContent(strings.Repeat("r", 20000))}}),
		forYouSnap("<foryou_snapshot>Q1</foryou_snapshot>"),
	}
	cut := FindCutPoint(msgs, 0, 20_000, settings)
	if cut != 1 {
		t.Fatalf("cut = %d (%s), want 1: keep the assistant with its result", cut, msgs[cut].Role)
	}
}
