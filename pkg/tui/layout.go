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
	ToolName   string
	Action     string // verb: "write", "bash", "read", "edit", "search", "fetch"
	Target     string // path, command, query
	Header     string // tail-truncation notice (above body) — may be empty
	Body       string // content — may be empty (e.g. running tool with no output yet)
	Footer     string // head-truncation notice (below body) — may be empty
	IsDiff     bool   // true when Body contains unified diff (color +/- lines)
	Done       bool
	IsError    bool
	IsRejected bool
	Note       string // optional note shown in footer area (feedback/reason)
	Generating bool   // true while LLM is streaming args (before execution)
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
// Returns error if l is nil.
func SetLayoutDirect(l Layout) error {
	if l == nil {
		return fmt.Errorf("tui: SetLayoutDirect called with nil layout")
	}
	layoutMu.Lock()
	defer layoutMu.Unlock()
	activeLayout = l
	return nil
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
	isEditFallbackDiff := false
	if block.ToolName == "edit" {
		_, hasOld := stringArg(block.ToolArgs, "oldText")
		_, hasNew := stringArg(block.ToolArgs, "newText")
		isEditFallbackDiff = hasOld || hasNew
	}
	note := strings.TrimSpace(block.ToolNote)
	if note != "" {
		if footer != "" {
			footer = footer + "\n" + note
		} else {
			footer = note
		}
	}
	return ToolBlockData{
		ToolName:   block.ToolName,
		Action:     action,
		Target:     target,
		Header:     header,
		Body:       body,
		Footer:     footer,
		IsDiff:     block.ToolName == "edit" && (block.ToolDiff != "" || isEditFallbackDiff),
		Done:       block.ToolDone,
		IsError:    block.IsError,
		IsRejected: block.Rejected,
		Note:       note,
		Generating: block.Generating,
	}
}

