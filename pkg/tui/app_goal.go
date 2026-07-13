package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/goal"
)

// --- Goal mode ---

// handleGoalCommand dispatches /goal: no args or "status" reports state, "stop"
// ends the loop, anything else is treated as the objective to start.
func (m appModel) handleGoalCommand(args string) (tea.Model, tea.Cmd) {
	switch args {
	case "", "status":
		info, _ := bus.QueryTyped[bus.GetGoal, bus.GoalInfo](m.runtime.Bus, bus.GetGoal{})
		if !info.Active {
			m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: "No goal active. Start one with /goal <objective>"})
		} else {
			m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: goalStatusText(info)})
		}
		m.updateViewport()
		return m, nil

	case "stop":
		if err := m.runtime.Bus.Execute(bus.ExitGoal{}); err != nil {
			m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: "Goal: " + err.Error()})
			m.updateViewport()
		}
		// GoalEnded event clears the segment and adds a block.
		return m, nil

	default:
		if m.s.running {
			m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: "Cannot start a goal while the agent is running"})
			m.updateViewport()
			return m, nil
		}
		gc, err := goal.ParseCommand(args)
		if err != nil {
			m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: "Goal: " + err.Error() + "\nusage: /goal <objective> " + goal.FlagsUsage})
			m.updateViewport()
			return m, nil
		}
		if err := m.runtime.Bus.Execute(bus.EnterGoal{
			Objective:     gc.Objective,
			CompactAt:     gc.CompactAt,
			VerifierSpec:  gc.VerifierSpec,
			MaxIterations: gc.MaxIterations,
			MaxStalled:    gc.MaxStalled,
			Timeout:       gc.Timeout,
			VerifyTimeout: gc.VerifyTimeout,
			VerifyOneShot: gc.VerifyOneShot,
			TotalBudget:   gc.TotalBudget,
			WorkDir:       gc.WorkDir,
		}); err != nil {
			m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: "Goal: " + err.Error()})
			m.updateViewport()
			return m, nil
		}
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "user", Raw: "/goal " + args})
		// EnterGoal already kicked the first iteration; reflect the run locally.
		m.prepareRun("goal")
		m.updateViewport()
		return m, nil
	}
}

func goalStatusText(info bus.GoalInfo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🎯 Goal active — %s\niteration %d", info.Objective, info.Iteration)
	if info.MaxIterations > 0 {
		fmt.Fprintf(&b, "/%d", info.MaxIterations)
	}
	if info.Stalled > 0 {
		fmt.Fprintf(&b, ", stalled %d", info.Stalled)
	}
	if info.WorkDir != "" {
		fmt.Fprintf(&b, "\nworkdir: %s", info.WorkDir)
	}
	return b.String()
}

func (m *appModel) handleGoalChanged(e bus.GoalChanged) []tea.Cmd {
	wasActive := m.s.goalActive
	m.s.goalActive = e.Active
	if e.Active {
		m.statusBar.UpdateGoalSegment("active")
		// Live start line, matching the persisted "start" marker shown on
		// reopen. Only on a fresh activation (iteration 0) so a re-announcement
		// can't duplicate it for an already-running goal.
		if !wasActive && e.Iteration == 0 {
			m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: "🎯 Goal started: " + e.Objective})
			m.s.viewportDirty = true
		}
	} else {
		m.statusBar.UpdateGoalSegment("")
	}
	return nil
}

func (m *appModel) handleGoalVerifyStarted(e bus.GoalVerifyStarted) []tea.Cmd {
	m.s.goalVerifying = true
	if m.s.goalActive {
		m.statusBar.UpdateGoalSegment("verifying…")
	}
	return []tea.Cmd{renderTick()}
}

func (m *appModel) handleGoalVerifyEnded(e bus.GoalVerifyEnded) []tea.Cmd {
	m.s.goalVerifying = false
	// Restore the iteration label; GoalIterationEnded (which lands right after)
	// will set the final label, but restore here too in case the verifier was
	// cancelled without an iteration verdict.
	if m.s.goalActive {
		m.statusBar.UpdateGoalSegment(fmt.Sprintf("iter %d", e.Iteration))
	}
	return []tea.Cmd{renderTick()}
}

func (m *appModel) handleGoalIterationEnded(e bus.GoalIterationEnded) []tea.Cmd {
	verdict := "not done yet"
	if e.Satisfied {
		verdict = "satisfied"
	}
	raw := fmt.Sprintf("🎯 Goal iteration %d — %s", e.Iteration, verdict)
	if fb := strings.TrimSpace(e.Feedback); fb != "" {
		raw += "\n" + fb
	}
	m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: raw})
	if m.s.goalActive {
		m.statusBar.UpdateGoalSegment(fmt.Sprintf("iter %d", e.Iteration))
	}
	m.s.viewportDirty = true
	// Seed one render tick: goal events can land while the agent is idle (the
	// verifier runs between iterations), when the render loop isn't re-arming.
	return []tea.Cmd{renderTick()}
}

func (m *appModel) handleGoalEnded(e bus.GoalEnded) []tea.Cmd {
	m.s.goalActive = false
	m.statusBar.UpdateGoalSegment("")
	m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: "🎯 Goal ended: " + e.Reason})
	m.s.viewportDirty = true
	// Seed one render tick: goal events can land while the agent is idle (the
	// verifier runs between iterations), when the render loop isn't re-arming.
	return []tea.Cmd{renderTick()}
}
