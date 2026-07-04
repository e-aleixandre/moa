package serve

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/session"
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
