package serve

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/session"
	"github.com/ealeixandre/moa/pkg/subagent"
)

// ReconfigureSession changes the model and/or thinking level of a session.
// Only allowed when the session is idle (not running).
func (m *Manager) ReconfigureSession(sessionID, modelSpec, thinking string) (map[string]string, error) {
	sess, ok := m.Get(sessionID)
	if !ok {
		return nil, ErrNotFound
	}

	state := sess.runtime.State.Current()
	if state == bus.StateRunning || state == bus.StatePermission {
		return nil, ErrBusy
	}

	result := map[string]string{}

	if modelSpec != "" {
		if err := core.ValidateModelSpec(modelSpec); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidModel, err)
		}
		if err := sess.runtime.Bus.Execute(bus.SwitchModel{ModelSpec: modelSpec}); err != nil {
			return nil, err
		}
		model, _ := bus.QueryTyped[bus.GetModel, core.Model](sess.runtime.Bus, bus.GetModel{})
		result["model"] = modelDisplayName(model)
	}

	if thinking != "" {
		normalized := normalizeThinkingLevel(thinking)
		if err := sess.runtime.Bus.Execute(bus.SetThinking{Level: normalized}); err != nil {
			return nil, err
		}
		result["thinking"] = normalized
	}

	// Fill in current values for non-changed fields.
	if result["model"] == "" {
		model, _ := bus.QueryTyped[bus.GetModel, core.Model](sess.runtime.Bus, bus.GetModel{})
		result["model"] = modelDisplayName(model)
	}
	if result["thinking"] == "" {
		t, _ := bus.QueryTyped[bus.GetThinkingLevel, string](sess.runtime.Bus, bus.GetThinkingLevel{})
		result["thinking"] = t
	}

	return result, nil
}

func normalizeThinkingLevel(level string) string {
	normalized := strings.ToLower(strings.TrimSpace(level))
	switch normalized {
	case "none":
		return "off"
	default:
		return normalized
	}
}

// SetTitle renames a session, marking the title as manually set so background
// auto-titling won't overwrite it, and persists the change immediately.
func (m *Manager) SetTitle(sessionID, title string) (string, error) {
	sess, ok := m.Get(sessionID)
	if !ok {
		return "", ErrNotFound
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return "", fmt.Errorf("title cannot be empty")
	}
	if len(title) > maxTitleLength {
		title = title[:maxTitleLength] + "…"
	}
	sess.mu.Lock()
	sess.Title = title
	sess.TitleSource = session.TitleSourceManual
	sess.Updated = time.Now()
	sess.mu.Unlock()
	// One-shot auto-title (if it hasn't fired yet) must not later clobber a
	// manual rename; claim the guard.
	sess.autoTitled.Store(true)
	if sess.persister != nil {
		sess.persister.saveTitle(title, session.TitleSourceManual)
	}
	m.invalidateSavedCache()
	return title, nil
}

// SetCompactAt changes a session's soft compaction threshold (tokens), so the
// agent compacts once context passes it rather than waiting for the full model
// window. 0 restores the window-based default. Like model/thinking this
// reconfigures the agent, so it is only allowed while the session is idle.
func (m *Manager) SetCompactAt(sessionID string, tokens int) (int, error) {
	sess, ok := m.Get(sessionID)
	if !ok {
		return 0, ErrNotFound
	}
	state := sess.runtime.State.Current()
	if state == bus.StateRunning || state == bus.StatePermission {
		return 0, ErrBusy
	}
	if err := sess.runtime.Bus.Execute(bus.SetCompactAt{Tokens: tokens}); err != nil {
		return 0, err
	}
	current, _ := bus.QueryTyped[bus.GetCompactAt, int](sess.runtime.Bus, bus.GetCompactAt{})
	return current, nil
}

