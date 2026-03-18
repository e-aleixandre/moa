package tui

import (
	"encoding/base64"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/clipboard"
	"github.com/ealeixandre/moa/pkg/core"
)

// truncateLabel creates a short label for checkpoint identification.
func truncateLabel(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}

func extractToolNote(resultText string, rejected bool) string {
	t := strings.TrimSpace(resultText)
	if t == "" {
		return ""
	}

	if rejected {
		t = strings.TrimPrefix(t, "Error: ")
		t = strings.TrimPrefix(t, "Permission denied: ")
		t = strings.TrimSpace(t)
		if t == "" || t == "denied by user" {
			return "Rejected"
		}
		return "Rejected reason: " + t
	}

	const marker = "Permission feedback:"
	idx := strings.LastIndex(t, marker)
	if idx < 0 {
		return ""
	}
	fb := strings.TrimSpace(t[idx+len(marker):])
	if fb == "" {
		return ""
	}
	return "Feedback: " + fb
}

// --- Agent interaction ---

// prepareRun sets up the common run state.
func (m *appModel) prepareRun(label string) {
	m.s.running = true
	msgs := m.currentMessages()
	m.s.runStartMsgCount = len(msgs)
	m.s.runStartBlockIdx = len(m.s.blocks)
	m.s.streamState = stateStreaming
	m.s.streamText = ""
	m.s.thinkingText = ""
	m.s.streamCache = ""
	m.input.textarea.Placeholder = "Steer the agent... (Enter to send)"
	m.status.SetText("working...")
}

// launchAgentSend returns a tea.Batch that executes SendPrompt via bus.
func (m appModel) launchAgentSend(text string) tea.Cmd {
	b := m.runtime.Bus
	return tea.Batch(
		func() tea.Msg {
			err := b.Execute(bus.SendPrompt{Text: text})
			if err != nil {
				return agentSendErrorMsg{Err: err}
			}
			return nil
		},
		renderTick(),
		m.status.spinner.Tick,
	)
}

// launchAgentSendWithCustom sends with custom metadata via bus.
func (m appModel) launchAgentSendWithCustom(text string, custom map[string]any) tea.Cmd {
	b := m.runtime.Bus
	return tea.Batch(
		func() tea.Msg {
			err := b.Execute(bus.SendPrompt{Text: text, Custom: custom})
			if err != nil {
				return agentSendErrorMsg{Err: err}
			}
			return nil
		},
		renderTick(),
		m.status.spinner.Tick,
	)
}

// launchAgentSendWithContent sends structured content via bus.
func (m appModel) launchAgentSendWithContent(content []core.Content) tea.Cmd {
	b := m.runtime.Bus
	return tea.Batch(
		func() tea.Msg {
			err := b.Execute(bus.SendPromptWithContent{Content: content})
			if err != nil {
				return agentSendErrorMsg{Err: err}
			}
			return nil
		},
		renderTick(),
		m.status.spinner.Tick,
	)
}

// checkClipboardImage reads image data from the system clipboard.
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

	m.s.pendingStatus = ""

	// Prepend reviewer feedback if user is refining.
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

	// Set session title from the first user message.
	if m.session != nil {
		m.session.SetTitle(text, 80)
	}

	m.prepareRun(truncateLabel(text))
	m.updateViewport()

	if hasImage {
		encoded := base64.StdEncoding.EncodeToString(imageData)
		content := []core.Content{
			core.TextContent(text),
			core.ImageContent(encoded, imageMime),
		}
		return m, m.launchAgentSendWithContent(content)
	}
	return m, m.launchAgentSend(text)
}

// --- Reconciliation ---

// patchFromMessages corrects the last assistant/thinking block content from
// the source-of-truth messages.
func (m *appModel) patchFromMessages(msgs []core.AgentMessage) {
	if msgs == nil {
		return
	}
	var newMsgs []core.AgentMessage
	if m.s.runStartMsgCount < len(msgs) {
		newMsgs = msgs[m.s.runStartMsgCount:]
	} else {
		return
	}

	type assistantEntry struct {
		text     string
		thinking string
	}
	var entries []assistantEntry
	for _, msg := range newMsgs {
		if msg.Role != "assistant" {
			continue
		}
		var e assistantEntry
		for _, c := range msg.Content {
			if c.Type == "text" && c.Text != "" {
				e.text = c.Text
			}
			if c.Type == "thinking" && c.Thinking != "" {
				e.thinking = c.Thinking
			}
		}
		if e.text != "" || e.thinking != "" {
			entries = append(entries, e)
		}
	}
	if len(entries) == 0 {
		return
	}

	searchFrom := m.s.runStartBlockIdx

	entryIdx := 0
	for i := searchFrom; i < len(m.s.blocks) && entryIdx < len(entries); i++ {
		switch m.s.blocks[i].Type {
		case "thinking":
			if entries[entryIdx].thinking != "" {
				m.s.blocks[i].Raw = entries[entryIdx].thinking
				m.s.blocks[i].touch()
			}
		case "assistant":
			m.s.blocks[i].Raw = entries[entryIdx].text
			m.s.blocks[i].touch()
			entryIdx++
		}
	}

	for ; entryIdx < len(entries); entryIdx++ {
		if entries[entryIdx].thinking != "" {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "thinking", Raw: entries[entryIdx].thinking,
			})
		}
		if entries[entryIdx].text != "" {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "assistant", Raw: entries[entryIdx].text,
			})
		}
	}
}

// rebuildFromMessages reconstructs blocks from the agent's messages.
func (m *appModel) rebuildFromMessages(msgs []core.AgentMessage) {
	m.s.blocks = m.s.blocks[:0]

	pendingCalls := make(map[string]core.Content)

	for _, msg := range msgs {
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
			continue
		case "user":
			if len(msg.Content) > 0 {
				text := msg.Content[0].Text
				if source, _ := msg.Custom["source"].(string); source == "subagent" {
					task, _ := msg.Custom["subagent_task"].(string)
					status, _ := msg.Custom["subagent_status"].(string)
					result, _ := msg.Custom["subagent_result"].(string)
					m.s.blocks = append(m.s.blocks, messageBlock{
						Type:           "subagent",
						SubagentTask:   task,
						SubagentStatus: status,
						SubagentResult: result,
					})
				} else if task, status, result, ok := parseSubagentNotification(text); ok {
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
				for _, c := range msg.Content {
					if c.Type == "image" {
						kb := len(c.Data) * 3 / 4 / 1024
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

			isRejected := false
			if msg.Custom != nil {
				if v, ok := msg.Custom["rejected"].(bool); ok {
					isRejected = v
				}
			}
			trimmed := strings.TrimSpace(resultText)
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type:       "tool",
				ToolCallID: msg.ToolCallID,
				ToolName:   msg.ToolName,
				ToolArgs:   tc.Arguments,
				ToolResult: trimmed,
				ToolDone:   true,
				IsError:    msg.IsError,
				Rejected:   isRejected,
				ToolNote:   extractToolNote(trimmed, isRejected),
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
