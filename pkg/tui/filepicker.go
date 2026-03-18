package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const (
	filePickerMaxVisible = 8
	filePickerMaxResults = 50
)

// filePicker shows a filterable file list when the user types "@".
type filePicker struct {
	active  bool
	filter  string // text after "@" used to filter
	matches []fileEntry
	cursor  int
	scroll  int // first visible index
	workDir string
	cache   []fileEntry // lazily populated file list
}

type fileEntry struct {
	path    string // relative to workdir
	isDir   bool

}

func (p *filePicker) SetWorkDir(dir string) {
	if dir != p.workDir {
		p.workDir = dir
		p.cache = nil // invalidate on dir change
	}
}

// Invalidate forces a rescan on next activation.
func (p *filePicker) Invalidate() {
	p.cache = nil
}

// Update checks if the current text triggers the file picker.
// It looks for an "@" token at or before the cursor position.
func (p *filePicker) Update(text string, cursorPos int) {
	// Find the @ that starts the current mention.
	// Walk backwards from cursor to find "@".
	atIdx := -1
	for i := cursorPos - 1; i >= 0; i-- {
		ch := text[i]
		if ch == '@' {
			atIdx = i
			break
		}
		// Stop at whitespace or newline — no @ in this token.
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' {
			break
		}
	}

	if atIdx < 0 {
		p.active = false
		return
	}

	// The @ must be at start of line or preceded by whitespace.
	if atIdx > 0 {
		prev := text[atIdx-1]
		if prev != ' ' && prev != '\t' && prev != '\n' && prev != '\r' {
			p.active = false
			return
		}
	}

	filter := text[atIdx+1 : cursorPos]

	// If there's a space in the filter, the mention is done.
	if strings.ContainsAny(filter, " \t\n\r") {
		p.active = false
		return
	}

	p.active = true
	p.filter = filter
	p.matches = p.filterFiles(filter)

	// Clamp cursor
	if p.cursor >= len(p.matches) {
		p.cursor = max(0, len(p.matches)-1)
	}
	if p.cursor < p.scroll {
		p.scroll = p.cursor
	}
}

func (p *filePicker) MoveUp() {
	if p.cursor > 0 {
		p.cursor--
		if p.cursor < p.scroll {
			p.scroll = p.cursor
		}
	}
}

func (p *filePicker) MoveDown() {
	if p.cursor < len(p.matches)-1 {
		p.cursor++
		if p.cursor >= p.scroll+filePickerMaxVisible {
			p.scroll = p.cursor - filePickerMaxVisible + 1
		}
	}
}

// Selected returns the highlighted file path, or "".
func (p *filePicker) Selected() string {
	if !p.active || len(p.matches) == 0 {
		return ""
	}
	return p.matches[p.cursor].path
}

// SelectedIsDir returns true if the highlighted entry is a directory.
func (p *filePicker) SelectedIsDir() bool {
	if !p.active || len(p.matches) == 0 {
		return false
	}
	return p.matches[p.cursor].isDir
}

func (p *filePicker) Close() {
	p.active = false
	p.filter = ""
	p.cursor = 0
	p.scroll = 0
}

func (p *filePicker) View(width int, theme Theme) string {
	if !p.active || len(p.matches) == 0 {
		return ""
	}

	dim := lipgloss.NewStyle().Foreground(theme.Overlay0)
	nameStyle := lipgloss.NewStyle().Foreground(theme.Green)
	dirStyle := lipgloss.NewStyle().Foreground(theme.Blue)
	sel := lipgloss.NewStyle().Foreground(theme.Text).Bold(true)

	// Windowed view
	end := p.scroll + filePickerMaxVisible
	if end > len(p.matches) {
		end = len(p.matches)
	}
	visible := p.matches[p.scroll:end]

	var lines []string
	for vi, entry := range visible {
		i := p.scroll + vi
		cursor := "  "
		if i == p.cursor {
			cursor = "▸ "
		}

		icon := "📄"
		style := nameStyle
		if entry.isDir {
			icon = "📁"
			style = dirStyle
		}

		var line string
		if i == p.cursor {
			line = fmt.Sprintf("%s%s %s", cursor, icon, sel.Render(entry.path))
		} else {
			line = fmt.Sprintf("%s%s %s", cursor, icon, style.Render(entry.path))
		}

		// Show parent dir hint if the filename alone might be ambiguous
		dir := filepath.Dir(entry.path)
		if dir != "." && i != p.cursor {
			line += "  " + dim.Render(dir)
		}

		lines = append(lines, line)
	}

	// Scroll indicator
	total := len(p.matches)
	if total > filePickerMaxVisible {
		info := fmt.Sprintf(" %d/%d", p.cursor+1, total)
		lines = append(lines, dim.Render(info))
	}

	content := strings.Join(lines, "\n")
	innerWidth := width - 4
	if innerWidth < 30 {
		innerWidth = 30
	}
	return pickerBorderStyle.Width(innerWidth).Render(content)
}

