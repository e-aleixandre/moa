package serve

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/goal"
	"github.com/ealeixandre/moa/pkg/schedule"
	"github.com/ealeixandre/moa/pkg/tasks"
	"github.com/ealeixandre/moa/pkg/verify"
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
	"goal":        cmdGoal,
	"tasks":       cmdTasks,
	"permissions": cmdPermissions,
	"undo":        cmdUndo,
	"path":        cmdPath,
	"verify":      cmdVerify,
	"rename":      cmdRename,
	"schedule":    cmdSchedule,
}

func cmdSchedule(m *Manager, sess *ManagedSession, args []string) (*CommandResult, error) {
	if m.scheduler == nil {
		return &CommandResult{OK: false, Message: "schedule storage is unavailable"}, nil
	}
	if len(args) == 0 || args[0] == "list" {
		var lines []string
		for _, record := range m.scheduler.list() {
			if record.SessionID != sess.ID {
				continue
			}
			lines = append(lines, fmt.Sprintf("%s %s %s — %s", record.ID, record.Status, record.DueAt.In(time.Local).Format("2006-01-02 15:04 MST"), record.Text))
		}
		if len(lines) == 0 {
			return &CommandResult{OK: true, Message: "no schedules"}, nil
		}
		return &CommandResult{OK: true, Message: strings.Join(lines, "\n")}, nil
	}
	if args[0] == "cancel" {
		if len(args) != 2 {
			return &CommandResult{OK: false, Message: "usage: /schedule cancel <id>"}, nil
		}
		var owned bool
		for _, record := range m.scheduler.list() {
			if record.ID == args[1] && record.SessionID == sess.ID {
				owned = true
				break
			}
		}
		if !owned {
			return &CommandResult{OK: false, Message: "schedule not found"}, nil
		}
		record, err := m.scheduler.cancel(args[1])
		if err != nil {
			return &CommandResult{OK: false, Message: err.Error()}, nil
		}
		return &CommandResult{OK: true, Message: "canceled schedule " + record.ID}, nil
	}

	parsed, err := schedule.ParseCreateArgs(strings.Join(args, " "), time.Local)
	if err != nil {
		return &CommandResult{OK: false, Message: err.Error() + " — usage: /schedule at YYYY-MM-DD HH:MM [IANA-zone] -- text | in <duration> -- text"}, nil
	}
	record, err := m.scheduler.create(schedule.Schedule{
		SessionID: sess.ID,
		Text:      parsed.Text,
		DueAt:     parsed.DueAt,
		TimeZone:  parsed.TimeZone,
	})
	if err != nil {
		return &CommandResult{OK: false, Message: err.Error()}, nil
	}
	return &CommandResult{OK: true, Message: fmt.Sprintf("scheduled %s at %s", record.ID, record.DueAt.In(time.Local).Format("2006-01-02 15:04 MST"))}, nil
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

func cmdClear(m *Manager, sess *ManagedSession, _ []string) (*CommandResult, error) {
	if err := requireIdle(sess); err != nil {
		return nil, err
	}
	// "clear context" must not destroy data: start a fresh session and leave the
	// previous one intact on disk (recoverable from the session list), matching
	// the TUI. The frontend switches the tile to NewSessionID.
	newSess, err := m.CreateSession(CreateOpts{CWD: sess.CWD})
	if err != nil {
		return &CommandResult{OK: false, Message: "could not start a new conversation: " + err.Error()}, nil
	}
	return &CommandResult{OK: true, Message: "started a new conversation", NewSessionID: newSess.ID}, nil
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

func cmdRename(m *Manager, sess *ManagedSession, args []string) (*CommandResult, error) {
	if len(args) == 0 {
		return &CommandResult{OK: false, Message: "usage: /rename <new title>"}, nil
	}
	title, err := m.SetTitle(sess.ID, strings.Join(args, " "))
	if err != nil {
		return &CommandResult{OK: false, Message: err.Error()}, nil
	}
	return &CommandResult{OK: true, Message: "renamed to: " + title}, nil
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
		return &CommandResult{OK: false, Message: "usage: /thinking <off|low|medium|high|xhigh>"}, nil
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

func cmdGoal(_ *Manager, sess *ManagedSession, args []string) (*CommandResult, error) {
	b := sess.runtime.Bus

	if len(args) == 0 || args[0] == "status" {
		info, _ := bus.QueryTyped[bus.GetGoal, bus.GoalInfo](b, bus.GetGoal{})
		if !info.Active {
			return &CommandResult{OK: true, Message: "no goal active — start one with /goal <objective>"}, nil
		}
		msg := fmt.Sprintf("goal active: %s (iteration %d", info.Objective, info.Iteration)
		if info.MaxIterations > 0 {
			msg += fmt.Sprintf("/%d", info.MaxIterations)
		}
		if info.Stalled > 0 {
			msg += fmt.Sprintf(", stalled %d", info.Stalled)
		}
		msg += ")"
		if info.WorkDir != "" {
			msg += "\nworkdir: " + info.WorkDir
		}
		return &CommandResult{OK: true, Message: msg}, nil
	}

	if args[0] == "stop" {
		if err := b.Execute(bus.ExitGoal{}); err != nil {
			return &CommandResult{OK: false, Message: err.Error()}, nil
		}
		return &CommandResult{OK: true, Message: "goal stopped"}, nil
	}

	// Anything else is the objective (plus optional knobs) to start.
	if err := requireIdle(sess); err != nil {
		return nil, err
	}
	gc, err := goal.ParseCommand(strings.Join(args, " "))
	if err != nil {
		return &CommandResult{OK: false, Message: err.Error() + " — usage: /goal <objective> " + goal.FlagsUsage}, nil
	}
	if err := b.Execute(bus.EnterGoal{
		Objective:     gc.Objective,
		CompactAt:     gc.CompactAt,
		VerifierSpec:  gc.VerifierSpec,
		MaxIterations: gc.MaxIterations,
		MaxStalled:    gc.MaxStalled,
		Timeout:       gc.Timeout,
		VerifyTimeout: gc.VerifyTimeout,
		VerifyOneShot: gc.VerifyOneShot,
		TotalBudget:   gc.TotalBudget,
		WorkDir:       gc.WorkDir,
	}); err != nil {
		return &CommandResult{OK: false, Message: err.Error()}, nil
	}
	return &CommandResult{OK: true, Message: "goal started: " + gc.Objective}, nil
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
	return &CommandResult{OK: true, Message: "⏪ Undo: reverted file edits from the last turn (bash/MCP/subagent changes are not tracked)"}, nil
}

// cmdVerify runs the project's verification checks, mirroring the TUI's
// manual /verify command. It reuses the core verify.Execute entry point and
// publishes AutoVerify events so the web frontend paints the running spinner.
func cmdVerify(_ *Manager, sess *ManagedSession, _ []string) (*CommandResult, error) {
	if err := bus.RequireManualVerifyAllowed(sess.runtime.Bus); err != nil {
		return &CommandResult{OK: false, Message: err.Error()}, nil
	}
	if err := requireIdle(sess); err != nil {
		return nil, err
	}

	// Serialize: unlike the single-threaded TUI, two concurrent web POSTs can
	// reach here at once. Reject the second so their AutoVerify events don't
	// interleave and their verify processes don't clobber each other.
	if !sess.verifyRunning.CompareAndSwap(false, true) {
		return &CommandResult{OK: false, Message: "verify already running"}, nil
	}
	defer sess.verifyRunning.Store(false)

	b := sess.runtime.Bus
	b.Publish(bus.AutoVerifyStarted{SessionID: sess.ID})

	// Derive from the session context so a shutdown (which cancels it) cancels
	// the verify subprocess instead of leaking it for up to five minutes.
	ctx, cancel := context.WithTimeout(sess.infra.sessionCtx, 5*time.Minute)
	defer cancel()

	result, err := verify.Execute(ctx, sess.CWD)
	if err != nil {
		b.Publish(bus.AutoVerifyEnded{SessionID: sess.ID, Err: err})
		return &CommandResult{OK: false, Message: err.Error()}, nil
	}

	if result.AllPass {
		b.Publish(bus.AutoVerifyEnded{SessionID: sess.ID, AllPass: true})
		return &CommandResult{OK: true, Message: fmt.Sprintf("✅ Verify: all %d checks passed", len(result.Checks))}, nil
	}

	b.Publish(bus.AutoVerifyEnded{SessionID: sess.ID, Summary: verify.FormatResult(result)})

	passed := 0
	var failed []string
	for _, c := range result.Checks {
		if c.Passed {
			passed++
		} else {
			failed = append(failed, c.Name)
		}
	}
	msg := fmt.Sprintf("❌ Verify: %d/%d checks passed — failed: %s", passed, len(result.Checks), strings.Join(failed, ", "))
	return &CommandResult{OK: false, Message: msg}, nil
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
