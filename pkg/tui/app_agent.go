package tui

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ealeixandre/moa/pkg/clipboard"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/planmode"
)

// --- Agent interaction ---

// prepareRun sets up the common run state (running flag, gen counter, stream state, status).
// Returns the run generation for tagging the result.
func (m *appModel) prepareRun() uint64 {
	m.s.running = true
	m.s.runGen++
	m.s.runStartMsgCount = len(m.agent.Messages())
	m.s.runStartBlockIdx = len(m.s.blocks)
	m.runGenAddr.Store(m.s.runGen)
	m.s.streamState = stateStreaming
	m.s.streamText = ""
	m.s.thinkingText = ""
	m.s.streamCache = ""
	m.input.textarea.Placeholder = "Steer the agent... (Enter to send)"
	m.status.SetText("working...")
	return m.s.runGen
}

// launchAgentSend returns a tea.Batch that runs agent.Send and starts
// the render tick and spinner.
func (m appModel) launchAgentSend(text string, gen uint64) tea.Cmd {
	agentRef := m.agent
	baseCtx := m.baseCtx
	return tea.Batch(
		func() tea.Msg {
			msgs, err := agentRef.Send(baseCtx, text)
			return agentRunResultMsg{Err: err, Messages: msgs, RunGen: gen}
		},
		renderTick(),
		m.status.spinner.Tick,
	)
}

// launchAgentSendWithContent returns a tea.Batch that runs agent.SendWithContent
// and starts the render tick and spinner.
func (m appModel) launchAgentSendWithContent(content []core.Content, gen uint64) tea.Cmd {
	agentRef := m.agent
	baseCtx := m.baseCtx
	return tea.Batch(
		func() tea.Msg {
			msgs, err := agentRef.SendWithContent(baseCtx, content)
			return agentRunResultMsg{Err: err, Messages: msgs, RunGen: gen}
		},
		renderTick(),
		m.status.spinner.Tick,
	)
}

// checkClipboardImage reads image data from the system clipboard.
// Calls ReadImage directly — no separate HasImage probe (avoids TOCTOU + extra subprocess).
func (m appModel) checkClipboardImage() tea.Cmd {
	return func() tea.Msg {
		data, mime, err := clipboard.ReadImage()
		return clipboardImageMsg{Data: data, MimeType: mime, Err: err}
	}
}

// startAgentRun sends a prompt to the agent and starts streaming.
func (m appModel) startAgentRun(text string) (tea.Model, tea.Cmd) {
	if err := m.commitPendingTimelineEvent(); err != nil {
		m.s.pendingStatus = "✗ " + err.Error()
		return m, nil
	}

	// Clear transient status — it's live-only and never persisted.
	m.s.pendingStatus = ""

	// Prepend reviewer feedback if user is refining with own instructions.
	if m.pendingReviewFeedback != "" {
		text = "The reviewer found issues with your plan. Address the feedback and my additional instructions, then resubmit with `submit_plan`:\n\n**Reviewer feedback:**\n" + m.pendingReviewFeedback + "\n\n**My instructions:**\n" + text
		m.pendingReviewFeedback = ""
	}

	m.s.blocks = append(m.s.blocks, messageBlock{Type: "user", Raw: text})

	// Consume pending image if any.
	hasImage := m.s.pendingImage != nil
	var imageData []byte
	var imageMime string
	if hasImage {
		imageData = m.s.pendingImage
		imageMime = m.s.pendingImageMime
		m.s.pendingImage = nil
		m.s.pendingImageMime = ""
		kb := len(imageData) / 1024
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "status",
			Raw:  fmt.Sprintf("📎 Image attached (%d KB, %s)", kb, imageMime),
		})
	}

	// Set session title from the first user message
	if m.session != nil {
		m.session.SetTitle(text, 80)
	}

	gen := m.prepareRun()
	m.updateViewport()

	if hasImage {
		encoded := base64.StdEncoding.EncodeToString(imageData)
		content := []core.Content{
			core.TextContent(text),
			core.ImageContent(encoded, imageMime),
		}
		return m, m.launchAgentSendWithContent(content, gen)
	}
	return m, m.launchAgentSend(text, gen)
}

