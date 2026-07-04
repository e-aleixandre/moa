package goal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnter_ActivatesAndCreatesStateFile(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "sub", "STATE.md")

	g := New()
	var changes []bool
	g.SetOnChange(func(active bool) { changes = append(changes, active) })

	if err := g.Enter(Options{Objective: "make tests pass", StatePath: statePath}); err != nil {
		t.Fatalf("Enter failed: %v", err)
	}
	if !g.Active() {
		t.Fatal("goal should be active after Enter")
	}
	info := g.Info()
	if info.Objective != "make tests pass" {
		t.Fatalf("objective not stored: %q", info.Objective)
	}
	if info.MaxStalled != DefaultMaxStalled {
		t.Fatalf("MaxStalled should default to %d, got %d", DefaultMaxStalled, info.MaxStalled)
	}

	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("state file not created: %v", err)
	}
	if !strings.Contains(string(data), "make tests pass") {
		t.Fatal("state file should mention the objective")
	}
	if len(changes) != 1 || changes[0] != true {
		t.Fatalf("onChange should fire once with true, got %v", changes)
	}
}

func TestBudgetAccumulation(t *testing.T) {
	dir := t.TempDir()
	g := New()
	if err := g.Enter(Options{
		Objective:   "x",
		StatePath:   filepath.Join(dir, "STATE.md"),
		TotalBudget: 5.0,
	}); err != nil {
		t.Fatal(err)
	}
	if info := g.Info(); info.TotalBudget != 5.0 || info.Spent != 0 {
		t.Fatalf("fresh goal: TotalBudget=%v Spent=%v", info.TotalBudget, info.Spent)
	}
	if got := g.AddSpent(1.5); got != 1.5 {
		t.Fatalf("AddSpent returned %v, want 1.5", got)
	}
	g.AddSpent(2.0)
	g.AddSpent(-1) // negative cost ignored
	if got := g.Spent(); got != 3.5 {
		t.Fatalf("Spent=%v, want 3.5", got)
	}
	if info := g.Info(); info.Spent != 3.5 {
		t.Fatalf("Info.Spent=%v, want 3.5", info.Spent)
	}
	// Re-entering resets the accumulator.
	if err := g.Enter(Options{Objective: "y", StatePath: filepath.Join(dir, "STATE2.md"), TotalBudget: 5.0}); err != nil {
		t.Fatal(err)
	}
	if got := g.Spent(); got != 0 {
		t.Fatalf("Spent after re-enter=%v, want 0", got)
	}
}

func TestEnter_PreservesExistingStateFile(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "STATE.md")
	if err := os.WriteFile(statePath, []byte("EXISTING BRAIN"), 0o644); err != nil {
		t.Fatal(err)
	}

	g := New()
	if err := g.Enter(Options{Objective: "x", StatePath: statePath}); err != nil {
		t.Fatalf("Enter failed: %v", err)
	}
	data, _ := os.ReadFile(statePath)
	if string(data) != "EXISTING BRAIN" {
		t.Fatal("Enter must not overwrite an existing state file")
	}
}

func TestEnter_RequiresObjective(t *testing.T) {
	g := New()
	if err := g.Enter(Options{StatePath: filepath.Join(t.TempDir(), "s.md")}); err == nil {
		t.Fatal("Enter should reject an empty objective")
	}
	if g.Active() {
		t.Fatal("goal should not be active after a failed Enter")
	}
}

func TestEnter_SetsDeadlineWhenTimeout(t *testing.T) {
	g := New()
	if err := g.Enter(Options{Objective: "x", StatePath: filepath.Join(t.TempDir(), "s.md"), Timeout: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if g.Info().Deadline.IsZero() {
		t.Fatal("deadline should be set when Timeout > 0")
	}

	g2 := New()
	_ = g2.Enter(Options{Objective: "x", StatePath: filepath.Join(t.TempDir(), "s.md")})
	if !g2.Info().Deadline.IsZero() {
		t.Fatal("deadline should be zero when Timeout == 0")
	}
}

func TestExit_Deactivates(t *testing.T) {
	g := New()
	var changes []bool
	g.SetOnChange(func(active bool) { changes = append(changes, active) })
	_ = g.Enter(Options{Objective: "x", StatePath: filepath.Join(t.TempDir(), "s.md")})

	g.Exit()
	if g.Active() {
		t.Fatal("goal should be inactive after Exit")
	}
	g.Exit() // idempotent — must not fire onChange again
	if len(changes) != 2 || changes[1] != false {
		t.Fatalf("onChange should be [true,false], got %v", changes)
	}
}

func TestCounters(t *testing.T) {
	g := New()
	_ = g.Enter(Options{Objective: "x", StatePath: filepath.Join(t.TempDir(), "s.md")})

	if got := g.BeginIteration(); got != 1 {
		t.Fatalf("first iteration should be 1, got %d", got)
	}
	if got := g.BeginIteration(); got != 2 {
		t.Fatalf("second iteration should be 2, got %d", got)
	}
	if got := g.IncStalled(); got != 1 {
		t.Fatalf("first stalled should be 1, got %d", got)
	}
	if got := g.IncStalled(); got != 2 {
		t.Fatalf("second stalled should be 2, got %d", got)
	}
	g.ResetStalled()
	if g.Info().Stalled != 0 {
		t.Fatal("ResetStalled should zero the counter")
	}
	// Enter resets counters.
	_ = g.Enter(Options{Objective: "y", StatePath: filepath.Join(t.TempDir(), "s.md")})
	if info := g.Info(); info.Iteration != 0 || info.Stalled != 0 {
		t.Fatalf("Enter should reset counters, got iteration=%d stalled=%d", info.Iteration, info.Stalled)
	}
}

func TestGoalDirective_ContainsObjectiveAndStatePath(t *testing.T) {
	d := GoalDirective(Info{Objective: "refactor auth", StatePath: ".moa/goal/STATE.md"})
	if !strings.Contains(d, "refactor auth") {
		t.Fatal("directive should contain the objective")
	}
	if !strings.Contains(d, ".moa/goal/STATE.md") {
		t.Fatal("directive should contain the state path")
	}
	if !strings.Contains(d, "GOAL MODE ACTIVE") {
		t.Fatal("directive should carry the goal-mode marker")
	}
}