func (p *filePicker) filterFiles(filter string) []fileEntry {
	if p.cache == nil {
		p.cache = p.scanFiles()
	}

	if filter == "" {
		if len(p.cache) > filePickerMaxResults {
			return p.cache[:filePickerMaxResults]
		}
		return p.cache
	}

	lower := strings.ToLower(filter)
	var exact, prefix, contains []fileEntry

	for _, entry := range p.cache {
		lp := strings.ToLower(entry.path)
		base := strings.ToLower(filepath.Base(entry.path))

		if lp == lower {
			exact = append(exact, entry)
		} else if strings.HasPrefix(lp, lower) || strings.HasPrefix(base, lower) {
			prefix = append(prefix, entry)
		} else if strings.Contains(lp, lower) {
			contains = append(contains, entry)
		}

		if len(exact)+len(prefix)+len(contains) >= filePickerMaxResults {
			break
		}
	}

	result := make([]fileEntry, 0, len(exact)+len(prefix)+len(contains))
	result = append(result, exact...)
	result = append(result, prefix...)
	result = append(result, contains...)
	return result
}

func (p *filePicker) scanFiles() []fileEntry {
	if p.workDir == "" {
		return nil
	}

	var entries []fileEntry

	// Use a simple walk, respecting .gitignore-like patterns.
	skipDirs := map[string]bool{
		".git":         true,
		"node_modules": true,
		"vendor":       true,
		".next":        true,
		"dist":         true,
		"build":        true,
		"__pycache__":  true,
		".venv":        true,
		".cache":       true,
		".idea":        true,
		".vscode":      true,
	}

	_ = filepath.WalkDir(p.workDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()

		// Skip hidden dirs (except root).
		if d.IsDir() && path != p.workDir {
			if skipDirs[name] || (strings.HasPrefix(name, ".") && name != ".") {
				return filepath.SkipDir
			}
		}

		// Skip root itself.
		if path == p.workDir {
			return nil
		}

		rel, err := filepath.Rel(p.workDir, path)
		if err != nil {
			return nil
		}

		entries = append(entries, fileEntry{
			path:  rel,
			isDir: d.IsDir(),
		})

		// Cap total to avoid scanning huge repos.
		if len(entries) > 5000 {
			return filepath.SkipAll
		}
		return nil
	})

	// Sort: directories first, then alphabetical.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].isDir != entries[j].isDir {
			return entries[i].isDir
		}
		return entries[i].path < entries[j].path
	})

	return entries
}

// --- Tab path completion ---

// tabCompletePath attempts to complete a path-like token at the cursor position.
// Returns the completed text and true if a completion was found.
func tabCompletePath(text string, cursorPos int, workDir string) (newText string, newCursor int, ok bool) {
	if cursorPos > len(text) {
		cursorPos = len(text)
	}

	// Find the start of the current token (walk backwards from cursor).
	start := cursorPos
	for start > 0 {
		ch := text[start-1]
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' {
			break
		}
		start--
	}

	token := text[start:cursorPos]
	if token == "" {
		return "", 0, false
	}

	// Only complete if it looks like a path (contains / or . prefix).
	if !strings.Contains(token, "/") && !strings.HasPrefix(token, ".") && !strings.Contains(token, string(os.PathSeparator)) {
		return "", 0, false
	}

	// Resolve the path.
	var dir, prefix string
	resolved := token
	if !filepath.IsAbs(resolved) && workDir != "" {
		resolved = filepath.Join(workDir, resolved)
	}

	// Check if the token ends with a separator — list directory contents.
	if strings.HasSuffix(token, "/") || strings.HasSuffix(token, string(os.PathSeparator)) {
		dir = resolved
		prefix = ""
	} else {
		dir = filepath.Dir(resolved)
		prefix = filepath.Base(resolved)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", 0, false
	}

	// Find matches.
	lower := strings.ToLower(prefix)
	var matches []os.DirEntry
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(prefix, ".") {
			continue // skip hidden unless explicitly typing dot
		}
		if prefix == "" || strings.HasPrefix(strings.ToLower(name), lower) {
			matches = append(matches, e)
		}
	}

	if len(matches) == 0 {
		return "", 0, false
	}

	// Find common prefix among matches.
	common := matches[0].Name()
	for _, m := range matches[1:] {
		common = commonPrefix(common, m.Name())
	}

	if len(common) <= len(prefix) && len(matches) > 1 {
		// No extension possible — cycle or show options? For now, no-op.
		return "", 0, false
	}

	// Build the completed token, preserving the user's original prefix style.
	var tokenDir string
	if strings.HasSuffix(token, "/") || strings.HasSuffix(token, string(os.PathSeparator)) {
		tokenDir = token
	} else {
		tokenDir = filepath.Dir(token)
		if tokenDir == "." {
			// Preserve "./" if the user typed it explicitly.
			if strings.HasPrefix(token, "./") || strings.HasPrefix(token, "."+string(os.PathSeparator)) {
				tokenDir = "./"
			} else {
				tokenDir = ""
			}
		} else {
			tokenDir += "/"
		}
	}

	completed := tokenDir + common
	// Add trailing slash for directories (single match).
	if len(matches) == 1 && matches[0].IsDir() {
		completed += "/"
	}

	newText = text[:start] + completed + text[cursorPos:]
	newCursor = start + len(completed)
	return newText, newCursor, true
}

func commonPrefix(a, b string) string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return a[:i]
		}
	}
	return a[:n]
}
