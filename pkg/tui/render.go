package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/ealeixandre/go-agent/pkg/tool"
)

// messageBlock holds raw conversation content. Never stores pre-rendered text.
// refreshViewport() renders blocks on demand using current terminal width,
// so resize reflows correctly.
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
		glamour.WithWordWrap(r.width-4),
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

// renderBlocks renders all blocks for the viewport using the given renderer.
func renderBlocks(blocks []messageBlock, r *renderer) string {
	var b strings.Builder
	for _, block := range blocks {
		switch block.Type {
		case "user":
			b.WriteString(FormatUserMessage(block.Raw) + "\n\n")
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
