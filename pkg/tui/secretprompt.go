package tui

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/e-aleixandre/moa/pkg/secrets"
)

const (
	secretAlias secretPromptPhase = iota
	secretValue
	secretMore
)

const maxSecretValueSize = 16 << 10

var secretAliasPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type secretPromptPhase int

// secretPrompt replaces the composer while collecting a secret batch. Values
// only ever live in its masked buffer and are never passed through inputModel.
type secretPrompt struct {
	active  bool
	phase   secretPromptPhase
	names   []string
	entries []secrets.Entry
	index   int
	buffer  string
	askMore bool
}

func (p *secretPrompt) Start(names []string) {
	p.active = true
	p.names = append(p.names[:0], names...)
	p.entries = p.entries[:0]
	p.index = 0
	p.buffer = ""
	p.askMore = len(names) == 0
	if p.askMore {
		p.phase = secretAlias
	} else {
		p.phase = secretValue
	}
}

// Cancel drops every in-memory value collected so far.
func (p *secretPrompt) Cancel() {
	for i := range p.entries {
		p.entries[i].Value = ""
	}
	p.active = false
	p.names = nil
	p.entries = nil
	p.index = 0
	p.buffer = ""
	p.askMore = false
}

func (p *secretPrompt) Type(s string) {
	p.buffer += s
}

func (p *secretPrompt) Backspace() {
	p.buffer = trimLastRune(p.buffer)
}

// Submit accepts the current alias or masked value. complete is true once a
// whole batch has been collected.
func (p *secretPrompt) Submit() (complete bool, err error) {
	switch p.phase {
	case secretAlias:
		name := strings.TrimSpace(p.buffer)
		if !secretAliasPattern.MatchString(name) {
			return false, fmt.Errorf("invalid secret alias (use letters, numbers, ., _ or -; max 64 characters)")
		}
		for _, existing := range p.names {
			if strings.EqualFold(existing, name) {
				return false, fmt.Errorf("duplicate secret alias")
			}
		}
		if len(p.names) >= 16 {
			return false, fmt.Errorf("at most 16 secrets are allowed")
		}
		p.names = append(p.names, name)
		p.buffer = ""
		p.phase = secretValue
		return false, nil
	case secretValue:
		if p.buffer == "" {
			return false, fmt.Errorf("secret value cannot be empty")
		}
		if len(p.buffer) > maxSecretValueSize {
			return false, fmt.Errorf("secret value exceeds %d bytes", maxSecretValueSize)
		}
		p.entries = append(p.entries, secrets.Entry{Name: p.names[p.index], Value: p.buffer})
		p.buffer = ""
		p.index++
		if p.index < len(p.names) {
			return false, nil
		}
		if p.askMore {
			p.phase = secretMore
			return false, nil
		}
		return true, nil
	}
	return false, nil
}

func (p *secretPrompt) Entries() []secrets.Entry {
	entries := append([]secrets.Entry(nil), p.entries...)
	p.Cancel()
	return entries
}

func secretBatchStatus(names []string) string {
	if len(names) == 0 {
		return "🔐 Secrets staged\nMoa sent only the directory path and aliases to the model."
	}
	return "🔐 Secrets staged: " + strings.Join(names, ", ") + "\nMoa sent only the directory path and aliases to the model."
}

func secretAliases(custom map[string]any) []string {
	aliases, ok := custom["secret_aliases"]
	if !ok {
		return nil
	}
	switch values := aliases.(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		names := make([]string, 0, len(values))
		for _, value := range values {
			if name, ok := value.(string); ok {
				names = append(names, name)
			}
		}
		return names
	default:
		return nil
	}
}

func (p *secretPrompt) View(width int, theme Theme) string {
	if !p.active {
		return ""
	}

	dim := lipgloss.NewStyle().Foreground(theme.Overlay0)
	label := lipgloss.NewStyle().Foreground(theme.Mauve).Bold(true)
	body := lipgloss.NewStyle().Foreground(theme.Text)
	var lines []string

	switch p.phase {
	case secretAlias:
		lines = append(lines, label.Render("  🔐 Secret alias"))
		lines = append(lines, body.Render("  "+p.buffer+"█"))
		lines = append(lines, dim.Render("  Enter continue · Esc cancel"))
	case secretValue:
		lines = append(lines, label.Render("  🔐 Value for "+p.names[p.index]))
		lines = append(lines, body.Render("  "+strings.Repeat("•", utf8.RuneCountInString(p.buffer))+"█"))
		lines = append(lines, dim.Render("  Value is masked and will not enter chat history · Enter continue · Esc cancel"))
	case secretMore:
		lines = append(lines, label.Render("  🔐 Add another secret?"))
		lines = append(lines, dim.Render("  y add another · Enter/n save batch · Esc cancel"))
	}

	innerWidth := width - 4
	if innerWidth < 30 {
		innerWidth = 30
	}
	return pickerBorderStyle.Width(innerWidth).Render(strings.Join(lines, "\n"))
}