// SetPermissionMode changes the permission mode for a session via bus command.
func (m *Manager) SetPermissionMode(sessionID, modeStr string) (string, error) {
	sess, ok := m.Get(sessionID)
	if !ok {
		return "", ErrNotFound
	}
	if err := sess.runtime.Bus.Execute(bus.SetPermissionMode{Mode: modeStr}); err != nil {
		return "", err
	}
	mode, _ := bus.QueryTyped[bus.GetPermissionMode, string](sess.runtime.Bus, bus.GetPermissionMode{})
	return mode, nil
}

// Cancel aborts the running agent in a session via bus command.
func (m *Manager) Cancel(sessionID string) error {
	sess, ok := m.Get(sessionID)
	if !ok {
		return ErrNotFound
	}

	state := sess.runtime.State.Current()
	if state != bus.StateRunning && state != bus.StatePermission {
		return fmt.Errorf("session is not running")
	}

	return sess.runtime.Bus.Execute(bus.AbortRun{})
}

// CancelSubagent requests cancellation of a single (async) subagent job
// belonging to a session, without aborting the parent run.
func (m *Manager) CancelSubagent(sessionID, jobID string) error {
	sess, ok := m.Get(sessionID)
	if !ok {
		return ErrNotFound
	}
	if sess.subagents == nil {
		return ErrNotFound
	}
	if !sess.subagents.Cancel(jobID) {
		return ErrNotFound
	}
	return nil
}

// CancelBashJob cancels a session-scoped background bash job.
func (m *Manager) CancelBashJob(sessionID, jobID string) error {
	sess, ok := m.Get(sessionID)
	if !ok || sess.bashJobs == nil || !sess.bashJobs.Cancel(jobID) {
		return ErrNotFound
	}
	return nil
}

// PromoteSubagent flips a running sync subagent job to async, unblocking its
// parent's blocking tool call while the child keeps running in the
// background. Returns ErrNotFound if the session/job doesn't exist, and
// propagates subagent.ErrNotSync / subagent.ErrNotRunning otherwise so
// callers can map them to specific responses.
func (m *Manager) PromoteSubagent(sessionID, jobID string) error {
	sess, ok := m.Get(sessionID)
	if !ok {
		return ErrNotFound
	}
	if sess.subagents == nil {
		return ErrNotFound
	}
	if err := sess.subagents.Promote(jobID); err != nil {
		if errors.Is(err, subagent.ErrUnknownJob) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// SteerSubagent queues a message for inter-step delivery to the running child
// agent of a subagent job. Returns ErrNotFound if the session or job doesn't
// exist. The bool reports whether the message was actually queued (false if the
// job has no live child agent yet or has already finished).
func (m *Manager) SteerSubagent(sessionID, jobID, text string) (bool, error) {
	sess, ok := m.Get(sessionID)
	if !ok {
		return false, ErrNotFound
	}
	if sess.subagents == nil {
		return false, ErrNotFound
	}
	if !sess.subagents.Has(jobID) {
		return false, ErrNotFound
	}
	return sess.subagents.Steer(jobID, text), nil
}

// subagentStoreFor returns the persisted-transcript store for an active
// session, or nil if the session isn't active / persistence is unavailable.
func (m *Manager) subagentStoreFor(sessionID string) *session.SubagentStore {
	sess, ok := m.Get(sessionID)
	if !ok || sess.persister == nil {
		return nil
	}
	return sess.persister.subagentStore(sessionID)
}

// ListSubagentTranscripts returns the persisted subagent transcripts for a
// session (newest-finished first). Returns ErrNotFound if the session isn't
// active or has no persistence.
func (m *Manager) ListSubagentTranscripts(sessionID string) ([]session.SubagentTranscript, error) {
	store := m.subagentStoreFor(sessionID)
	if store == nil {
		return nil, ErrNotFound
	}
	return store.List()
}

// GetSubagentTranscript loads one persisted transcript by jobID.
func (m *Manager) GetSubagentTranscript(sessionID, jobID string) (*session.SubagentTranscript, error) {
	store := m.subagentStoreFor(sessionID)
	if store == nil {
		return nil, ErrNotFound
	}
	t, err := store.Load(jobID)
	if err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return t, nil
}
