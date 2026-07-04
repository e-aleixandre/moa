package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ealeixandre/moa/pkg/agent"
	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/core"
)

// assertHasRenderTick fails unless one of cmds, when invoked, yields renderTickMsg.
func assertHasRenderTick(t *testing.T, cmds []tea.Cmd) {
	t.Helper()
	for _, c := range cmds {
		if c == nil {
			continue
		}
		if _, ok := c().(renderTickMsg); ok {
			return
		}
	}
	t.Fatal("expected a renderTick cmd among returned cmds")
}

func TestHandleGoalIterationEnded_SeedsRenderTick(t *testing.T) {
	m := newTestModel()
	// newTestModel is state-level only; statusBar isn't initialized there, but
	// handleGoalIterationEnded touches it when goalActive is set.
	m.statusBar = NewStatusLine(lipgloss.NewStyle())
	m.s.goalActive = true
	cmds := m.handleGoalIterationEnded(bus.GoalIterationEnded{Iteration: 1, Satisfied: false, Feedback: "keep going"})
	assertHasRenderTick(t, cmds)
	if !m.s.viewportDirty {
		t.Error("expected viewportDirty set")
	}
}

func TestHandleGoalEnded_SeedsRenderTick(t *testing.T) {
	m := newTestModel()
	m.statusBar = NewStatusLine(lipgloss.NewStyle())
	cmds := m.handleGoalEnded(bus.GoalEnded{Reason: "objective met"})
	assertHasRenderTick(t, cmds)
}

func TestHandlePlanReviewResult_NotReviewing_ReleasesUI(t *testing.T) {
	m := newTestModel()
	ag, err := agent.New(agent.AgentConfig{
		Provider: staticProvider{text: "ok"},
		Model:    core.Model{ID: "claude-sonnet-4-6", Provider: "anthropic", Name: "Claude Sonnet 4.6", MaxInput: 200_000},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	m.runtime = newTestRuntime(t, ag)
	m.input.SetEnabled(false)
	ch := make(chan planReviewStreamMsg, 1)
	m.reviewStreamCh = ch
	m.reviewGen = 7
	// handlePlanReviewResult has a value receiver, so the mutated state comes
	// back through the returned tea.Model, not the original m (see app_test.go
	// for the same pattern with other value-receiver handlers).
	result, _ := m.handlePlanReviewResult(planReviewResultMsg{ReviewGen: 7})
	rm := result.(appModel)
	if rm.reviewStreamCh != nil {
		t.Error("expected reviewStreamCh cleared")
	}
	if !rm.input.enabled {
		t.Error("expected input re-enabled")
	}
}

func TestHandleBusEvent_GoalRunStarted_SeedsRenderTick(t *testing.T) {
	m := newTestModel()
	ag, err := agent.New(agent.AgentConfig{
		Provider: staticProvider{text: "ok"},
		Model:    core.Model{ID: "claude-sonnet-4-6", Provider: "anthropic", Name: "Claude Sonnet 4.6", MaxInput: 200_000},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	m.runtime = newTestRuntime(t, ag)
	m.s.goalActive = true
	m.s.running = false
	cmds := m.handleBusEvent(bus.RunStarted{RunGen: 1})
	assertHasRenderTick(t, cmds)
	if !m.s.running {
		t.Error("expected running set by prepareRun")
	}
}
