package bootstrap

import (
	"fmt"
	"log/slog"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/permission"
	"github.com/ealeixandre/moa/pkg/session"
)

// RestoreResult contains the outcome of restoring session metadata.
// Frontends use this to update their UI (status bars, etc.).
type RestoreResult struct {
	Model          core.Model // Restored model (zero if unchanged).
	ModelName      string     // Display name for the model.
	Thinking       string     // Restored thinking level (empty if unchanged).
	PermissionMode string     // Restored permission mode ("yolo", "ask", "auto").
}

// RestoreFromMetadata reads persisted runtime configuration from a session's
// metadata and applies it to the Session's agent and permission gate.
// This is the single source of truth for session restore — all frontends
// (TUI, serve, headless CLI) should use this instead of duplicating logic.
//
// providerFactory is needed to create a new provider when the model's provider
// changes. It may be nil if model restore is not needed.
//
// Returns a RestoreResult describing what changed, for UI updates.
func (s *Session) RestoreFromMetadata(sess *session.Session, providerFactory func(core.Model) (core.Provider, error)) RestoreResult {
	if sess == nil || sess.Metadata == nil {
		return RestoreResult{PermissionMode: s.CurrentPermissionMode()}
	}

	model, _, permMode, thinking := sess.RuntimeMeta()

	result := RestoreResult{
		PermissionMode: s.CurrentPermissionMode(),
	}

	// 1. Restore model.
	if model != "" && providerFactory != nil {
		if restored, name, ok := s.restoreModel(model, providerFactory); ok {
			result.Model = restored
			result.ModelName = name
		}
	}

	// 2. Restore thinking level.
	if thinking != "" {
		if s.restoreThinking(thinking) {
			result.Thinking = thinking
		}
	}

	// 3. Restore permission mode.
	if permMode != "" {
		s.restorePermissionMode(permMode, providerFactory)
	}
	result.PermissionMode = s.CurrentPermissionMode()

	return result
}

// restoreModel reconfigures the agent with a different model.
// Returns the resolved model, display name, and true on success.
func (s *Session) restoreModel(spec string, providerFactory func(core.Model) (core.Provider, error)) (core.Model, string, bool) {
	resolved, ok := core.ResolveModel(spec)
	if !ok || resolved.ID == "" {
		return core.Model{}, "", false
	}

	current := s.Agent.Model()
	if resolved.ID == current.ID && resolved.Provider == current.Provider {
		// Same model, no change needed — but return it for display.
		name := resolved.Name
		if name == "" {
			name = resolved.ID
		}
		return resolved, name, true
	}

	var prov core.Provider
	if resolved.Provider != current.Provider {
		p, err := providerFactory(resolved)
		if err != nil {
			slog.Warn("restore: cannot create provider for model", "spec", spec, "error", err)
			return core.Model{}, "", false
		}
		prov = p
	}

	if err := s.Agent.Reconfigure(prov, resolved, s.Agent.ThinkingLevel()); err != nil {
		slog.Warn("restore: cannot reconfigure model", "spec", spec, "error", err)
		return core.Model{}, "", false
	}

	name := resolved.Name
	if name == "" {
		name = resolved.ID
	}
	s.Model = resolved
	return resolved, name, true
}

// restoreThinking reconfigures the agent's thinking level.
func (s *Session) restoreThinking(level string) bool {
	valid := map[string]bool{"off": true, "minimal": true, "low": true, "medium": true, "high": true}
	if !valid[level] {
		return false
	}
	if err := s.Agent.Reconfigure(nil, s.Agent.Model(), level); err != nil {
		slog.Warn("restore: cannot set thinking level", "level", level, "error", err)
		return false
	}
	return true
}

// restorePermissionMode restores the permission gate mode.
func (s *Session) restorePermissionMode(mode string, providerFactory func(core.Model) (core.Provider, error)) {
	switch mode {
	case "yolo":
		s.Gate = nil
	case "ask", "auto":
		permMode := permission.Mode(mode)
		if s.Gate == nil {
			s.Gate = permission.New(permMode, permission.Config{})
		} else {
			s.Gate.SetMode(permMode)
		}
		if mode == "auto" && providerFactory != nil {
			evalSpec := s.MoaCfg.Permissions.Model
			if evalSpec == "" {
				evalSpec = "haiku"
			}
			evalModel, _ := core.ResolveModel(evalSpec)
			if prov, err := providerFactory(evalModel); err == nil {
				s.Gate.SetEvaluator(permission.NewEvaluator(prov, evalModel))
			}
		}
	default:
		slog.Warn("restore: unknown permission mode", "mode", mode)
	}
}

// CurrentPermissionMode returns the string representation of the current mode.
func (s *Session) CurrentPermissionMode() string {
	if s.Gate == nil {
		return "yolo"
	}
	return string(s.Gate.Mode())
}

// FullModelSpec returns "provider/id" for the given model, or just "id".
func FullModelSpec(model core.Model) string {
	if model.Provider != "" {
		return fmt.Sprintf("%s/%s", model.Provider, model.ID)
	}
	return model.ID
}
