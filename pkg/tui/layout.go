package tui

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
)

// Layout controls how each block type is rendered to a string.
// Implementations receive the active Theme so they can build styles on demand.
// All methods receive width for consistent line-filling behavior.
//
// Startup-time only: set the active layout before the TUI starts.
type Layout interface {
	RenderUserMessage(text string, width int, theme Theme) string
	RenderThinking(text string, width int, theme Theme) string
	RenderAssistantText(glamourRendered string, width int) string
	RenderToolBlock(block ToolBlockData, width int, theme Theme) string
	RenderError(text string, width int, theme Theme) string
	RenderStatus(text string, width int, theme Theme) string
	RenderLiveNotice(text string, width int, theme Theme) string
}

// ToolBlockData is the layout-facing view of a tool block.
// All content is pre-processed (truncated/extracted). Layout only arranges it.
type ToolBlockData struct {
	ToolName string
	Action   string // verb: "write", "bash", "read", "edit", "search", "fetch"
	Target   string // path, command, query
	Header   string // tail-truncation notice (above body) — may be empty
	Body     string // content — may be empty (e.g. running tool with no output yet)
	Footer   string // head-truncation notice (below body) — may be empty
	IsDiff   bool   // true when Body contains unified diff (color +/- lines)
	Done     bool
	IsError  bool
}

// --- Layout registry ---

var (
	layoutMu     sync.RWMutex
	activeLayout Layout = &SplitLayout{} // default; overridden by init or SetLayout
	layouts             = make(map[string]Layout)
)

func init() {
	layouts["split"] = &SplitLayout{}
	layouts["flat"] = &FlatLayout{}
}

// RegisterLayout adds a layout to the registry. Returns error on name collision.
func RegisterLayout(name string, l Layout) error {
	layoutMu.Lock()
	defer layoutMu.Unlock()
	if _, exists := layouts[name]; exists {
		return fmt.Errorf("layout %q already registered", name)
	}
	layouts[name] = l
	return nil
}

// SetLayout activates a registered layout by name. Must be called before TUI starts.
func SetLayout(name string) error {
	layoutMu.Lock()
	defer layoutMu.Unlock()
	l, ok := layouts[name]
	if !ok {
		return fmt.Errorf("layout %q not found", name)
	}
	activeLayout = l
	return nil
}

// SetLayoutDirect activates a layout instance directly (for extensions providing custom layouts).
// Panics on nil.
func SetLayoutDirect(l Layout) {
	if l == nil {
		panic("tui: SetLayoutDirect called with nil layout")
	}
	layoutMu.Lock()
	defer layoutMu.Unlock()
	activeLayout = l
}

// GetActiveLayout returns the current layout. Never nil after init.
func GetActiveLayout() Layout {
	layoutMu.RLock()
	defer layoutMu.RUnlock()
	return activeLayout
}

// --- Shared content helpers ---
// Used by all layouts for content extraction and truncation.

const maxToolPreviewLines = 10

// buildToolBlockData extracts a ToolBlockData from a messageBlock.
// expanded=true disables truncation.
func buildToolBlockData(block messageBlock, expanded bool) ToolBlockData {
	maxLines := maxToolPreviewLines
	if expanded {
		maxLines = 0
	}
	action, target, header, body, footer := summarizeToolBlock(block, maxLines)
	return ToolBlockData{
		ToolName: block.ToolName,
		Action:   action,
		Target:   target,
		Header:   header,
		Body:     body,
		Footer:   footer,
		IsDiff:   block.ToolDiff != "" && !block.IsError,
		Done:     block.ToolDone,
		IsError:  block.IsError,
	}
}

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
		} else if block.ToolDiff != "" {
			body = block.ToolDiff
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

// --- Truncation helpers ---

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

// --- ANSI-aware background painting ---
// Shared between layouts to avoid duplicating this fragile logic.

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

// --- Arg extraction helpers ---

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
