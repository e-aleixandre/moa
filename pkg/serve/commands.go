package serve

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/tasks"
)

// commandHandler executes a slash command for a session.
type commandHandler func(m *Manager, sess *ManagedSession, args []string) (*CommandResult, error)

// commandRegistry maps command names to handlers.
var commandRegistry = map[string]commandHandler{
	"clear":       cmdClear,
	"compact":     cmdCompact,
	"model":       cmdModel,
	"thinking":    cmdThinking,
	"plan":        cmdPlan,
	"tasks":       cmdTasks,
	"permissions": cmdPermissions,
	"undo":        cmdUndo,
	"path":        cmdPath,
}

// ExecCommand executes a slash command in a session.
func (m *Manager) ExecCommand(sessionID, rawCommand string) (*CommandResult, error) {
	sess, ok := m.Get(sessionID)
	if !ok {
		return nil, ErrNotFound
	}

	parts := strings.Fields(rawCommand)
	if len(parts) == 0 {
		return &CommandResult{OK: false, Message: "empty command"}, nil
	}

	cmd := strings.TrimPrefix(parts[0], "/")
	args := parts[1:]

	handler, ok := commandRegistry[cmd]
	if !ok {
		return &CommandResult{OK: false, Message: "unknown command: /" + cmd}, nil
	}
	return handler(m, sess, args)
}

// requireIdle returns ErrBusy if the session is running or waiting for permission.
func requireIdle(sess *ManagedSession) error {
	state := sess.runtime.State.Current()
	if state == bus.StateRunning || state == bus.StatePermission {
		return ErrBusy
	}
	return nil
}

func cmdClear(_ *Manager, sess *ManagedSession, _ []string) (*CommandResult, error) {
	if err := requireIdle(sess); err != nil {
		return nil, err
	}
	if err := sess.runtime.Bus.Execute(bus.ClearSession{}); err != nil {
		return &CommandResult{OK: false, Message: err.Error()}, nil
	}
	return &CommandResult{OK: true, Message: "conversation cleared"}, nil
}

func cmdCompact(_ *Manager, sess *ManagedSession, _ []string) (*CommandResult, error) {
	if err := requireIdle(sess); err != nil {
		return nil, err
	}
	if err := sess.runtime.Bus.Execute(bus.CompactSession{}); err != nil {
		return &CommandResult{OK: false, Message: "compaction failed: " + err.Error()}, nil
	}
	return &CommandResult{OK: true, Message: "conversation compacted"}, nil
}

func cmdModel(m *Manager, sess *ManagedSession, args []string) (*CommandResult, error) {
	if len(args) == 0 {
		return &CommandResult{OK: false, Message: "usage: /model <name>"}, nil
	}
	result, err := m.ReconfigureSession(sess.ID, strings.Join(args, " "), "")
	if err != nil {
		if errors.Is(err, ErrBusy) {
			return nil, ErrBusy
		}
		return &CommandResult{OK: false, Message: err.Error()}, nil
	}
	return &CommandResult{OK: true, Message: "model: " + result["model"]}, nil
}

func cmdThinking(m *Manager, sess *ManagedSession, args []string) (*CommandResult, error) {
	if len(args) == 0 {
		return &CommandResult{OK: false, Message: "usage: /thinking <off|low|medium|high>"}, nil
	}
	result, err := m.ReconfigureSession(sess.ID, "", args[0])
	if err != nil {
		if errors.Is(err, ErrBusy) {
			return nil, ErrBusy
		}
		return &CommandResult{OK: false, Message: err.Error()}, nil
	}
	return &CommandResult{OK: true, Message: "thinking: " + result["thinking"]}, nil
}

func cmdPlan(_ *Manager, sess *ManagedSession, args []string) (*CommandResult, error) {
	if err := requireIdle(sess); err != nil {
		return nil, err
	}

	b := sess.runtime.Bus

	// Check current mode via query.
	planInfo, _ := bus.QueryTyped[bus.GetPlanMode, bus.PlanModeInfo](b, bus.GetPlanMode{})

	if len(args) > 0 && args[0] == "exit" {
		if err := b.Execute(bus.ExitPlanMode{}); err != nil {
			return &CommandResult{OK: false, Message: err.Error()}, nil
		}
		return &CommandResult{OK: true, Message: "exited plan mode"}, nil
	}

	if planInfo.Mode == "off" {
		if err := b.Execute(bus.EnterPlanMode{}); err != nil {
			return &CommandResult{OK: false, Message: err.Error()}, nil
		}
		// Re-query to get the plan file path.
		planInfo, _ = bus.QueryTyped[bus.GetPlanMode, bus.PlanModeInfo](b, bus.GetPlanMode{})
		return &CommandResult{OK: true, Message: "entered plan mode → " + planInfo.PlanFile}, nil
	}

	return &CommandResult{OK: true, Message: "plan mode: " + planInfo.Mode}, nil
}

func cmdTasks(_ *Manager, sess *ManagedSession, args []string) (*CommandResult, error) {
	b := sess.runtime.Bus

	if len(args) == 0 {
		return cmdTasksList(b)
	}
	switch args[0] {
	case "done":
		return cmdTasksDone(b, args[1:])
	case "reset":
		return cmdTasksReset(b)
	default:
		return &CommandResult{OK: false, Message: "usage: /tasks [done <id> | reset]"}, nil
	}
}

