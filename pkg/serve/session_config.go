package serve

import (
	"fmt"
	"strings"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/permission"
)

// ReconfigureSession changes the model and/or thinking level of a session.
// Only allowed when the session is idle (not running).
func (m *Manager) ReconfigureSession(sessionID, modelSpec, thinking string) (map[string]string, error) {
	sess, ok := m.Get(sessionID)
	if !ok {
		return nil, ErrNotFound
	}

	sess.mu.Lock()
	if sess.State == StateRunning || sess.State == StatePermission {
		sess.mu.Unlock()
		return nil, ErrBusy
	}

	// Resolve model (keep current if empty).
	model := sess.runtime.resolvedModel
	if modelSpec != "" {
		model, _ = core.ResolveModel(modelSpec)
	}

	// Resolve thinking (keep current if empty).
	thinkingLevel := sess.runtime.agent.ThinkingLevel()
	if thinking != "" {
		thinkingLevel = normalizeThinkingLevel(thinking)
	}
	sess.mu.Unlock()

	// Create provider for the (possibly new) model.
	prov, err := m.providerFactory(model)
	if err != nil {
		return nil, fmt.Errorf("provider: %w", err)
	}

	// Reconfigure the agent (strips thinking blocks on model change).
	if err := sess.runtime.agent.Reconfigure(prov, model, thinkingLevel); err != nil {
		return nil, err
	}

	sess.mu.Lock()
	sess.runtime.resolvedModel = model
	sess.Model = modelDisplayName(model)
	result := map[string]string{
		"model":    sess.Model,
		"thinking": thinkingLevel,
	}
	sess.mu.Unlock()

	sess.broadcast(Event{Type: "config_change", Data: ConfigChangeData{
		Model:    result["model"],
		Thinking: result["thinking"],
	}})
	sess.broadcastContextUpdate()
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

// SetPermissionMode changes the permission mode for a session.
func (m *Manager) SetPermissionMode(sessionID, modeStr string) (string, error) {
	valid := map[string]permission.Mode{
		"yolo": permission.ModeYolo,
		"ask":  permission.ModeAsk,
		"auto": permission.ModeAuto,
	}
	newMode, ok := valid[strings.ToLower(modeStr)]
	if !ok {
		return "", fmt.Errorf("invalid permission mode %q (options: yolo, ask, auto)", modeStr)
	}

	sess, ok := m.Get(sessionID)
	if !ok {
		return "", ErrNotFound
	}

	sess.mu.Lock()
	if newMode == permission.ModeYolo {
		// Stop existing bridge if running.
		if sess.approvals.bridgeStop != nil {
			close(sess.approvals.bridgeStop)
			sess.approvals.bridgeStop = nil
		}
		sess.runtime.gate = nil
	} else if sess.runtime.gate == nil {
		// Transitioning from yolo → ask/auto: create new gate + bridge.
		sess.runtime.gate = permission.New(newMode, permission.Config{})
		sess.approvals.bridgeStop = make(chan struct{})
		go sess.permissionBridge(sess.runtime.sessionCtx)
	} else {
		sess.runtime.gate.SetMode(newMode)
	}
	result := sess.permissionMode()
	sess.mu.Unlock()

	sess.broadcast(Event{Type: "config_change", Data: ConfigChangeData{
		PermissionMode: result,
	}})
	return result, nil
}

// Cancel aborts the running agent in a session.
func (m *Manager) Cancel(sessionID string) error {
	sess, ok := m.Get(sessionID)
	if !ok {
		return ErrNotFound
	}
	sess.mu.Lock()
	if sess.State != StateRunning && sess.State != StatePermission {
		sess.mu.Unlock()
		return fmt.Errorf("session is not running")
	}
	cancel := sess.runCancel
	sess.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	return nil
}
