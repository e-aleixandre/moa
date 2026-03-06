package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/ealeixandre/go-agent/pkg/tool"
)

// messageBlock holds raw conversation content. Never stores pre-rendered text.
// Blocks are rendered on demand (flush to scrollback or View()) using current
// terminal width, so resize reflows correctly.
type messageBlock struct {
	Type     string         // "user", "assistant", "tool_start", "tool_end", "error", "status"
	Raw      string         // raw content: markdown for assistant, plain text for others
	ToolName string         // for tool_start, tool_end
	ToolArgs map[string]any // for tool_start
	IsError  bool           // for tool_end
}

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

// FormatToolStart formats the beginning of a tool call.
func FormatToolStart(name string, args map[string]any) string {
	summary := tool.SummarizeArgs(args)
	return toolNameStyle.Render(fmt.Sprintf("  [%s]", name)) + " " + toolArgsStyle.Render(summary)
}

// FormatToolEnd formats the result of a tool call.
func FormatToolEnd(name string, isError bool) string {
	icon := toolSuccessStyle.Render("✓")
	if isError {
		icon = toolErrorStyle.Render("✗")
	}
	return toolNameStyle.Render(fmt.Sprintf("  [%s]", name)) + " " + icon
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
	case "tool_start":
		return FormatToolStart(block.ToolName, block.ToolArgs)
	case "tool_end":
		return FormatToolEnd(block.ToolName, block.IsError)
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
		switch block.Type {
		case "user":
			b.WriteString(FormatUserMessage(block.Raw) + "\n\n")
		case "thinking":
			if !showThinking {
				continue
			}
			styled := thinkingStyle.Width(r.width - 2).PaddingLeft(2).Render(block.Raw)
			b.WriteString(styled + "\n")
		case "assistant":
			b.WriteString(r.RenderMarkdown(block.Raw) + "\n")
		case "tool_start":
			b.WriteString(FormatToolStart(block.ToolName, block.ToolArgs) + "\n")
		case "tool_end":
			b.WriteString(FormatToolEnd(block.ToolName, block.IsError) + "\n")
		case "error":
			b.WriteString(errorStyle.Render(block.Raw) + "\n")
		case "status":
			b.WriteString(statusStyle.Render(block.Raw) + "\n")
		}
	}
	return b.String()
}