func cmdTasksList(b bus.EventBus) (*CommandResult, error) {
	taskList, _ := bus.QueryTyped[bus.GetTasks, []tasks.Task](b, bus.GetTasks{})
	if len(taskList) == 0 {
		return &CommandResult{OK: true, Message: "No tasks"}, nil
	}
	done := 0
	var lines []string
	for _, t := range taskList {
		icon := "☐"
		if t.Status == "done" {
			icon = "☑"
			done++
		}
		lines = append(lines, fmt.Sprintf("%s #%d: %s", icon, t.ID, t.Title))
	}
	lines = append(lines, fmt.Sprintf("\n%d/%d complete", done, len(taskList)))
	return &CommandResult{OK: true, Message: strings.Join(lines, "\n")}, nil
}

func cmdTasksDone(b bus.EventBus, args []string) (*CommandResult, error) {
	if len(args) == 0 {
		return &CommandResult{OK: false, Message: "usage: /tasks done <id>"}, nil
	}
	var id int
	if _, err := fmt.Sscanf(args[0], "%d", &id); err != nil {
		return &CommandResult{OK: false, Message: "invalid task ID: " + args[0]}, nil
	}
	if err := b.Execute(bus.MarkTaskDone{TaskID: id}); err != nil {
		return &CommandResult{OK: false, Message: err.Error()}, nil
	}
	return &CommandResult{OK: true, Message: fmt.Sprintf("✅ Task #%d marked done", id)}, nil
}

func cmdTasksReset(b bus.EventBus) (*CommandResult, error) {
	if err := b.Execute(bus.ResetTasks{}); err != nil {
		return &CommandResult{OK: false, Message: err.Error()}, nil
	}
	return &CommandResult{OK: true, Message: "Tasks cleared"}, nil
}

func cmdPermissions(m *Manager, sess *ManagedSession, args []string) (*CommandResult, error) {
	if len(args) == 0 {
		mode, _ := bus.QueryTyped[bus.GetPermissionMode, string](sess.runtime.Bus, bus.GetPermissionMode{})
		return &CommandResult{OK: true, Message: "permissions: " + mode}, nil
	}
	newMode, err := m.SetPermissionMode(sess.ID, args[0])
	if err != nil {
		return &CommandResult{OK: false, Message: err.Error()}, nil
	}
	return &CommandResult{OK: true, Message: "permissions: " + newMode}, nil
}

func cmdUndo(_ *Manager, sess *ManagedSession, _ []string) (*CommandResult, error) {
	if err := requireIdle(sess); err != nil {
		return nil, err
	}
	if err := sess.runtime.Bus.Execute(bus.UndoLastChange{}); err != nil {
		return &CommandResult{OK: false, Message: err.Error()}, nil
	}
	return &CommandResult{OK: true, Message: "⏪ Undo applied"}, nil
}

func cmdPath(_ *Manager, sess *ManagedSession, args []string) (*CommandResult, error) {
	b := sess.runtime.Bus

	// Check availability via query.
	pathInfo, _ := bus.QueryTyped[bus.GetPathPolicy, bus.PathPolicyInfo](b, bus.GetPathPolicy{})
	if pathInfo.WorkspaceRoot == "" {
		return &CommandResult{OK: false, Message: "path policy not available"}, nil
	}

	sub := "list"
	if len(args) > 0 {
		sub = args[0]
	}

	switch sub {
	case "list":
		var lines []string
		lines = append(lines, "workspace: "+pathInfo.WorkspaceRoot)
		lines = append(lines, "scope: "+pathInfo.Scope)
		if len(pathInfo.AllowedPaths) > 0 {
			lines = append(lines, "allowed paths:")
			for _, p := range pathInfo.AllowedPaths {
				lines = append(lines, "  "+p)
			}
		}
		return &CommandResult{OK: true, Message: strings.Join(lines, "\n")}, nil

	case "add":
		if len(args) < 2 {
			return &CommandResult{OK: false, Message: "usage: /path add <dir>"}, nil
		}
		if err := b.Execute(bus.AddAllowedPath{Path: args[1]}); err != nil {
			return &CommandResult{OK: false, Message: err.Error()}, nil
		}
		// Re-query for updated scope.
		pathInfo, _ = bus.QueryTyped[bus.GetPathPolicy, bus.PathPolicyInfo](b, bus.GetPathPolicy{})
		return &CommandResult{OK: true, Message: fmt.Sprintf("added %s (scope: %s)", args[1], pathInfo.Scope)}, nil

	case "rm", "remove":
		if len(args) < 2 {
			return &CommandResult{OK: false, Message: "usage: /path rm <dir>"}, nil
		}
		if err := b.Execute(bus.RemoveAllowedPath{Path: args[1]}); err != nil {
			return &CommandResult{OK: false, Message: err.Error()}, nil
		}
		pathInfo, _ = bus.QueryTyped[bus.GetPathPolicy, bus.PathPolicyInfo](b, bus.GetPathPolicy{})
		return &CommandResult{OK: true, Message: fmt.Sprintf("removed %s (scope: %s)", args[1], pathInfo.Scope)}, nil

	case "scope":
		if len(args) < 2 {
			return &CommandResult{OK: true, Message: "scope: " + pathInfo.Scope}, nil
		}
		if err := b.Execute(bus.SetPathScope{Scope: args[1]}); err != nil {
			return &CommandResult{OK: false, Message: err.Error()}, nil
		}
		pathInfo, _ = bus.QueryTyped[bus.GetPathPolicy, bus.PathPolicyInfo](b, bus.GetPathPolicy{})
		return &CommandResult{OK: true, Message: "scope: " + pathInfo.Scope}, nil

	default:
		return &CommandResult{OK: false, Message: "usage: /path [list|add <dir>|rm <dir>|scope workspace|unrestricted]"}, nil
	}
}
