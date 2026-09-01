// handlers_config.go contains bus handlers for the corresponding session concerns.

package bus

import (
	"fmt"
	"strings"

	"github.com/e-aleixandre/moa/pkg/core"
)

func registerConfigPromptHandlers(sctx *SessionContext) {
	b := sctx.Bus
	b.OnCommand(func(cmd ReloadPromptSources) error {
		// Nil outside serve (CLI, tests), where nothing owns a prompt builder.
		// Treated as done rather than as a failure: the barrier would otherwise
		// be retried forever against a session that can never satisfy it.
		if sctx.ReloadPrompt == nil {
			return nil
		}
		sctx.ReloadPrompt()
		return nil
	})
}

func registerConfigHandlers(sctx *SessionContext) {
	b := sctx.Bus
	b.OnCommand(func(cmd SwitchModel) error {
		if sctx.ProviderFactory == nil {
			return fmt.Errorf("model switching unavailable: provider factory not configured")
		}
		newModel, ok := core.ResolveModel(cmd.ModelSpec)
		if !ok {
			if err := core.ValidateModelSpec(cmd.ModelSpec); err != nil {
				return err
			}
		}
		newProvider, err := sctx.ProviderFactory(newModel)
		if err != nil {
			return fmt.Errorf("provider error: %w", err)
		}
		oldWindow := sctx.Agent.Model().MaxInput
		if err := sctx.Agent.SetModel(newProvider, newModel); err != nil {
			return err
		}
		if effective, err := core.EffectiveThinkingLevel(newModel, sctx.Agent.ThinkingLevel()); err != nil {
			return err
		} else if effective != sctx.Agent.ThinkingLevel() {
			if err := sctx.Agent.SetThinkingLevel(effective); err != nil {
				return err
			}
		}
		modelName := newModel.Name
		if modelName == "" {
			modelName = newModel.ID
		}
		// A model that can't serve fast mode silently drops the flag, so a
		// session left marked fast would claim a speed it isn't getting.
		// Turning it off here also means coming back to a capable model
		// doesn't quietly resume paying the premium.
		if sctx.Agent.Fast() && !core.SupportsFast(newModel.ID) {
			sctx.Agent.SetFast(false)
		}
		fast := sctx.Agent.Fast()
		fastSupported := core.SupportsFast(newModel.ID)
		fastNote := core.FastNote(newModel.ID)
		changed := ConfigChanged{
			SessionID:     sctx.SessionID,
			Model:         modelName,
			Provider:      newModel.Provider,
			Thinking:      sctx.Agent.ThinkingLevel(),
			ContextWindow: newModel.MaxInput,
			Fast:          &fast,
			FastSupported: &fastSupported,
			FastNote:      &fastNote,
		}
		if at, ok := rescaleCompactAt(sctx, oldWindow, newModel.MaxInput); ok {
			changed.CompactAt = &at
		}
		sctx.Bus.Publish(changed)
		return nil
	})

	b.OnCommand(func(cmd SetThinking) error {
		effective, err := core.EffectiveThinkingLevel(sctx.Agent.Model(), cmd.Level)
		if err != nil {
			return err
		}
		if err := sctx.Agent.SetThinkingLevel(effective); err != nil {
			return err
		}
		sctx.Bus.Publish(ConfigChanged{
			SessionID: sctx.SessionID,
			Thinking:  effective,
		})
		return nil
	})

	b.OnCommand(func(cmd SetCompactAt) error {
		if cmd.Tokens < 0 {
			return fmt.Errorf("compaction threshold cannot be negative")
		}
		if err := sctx.Agent.SetCompactAt(cmd.Tokens); err != nil {
			return err
		}
		tokens := cmd.Tokens
		sctx.Bus.Publish(ConfigChanged{
			SessionID: sctx.SessionID,
			CompactAt: &tokens,
		})
		return nil
	})

	b.OnCommand(func(cmd SetDefaultCompactAt) error {
		if cmd.Tokens < 0 {
			return fmt.Errorf("compaction threshold cannot be negative")
		}
		// Deliberately not gated on "agent is running": a global threshold must
		// reach long runs, which are the ones that overflow.
		sctx.Agent.SetDefaultCompactAt(cmd.Tokens)
		return nil
	})
}

func registerPathPolicyHandlers(sctx *SessionContext) {
	b := sctx.Bus
	b.OnCommand(func(cmd SetPathScope) error {
		if sctx.PathPolicy == nil {
			return fmt.Errorf("path policy not available")
		}
		scope := strings.ToLower(cmd.Scope)
		// Normalize ws+N → workspace (extra paths come via AddAllowedPath).
		if strings.HasPrefix(scope, "ws") {
			scope = "workspace"
		}
		switch scope {
		case "workspace":
			sctx.PathPolicy.SetUnrestricted(false)
		case "unrestricted":
			sctx.PathPolicy.SetUnrestricted(true)
		default:
			return fmt.Errorf("invalid scope %q (options: workspace, unrestricted)", cmd.Scope)
		}
		sctx.Bus.Publish(ConfigChanged{
			SessionID: sctx.SessionID,
			PathScope: sctx.PathPolicy.Scope(),
		})
		return nil
	})

	b.OnCommand(func(cmd AddAllowedPath) error {
		if sctx.PathPolicy == nil {
			return fmt.Errorf("path policy not available")
		}
		if err := sctx.PathPolicy.AddPath(cmd.Path); err != nil {
			return err
		}
		sctx.Bus.Publish(ConfigChanged{
			SessionID: sctx.SessionID,
			PathScope: sctx.PathPolicy.Scope(),
		})
		return nil
	})

	b.OnCommand(func(cmd RemoveAllowedPath) error {
		if sctx.PathPolicy == nil {
			return fmt.Errorf("path policy not available")
		}
		if !sctx.PathPolicy.RemovePath(cmd.Path) {
			return fmt.Errorf("%s not in allowed paths", cmd.Path)
		}
		sctx.Bus.Publish(ConfigChanged{
			SessionID: sctx.SessionID,
			PathScope: sctx.PathPolicy.Scope(),
		})
		return nil
	})
}

func rescaleCompactAt(sctx *SessionContext, oldWindow, newWindow int) (int, bool) {
	current := sctx.Agent.CompactAt()
	if current <= 0 || oldWindow <= 0 || newWindow <= 0 || newWindow == oldWindow {
		return 0, false
	}
	rescaled := int(float64(current) / float64(oldWindow) * float64(newWindow))
	if floor := sctx.Agent.CompactAtFloor(); rescaled < floor {
		rescaled = floor
	}
	if rescaled >= newWindow {
		rescaled = 0
	}
	if rescaled == current {
		return 0, false
	}
	if err := sctx.Agent.SetCompactAt(rescaled); err != nil {
		return 0, false
	}
	return rescaled, true
}