// startNotificationRun starts an agent run triggered by a subagent completion
// notification. Shows a subagent block (not a user block) and starts agent.Send
// so the LLM can react to the notification.
func (m appModel) startNotificationRun(n SubagentNotification) (tea.Model, tea.Cmd) {
	if err := m.commitPendingTimelineEvent(); err != nil {
		m.s.pendingStatus = "✗ " + err.Error()
		return m, nil
	}

	m.s.pendingStatus = ""
	m.s.blocks = append(m.s.blocks, messageBlock{
		Type:           "subagent",
		SubagentTask:   n.Task,
		SubagentStatus: n.Status,
		SubagentResult: n.ResultTail,
	})

	gen := m.prepareRun()
	m.updateViewport()
	return m, m.launchAgentSend(n.AgentText, gen)
}

// handleAgentEvent processes a single agent event, updating TUI state.
// Viewport refreshes happen via renderTick, not per-event.
func (m *appModel) handleAgentEvent(e core.AgentEvent) {
	switch e.Type {
	case core.AgentEventMessageUpdate:
		if e.AssistantEvent == nil {
			return
		}
		switch e.AssistantEvent.Type {
		case core.ProviderEventTextDelta:
			m.s.streamText += e.AssistantEvent.Delta
			m.s.dirty = true
		case core.ProviderEventThinkingDelta:
			m.s.thinkingText += e.AssistantEvent.Delta
			m.s.dirty = true
		}

	case core.AgentEventMessageStart:
		m.s.streamText = ""
		m.s.thinkingText = ""
		m.s.streamCache = ""
		m.s.streamState = stateStreaming
		m.status.SetText("generating...")

	case core.AgentEventMessageEnd:
		if m.s.thinkingText != "" {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "thinking", Raw: m.s.thinkingText,
			})
		}
		assistantText := m.s.streamText
		if assistantText == "" {
			for _, c := range e.Message.Content {
				if c.Type == "text" {
					assistantText += c.Text
				}
			}
		}
		if assistantText != "" {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "assistant", Raw: assistantText,
			})
		}
		m.s.streamText = ""
		m.s.thinkingText = ""
		m.s.streamCache = ""
		m.s.viewportDirty = true

	case core.AgentEventToolExecStart:
		m.s.activeTools++
		m.s.streamState = stateToolRunning
		if m.s.activeTools == 1 {
			m.status.SetText("running " + e.ToolName + "...")
		} else {
			m.status.SetText(fmt.Sprintf("running %d tools...", m.s.activeTools))
		}
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "tool", ToolCallID: e.ToolCallID, ToolName: e.ToolName, ToolArgs: e.Args,
		})
		m.s.viewportDirty = true

	case core.AgentEventToolExecUpdate:
		for i := len(m.s.blocks) - 1; i >= 0; i-- {
			b := &m.s.blocks[i]
			if b.Type == "tool" && b.ToolCallID == e.ToolCallID {
				if e.Result != nil {
					for _, c := range e.Result.Content {
						if c.Type == "text" {
							if b.ToolName == "edit" {
								b.ToolDiff = c.Text
							} else {
								b.ToolResult += c.Text
							}
						}
					}
					m.s.viewportDirty = true
				}
				break
			}
		}

	case core.AgentEventToolExecEnd:
		m.s.activeTools--
		for i := len(m.s.blocks) - 1; i >= 0; i-- {
			b := &m.s.blocks[i]
			if b.Type == "tool" && b.ToolCallID == e.ToolCallID {
				b.ToolDone = true
				b.IsError = e.IsError
				b.ToolResult = toolResultText(e.Result)
				break
			}
		}
		m.s.viewportDirty = true
		if m.s.activeTools <= 0 {
			m.s.activeTools = 0
			m.s.streamState = stateStreaming
			m.status.SetText("generating...")
		} else if m.s.activeTools == 1 {
			m.status.SetText("running tool...")
		} else {
			m.status.SetText(fmt.Sprintf("running %d tools...", m.s.activeTools))
		}
		// Live task progress update when a tasks tool call finishes.
		if e.ToolName == "tasks" && m.taskStore != nil {
			m.refreshTaskDisplay()
		}

	case core.AgentEventSteer:
		if task, status, result, ok := parseSubagentNotification(e.Text); ok {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type:           "subagent",
				SubagentTask:   task,
				SubagentStatus: status,
				SubagentResult: result,
			})
		} else {
			m.s.blocks = append(m.s.blocks, messageBlock{Type: "user", Raw: e.Text})
		}
		m.s.viewportDirty = true

	case core.AgentEventCompactionStart:
		m.status.SetText("compacting context...")

	case core.AgentEventCompactionEnd:
		if e.Error != nil {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "status",
				Raw:  "⚠ Compaction failed: " + e.Error.Error() + " (continuing with full context)",
			})
		} else if e.Compaction != nil {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "status",
				Raw:  fmt.Sprintf("✂ Context compacted (%dK → %dK tokens)", e.Compaction.TokensBefore/1000, e.Compaction.TokensAfter/1000),
			})
		}
		m.status.SetText("generating...")
		m.s.viewportDirty = true
	}
}

