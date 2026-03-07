package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/ealeixandre/moa/pkg/core"
)

// messageBlock holds raw conversation content. Never stores pre-rendered text.
// Blocks are rendered on demand (flush to scrollback or View()) using current
// terminal width, so resize reflows correctly.
type messageBlock struct {
	Type string // "user", "assistant", "tool", "error", "status", "thinking"
	Raw  string // raw content: markdown for assistant, plain text for others

	// Tool blocks (Type == "tool")
	ToolCallID string         // matches AgentEvent.ToolCallID
	ToolName   string         // tool name
	ToolArgs   map[string]any // call arguments
	ToolResult string         // raw result text (populated on completion)
	ToolDone   bool           // true after tool_execution_end
	IsError    bool           // true if the tool returned an error
}

const maxToolPreviewLines = 10

// renderer caches the glamour TermRenderer. Recreated only on width change.
type renderer struct {
	glamour *glamour.TermRenderer
	width   int
}

func newRenderer(width int) *renderer {
	r := &renderer{width: width}
	r.rebuild()
	return r
}

// SetWidth updates the renderer width and rebuilds glamour if changed.
func (r *renderer) SetWidth(width int) {
	if r.width != width {
		r.width = width
		r.rebuild()
	}
}

func (r *renderer) rebuild() {
	gr, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(r.width),
	)
	if err == nil {
		r.glamour = gr
	}
}

// RenderMarkdown applies glamour to a complete message.
func (r *renderer) RenderMarkdown(text string) string {
	if r.glamour == nil || strings.TrimSpace(text) == "" {
		return text
	}
	out, err := r.glamour.Render(text)
	if err != nil {
		return text
	}
	return strings.TrimRight(out, "\n")
}

// FormatUserMessage formats a user message with the ❯ prefix.
func FormatUserMessage(text string) string {
	return userPrefixStyle.Render("❯ ") + text
}

// renderToolBlock renders a full-width panel: title line, body content, footer.
// Matches pi's style: subtle background, green action, peach target, no borders.
func renderToolBlock(block messageBlock, width int) string {
	action, target, body, footer := summarizeToolBlock(block)

	// Title: action target [running…]
	title := toolActionStyle.Render(action)
	if target != "" {
		title += " " + toolTargetStyle.Render(target)
	}
	if !block.ToolDone {
		title += " " + toolDimStyle.Render("running…")
	}

	// Assemble content lines
	var lines []string
	lines = append(lines, title)
	if body != "" {
		bodyStyle := toolBodyStyle
		if block.IsError {
			bodyStyle = toolErrorBodyStyle
		}
		lines = append(lines, "") // blank line between title and body
		lines = append(lines, bodyStyle.Render(body))
	}
	if footer != "" {
		lines = append(lines, toolFooterStyle.Render(footer))
	}

	return toolBlockStyle.Width(width).Render(strings.Join(lines, "\n"))
}

// renderSingleBlock renders a single block. Returns empty string for hidden blocks
// (e.g., thinking when showThinking is false). Used by flush logic and View.
func renderSingleBlock(block messageBlock, r *renderer, showThinking bool) string {
	switch block.Type {
	case "user":
		return FormatUserMessage(block.Raw) + "\n"
	case "thinking":
		if !showThinking {
			return ""
		}
		return thinkingStyle.Width(r.width - 2).PaddingLeft(2).Render(block.Raw)
	case "assistant":
		return r.RenderMarkdown(block.Raw)
	case "tool":
		return renderToolBlock(block, r.width)
	case "error":
		return errorStyle.Render(block.Raw)
	case "status":
		return statusStyle.Render(block.Raw)
	default:
		return ""
	}
}

// renderBlocks renders all blocks as a single string. Used by expand mode (Ctrl+O pager).
func renderBlocks(blocks []messageBlock, r *renderer, showThinking bool) string {
	var b strings.Builder
	for _, block := range blocks {
		if s := renderSingleBlock(block, r, showThinking); s != "" {
			b.WriteString(s)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// --- Tool block content ---

// toolResultText extracts text content from a tool Result for block storage.
func toolResultText(result *core.Result) string {
	if result == nil {
		return ""
	}
	var sb strings.Builder
	for _, c := range result.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return strings.TrimSpace(sb.String())
}

// summarizeToolBlock extracts the display components for a tool block.
// Returns action (verb), target (path/command), body (content), and footer (truncation hint).
func summarizeToolBlock(block messageBlock) (action, target, body, footer string) {
	switch block.ToolName {
	case "bash":
		action = "bash"
		target, _ = stringArg(block.ToolArgs, "command")
		if block.IsError {
			body = block.ToolResult
		} else if block.ToolDone {
			body = block.ToolResult
		}

	case "read":
		action = "read"
		target, _ = stringArg(block.ToolArgs, "path")
		if block.ToolDone {
			body = block.ToolResult
		}

	case "write":
		action = "write"
		target, _ = stringArg(block.ToolArgs, "path")
		if block.IsError {
			body = block.ToolResult
		} else if content, ok := stringArg(block.ToolArgs, "content"); ok {
			body = content
		}

	case "edit":
		action = "edit"
		target, _ = stringArg(block.ToolArgs, "path")
		if block.IsError {
			body = block.ToolResult
		} else if newText, ok := stringArg(block.ToolArgs, "newText"); ok {
			body = newText
		}

	case "web_search":
		action = "search"
		if query, ok := stringArg(block.ToolArgs, "query"); ok {
			target = query
		} else if queries, ok := block.ToolArgs["queries"].([]any); ok {
			target = fmt.Sprintf("%d queries", len(queries))
		}
		if block.ToolDone {
			body = block.ToolResult
		}

	case "fetch_content":
		action = "fetch"
		if url, ok := stringArg(block.ToolArgs, "url"); ok {
			target = url
		} else if urls, ok := block.ToolArgs["urls"].([]any); ok {
			target = fmt.Sprintf("%d URLs", len(urls))
		}
		if block.ToolDone {
			body = block.ToolResult
		}

	default:
		action = block.ToolName
		target = sortedArgSummary(block.ToolArgs)
		if block.ToolDone {
			body = block.ToolResult
		}
	}

	if body != "" {
		body, footer = truncateBlockText(body, maxToolPreviewLines)
	}
	return action, target, body, footer
}

// --- Helpers ---

func truncateBlockText(text string, maxLines int) (body, footer string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", ""
	}
	lines := strings.Split(text, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) <= maxLines {
		return strings.Join(lines, "\n"), ""
	}
	total := len(lines)
	remaining := total - maxLines
	return strings.Join(lines[:maxLines], "\n"),
		fmt.Sprintf("… (%d more lines, %d total, ctrl+o to expand)", remaining, total)
}

func stringArg(args map[string]any, key string) (string, bool) {
	if args == nil {
		return "", false
	}
	v, ok := args[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	return s, true
}

func sortedArgSummary(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		s := fmt.Sprintf("%v", args[key])
		if len(s) > 80 {
			s = s[:77] + "…"
		}
		parts = append(parts, fmt.Sprintf("%s=%s", key, s))
	}
	result := strings.Join(parts, " ")
	if len(result) > 200 {
		result = result[:197] + "…"
	}
	return result
}
