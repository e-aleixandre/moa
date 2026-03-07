package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/tool"
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
	ToolResult string         // truncated result text (populated on completion)
	ToolDone   bool           // true after tool_execution_end
	IsError    bool           // true if the tool returned an error
}

const maxToolResultLines = 4

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

// --- Format functions (pure, no state) ---

// FormatUserMessage formats a user message with the ❯ prefix.
func FormatUserMessage(text string) string {
	return userPrefixStyle.Render("❯ ") + text
}

// renderToolBlock renders a unified tool block with left border, showing
// the tool name, arguments summary, and (if done) a truncated result.
func renderToolBlock(block messageBlock, width int) string {
	// Status icon.
	var icon string
	if !block.ToolDone {
		icon = toolRunningStyle.Render("●")
	} else if block.IsError {
		icon = toolErrorStyle.Render("✗")
	} else {
		icon = toolSuccessStyle.Render("✓")
	}

	// Header: icon + name + args
	header := icon + " " + toolHeaderStyle.Render(block.ToolName)
	if summary := tool.SummarizeArgs(block.ToolArgs); summary != "" {
		header += " " + toolArgsStyle.Render(summary)
	}

	content := header
	if block.ToolResult != "" {
		content += "\n" + toolResultStyle.Render(block.ToolResult)
	}

	blockWidth := width - 4 // margin(2) + border(1) + padding(1)
	if blockWidth < 40 {
		blockWidth = 40
	}
	return toolBlockStyle.Width(blockWidth).Render(content)
}

// renderSingleBlock renders a single block with trailing spacing that matches
// renderBlocks (used by expand/Ctrl+O). Returns empty string for hidden blocks
// (e.g., thinking when showThinking is false). Used by flush logic and View.
func renderSingleBlock(block messageBlock, r *renderer, showThinking bool) string {
	switch block.Type {
	case "user":
		return FormatUserMessage(block.Raw) + "\n" // blank line after user prompt
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
// Delegates to renderSingleBlock for each block, adding a newline separator.
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

// toolResultText extracts and truncates the text content from a tool result.
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
	return truncateLines(strings.TrimSpace(sb.String()), maxToolResultLines)
}

// truncateLines limits text to maxLines, appending a count of hidden lines.
func truncateLines(text string, maxLines int) string {
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	// Trim trailing blank lines.
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) <= maxLines {
		return strings.Join(lines, "\n")
	}
	remaining := len(lines) - maxLines
	return strings.Join(lines[:maxLines], "\n") + fmt.Sprintf("\n… (%d more lines)", remaining)
}