// --- Reconciliation ---

// handleRunResult processes the final result from agent.Send().
func (m appModel) handleRunResult(msg agentRunResultMsg) (tea.Model, tea.Cmd) {
	// Ignore results from previous runs (e.g., aborted run finishing late)
	if msg.RunGen != m.s.runGen {
		return m, nil
	}
	// Bump generation so any late-arriving events from this run are ignored.
	m.s.runGen++
	m.runGenAddr.Store(m.s.runGen)

	// Patch: correct last assistant/thinking content from source-of-truth.
	// Does NOT rebuild blocks — preserves event-derived blocks (tool with args, etc.).
	m.patchFromMessages(msg.Messages)

	m.s.running = false
	m.s.streamState = stateIdle
	m.s.streamText = ""
	m.s.thinkingText = ""
	m.s.streamCache = ""
	m.status.SetText("")
	m.input.textarea.Placeholder = "Ask anything... (Ctrl+J for newline)"
	m.input.SetEnabled(true)
	m.refreshContextSegment()
	m.accumulateCost(msg.Messages)

	if msg.Err != nil && !errors.Is(msg.Err, context.Canceled) {
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "error", Raw: "Error: " + msg.Err.Error(),
		})
	}

	// Plan mode: check if plan was submitted → show action menu.
	if m.planMode != nil && m.planMode.OnPlanSubmitted() {
		m.statusBar.UpdatePlanSegment("ready")
		m.planMenu.OpenPostSubmit()
		m.lastMenuVariant = menuPostSubmit
		m.input.SetEnabled(false)
	}

	// Plan mode: check if all tasks done during execution → auto-exit.
	if m.planMode != nil && m.planMode.Mode() == planmode.ModeExecuting && m.taskStore != nil && m.taskStore.AllDone() {
		m.planMode.Exit()
		m.syncPermissionCheck()
		m.rebuildSystemPrompt()
		m.statusBar.UpdatePlanSegment("")
		m.statusBar.UpdateTasksSegment(0, 0)
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "status", Raw: "✅ All tasks complete — plan mode finished",
		})
	}

	// Update task progress in status bar.
	m.refreshTaskDisplay()

	m.updateViewport()
	return m, m.saveSession(msg.Messages)
}

// patchFromMessages corrects the last assistant/thinking block content from
// the source-of-truth messages. Does NOT rebuild — preserves event-derived blocks
// (tool blocks with args and results, etc.) that messages don't contain.
//
// Only searches blocks from runStartBlockIdx onwards (current run). This prevents
// patching a block from a previous turn, which would leave the current turn's
// content missing from the viewport.
//
// Also creates missing blocks: if agentRunResultMsg arrives before the
// AgentEventMessageEnd event is processed (async emitter race), the assistant/thinking
// blocks won't exist yet. In that case, append them.
func (m *appModel) patchFromMessages(msgs []core.AgentMessage) {
	if msgs == nil {
		return
	}
	// Only look at messages produced during this run (after runStartMsgCount).
	// This prevents re-creating assistant blocks from a previous turn on abort.
	var newMsgs []core.AgentMessage
	if m.s.runStartMsgCount < len(msgs) {
		newMsgs = msgs[m.s.runStartMsgCount:]
	} else {
		newMsgs = nil
	}

	// Extract the final assistant text from new messages only.
	var lastAssistantText string
	var lastThinkingText string
	for i := len(newMsgs) - 1; i >= 0; i-- {
		if newMsgs[i].Role == "assistant" {
			for _, c := range newMsgs[i].Content {
				if c.Type == "text" && c.Text != "" {
					lastAssistantText = c.Text
				}
				if c.Type == "thinking" && c.Thinking != "" {
					lastThinkingText = c.Thinking
				}
			}
			break
		}
	}

	// Search boundary: only patch blocks from the current run.
	searchFrom := m.s.runStartBlockIdx

	// Patch or create thinking block
	if lastThinkingText != "" {
		found := false
		for i := len(m.s.blocks) - 1; i >= searchFrom; i-- {
			if m.s.blocks[i].Type == "thinking" {
				m.s.blocks[i].Raw = lastThinkingText
				found = true
				break
			}
		}
		if !found {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "thinking", Raw: lastThinkingText,
			})
		}
	}

	// Patch or create assistant block
	if lastAssistantText != "" {
		found := false
		for i := len(m.s.blocks) - 1; i >= searchFrom; i-- {
			if m.s.blocks[i].Type == "assistant" {
				m.s.blocks[i].Raw = lastAssistantText
				found = true
				break
			}
		}
		if !found {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "assistant", Raw: lastAssistantText,
			})
		}
	}
}