// buildSubagentBlockData extracts a ToolBlockData from a subagent completion block.
// Reuses the tool block visual format — same truncation, same expand behavior.
func buildSubagentBlockData(block messageBlock, expanded bool) ToolBlockData {
	maxLines := maxToolPreviewLines
	if expanded {
		maxLines = 0
	}

	icon := "✓"
	switch block.SubagentStatus {
	case "failed":
		icon = "✗"
	case "cancelled":
		icon = "⊘"
	}

	action := "subagent " + icon
	target := block.SubagentTask
	body := block.SubagentResult

	var header, footer string
	if body != "" && maxLines > 0 {
		header, body = truncateBlockTextTail(body, maxLines)
	} else if body != "" {
		body = strings.TrimSpace(body)
	}

	return ToolBlockData{
		ToolName: "subagent",
		Action:   action,
		Target:   target,
		Header:   header,
		Body:     body,
		Footer:   footer,
		Done:     true,
		IsError:  block.SubagentStatus == "failed",
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
		if block.ToolDiff != "" {
			body = block.ToolDiff
		} else {
			oldText, _ := stringArg(block.ToolArgs, "oldText")
			newText, _ := stringArg(block.ToolArgs, "newText")
			if oldText != "" || newText != "" {
				body = fallbackEditDiff(oldText, newText)
			} else {
				body = block.ToolResult
			}
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

	case "plan_review":
		action = "🔍 plan review"
		target, _ = stringArg(block.ToolArgs, "plan")
		body = block.ToolResult
		tail = true

	case "request_review":
		action = "🔍 code review"
		if summary, ok := stringArg(block.ToolArgs, "summary"); ok {
			if len(summary) > 80 {
				summary = summary[:77] + "..."
			}
			target = summary
		}
		body = block.ToolResult
		tail = true

	case "tasks":
		action, _ = stringArg(block.ToolArgs, "action")
		switch action {
		case "create":
			action = "📝 new task"
			target, _ = stringArg(block.ToolArgs, "title")
		case "done":
			action = "✅ task done"
			if id, ok := block.ToolArgs["id"]; ok {
				target = fmt.Sprintf("#%v", id)
			}
		case "list":
			action = "📋 list tasks"
		default:
			action = "tasks " + action
			if id, ok := block.ToolArgs["id"]; ok {
				target = fmt.Sprintf("#%v", id)
			}
		}
		body = block.ToolResult

	case "ask_user":
		action = "❓ questions"
		target, body = formatAskUserBlock(block)

	case "subagent":
		action = "⚡ subagent"
		target, _ = stringArg(block.ToolArgs, "task")
		// Build metadata badges for model, thinking, tools
		var badges []string
		if m, ok := stringArg(block.ToolArgs, "model"); ok && m != "" {
			badges = append(badges, m)
		}
		if th, ok := stringArg(block.ToolArgs, "thinking"); ok && th != "" {
			badges = append(badges, "thinking:"+th)
		}
		if tools, ok := block.ToolArgs["tools"].([]any); ok && len(tools) > 0 {
			names := make([]string, 0, len(tools))
			for _, t := range tools {
				if s, ok := t.(string); ok {
					names = append(names, s)
				}
			}
			badges = append(badges, strings.Join(names, ","))
		}
		if len(badges) > 0 {
			target += "  [" + strings.Join(badges, " · ") + "]"
		}
		body = block.ToolResult
		tail = true

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

// diffLineKind classifies one diff line:
//
//	 2 = hunk header (@@ ... @@)
//	 1 = addition (+)
//	-1 = deletion (-)
//	 0 = context/unknown
//
// Supports both plain unified diff lines ("+foo", "-bar") and numbered lines
// used by edit previews ("   4 +foo", "  10 -bar").
func diffLineKind(line string) int {
	if strings.HasPrefix(line, "@@") {
		return 2
	}
	if strings.HasPrefix(line, "+") {
		return 1
	}
	if strings.HasPrefix(line, "-") {
		return -1
	}
	i := 0
	for i < len(line) && line[i] == ' ' {
		i++
	}
	j := i
	for j < len(line) && line[j] >= '0' && line[j] <= '9' {
		j++
	}
	if j == i {
		return 0
	}
	k := j
	for k < len(line) && line[k] == ' ' {
		k++
	}
	if k >= len(line) {
		return 0
	}
	switch line[k] {
	case '+':
		return 1
	case '-':
		return -1
	default:
		return 0
	}
}

// fallbackEditDiff builds a readable numbered diff from old/new text so edit
// cards show a diff immediately (before tool execution/update events arrive).
func fallbackEditDiff(oldText, newText string) string {
	oldLines := splitPreserveEmpty(oldText)
	newLines := splitPreserveEmpty(newText)
	if len(oldLines) == 0 && len(newLines) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("@@ -1 +1 @@\n")

	maxN := len(oldLines)
	if len(newLines) > maxN {
		maxN = len(newLines)
	}
	for i := 0; i < maxN; i++ {
		hasOld := i < len(oldLines)
		hasNew := i < len(newLines)
		if hasOld && hasNew && oldLines[i] == newLines[i] {
			fmt.Fprintf(&sb, "%4d  %s\n", i+1, oldLines[i])
			continue
		}
		if hasOld {
			fmt.Fprintf(&sb, "%4d -%s\n", i+1, oldLines[i])
		}
		if hasNew {
			fmt.Fprintf(&sb, "%4d +%s\n", i+1, newLines[i])
		}
	}
	return strings.TrimSuffix(sb.String(), "\n")
}

func splitPreserveEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
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
	return fmt.Sprintf("… (%d previous lines, %d total, ctrl+e to expand)", hidden, total),
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
		fmt.Sprintf("… (%d more lines, %d total, ctrl+e to expand)", remaining, total)
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

// formatAskUserBlock extracts a human-readable target and body from ask_user args/result.
// Target shows the first question text. Body renders each question with bullet-style
// options where ● marks the chosen answer and ○ marks the rest. Custom answers
// (not matching any option) are shown with a ✎ prefix.
func formatAskUserBlock(block messageBlock) (target, body string) {
	questions, _ := block.ToolArgs["questions"].([]any)
	if len(questions) == 0 {
		return "", block.ToolResult
	}

	answers := parseAskUserAnswers(block.ToolResult, len(questions))

	var sb strings.Builder
	for i, raw := range questions {
		q, _ := raw.(map[string]any)
		if q == nil {
			continue
		}
		text, _ := q["question"].(string)
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(text)

		answer := ""
		if i < len(answers) {
			answer = answers[i]
		}

		opts := extractStringSlice(q["options"])
		if len(opts) > 0 {
			isCustom := answer != "" && !containsString(opts, answer)
			for _, opt := range opts {
				if opt == answer {
					fmt.Fprintf(&sb, "\n  ● %s", opt)
				} else {
					fmt.Fprintf(&sb, "\n  ○ %s", opt)
				}
			}
			if isCustom {
				fmt.Fprintf(&sb, "\n  ✎ %s", answer)
			}
		} else if answer != "" {
			fmt.Fprintf(&sb, "\n  → %s", answer)
		}

		if i == 0 {
			target = text
			if len(target) > 80 {
				target = target[:77] + "…"
			}
		}
	}
	return target, sb.String()
}

// parseAskUserAnswers extracts individual answers from the tool result.
func parseAskUserAnswers(result string, count int) []string {
	if result == "" {
		return nil
	}
	if count == 1 {
		return []string{strings.TrimSpace(result)}
	}
	answers := make([]string, 0, count)
	for _, line := range strings.Split(result, "\n") {
		if strings.HasPrefix(line, "A: ") {
			answers = append(answers, strings.TrimPrefix(line, "A: "))
		}
	}
	return answers
}

func extractStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func containsString(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
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
