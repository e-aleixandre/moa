package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
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

const (
	maxToolPreviewLines = 10
	toolPadLeft         = 2 // chars of padding inside the block
)

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

func (r *renderer) SetWidth(width int) {
	if r.width != width {
		r.width = width
		r.rebuild()
	}
}

func (r *renderer) rebuild() {
	gr, err := glamour.NewTermRenderer(
		glamour.WithStylesFromJSONBytes(glamourStyleJSON),
		glamour.WithWordWrap(r.width),
	)
	if err == nil {
		r.glamour = gr
	}
}

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

func FormatUserMessage(text string) string {
	return userPrefixStyle.Render("❯ ") + text
}

// --- Tool block rendering ---
//
// Background colors in lipgloss break when inner Style.Render() calls emit
// ANSI reset (\e[0m) — the reset kills the outer Background for the rest of
// the line. Fix: every text span and every padding space carries its own
// explicit Background, so each segment re-establishes it after the prior reset.

// renderToolBlock renders a full-width panel with Surface0 background.
// Each line is independently styled so the background is continuous.
// When expanded is true, body content is shown in full (no truncation).
func renderToolBlock(block messageBlock, width int, expanded bool) string {
	maxLines := maxToolPreviewLines
	if expanded {
		maxLines = 0
	}
	action, target, header, body, footer := summarizeToolBlock(block, maxLines)
	bg := ActiveTheme.Surface0
	bgS := lipgloss.NewStyle().Background(bg)
	pad := bgS.Render(strings.Repeat(" ", toolPadLeft))

	// Title line: action [target] [running…]
	title := pad + toolActionStyle.Background(bg).Render(action)
	if target != "" {
		title += bgS.Render(" ") + toolTargetStyle.Background(bg).Render(target)
	}
	if !block.ToolDone {
		title += bgS.Render(" ") + toolDimStyle.Background(bg).Render("running…")
	}

	var lines []string
	lines = append(lines, bgPadRight(title, width, bg))

	if header != "" || body != "" {
		lines = append(lines, bgEmptyLine(width, bg))
	}

	if header != "" {
		lines = append(lines, bgPadRight(
			pad+toolFooterStyle.Background(bg).Render(header),
			width, bg))
	}

	if body != "" {
		bodyStyle := toolBodyStyle
		if block.IsError {
			bodyStyle = toolErrorBodyStyle
		}
		for _, bl := range strings.Split(body, "\n") {
			content := pad + bodyStyle.Background(bg).Render(bl)
			lines = append(lines, bgPadRight(content, width, bg))
		}
	}

	if footer != "" {
		lines = append(lines, bgPadRight(
			pad+toolFooterStyle.Background(bg).Render(footer),
			width, bg))
	}

	return strings.Join(lines, "\n")
}

// bgPadRight pads a pre-styled line to width with background-colored spaces.
func bgPadRight(line string, width int, bg lipgloss.Color) string {
	vis := lipgloss.Width(line)
	pad := width - vis
	if pad <= 0 {
		return line
	}
	return line + lipgloss.NewStyle().Background(bg).Render(strings.Repeat(" ", pad))
}

// bgEmptyLine returns a full-width line of background-colored spaces.
func bgEmptyLine(width int, bg lipgloss.Color) string {
	return lipgloss.NewStyle().Background(bg).Render(strings.Repeat(" ", width))
}

// --- Block rendering ---

func renderSingleBlock(block messageBlock, r *renderer, showThinking bool) string {
	return renderSingleBlockEx(block, r, showThinking, false)
}

func renderSingleBlockEx(block messageBlock, r *renderer, showThinking bool, expanded bool) string {
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
		// Trailing newline creates a blank-line gap between consecutive tool
		// blocks (and before the next assistant text) when joined by "\n".
		return renderToolBlock(block, r.width, expanded) + "\n"
	case "error":
		return errorStyle.Render(block.Raw)
	case "status":
		return statusStyle.Render(block.Raw)
	default:
		return ""
	}
}

// renderBlocks renders all blocks. Used by session restore (expanded=false)
// and Ctrl+O reprint (expanded=true).
func renderBlocks(blocks []messageBlock, r *renderer, showThinking bool, expanded bool) string {
	var b strings.Builder
	for _, block := range blocks {
		if s := renderSingleBlockEx(block, r, showThinking, expanded); s != "" {
			b.WriteString(s)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// --- Tool result extraction ---

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

// --- Tool block content summarization ---

// summarizeToolBlock extracts display components. maxLines=0 means no truncation.
// header appears above body (tail-truncated tools), footer appears below (head-truncated).
func summarizeToolBlock(block messageBlock, maxLines int) (action, target, header, body, footer string) {
	tail := false // tail truncation: keep last N lines (bash, default)

	switch block.ToolName {
	case "bash":
		action = "bash"
		target, _ = stringArg(block.ToolArgs, "command")
		body = block.ToolResult
		tail = true

	case "read":
		action = "read"
		target, _ = stringArg(block.ToolArgs, "path")
		body = block.ToolResult

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
		body = block.ToolResult

	case "fetch_content":
		action = "fetch"
		if url, ok := stringArg(block.ToolArgs, "url"); ok {
			target = url
		} else if urls, ok := block.ToolArgs["urls"].([]any); ok {
			target = fmt.Sprintf("%d URLs", len(urls))
		}
		body = block.ToolResult

	default:
		action = block.ToolName
		target = sortedArgSummary(block.ToolArgs)
		body = block.ToolResult
		tail = true
	}

	if body != "" && maxLines > 0 {
		if tail {
			header, body = truncateBlockTextTail(body, maxLines)
		} else {
			body, footer = truncateBlockText(body, maxLines)
		}
	} else if body != "" {
		body = strings.TrimSpace(body)
	}
	return action, target, header, body, footer
}

// --- Helpers ---

// truncateBlockTextTail keeps the LAST maxLines. Returns (header, body).
// header shows the count of hidden lines above.
func truncateBlockTextTail(text string, maxLines int) (header, body string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", ""
	}
	lines := strings.Split(text, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) <= maxLines {
		return "", strings.Join(lines, "\n")
	}
	total := len(lines)
	hidden := total - maxLines
	return fmt.Sprintf("… (%d previous lines, %d total, ctrl+o to expand)", hidden, total),
		strings.Join(lines[total-maxLines:], "\n")
}

// truncateBlockText keeps the FIRST maxLines. Returns (body, footer).
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
