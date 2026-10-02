package bus

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

func TestBackgroundCompactionReview_QuiescenceIncludesHeldSummary(t *testing.T) {
	f := bgIdleWithHeldSummary(t)
	if !f.ag.BackgroundCompaction().Active {
		t.Fatal("fixture: no held background job")
	}
	if got := f.rt.BackgroundWork(); got != 1 {
		t.Errorf("BackgroundWork=%d with one held background summary, want 1", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if f.rt.WaitQuiescent(ctx) {
		t.Error("WaitQuiescent returned true while a summary capable of saving/adopting is still held")
	}
	called := false
	if f.rt.DoIfQuiescent(func() { called = true }); called {
		t.Error("DoIfQuiescent executed while a background summary is still outstanding")
	}
	if f.rt.AdmitCloseIfQuiescent(func() {}) {
		t.Error("AdmitCloseIfQuiescent admitted automatic teardown while background compaction remains outstanding")
		f.rt.ReopenRunAdmission()
	}
}

func TestBackgroundCompactionReview_ActiveAcceptedSaveStopThenNewSend(t *testing.T) {
	f := newBGSoftBelowHard(t, func(call int, req core.Request) *core.Message {
		if call == 1 {
			return bgToolCall("hold", map[string]any{"gate": true}, "literal post-P")
		}
		return nil
	})
	f.send(t, "go")
	f.request(t, "first ordinary request")
	bgWaitClosed(t, f.toolStarted, "tool did not reach its gate")
	f.waitSummaryEntered(t)
	saving, hold := bgHoldSave(t, f, false)
	defer func() {
		select {
		case <-hold:
		default:
			close(hold)
		}
	}()
	f.sum.open()
	f.ag.WaitBackgroundCompaction()
	f.openTool()
	bgWaitClosed(t, saving, "active cut never entered accepted Save")

	if err := f.rt.Bus.Execute(SteerAgent{ID: "accepted-save-steer", Text: "recall exactly once"}); err != nil {
		t.Fatal(err)
	}
	var recalled []core.SteerItem
	stopped := make(chan error, 1)
	go func() { stopped <- f.rt.Bus.Execute(AbortAndRecall{RunGen: 1, DiscardedSteers: &recalled}) }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(bgWait):
		t.Fatal("Stop intent waited on the accepted save")
	}
	if len(recalled) != 1 || recalled[0].ID != "accepted-save-steer" {
		t.Fatalf("recalled=%v, want the queued steer exactly once", recalled)
	}
	// The first run still owns the slot until its accepted save settles.
	if err := f.rt.Bus.Execute(SendPrompt{Text: "not yet accepted"}); err == nil {
		t.Fatal("new prompt was reported accepted before the stopped run settled")
	}
	close(hold)
	f.waitCut(t)
	e := f.waitEnded(t, "stopped run after its accepted save")
	if e.Err != nil && !errors.Is(e.Err, context.Canceled) {
		t.Fatalf("unexpected stopped-run error: %v", e.Err)
	}
	if r, quiet := f.noRequest(); !quiet {
		t.Fatalf("cancelled originating run made another request: %v", bgShapesLLM(r.req.Messages))
	}
	want, _ := f.rt.Context().Tree.BuildContext()
	if !reviewSameContext(f.ag.Messages(), want) {
		t.Fatalf("tree and Agent after Stop differ:\nAgent %v\nTree %v", catomicMsgs(f.ag.Messages()), catomicMsgs(want))
	}
	f.send(t, "accepted after Stop")
	r := f.request(t, "new send after accepted cut and Stop")
	if !bgHasSummary(r.req) {
		t.Fatal("new run did not inherit the durable adopted context")
	}
	f.waitEnded(t, "new run")
	f.rt.Bus.Drain(bgWait)
	entries, leaf := f.savedRaw(t)
	tree, err := session.NewTreeFromEntries(entries, leaf)
	if err != nil {
		t.Fatal(err)
	}
	ctxMsgs, _ := tree.BuildContext()
	if !reviewSameContext(f.ag.Messages(), ctxMsgs) {
		t.Fatalf("new Send context differs:\nAgent %v\nSaved %v", catomicMsgs(f.ag.Messages()), catomicMsgs(ctxMsgs))
	}
	joined := ""
	for _, m := range ctxMsgs {
		for _, c := range m.Content {
			joined += c.Text + "\n"
		}
	}
	if strings.Count(joined, "accepted after Stop") != 1 || strings.Contains(joined, "not yet accepted") || strings.Contains(joined, "recall exactly once") {
		t.Fatal("accepted/rejected/recalled prompt identities were not preserved")
	}
}

func TestBackgroundCompactionReview_NoCutBelowOrAtHardContinues(t *testing.T) {
	for _, exact := range []bool{false, true} {
		name := "below_hard"
		if exact {
			name = "exact_hard"
		}
		t.Run(name, func(t *testing.T) {
			f := newBGFix(t, bgCfg{window: 100000, compactAt: 40000, reserve: 1000, keep: 8000, initial: []core.AgentMessage{bgUser("uncuttable ", 160000)}})
			model := f.ag.Model()
			if exact {
				p := append(append([]core.AgentMessage(nil), f.initial...), core.WrapMessage(core.NewUserMessage("go")))
				model.MaxInput = core.EstimateContextTokens(p, f.ag.SystemPrompt(), f.reg.Specs(), 0).Tokens + f.settings.ReserveTokens
				if err := f.ag.Reconfigure(nil, model, "", 40000); err != nil {
					t.Fatal(err)
				}
			}
			f.send(t, "go")
			r := f.request(t, "uncuttable context that fits hard")
			if got := bgEstimate(f, r.req); got > model.MaxInput-f.settings.ReserveTokens || (exact && got != model.MaxInput-f.settings.ReserveTokens) {
				t.Fatalf("bad hard threshold control: estimate=%d model=%d reserve=%d", got, model.MaxInput, f.settings.ReserveTokens)
			}
			if bgHasSummary(r.req) || f.sum.calls() != 0 {
				t.Fatal("no-cut context was unexpectedly summarized")
			}
			if e := f.waitEnded(t, "no-cut context below/at hard"); e.Err != nil {
				t.Fatal(e.Err)
			}
		})
	}
}

// Tree.BuildContext synthesizes its summary under the boundary ID by the
// existing STEP1 convention. Literal retained/post-P IDs must match exactly.
func reviewSameContext(a, b []core.AgentMessage) bool {
	x, y := catomicMsgs(a), catomicMsgs(b)
	if len(x) > 0 && len(y) > 0 && x[0].Role == "compaction_summary" && y[0].Role == "compaction_summary" {
		x[0].ID, y[0].ID = "", ""
	}
	return equalMsgs(x, y)
}

func TestBackgroundCompactionReview_StateIdleCallbackDoesNotInvertCut(t *testing.T) {
	f := bgIdleWithHeldSummary(t)
	saving, hold := bgHoldSave(t, f, false)
	defer func() {
		select {
		case <-hold:
		default:
			close(hold)
		}
	}()
	f.sum.open()
	bgWaitClosed(t, saving, "idle accepted save did not start")
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		var err error
		if !f.rt.State.DoIfIdle(func() {
			close(entered)
			err = f.ag.Reconfigure(nil, f.ag.Model(), "", 40000)
		}) {
			err = errors.New("idle callback was unexpectedly refused")
		}
		done <- err
	}()
	bgWaitClosed(t, entered, "idle callback never acquired State lock")
	close(hold)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(bgWait):
		t.Fatal("State lock / cut gate inversion prevented accepted save and idle callback from settling")
	}
	f.waitCut(t)
}
