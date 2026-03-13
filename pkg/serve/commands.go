package serve

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ealeixandre/moa/pkg/planmode"
)

// commandHandler executes a slash command for a session.
// args are the arguments after the command name (may be empty).
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
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.State == StateRunning || sess.State == StatePermission {
		return ErrBusy
	}
	return nil
}

func cmdClear(_ *Manager, sess *ManagedSession, _ []string) (*CommandResult, error) {
	if err := requireIdle(sess); err != nil {
		return nil, err
	}
	if err := sess.runtime.agent.Reset(); err != nil {
		return &CommandResult{OK: false, Message: err.Error()}, nil
	}
	sess.mu.Lock()
	sess.messages = nil
	sess.mu.Unlock()

	sess.save()
	sess.broadcast(Event{Type: "command", Data: CommandData{Command: "clear"}})
	return &CommandResult{OK: true, Message: "conversation cleared"}, nil
}

func cmdCompact(_ *Manager, sess *ManagedSession, _ []string) (*CommandResult, error) {
	if err := requireIdle(sess); err != nil {
		return nil, err
	}
	if _, err := sess.runtime.agent.Compact(sess.runtime.sessionCtx); err != nil {
		return &CommandResult{OK: false, Message: "compaction failed: " + err.Error()}, nil
	}
	sess.mu.Lock()
	sess.messages = sess.runtime.agent.Messages()
	sess.mu.Unlock()

	sess.save()
	sess.broadcast(Event{Type: "command", Data: CommandData{
		Command:  "compact",
		Messages: sess.runtime.agent.Messages(),
	}})
	sess.broadcastContextUpdate()
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
	if sess.runtime.planMode == nil {
		return &CommandResult{OK: false, Message: "plan mode not available"}, nil
	}
	if err := requireIdle(sess); err != nil {
		return nil, err
	}

	mode := sess.runtime.planMode.Mode()

	if len(args) > 0 && args[0] == "exit" {
		if mode == planmode.ModeOff {
			return &CommandResult{OK: false, Message: "not in plan mode"}, nil
		}
		sess.runtime.planMode.Exit()
		sess.broadcast(Event{Type: "plan_mode", Data: PlanModeData{
			Mode: string(planmode.ModeOff),
		}})
		return &CommandResult{OK: true, Message: "exited plan mode"}, nil
	}

	if mode == planmode.ModeOff {
		planPath, err := sess.runtime.planMode.Enter()
		if err != nil {
			return &CommandResult{OK: false, Message: err.Error()}, nil
		}
		sess.broadcast(Event{Type: "plan_mode", Data: PlanModeData{
			Mode:     string(planmode.ModePlanning),
			PlanFile: planPath,
		}})
		return &CommandResult{OK: true, Message: "entered plan mode → " + planPath}, nil
	}

	return &CommandResult{OK: true, Message: "plan mode: " + string(mode)}, nil
}

func cmdTasks(_ *Manager, sess *ManagedSession, args []string) (*CommandResult, error) {
	if sess.runtime.taskStore == nil {
		return &CommandResult{OK: false, Message: "task tracking not available"}, nil
	}
	if len(args) == 0 {
		return cmdTasksList(sess)
	}
	switch args[0] {
	case "done":
		return cmdTasksDone(sess, args[1:])
	case "reset":
		return cmdTasksReset(sess)
	default:
		return &CommandResult{OK: false, Message: "usage: /tasks [done <id> | reset]"}, nil
	}
}

func cmdTasksList(sess *ManagedSession) (*CommandResult, error) {
	taskList := sess.runtime.taskStore.Tasks()
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

func cmdTasksDone(sess *ManagedSession, args []string) (*CommandResult, error) {
	if len(args) == 0 {
		return &CommandResult{OK: false, Message: "usage: /tasks done <id>"}, nil
	}
	var id int
	if _, err := fmt.Sscanf(args[0], "%d", &id); err != nil {
		return &CommandResult{OK: false, Message: "invalid task ID: " + args[0]}, nil
	}
	if !sess.runtime.taskStore.MarkDone(id) {
		return &CommandResult{OK: false, Message: fmt.Sprintf("task #%d not found", id)}, nil
	}
	sess.broadcast(Event{Type: "tasks_update", Data: TasksUpdateData{Tasks: sess.runtime.taskStore.Tasks()}})
	return &CommandResult{OK: true, Message: fmt.Sprintf("✅ Task #%d marked done", id)}, nil
}

func cmdTasksReset(sess *ManagedSession) (*CommandResult, error) {
	sess.runtime.taskStore.Reset()
	sess.broadcast(Event{Type: "tasks_update", Data: TasksUpdateData{Tasks: sess.runtime.taskStore.Tasks()}})
	return &CommandResult{OK: true, Message: "Tasks cleared"}, nil
}

func cmdPermissions(m *Manager, sess *ManagedSession, args []string) (*CommandResult, error) {
	if len(args) == 0 {
		mode := sess.permissionMode()
		return &CommandResult{OK: true, Message: "permissions: " + mode}, nil
	}
	newMode, err := m.SetPermissionMode(sess.ID, args[0])
	if err != nil {
		return &CommandResult{OK: false, Message: err.Error()}, nil
	}
	return &CommandResult{OK: true, Message: "permissions: " + newMode}, nil
}
