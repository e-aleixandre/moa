package bus

import (
	"context"
	"fmt"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/tool"
)

// UserShellTimeout bounds how long a "!" / "!!" shell escape may run before
// being killed. Shared by TUI and web so behaviour matches.
const UserShellTimeout = 5 * time.Minute

// UserShellMaxOutput caps captured shell-escape output (head+tail, combined
// stdout+stderr) at 50KB. This is user-triggered output, not model output, so
// no disk spill is used — it is simply truncated.
const UserShellMaxOutput = 50 * 1024

// UserShellDelivery describes how a completed shell escape's output was
// handed to the agent/conversation.
type UserShellDelivery string

const (
	// UserShellDeliverySteer means the output was injected into a running
	// agent via SteerAgent (the agent was running or awaiting a permission
	// decision when the command finished).
	UserShellDeliverySteer UserShellDelivery = "steer"
	// UserShellDeliveryAppend means the output was appended to the
	// conversation directly (the agent was idle).
	UserShellDeliveryAppend UserShellDelivery = "append"
	// UserShellDeliveryNone means the output was not delivered anywhere
	// (silent "!!" while the agent was idle).
	UserShellDeliveryNone UserShellDelivery = "none"
)

// UserShellResult is the outcome of RunUserShell.
type UserShellResult struct {
	Command   string
	Output    string
	ExitCode  int
	TimedOut  bool
	Delivered UserShellDelivery
	// DeliveryErr is set if handing the result to the bus (SteerAgent /
	// AppendToConversation) failed. The shell command itself still ran and
	// Output/ExitCode are valid; callers must surface this, not swallow it.
	DeliveryErr error
}

// UserShellExecuted is published after a "!" / "!!" shell escape completes
// and its output has been delivered (or an attempt was made to deliver it).
// Both TUI and web frontends can render from this single event.
type UserShellExecuted struct {
	SessionID string
	Command   string
	Output    string
	ExitCode  int
	TimedOut  bool
	Delivered UserShellDelivery
}

// RunUserShell executes a user-triggered "!" / "!!" shell escape command
// against the session's working directory and delivers its output to the
// agent or conversation, matching the semantics of tool.RunShell (process
// group handling, timeout, head+tail output cap).
//
// Delivery is decided from the session state *after* the command finishes,
// not when it was launched, to avoid racing a run that completes while the
// shell command is still executing:
//   - agent busy (StateRunning or StatePermission — an approval prompt is
//     still conceptually "busy") and not silent → SteerAgent.
//   - agent busy and silent ("!!") → not delivered; don't interrupt a live
//     run/approval with a background command's output.
//   - agent idle (or errored) → AppendToConversation, tagged role "shell"
//     when silent, "user" otherwise, so frontends can render distinctly.
//
// The context passed in bounds cancellation (e.g. session shutdown); the
// timeout is applied internally regardless of ctx's own deadline.
func RunUserShell(ctx context.Context, sctx *SessionContext, command string, silent bool) UserShellResult {
	sr := tool.RunShell(ctx, tool.ShellConfig{
		Command:   command,
		Dir:       sctx.CWD,
		Timeout:   UserShellTimeout,
		MaxOutput: UserShellMaxOutput,
	})

	output := sr.Stdout
	if sr.Stderr != "" {
		if output != "" {
			output += "\n"
		}
		output += sr.Stderr
	}

	result := UserShellResult{
		Command:  command,
		Output:   output,
		ExitCode: sr.ExitCode,
		TimedOut: sr.TimedOut,
	}

	running := false
	if sctx.State != nil {
		s := sctx.State.Current()
		running = s == StateRunning || s == StatePermission
	}

	switch {
	case running && !silent:
		body := fmt.Sprintf("Shell output (from user):\n$ %s\n%s", command, outputOrPlaceholder(output))
		result.Delivered = UserShellDeliverySteer
		if err := sctx.Bus.Execute(SteerAgent{SessionID: sctx.SessionID, ID: core.NewSteerID(), Text: body, Internal: true}); err != nil {
			result.DeliveryErr = fmt.Errorf("bus: delivering shell output via steer: %w", err)
		}
	case running && silent:
		// Silent ("!!") while the agent is busy: don't interrupt it. The
		// output is still shown locally by the caller's own message block,
		// just not injected into the conversation.
		result.Delivered = UserShellDeliveryNone
	default:
		// Idle: always deliver, tagged by role so the frontend can render it
		// distinctly ("shell" for silent, "user" for regular "!").
		body := fmt.Sprintf("$ %s\n%s", command, outputOrPlaceholder(output))
		role := "user"
		if silent {
			role = "shell"
		}
		msg := core.AgentMessage{
			Message: core.Message{
				Role:    role,
				Content: []core.Content{core.TextContent(body)},
			},
			Custom: map[string]any{"shell": true},
		}
		result.Delivered = UserShellDeliveryAppend
		if err := sctx.Bus.Execute(AppendToConversation{SessionID: sctx.SessionID, Message: msg}); err != nil {
			result.DeliveryErr = fmt.Errorf("bus: delivering shell output via append: %w", err)
		}
	}

	sctx.Bus.Publish(UserShellExecuted{
		SessionID: sctx.SessionID,
		Command:   command,
		Output:    output,
		ExitCode:  result.ExitCode,
		TimedOut:  result.TimedOut,
		Delivered: result.Delivered,
	})

	return result
}

func outputOrPlaceholder(output string) string {
	if output == "" {
		return "(no output)"
	}
	return output
}
