package bus

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/tasks"
)

// RegisterHandlers registers command and query handlers for a session on its bus.
// Call once after creating a SessionContext.
func RegisterHandlers(sctx *SessionContext) {
	b := sctx.Bus

	// -------------------------------------------------------------------
	// Commands
	// -------------------------------------------------------------------

	b.OnCommand(func(cmd AbortRun) error {
		sctx.Agent.Abort()
		return nil
	})

	b.OnCommand(func(cmd SteerAgent) error {
		sctx.Agent.Steer(cmd.Text)
		return nil
	})

	b.OnCommand(func(cmd SwitchModel) error {
		newModel, ok := core.ResolveModel(cmd.ModelSpec)
		if !ok {
			return fmt.Errorf("unknown model: %s", cmd.ModelSpec)
		}
		newProvider, err := sctx.ProviderFactory(newModel)
		if err != nil {
			return fmt.Errorf("provider error: %w", err)
		}
		if err := sctx.Agent.SetModel(newProvider, newModel); err != nil {
			return err
		}
		sctx.Bus.Publish(ConfigChanged{
			SessionID: sctx.SessionID,
			Model:     newModel.Name,
			Thinking:  sctx.Agent.ThinkingLevel(),
		})
		return nil
	})

	b.OnCommand(func(cmd SetThinking) error {
		if err := sctx.Agent.SetThinkingLevel(cmd.Level); err != nil {
			return err
		}
		sctx.Bus.Publish(ConfigChanged{
			SessionID: sctx.SessionID,
			Thinking:  cmd.Level,
		})
		return nil
	})

	b.OnCommand(func(cmd ClearSession) error {
		if err := sctx.Agent.Reset(); err != nil {
			return err
		}
		sctx.Bus.Publish(CommandExecuted{
			SessionID: sctx.SessionID,
			Command:   "clear",
		})
		return nil
	})

	b.OnCommand(func(cmd CompactSession) error {
		_, err := sctx.Agent.Compact(sctx.SessionCtx)
		// CompactionStarted/CompactionEnded events arrive via the bridge.
		// Publish CommandExecuted so frontends can refresh the message list.
		sctx.Bus.Publish(CommandExecuted{
			SessionID: sctx.SessionID,
			Command:   "compact",
			Messages:  sctx.Agent.Messages(),
		})
		return err
	})

	b.OnCommand(func(cmd UndoLastChange) error {
		if sctx.Checkpoints == nil {
			return fmt.Errorf("checkpoints not available")
		}
		cp, err := sctx.Checkpoints.Undo()
		if err != nil {
			return err
		}
		var restoreErrs []string
		for _, snap := range cp.Files {
			if snap.Content == nil {
				// File was created by the agent — delete it.
				if rmErr := os.Remove(snap.Path); rmErr != nil && !os.IsNotExist(rmErr) {
					restoreErrs = append(restoreErrs, fmt.Sprintf("delete %s: %v", filepath.Base(snap.Path), rmErr))
				}
			} else {
				// File existed before — restore original content.
				if wErr := os.WriteFile(snap.Path, snap.Content, snap.Perm); wErr != nil {
					restoreErrs = append(restoreErrs, fmt.Sprintf("restore %s: %v", filepath.Base(snap.Path), wErr))
				}
			}
		}
		if len(restoreErrs) > 0 {
			// Push checkpoint back so undo can be retried.
			sctx.Checkpoints.Repush(cp)
			return fmt.Errorf("partial restore: %s", strings.Join(restoreErrs, "; "))
		}
		return nil
	})

	b.OnCommand(func(cmd MarkTaskDone) error {
		if sctx.TaskStore == nil {
			return fmt.Errorf("task store not available")
		}
		if err := sctx.TaskStore.MarkDoneErr(cmd.TaskID); err != nil {
			return err
		}
		sctx.Bus.Publish(TasksUpdated{
			SessionID: sctx.SessionID,
			Tasks:     sctx.TaskStore.Tasks(),
		})
		return nil
	})

	b.OnCommand(func(cmd ResetTasks) error {
		if sctx.TaskStore == nil {
			return fmt.Errorf("task store not available")
		}
		sctx.TaskStore.Reset()
		sctx.Bus.Publish(TasksUpdated{
			SessionID: sctx.SessionID,
			Tasks:     sctx.TaskStore.Tasks(),
		})
		return nil
	})

	// -------------------------------------------------------------------
	// Queries
	// -------------------------------------------------------------------

	b.OnQuery(func(q GetMessages) ([]core.AgentMessage, error) {
		return sctx.Agent.Messages(), nil
	})

	b.OnQuery(func(q GetModel) (core.Model, error) {
		return sctx.Agent.Model(), nil
	})

	b.OnQuery(func(q GetThinkingLevel) (string, error) {
		return sctx.Agent.ThinkingLevel(), nil
	})

	b.OnQuery(func(q GetContextUsage) (int, error) {
		model := sctx.Agent.Model()
		if model.MaxInput <= 0 {
			return -1, nil
		}
		msgs := sctx.Agent.Messages()
		est := core.EstimateContextTokens(msgs, "", nil, sctx.Agent.CompactionEpoch())
		pct := (est.Tokens * 100) / model.MaxInput
		if pct > 100 {
			pct = 100
		}
		return pct, nil
	})

	b.OnQuery(func(q GetTasks) ([]tasks.Task, error) {
		if sctx.TaskStore == nil {
			return nil, nil
		}
		return sctx.TaskStore.Tasks(), nil
	})

	b.OnQuery(func(q GetPlanMode) (PlanModeInfo, error) {
		if sctx.PlanMode == nil {
			return PlanModeInfo{Mode: "off"}, nil
		}
		return PlanModeInfo{
			Mode:     string(sctx.PlanMode.Mode()),
			PlanFile: sctx.PlanMode.PlanFilePath(),
		}, nil
	})

	b.OnQuery(func(q GetCompactionEpoch) (int, error) {
		return sctx.Agent.CompactionEpoch(), nil
	})

	b.OnQuery(func(q GetPermissionMode) (string, error) {
		if sctx.Gate == nil {
			return "yolo", nil
		}
		return string(sctx.Gate.Mode()), nil
	})

	b.OnQuery(func(q GetPathPolicy) (PathPolicyInfo, error) {
		if sctx.PathPolicy == nil {
			return PathPolicyInfo{}, nil
		}
		return PathPolicyInfo{
			WorkspaceRoot: sctx.PathPolicy.WorkspaceRoot(),
			Scope:         sctx.PathPolicy.Scope(),
			AllowedPaths:  sctx.PathPolicy.AllowedPaths(),
		}, nil
	})
}
