package serve

import (
	"fmt"
	"strings"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

// reloadOutcome is what a /reload did to one session.
type reloadOutcome struct {
	SessionID string   `json:"session_id"`
	Title     string   `json:"title,omitempty"`
	Changed   []string `json:"changed,omitempty"`
	Queued    bool     `json:"queued,omitempty"`
}

// reloadSession re-reads the on-disk prompt inputs and, if any changed, rebuilds
// the session's base system prompt.
//
// The rebuild is the whole mechanism: a notice appended to the conversation
// could only ever add instructions, never retract one still present in the
// prompt, so deleting a rule from AGENTS.md would not take effect. Rebuilding
// makes a reload mean what it says.
//
// It is deliberately silent — the model is not told a reload happened. There is
// nothing for it to act on beyond the instructions themselves, and announcing it
// would only invite it to dwell on the change.
//
// Must run at quiescence: the agent loop captures the system prompt when a run
// starts, and SetSystemPrompt refuses while running.
func (s *ManagedSession) reloadSession() []string {
	src := s.infra.promptSources
	build := s.infra.buildBasePrompt
	reg := s.infra.toolReg
	if src == nil || build == nil || reg == nil || s.runtime == nil {
		return nil
	}

	changed := src.Reload()
	if len(changed) == 0 {
		// Nothing to do, and nothing to pay: leaving the prompt untouched keeps
		// the cached prefix valid.
		return nil
	}

	labels := make([]string, 0, len(changed))
	for _, c := range changed {
		labels = append(labels, c.Label)
	}
	s.runtime.RefreshBaseSystemPrompt(build(reg.Specs()))
	return labels
}

// formatReloadReport turns per-session outcomes into the message the user reads.
//
// A reload applies to every live session, so the report has to say what happened
// in each one: which files changed where, and which sessions were busy and will
// pick it up when they settle.
func formatReloadReport(outcomes []reloadOutcome) string {
	var applied, queued []string
	for _, o := range outcomes {
		name := o.Title
		if name == "" {
			name = o.SessionID
		}
		switch {
		case o.Queued:
			queued = append(queued, name)
		case len(o.Changed) > 0:
			applied = append(applied, fmt.Sprintf("%s (%s)", name, strings.Join(o.Changed, ", ")))
		}
	}

	if len(applied) == 0 && len(queued) == 0 {
		return "Nothing changed on disk."
	}

	var b strings.Builder
	if len(applied) > 0 {
		fmt.Fprintf(&b, "Reloaded in %d session(s):\n", len(applied))
		for _, a := range applied {
			fmt.Fprintf(&b, "  - %s\n", a)
		}
	}
	if len(queued) > 0 {
		fmt.Fprintf(&b, "Queued for %d busy session(s), applied when they settle:\n", len(queued))
		for _, q := range queued {
			fmt.Fprintf(&b, "  - %s\n", q)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// cmdReload re-reads AGENTS.md, the skills index and the memory index from disk
// and rebuilds the system prompt of every live session.
//
// The scope is deliberate: all three inputs are global to the workspace, and the
// memory index is written by the agent itself, so reloading only the session
// that asked would leave the others running on stale instructions with no sign
// of it.
//
// A session that is busy gets the reload queued as a barrier rather than
// refused. Applying instructions is not urgent, but losing the request is
// confusing: the user edited a file and asked for it to take effect.
func cmdReload(m *Manager, sess *ManagedSession, _ []string) (*CommandResult, error) {
	if err := requireIdle(sess); err != nil {
		return nil, err
	}

	// Reload the caller first, so its own outcome leads the report.
	outcomes := []reloadOutcome{{
		SessionID: sess.ID,
		Title:     sess.title(),
		Changed:   sess.reloadSession(),
	}}

	for _, other := range m.liveSessionsExcept(sess.ID) {
		out := reloadOutcome{SessionID: other.ID, Title: other.title()}
		if requireIdle(other) != nil {
			// Busy: queue the barrier on that session's own rail. It runs at
			// its next idle point, in send order with anything else queued.
			if err := other.runtime.Bus.Execute(bus.QueueCommand{
				ID:  core.NewSteerID(),
				Raw: "/reload",
			}); err == nil {
				out.Queued = true
				outcomes = append(outcomes, out)
			}
			continue
		}
		out.Changed = other.reloadSession()
		outcomes = append(outcomes, out)
	}

	return &CommandResult{OK: true, Message: formatReloadReport(outcomes)}, nil
}

// liveSessionsExcept returns the loaded sessions other than id.
//
// Only sessions already in memory: one on disk has no prompt to rebuild and
// will read the new files when it is resumed.
func (m *Manager) liveSessionsExcept(id string) []*ManagedSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*ManagedSession, 0, len(m.sessions))
	for sid, s := range m.sessions {
		if s == nil || sid == id {
			continue
		}
		out = append(out, s)
	}
	return out
}
