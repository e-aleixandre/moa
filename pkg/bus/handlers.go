package bus

import (
	"errors"
	"strings"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/goal"
)

// rebuildSystemPrompt recomposes the agent's system prompt from the base prompt
// plus the active goal directive. Called after a goal mode transition.
func rebuildSystemPrompt(sctx *SessionContext) error {
	if sctx.Goal == nil {
		return nil
	}
	prompt := sctx.BaseSystemPrompt
	if sctx.Goal != nil && sctx.Goal.Active() {
		prompt += "\n\n" + goal.GoalDirective(sctx.Goal.Info())
	}
	// Returned rather than dropped: SetSystemPrompt refuses mid-run, and a
	// caller that already recorded the new state has to know it did not take.
	return sctx.Agent.SetSystemPrompt(prompt)
}

// RegisterHandlers registers command and query handlers for a session on its bus.
// Call once after creating a SessionContext.
func RegisterHandlers(sctx *SessionContext) {
	registerHandlers(sctx, func(fn func()) { go fn() })
}

type detailedSteerer interface {
	TrySteer(core.SteerItem) error
}

func trySteer(controller AgentController, item core.SteerItem) error {
	if detailed, ok := controller.(detailedSteerer); ok {
		if err := detailed.TrySteer(item); !errors.Is(err, agent.ErrSteerQueueFull) {
			return err
		}
		return ErrSteerQueueFull
	}
	if controller.Steer(item) {
		return nil
	}
	return ErrSteerQueueFull
}

func registerHandlers(sctx *SessionContext, launchAutoVerify func(func())) {
	shared := newHandlerSharedState()
	registerRuntimeSubscriptions(sctx)
	registerRunControlHandlers(sctx)
	registerConfigPromptHandlers(sctx)
	registerManualVerifyHandler(sctx)
	registerCancelSteerHandler(sctx)
	registerConfigHandlers(sctx)
	registerHistoryHandlers(sctx)
	registerSessionQueryHandlers(sctx)
	registerRunPromptHandlers(sctx, shared)
	registerAppendHandler(sctx)
	registerPermissionHandlers(sctx)
	registerGoalHandlers(sctx)
	registerPathPolicyHandlers(sctx)
	registerPermissionQueryHandlers(sctx)
	registerTreeHandlers(sctx)
	registerRunReactors(sctx)
	registerAutoVerifyReactor(sctx, shared, launchAutoVerify)
	registerGoalReactor(sctx)
}

// firstLine returns the first line of text, truncated to 80 chars.
func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}

// messageText extracts the concatenated text content from an AgentMessage.
func messageText(msg core.AgentMessage) string {
	return contentText(msg.Content)
}

// contentText extracts the concatenated text blocks of a content payload. Used
// to give a content-bearing steer a renderable Text: clients render the chip
// and the Steered event from Text, not from the blocks.
func contentText(content []core.Content) string {
	var parts []string
	for _, c := range content {
		if c.Type == "text" && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "")
}

// cleanRunError renders a run error for user-facing display. It unwraps the
// internal "stream: provider: …" plumbing prefixes and, for a usage/quota
// limit, uses the typed error's clean message ("… quota exceeded: … (resets in
// X)") so the user sees an actionable reason instead of raw HTTP noise or —
// worse — a false "interrupted" label.
func cleanRunError(err error) string {
	if err == nil {
		return ""
	}
	if qe, ok := core.AsQuotaExceeded(err); ok {
		return qe.Error()
	}
	msg := err.Error()
	for _, prefix := range []string{"stream: ", "provider: "} {
		msg = strings.TrimPrefix(msg, prefix)
	}
	return msg
}

// hasSuccessfulEdits checks tool_result messages for successful file-editing tools.
func hasSuccessfulEdits(msgs []core.AgentMessage) bool {
	editTools := map[string]bool{
		"edit":        true,
		"write":       true,
		"multiedit":   true,
		"apply_patch": true,
	}
	for _, msg := range msgs {
		if msg.Role != "tool_result" {
			continue
		}
		if editTools[msg.ToolName] && !msg.IsError {
			return true
		}
	}
	return false
}
