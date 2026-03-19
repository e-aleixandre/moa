// runtime_config.go bridges bootstrap sessions with the bus/runtime layer.
//
// This is the only file in package bootstrap that imports pkg/bus.
// The dependency direction is intentional: bootstrap already knows all the
// domain types (agent, permission, tasks, planmode, etc.) that constitute a
// RuntimeConfig. This file simply groups them into the struct that
// NewSessionRuntime expects, eliminating manual field mapping in every caller.
package bootstrap

import (
	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/permission"
)

// RuntimeConfig returns a bus.RuntimeConfig pre-populated with all session
// dependencies that are common across frontends (CLI, TUI, serve).
//
// Callers must set at minimum: SessionID, Ctx. They typically also set
// Bus, Checkpoints, ProviderFactory, and frontend-specific fields like
// Persister, SteerFilter, or InitialMessages.
func (s *Session) RuntimeConfig() bus.RuntimeConfig {
	// GateConfig: use snapshot if gate exists (preserves allow/deny/rules/headless).
	// If gate is nil (yolo mode), fall back to Headless from session config —
	// needed so that switching from yolo to ask/auto at runtime reconstructs
	// the gate correctly for headless sessions.
	gateConfig := permission.Config{Headless: s.Headless}
	if s.Gate != nil {
		gateConfig = s.Gate.SnapshotConfig()
	}

	// BaseSystemPrompt: read from Agent (source of truth at runtime).
	base := s.SystemPrompt
	if s.Agent != nil {
		base = s.Agent.SystemPrompt()
	}

	return bus.RuntimeConfig{
		Agent:            s.Agent,
		TaskStore:        s.TaskStore,
		PlanMode:         s.PlanMode,
		Gate:             s.Gate,
		PathPolicy:       s.PathPolicy,
		AskBridge:        s.AskBridge,
		BaseSystemPrompt: base,
		CWD:              s.CWD,
		AutoVerify:       core.IsAutoVerifyEnabled(s.MoaCfg),
		GateConfig:       gateConfig,
	}
}
