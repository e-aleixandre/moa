// Package goal implements an autonomous maker→verifier loop ("goal mode").
//
// A Goal holds the loop's runtime state (objective, backstop budgets, counters).
// The bus wires a RunEnded reactor — the driver — that, when the maker stops,
// verifies the objective with a cheap separate model (see verify.go) and either
// ends the loop or relaunches the maker with feedback. The directive lives in
// the system prompt (see prompt.go) so it survives compaction; STATE.md is the
// durable, canonical brain.
package goal

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DefaultStatePath is where STATE.md lives when the caller doesn't override it.
const DefaultStatePath = ".moa/goal/STATE.md"

// DefaultMaxStalled ends the loop after this many consecutive unsatisfied
// iterations (the spin-loop guard) unless the caller overrides it.
const DefaultMaxStalled = 3

// Options configure a goal run.
type Options struct {
	Objective     string
	StatePath     string        // default DefaultStatePath
	VerifierSpec  string        // model spec for the verifier; "" = DefaultVerifierSpec
	MaxIterations int           // 0 = unlimited
	MaxStalled    int           // 0 = DefaultMaxStalled
	Timeout       time.Duration // 0 = no wall-clock deadline
}

// Info is an immutable snapshot for readers (UI, prompt builder, driver checks).
type Info struct {
	Active        bool
	Objective     string
	StatePath     string
	VerifierSpec  string
	Iteration     int
	Stalled       int
	MaxIterations int
	MaxStalled    int
	Deadline      time.Time
}

// Goal holds goal-mode runtime state. All exported methods are safe for
// concurrent use.
type Goal struct {
	mu        sync.Mutex
	active    bool
	opts      Options
	deadline  time.Time
	iteration int
	stalled   int

	// onChange fires after Enter/Exit (for TUI/serve status + system-prompt
	// rebuild). Called with the mutex released.
	onChange func(active bool)
}

// New creates an inactive Goal.
func New() *Goal { return &Goal{} }

// SetOnChange registers a callback fired after every activation change.
func (g *Goal) SetOnChange(fn func(active bool)) {
	g.mu.Lock()
	g.onChange = fn
	g.mu.Unlock()
}

// Active reports whether goal mode is on.
func (g *Goal) Active() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.active
}

// Info returns a snapshot of the current state.
func (g *Goal) Info() Info {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.snapshot()
}

// snapshot builds an Info; caller holds g.mu.
func (g *Goal) snapshot() Info {
	return Info{
		Active:        g.active,
		Objective:     g.opts.Objective,
		StatePath:     g.opts.StatePath,
		VerifierSpec:  g.opts.VerifierSpec,
		Iteration:     g.iteration,
		Stalled:       g.stalled,
		MaxIterations: g.opts.MaxIterations,
		MaxStalled:    g.opts.MaxStalled,
		Deadline:      g.deadline,
	}
}

// Enter activates goal mode: it normalizes options, resets counters, computes
// the deadline, and creates the STATE.md scaffold if it doesn't already exist
// (an existing file is preserved — it's the brain). Fires onChange(true).
func (g *Goal) Enter(opts Options) error {
	if opts.Objective == "" {
		return fmt.Errorf("goal: objective is required")
	}
	if opts.StatePath == "" {
		opts.StatePath = DefaultStatePath
	}
	if opts.MaxStalled == 0 {
		opts.MaxStalled = DefaultMaxStalled
	}

	if err := ensureStateFile(opts.StatePath, opts.Objective); err != nil {
		return err
	}

	g.mu.Lock()
	g.active = true
	g.opts = opts
	g.iteration = 0
	g.stalled = 0
	if opts.Timeout > 0 {
		g.deadline = time.Now().Add(opts.Timeout)
	} else {
		g.deadline = time.Time{}
	}
	onChange := g.onChange
	g.mu.Unlock()

	if onChange != nil {
		onChange(true)
	}
	return nil
}

// Exit deactivates goal mode. No-op if already off. Fires onChange(false).
func (g *Goal) Exit() {
	g.mu.Lock()
	if !g.active {
		g.mu.Unlock()
		return
	}
	g.active = false
	onChange := g.onChange
	g.mu.Unlock()

	if onChange != nil {
		onChange(false)
	}
}

// BeginIteration increments and returns the iteration count. The driver calls
// it when the maker stops, before verifying.
func (g *Goal) BeginIteration() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.iteration++
	return g.iteration
}

// ResetStalled clears the stalled counter (a satisfied iteration).
func (g *Goal) ResetStalled() {
	g.mu.Lock()
	g.stalled = 0
	g.mu.Unlock()
}

// IncStalled increments and returns the stalled counter (an unsatisfied iteration).
func (g *Goal) IncStalled() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stalled++
	return g.stalled
}

// ensureStateFile creates the STATE.md scaffold if it's missing. An existing
// file is left untouched so re-entering a goal preserves accumulated state.
func ensureStateFile(path, objective string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("goal: create state dir: %w", err)
		}
	}
	content := "# GOAL STATE — " + objective + "\n\n" + stateTemplate
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("goal: write state file: %w", err)
	}
	return nil
}

const stateTemplate = `## En curso (qué toca ahora)

## Hecho (mejora → commit)

## Descartado (approach → por qué)

## Bloqueado / necesita decisión del usuario

## Notas durables
`
