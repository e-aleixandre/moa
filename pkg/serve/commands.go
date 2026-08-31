package serve

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/goal"
	"github.com/e-aleixandre/moa/pkg/handoff"
	"github.com/e-aleixandre/moa/pkg/schedule"
	"github.com/e-aleixandre/moa/pkg/tasks"
	"github.com/e-aleixandre/moa/pkg/verify"
)

// commandHandler executes a slash command for a session.
type commandHandler func(m *Manager, sess *ManagedSession, args []string) (*CommandResult, error)

// commandRegistry maps command names to handlers.
var commandRegistry = map[string]commandHandler{
	"clear":           cmdClear,
	"handoff":         cmdHandoff,
	"compact":         cmdCompact,
	"prepare-compact": cmdPrepareCompact,
	"model":           cmdModel,
	"thinking":        cmdThinking,
	"goal":            cmdGoal,
	"tasks":           cmdTasks,
	"permissions":     cmdPermissions,
	"undo":            cmdUndo,
	"path":            cmdPath,
	"verify":          cmdVerify,
	"rename":          cmdRename,
	"schedule":        cmdSchedule,
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

// ExecCommand executes a slash command in a session. id is the client-minted
// stable ID for the optimistic chip when the command is enqueued as a barrier
// (busy session, PolicyQueue); it is ignored when the command runs immediately.
func (m *Manager) ExecCommand(sessionID, rawCommand, id string) (*CommandResult, error) {
	sess, ok := m.Get(sessionID)
	if !ok {
		return nil, ErrNotFound
	}
	// Same lifecycle barrier as Send: some commands (/compact, /goal…) start or
	// occupy a run, so they must not interleave with a close tearing the runtime
	// down. Held for the whole body; `closing` is then a stable read.
	sess.lifecycle.RLock()
	defer sess.lifecycle.RUnlock()
	if sess.closing.Load() {
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
		// Not a built-in: it may be a user-invocable skill. Skills are resolved
		// after the registry so one can never shadow a command the user relies
		// on; a colliding skill is reached as "/skill:<name>".
		if s, found := findInvocableSkill(sess.CWD, cmd); found {
			return runSkillCommand(sess, s, args)
		}
		return &CommandResult{OK: false, Message: "unknown command: /" + cmd}, nil
	}

	// While the session is busy, a command typed mid-run is classified by
	// policy: instant commands run now (they don't touch the live run), queued
	// commands become a barrier in the unified queue rail (executed in strict
	// send order at the next idle point), and the rest are refused. An idle
	// session runs every command immediately.
	if requireIdle(sess) != nil {
		switch bus.ClassifyCommand(rawCommand) {
		case bus.PolicyQueue:
			if id == "" {
				id = core.NewSteerID()
			}
			if err := sess.runtime.Bus.Execute(bus.QueueCommand{ID: id, Raw: rawCommand}); err != nil {
				if errors.Is(err, bus.ErrSteerQueueFull) {
					return nil, err
				}
				return &CommandResult{OK: false, Message: err.Error()}, nil
			}
			return &CommandResult{OK: true, Queued: true, ID: id, Message: "queued " + rawCommand}, nil
		case bus.PolicyReject:
			return nil, ErrBusy
		}
		// PolicyInstant falls through to run now.
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
	// The frontend switches the tile to NewSessionID.
	newSess, err := m.CreateSession(CreateOpts{CWD: sess.CWD})
	if err != nil {
		return &CommandResult{OK: false, Message: "could not start a new conversation: " + err.Error()}, nil
	}
	return &CommandResult{OK: true, Message: "started a new conversation", NewSessionID: newSess.ID}, nil
}

func cmdHandoff(m *Manager, sess *ManagedSession, args []string) (*CommandResult, error) {
	if err := requireIdle(sess); err != nil {
		return nil, err
	}
	queueLen, err := bus.QueryTyped[bus.GetQueueLen, int](sess.runtime.Bus, bus.GetQueueLen{})
	if err != nil {
		return &CommandResult{OK: false, Message: "could not inspect pending messages: " + err.Error()}, nil
	}
	if queueLen != 0 {
		return nil, ErrBusy
	}
	opts, err := handoff.Parse(args)
	if err != nil {
		return &CommandResult{OK: false, Message: err.Error()}, nil
	}
	ready := make(chan bus.HandoffReady, 1)
	settled := make(chan bus.HandoffSettled, 1)
	unsub := sess.runtime.Bus.Subscribe(func(e bus.HandoffReady) {
		ready <- e
	})
	unsubSettled := sess.runtime.Bus.Subscribe(func(e bus.HandoffSettled) {
		settled <- e
	})
	defer unsub()
	defer unsubSettled()
	if err := sess.runtime.Bus.Execute(bus.HandoffSession{SessionID: sess.ID, Options: opts}); err != nil {
		return &CommandResult{OK: false, Message: "handoff failed: " + err.Error()}, nil
	}
	timeout := time.NewTimer(2 * time.Minute)
	defer timeout.Stop()
	var readyEvent *bus.HandoffReady
	completed := false
	for {
		if completed && readyEvent != nil {
			permission, _ := bus.QueryTyped[bus.GetPermissionMode, string](sess.runtime.Bus, bus.GetPermissionMode{})
			newSess, err := m.CreateSession(CreateOpts{CWD: sess.CWD, Model: readyEvent.ModelSpec, Thinking: readyEvent.Thinking, PermissionMode: permission})
			if err != nil {
				return &CommandResult{OK: false, Message: "could not start handoff session: " + err.Error()}, nil
			}
			if _, _, _, err := m.Send(newSess.ID, readyEvent.Prompt, nil, "", ""); err != nil {
				_ = m.CloseSession(newSess.ID)
				return &CommandResult{OK: false, Message: "could not start handoff: " + err.Error()}, nil
			}
			return &CommandResult{OK: true, Message: "started handoff", NewSessionID: newSess.ID}, nil
		}
		select {
		case event := <-ready:
			readyEvent = &event
		case event := <-settled:
			if event.Cancelled {
				return &CommandResult{OK: false, Message: "handoff cancelled"}, nil
			}
			if event.Err != nil {
				return &CommandResult{OK: false, Message: "handoff failed: " + event.Err.Error()}, nil
			}
			completed = true
		case <-timeout.C:
			return &CommandResult{OK: false, Message: "handoff timed out"}, nil
		}
	}
}

// cmdCompact starts a compaction. The bus command only ACCEPTS it (the model
// call takes tens of seconds on its own goroutine), so the response reports
// that the session is now busy compacting — not that the conversation was
// compacted. The outcome travels as WS events (compaction_end, or state_change
// to error), which is what lets the client survive a suspended tab.
func cmdCompact(_ *Manager, sess *ManagedSession, args []string) (*CommandResult, error) {
	if err := requireIdle(sess); err != nil {
		return nil, err
	}
	focus := strings.TrimSpace(strings.Join(args, " "))
	if err := sess.runtime.Bus.Execute(bus.CompactSession{Focus: focus}); err != nil {
		return &CommandResult{OK: false, Message: "compaction failed: " + err.Error()}, nil
	}
	// Queued without an ID: the command was started, not enqueued behind a run,
	// so no command_dequeued will ever retire an optimistic chip for it.
	return &CommandResult{OK: true, Queued: true, Message: "compaction started"}, nil
}

// cmdPrepareCompact runs a preparation turn and then compacts. Its handler is
// already asynchronous (launchRun), so the response is likewise an acceptance.
func cmdPrepareCompact(_ *Manager, sess *ManagedSession, _ []string) (*CommandResult, error) {
	if err := requireIdle(sess); err != nil {
		return nil, err
	}
	if err := sess.runtime.Bus.Execute(bus.PrepareCompactSession{}); err != nil {
		return &CommandResult{OK: false, Message: "preparation/compaction failed: " + err.Error()}, nil
	}
	return &CommandResult{OK: true, Queued: true, Message: "preparing context; compaction will follow"}, nil
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

// cmdVerify runs the project's verification checks, mirroring the
// manual /verify command. It reuses the core verify.Execute entry point and
// publishes AutoVerify events so the web frontend paints the running spinner.
func cmdVerify(_ *Manager, sess *ManagedSession, args []string) (*CommandResult, error) {
	if err := bus.RequireManualVerifyAllowed(sess.runtime.Bus); err != nil {
		return &CommandResult{OK: false, Message: err.Error()}, nil
	}
	if err := requireIdle(sess); err != nil {
		return nil, err
	}

	// `/verify <dir>` targets another repository or worktree, for sessions whose
	// work spans more than one checkout.
	cwd, err := verify.ResolveWorkDir(sess.CWD, strings.Join(args, " "), sess.runtime.Context().PathPolicy)
	if err != nil {
		return &CommandResult{OK: false, Message: err.Error()}, nil
	}

	// Serialize: two concurrent web POSTs can
	// reach here at once. Reject the second so their AutoVerify events don't
	// interleave and their verify processes don't clobber each other.
	if !sess.verifyRunning.CompareAndSwap(false, true) {
		return &CommandResult{OK: false, Message: "verify already running"}, nil
	}
	defer sess.verifyRunning.Store(false)

	b := sess.runtime.Bus
	dir := ""
	if cwd != sess.CWD {
		dir = cwd
	}
	b.Publish(bus.AutoVerifyStarted{SessionID: sess.ID, Dir: dir, Manual: true})

	// Derive from the session context so a shutdown (which cancels it) cancels
	// the verify subprocess instead of leaking it for up to five minutes.
	ctx, cancel := context.WithTimeout(sess.infra.sessionCtx, 5*time.Minute)
	defer cancel()

	result, err := verify.Execute(ctx, cwd)
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
