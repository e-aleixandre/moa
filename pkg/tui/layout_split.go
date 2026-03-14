package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/wordwrap"
)

// SplitLayout renders the v3 split design with per-verb accent colors,
// right-aligned status badges, and generous spacing.
//
// Tool blocks use a single Surface0 background (matching the HTML mockup).
type SplitLayout struct{}

const splitPad = 4 // left padding inside tool blocks (chars)

// verbColor returns the accent color for a tool verb, with a fallback.
func verbColor(action string, t Theme) lipgloss.Color {
	switch action {
	case "write":
		return t.Yellow
	case "bash":
		return t.Peach
	case "read":
		return t.Teal
	case "edit":
		return t.Green
	case "search":
		return t.Mauve
	case "fetch":
		return t.Pink
	default:
		return t.Subtext1
	}
}

func (SplitLayout) RenderUserMessage(text string, width int, theme Theme) string {
	bar := lipgloss.NewStyle().Foreground(theme.Lavender).Render("│")
	label := lipgloss.NewStyle().Foreground(theme.Lavender).Bold(true).Render("YOU")

	// "│ " takes 2 visible chars. Wrap text to fit within width minus that prefix.
	const prefixWidth = 2 // "│ "
	wrapWidth := width - prefixWidth
	if wrapWidth < 20 {
		wrapWidth = 20
	}

	var lines []string
	lines = append(lines, bar+" "+label)

	// Word-wrap each input line so continuation lines also get the bar prefix.
	for _, inputLine := range strings.Split(text, "\n") {
		wrapped := wordwrap.String(inputLine, wrapWidth)
		for _, wl := range strings.Split(wrapped, "\n") {
			lines = append(lines, bar+" "+wl)
		}
	}
	return strings.Join(lines, "\n")
}

func (SplitLayout) RenderSteerMessage(text string, width int, theme Theme) string {
	bar := lipgloss.NewStyle().Foreground(theme.Overlay0).Render("│")
	label := lipgloss.NewStyle().Foreground(theme.Overlay0).Render("YOU (queued)")

	const prefixWidth = 2
	wrapWidth := width - prefixWidth
	if wrapWidth < 20 {
		wrapWidth = 20
	}

	dim := lipgloss.NewStyle().Foreground(theme.Overlay1)
	var lines []string
	lines = append(lines, bar+" "+label)
	for _, inputLine := range strings.Split(text, "\n") {
		wrapped := wordwrap.String(inputLine, wrapWidth)
		for _, wl := range strings.Split(wrapped, "\n") {
			lines = append(lines, bar+" "+dim.Render(wl))
		}
	}
	return strings.Join(lines, "\n")
}

func (SplitLayout) RenderThinking(text string, width int, theme Theme) string {
	return lipgloss.NewStyle().
		Foreground(theme.Overlay0).
		Width(width - 2).
		PaddingLeft(2).
		Render(text)
}

func (SplitLayout) RenderAssistantText(glamourRendered string, _ int) string {
	if glamourRendered == "" {
		return glamourRendered
	}
	pad := strings.Repeat(" ", assistantPadChars)
	var b strings.Builder
	for i, line := range strings.Split(glamourRendered, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		if line != "" {
			b.WriteString(pad)
			b.WriteString(line)
		}
	}
	return b.String()
}

