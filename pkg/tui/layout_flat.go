package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/wordwrap"
)

// FlatLayout renders the original v1 flat design with single-tone tool blocks
// and an inline "running…" indicator.
type FlatLayout struct{}

func (FlatLayout) RenderUserMessage(text string, width int, theme Theme) string {
	prefix := lipgloss.NewStyle().Foreground(theme.Blue).Bold(true).Render("❯ ")

	// "❯ " takes 2 visible chars. Wrap text to fit.
	const prefixWidth = 2
	wrapWidth := width - prefixWidth
	if wrapWidth < 20 {
		wrapWidth = 20
	}

	wrapped := wordwrap.String(text, wrapWidth)
	lines := strings.Split(wrapped, "\n")
	pad := strings.Repeat(" ", prefixWidth)

	var parts []string
	for i, line := range lines {
		if i == 0 {
			parts = append(parts, prefix+line)
		} else {
			parts = append(parts, pad+line)
		}
	}
	return strings.Join(parts, "\n")
}

func (FlatLayout) RenderSteerMessage(text string, width int, theme Theme) string {
	prefix := lipgloss.NewStyle().Foreground(theme.Overlay0).Render("❯ ")
	dim := lipgloss.NewStyle().Foreground(theme.Overlay1)
	label := lipgloss.NewStyle().Foreground(theme.Overlay0).Render(" (queued)")

	const prefixWidth = 2
	wrapWidth := width - prefixWidth
	if wrapWidth < 20 {
		wrapWidth = 20
	}

	wrapped := wordwrap.String(text, wrapWidth)
	lines := strings.Split(wrapped, "\n")
	pad := strings.Repeat(" ", prefixWidth)

	var parts []string
	for i, line := range lines {
		if i == 0 {
			parts = append(parts, prefix+dim.Render(line)+label)
		} else {
			parts = append(parts, pad+dim.Render(line))
		}
	}
	return strings.Join(parts, "\n")
}

func (FlatLayout) RenderThinking(text string, width int, theme Theme) string {
	return lipgloss.NewStyle().
		Foreground(theme.Overlay0).
		Width(width - 2).
		PaddingLeft(2).
		Render(text)
}

func (FlatLayout) RenderAssistantText(glamourRendered string, _ int) string {
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

func (FlatLayout) RenderToolBlock(block ToolBlockData, width int, theme Theme) string {
	bg := theme.Surface0
	bgS := lipgloss.NewStyle().Background(bg)
	pad := bgS.Render(strings.Repeat(" ", splitPad))

	actionStyle := lipgloss.NewStyle().Foreground(theme.Green).Bold(true)
	targetStyle := lipgloss.NewStyle().Foreground(theme.Peach)
	bodyStyle := lipgloss.NewStyle().Foreground(theme.Subtext1)
	if block.IsError {
		bodyStyle = lipgloss.NewStyle().Foreground(theme.Maroon)
	}
	footerStyle := lipgloss.NewStyle().Foreground(theme.Overlay0).Italic(true)
	dimStyle := lipgloss.NewStyle().Foreground(theme.Overlay0)

	// Title line: action [target] [running…]
	title := pad + actionStyle.Background(bg).Render(block.Action)
	if block.Target != "" {
		title += bgS.Render(" ") + targetStyle.Background(bg).Render(block.Target)
	}
	if !block.Done {
		title += bgS.Render(" ") + dimStyle.Background(bg).Render("running…")
	}

	var lines []string

	// Top padding
	lines = append(lines, bgEmptyLine(width, bg))
	lines = append(lines, bgPadRight(title, width, bg))

	hasBody := block.Header != "" || block.Body != "" || block.Footer != ""
	if hasBody {
		lines = append(lines, bgEmptyLine(width, bg))
	}

	if block.Header != "" {
		lines = append(lines, bgPadRight(
			pad+footerStyle.Background(bg).Render(block.Header),
			width, bg))
	}

	if block.Body != "" {
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
			style := bodyStyle.Background(bg)
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
		lines = append(lines, bgPadRight(
			pad+footerStyle.Background(bg).Render(block.Footer),
			width, bg))
	}

	// Bottom padding
	lines = append(lines, bgEmptyLine(width, bg))

	return strings.Join(lines, "\n")
}

func (FlatLayout) RenderError(text string, _ int, theme Theme) string {
	return lipgloss.NewStyle().Foreground(theme.Red).Render(text)
}

func (FlatLayout) RenderStatus(text string, _ int, theme Theme) string {
	return lipgloss.NewStyle().Foreground(theme.Overlay1).Render(text)
}

func (FlatLayout) RenderLiveNotice(text string, width int, theme Theme) string {
	text = strings.ReplaceAll(text, "\n", " ")
	if width > 0 {
		text = truncateVisible(text, width)
	}
	return lipgloss.NewStyle().Foreground(theme.Overlay1).Render(text)
}
