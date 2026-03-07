package tui

import (
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/ealeixandre/moa/pkg/core"
)

// assistantPadChars is the left padding added to assistant text by layouts.
// Glamour word-wraps to (width - assistantPadChars) so lines don't overflow.
const assistantPadChars = 2

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
		glamour.WithWordWrap(r.width-assistantPadChars),
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
	return strings.Trim(out, "\n")
}

// FormatUserMessage renders a user message using the active layout.
// Kept as a compatibility shim for external callers.
func FormatUserMessage(text string) string {
	return GetActiveLayout().RenderUserMessage(text, 80, ActiveTheme)
}

// --- Block rendering ---

func renderSingleBlock(block messageBlock, r *renderer, showThinking bool) string {
	return renderSingleBlockEx(block, r, showThinking, false)
}

func renderSingleBlockEx(block messageBlock, r *renderer, showThinking bool, expanded bool) string {
	l := GetActiveLayout()
	t := ActiveTheme
	w := r.width

	// Each block type gets a trailing "\n" for inter-block breathing room.
	// flushBlocks uses strings.Join(parts, "\n"), so trailing "\n" here
	// means: content\n + \n(join) + next = one blank line gap.
	// Tool blocks have their own internal top/bottom padding lines.
	switch block.Type {
	case "user":
		return l.RenderUserMessage(block.Raw, w, t) + "\n"
	case "thinking":
		if !showThinking {
			return ""
		}
		return l.RenderThinking(block.Raw, w, t) + "\n"
	case "assistant":
		return l.RenderAssistantText(r.RenderMarkdown(block.Raw), w) + "\n"
	case "tool":
		data := buildToolBlockData(block, expanded)
		return l.RenderToolBlock(data, w, t)
	case "error":
		return l.RenderError(block.Raw, w, t) + "\n"
	case "status":
		return l.RenderStatus(block.Raw, w, t) + "\n"
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