func (SplitLayout) RenderToolBlock(block ToolBlockData, width int, theme Theme) string {
	if width < 20 {
		width = 20
	}

	bg := theme.Surface0
	bgS := lipgloss.NewStyle().Background(bg)
	pad := bgS.Render(strings.Repeat(" ", splitPad))

	vc := verbColor(block.Action, theme)
	actionStr := lipgloss.NewStyle().Foreground(vc).Bold(true).Background(bg).Render(block.Action)

	// Badge: right-aligned status indicator
	badge := splitBadge(block, theme, bg)
	badgeWidth := lipgloss.Width(badge)

	// Target: truncated to fit between action and badge
	var targetStr string
	if block.Target != "" {
		targetStyle := lipgloss.NewStyle().Foreground(theme.Subtext1).Background(bg)
		actionWidth := lipgloss.Width(actionStr) + lipgloss.Width(pad)
		available := width - actionWidth - badgeWidth - 2
		if available > 3 {
			t := block.Target
			if lipgloss.Width(t) > available {
				t = truncateToWidth(t, available-1) + "…"
			}
			targetStr = bgS.Render(" ") + targetStyle.Render(t)
		}
	}

	leftContent := pad + actionStr + targetStr

	// Fill gap between left content and badge
	leftWidth := lipgloss.Width(leftContent)
	gap := width - leftWidth - badgeWidth
	if gap < 0 {
		gap = 0
	}
	gapStr := bgS.Render(strings.Repeat(" ", gap))
	headerLine := leftContent + gapStr + badge

	var lines []string

	// Top padding
	lines = append(lines, bgEmptyLine(width, bg))
	lines = append(lines, headerLine)

	hasBody := block.Header != "" || block.Body != "" || block.Footer != ""

	if hasBody {
		lines = append(lines, bgEmptyLine(width, bg))
	}

	if block.Header != "" {
		headerNotice := pad + lipgloss.NewStyle().
			Foreground(theme.Overlay0).Italic(true).Background(bg).
			Render(block.Header)
		lines = append(lines, bgPadRight(headerNotice, width, bg))
	}

	if block.Body != "" {
		bodyStyle := lipgloss.NewStyle().Foreground(theme.Subtext1).Background(bg)
		if block.IsError {
			bodyStyle = lipgloss.NewStyle().Foreground(theme.Maroon).Background(bg)
		}
		diffAdd := lipgloss.NewStyle().Foreground(theme.Green).Background(bg)
		diffDel := lipgloss.NewStyle().Foreground(theme.Red).Background(bg)
		diffHunk := lipgloss.NewStyle().Foreground(theme.Overlay0).Background(bg)
		stderrStyle := lipgloss.NewStyle().Foreground(theme.Maroon).Background(bg)
		inStderr := false
		for _, bl := range strings.Split(block.Body, "\n") {
			if bl == "STDERR:" {
				inStderr = true
				content := pad + stderrStyle.Bold(true).Render(bl)
				lines = append(lines, bgPadRight(content, width, bg))
				continue
			}
			style := bodyStyle
			if inStderr {
				style = stderrStyle
			} else if block.IsDiff {
				switch {
				case strings.HasPrefix(bl, "+"):
					style = diffAdd
				case strings.HasPrefix(bl, "-"):
					style = diffDel
				case strings.HasPrefix(bl, "@@"):
					style = diffHunk
				}
			}
			content := pad + style.Render(bl)
			lines = append(lines, bgPadRight(content, width, bg))
		}
	}

	if block.Footer != "" {
		footerNotice := pad + lipgloss.NewStyle().
			Foreground(theme.Overlay0).Italic(true).Background(bg).
			Render(block.Footer)
		lines = append(lines, bgPadRight(footerNotice, width, bg))
	}

	// Bottom padding
	lines = append(lines, bgEmptyLine(width, bg))

	return strings.Join(lines, "\n")
}

// splitBadge renders a right-aligned status badge.
func splitBadge(block ToolBlockData, theme Theme, bg lipgloss.Color) string {
	var text string
	var fg lipgloss.Color

	switch {
	case block.IsError:
		text = " ERROR "
		fg = theme.Red
	case block.Done:
		text = " DONE "
		fg = theme.Green
	default:
		text = " RUNNING "
		fg = theme.Yellow
	}

	return lipgloss.NewStyle().
		Foreground(fg).
		Bold(true).
		Background(bg).
		Render(text)
}

func (SplitLayout) RenderError(text string, _ int, theme Theme) string {
	return lipgloss.NewStyle().Foreground(theme.Red).Render(text)
}

func (SplitLayout) RenderStatus(text string, _ int, theme Theme) string {
	return lipgloss.NewStyle().Foreground(theme.Overlay1).Render(text)
}

func (SplitLayout) RenderLiveNotice(text string, width int, theme Theme) string {
	text = strings.ReplaceAll(text, "\n", " ")
	if width > 0 {
		text = truncateVisible(text, width)
	}
	return lipgloss.NewStyle().Foreground(theme.Overlay1).Render(text)
}

// truncateToWidth truncates a string to maxWidth visible characters.
func truncateToWidth(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxWidth {
		return s
	}
	return string(runes[:maxWidth])
}