// rebuildFromMessages reconstructs blocks from the agent's source-of-truth messages.
// Used only for initial recovery — normal flow uses patchFromMessages.
func (m *appModel) rebuildFromMessages(msgs []core.AgentMessage) {
	m.s.blocks = m.s.blocks[:0]

	// Collect tool_call content from assistant messages to pair with tool_results.
	pendingCalls := make(map[string]core.Content) // ToolCallID → tool_call Content

	for _, msg := range msgs {
		// Shell messages (both "user" with custom.shell and "shell" role)
		// render as bash tool blocks regardless of role.
		if isShellMessage(msg) {
			cmd, output := parseShellBody(firstTextContent(msg.Content))
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type:       "tool",
				ToolName:   "bash",
				ToolArgs:   map[string]any{"command": cmd},
				ToolResult: output,
				ToolDone:   true,
			})
			continue
		}

		switch msg.Role {
		case "shell":
			// Already handled above, but in case custom field is missing.
			continue
		case "user":
			if len(msg.Content) > 0 {
				text := msg.Content[0].Text
				if task, status, result, ok := parseSubagentNotification(text); ok {
					m.s.blocks = append(m.s.blocks, messageBlock{
						Type:           "subagent",
						SubagentTask:   task,
						SubagentStatus: status,
						SubagentResult: result,
					})
				} else {
					m.s.blocks = append(m.s.blocks, messageBlock{
						Type: "user", Raw: text,
					})
				}
				// Show image indicators for user messages with images
				for _, c := range msg.Content {
					if c.Type == "image" {
						kb := len(c.Data) * 3 / 4 / 1024 // base64 → raw size estimate
						m.s.blocks = append(m.s.blocks, messageBlock{
							Type: "status",
							Raw:  fmt.Sprintf("📎 Image attached (%d KB, %s)", kb, c.MimeType),
						})
					}
				}
			}
		case "assistant":
			for _, c := range msg.Content {
				switch {
				case c.Type == "thinking" && c.Thinking != "":
					m.s.blocks = append(m.s.blocks, messageBlock{
						Type: "thinking", Raw: c.Thinking,
					})
				case c.Type == "text" && c.Text != "":
					m.s.blocks = append(m.s.blocks, messageBlock{
						Type: "assistant", Raw: c.Text,
					})
				case c.Type == "tool_call":
					pendingCalls[c.ToolCallID] = c
				}
			}
		case "tool_result":
			tc := pendingCalls[msg.ToolCallID]
			delete(pendingCalls, msg.ToolCallID)

			resultText := ""
			for _, c := range msg.Content {
				if c.Type == "text" {
					resultText += c.Text
				}
			}

			m.s.blocks = append(m.s.blocks, messageBlock{
				Type:       "tool",
				ToolCallID: msg.ToolCallID,
				ToolName:   msg.ToolName,
				ToolArgs:   tc.Arguments,
				ToolResult: strings.TrimSpace(resultText),
				ToolDone:   true,
				IsError:    msg.IsError,
			})
		case "compaction_summary":
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "status", Raw: "✂ (conversation compacted)",
			})
		case "session_event":
			if eventType(msg.Custom) == "model_switch" {
				if text := firstTextContent(msg.Content); text != "" {
					m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: text})
				}
			}
		}
	}
}

// isShellMessage returns true for messages produced by ! or !! shell escapes.
func isShellMessage(msg core.AgentMessage) bool {
	if msg.Role == "shell" {
		return true
	}
	if msg.Custom != nil {
		if v, ok := msg.Custom["shell"]; ok {
			if b, ok := v.(bool); ok && b {
				return true
			}
		}
	}
	return false
}

// parseShellBody splits "$ command\noutput" back into command and output.
func parseShellBody(body string) (command, output string) {
	if !strings.HasPrefix(body, "$ ") {
		return "", body
	}
	body = body[2:]
	if idx := strings.IndexByte(body, '\n'); idx >= 0 {
		command = body[:idx]
		output = body[idx+1:]
		if output == "(no output)" {
			output = ""
		}
	} else {
		command = body
	}
	return command, output
}
