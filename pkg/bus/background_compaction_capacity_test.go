package bus

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/core"
)

// A pending P cannot absorb later tool output. Even after P's cut is durable,
// the next request still has to fit the hard model capacity.
func TestBackgroundCompactionReview_PostAdoptionRechecksHard(t *testing.T) {
	f := newBGSoftBelowHard(t, func(call int, req core.Request) *core.Message {
		if call == 1 {
			return &core.Message{Role: "assistant", Content: []core.Content{
				core.ToolCallContent("growth", "bgnoop", map[string]any{"grow": true}),
				core.ToolCallContent("hold", "bgnoop", map[string]any{"gate": true}),
			}, StopReason: "tool_use", Timestamp: time.Now().Unix()}
		}
		return nil
	})
	f.growLen.Store(440000)
	f.send(t, "go")
	first := f.request(t, "ordinary soft request")
	if got := bgEstimate(f, first.req); got > 99000 {
		t.Fatalf("bad fixture: first request %d > hard 99000", got)
	}
	bgWaitClosed(t, f.toolStarted, "the tool batch did not reach its gate")
	f.waitSummaryEntered(t)
	f.sum.open()
	f.ag.WaitBackgroundCompaction() // ready while the originating loop is held
	f.openTool()
	select {
	case r := <-f.prov.reqs:
		if got := bgEstimate(f, r.req); got > 99000 {
			t.Fatalf("post-adoption request exceeded hard: estimate=%d hard=99000 durable=%v hasSummary=%v", got, r.durable, bgHasSummary(r.req))
		}
	case e := <-f.ended:
		if e.Err != nil && !errors.Is(e.Err, agent.ErrContextCapacity) {
			t.Fatalf("unexpected safe-stop error: %v", e.Err)
		}
	case <-time.After(bgWait):
		t.Fatal("neither a capacity-safe request nor a safe stop arrived")
	}
}

func TestBackgroundCompactionReview_NoCutOverHardStops(t *testing.T) {
	f := newBGFix(t, bgCfg{window: 100000, compactAt: 40000, reserve: 1000, keep: 8000,
		initial: []core.AgentMessage{bgUser("uncuttable ", 440000)}})
	f.send(t, "go")
	select {
	case r := <-f.prov.reqs:
		t.Fatalf("no-cut context was sent over hard: estimate=%d hard=99000", bgEstimate(f, r.req))
	case e := <-f.ended:
		if !errors.Is(e.Err, agent.ErrContextCapacity) {
			t.Fatalf("no-cut over-hard run error=%v, want ErrContextCapacity", e.Err)
		}
	case <-time.After(bgWait):
		t.Fatal("uncuttable over-hard run never stopped")
	}
}

// All three continuation paths are pinned: exceeding hard must stop instead
// of either resubmitting oversized input or adopting the pending summary.
func TestBackgroundCompactionReview_PinnedContinuationStopsOverHard(t *testing.T) {
	for _, reason := range []string{"pause_turn", "continue", "max_tokens"} {
		t.Run(reason, func(t *testing.T) {
			f := newBGSoftBelowHard(t, func(call int, req core.Request) *core.Message {
				if call == 1 {
					content := core.TextContent(strings.Repeat("x", 440000))
					if reason == "max_tokens" {
						content = core.ThinkingContent(strings.Repeat("x", 440000))
					}
					return &core.Message{Role: "assistant", Content: []core.Content{content}, StopReason: reason, Timestamp: time.Now().Unix()}
				}
				return nil
			})
			f.send(t, "go")
			f.request(t, "first request")
			e := f.waitEnded(t, "pinned over-hard continuation")
			if !errors.Is(e.Err, agent.ErrContextCapacity) {
				t.Fatalf("pinned %s error=%v, want ErrContextCapacity", reason, e.Err)
			}
			if r, quiet := f.noRequest(); !quiet {
				t.Fatalf("pinned %s resubmitted oversized input: %d", reason, bgEstimate(f, r.req))
			}
			if f.durable.Load() {
				t.Fatal("pinned continuation was compacted")
			}
		})
	}
}
