package tui

import (
	"fmt"
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
	Type string // "user", "assistant", "tool", "error", "status", "thinking", "subagent"
	Raw  string // raw content: markdown for assistant, plain text for others

	// Tool blocks (Type == "tool")
	ToolCallID string         // matches AgentEvent.ToolCallID
	ToolName   string         // tool name
	ToolArgs   map[string]any // call arguments
	ToolResult string         // raw result text (populated on completion)
	ToolDiff   string         // diff output for edit tool (from onUpdate, preserved across ToolExecEnd)
	ToolDone   bool           // true after tool_execution_end
	IsError    bool           // true if the tool returned an error
	Rejected   bool           // true when permission-denied rejection
	ToolNote   string         // optional footer note (feedback/rejection reason)

	// Subagent blocks (Type == "subagent")
	SubagentStatus string // "completed", "failed", "cancelled"
	SubagentTask   string // original task description
	SubagentResult string // full result text (for expand)

	// Render cache — avoids re-rendering immutable blocks every frame.
	// Invalidated when version changes (block mutation) or width/layout changes.
	version       uint32 // incremented on every mutation
	cachedRender  string // last rendered string
	cachedWidth   int    // width used for cachedRender
	cachedVersion uint32 // version used for cachedRender
	cachedExpand  bool   // expanded flag used for cachedRender
	cachedThink   bool   // showThinking flag used for cachedRender
	cachedLayout  string // layout name used for cachedRender
}

// touch increments the block's version, invalidating any cached render.
func (b *messageBlock) touch() { b.version++ }

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

func renderSingleBlock(block *messageBlock, r *renderer, showThinking bool) string {
	return renderSingleBlockCached(block, r, showThinking, false)
}

// renderSingleBlockCached returns a cached render if the block, width, and
// display flags haven't changed since the last call. Otherwise re-renders
// and updates the cache.
func renderSingleBlockCached(block *messageBlock, r *renderer, showThinking bool, expanded bool) string {
	layoutName := fmt.Sprintf("%T", GetActiveLayout())
	if block.cachedRender != "" &&
		block.cachedVersion == block.version &&
		block.cachedWidth == r.width &&
		block.cachedExpand == expanded &&
		block.cachedThink == showThinking &&
		block.cachedLayout == layoutName {
		return block.cachedRender
	}
	s := renderSingleBlockImpl(*block, r, showThinking, expanded)
	block.cachedRender = s
	block.cachedVersion = block.version
	block.cachedWidth = r.width
	block.cachedExpand = expanded
	block.cachedThink = showThinking
	block.cachedLayout = layoutName
	return s
}

func renderSingleBlockImpl(block messageBlock, r *renderer, showThinking bool, expanded bool) string {
	l := GetActiveLayout()
	t := ActiveTheme
	w := r.width

	// Blocks return clean content — no trailing/leading newlines for spacing.
	// Each render path (renderBlocks, flushBlocks, View) joins with "\n\n"
	// to produce exactly one blank line between blocks.
	switch block.Type {
	case "user":
		return l.RenderUserMessage(block.Raw, w, t)
	case "thinking":
		if !showThinking {
			return ""
		}
		return l.RenderThinking(block.Raw, w, t)
	case "assistant":
		return l.RenderAssistantText(r.RenderMarkdown(block.Raw), w)
	case "tool":
		data := buildToolBlockData(block, expanded)
		return l.RenderToolBlock(data, w, t)
	case "subagent":
		data := buildSubagentBlockData(block, expanded)
		return l.RenderToolBlock(data, w, t)
	case "error":
		return l.RenderError(block.Raw, w, t)
	case "status":
		return l.RenderStatus(block.Raw, w, t)
	default:
		return ""
	}
}

// renderBlocks renders all blocks. Used by session restore (expanded=false)
// and Ctrl+O reprint (expanded=true).
func renderBlocks(blocks []messageBlock, r *renderer, showThinking bool, expanded bool) string {
	var parts []string
	for i := range blocks {
		if s := renderSingleBlockCached(&blocks[i], r, showThinking, expanded); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n\n")
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
