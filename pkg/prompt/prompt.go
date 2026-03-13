package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var placeholderRe = regexp.MustCompile(`\{\{(\w+)\}\}`)

// Template represents a reusable prompt template.
type Template struct {
	Name         string   // filename without .md extension
	Path         string   // absolute path to the template file
	Placeholders []string // unique placeholder names (e.g., ["file"])
	Content      string   // raw template content
}

// Discover scans template directories and returns available templates.
// Project templates (.moa/prompts/) override global ones (~/.config/moa/prompts/)
// when they share the same name. Results are sorted by name.
func Discover(cwd string) []Template {
	templates := make(map[string]Template)

	// Global templates (lower priority).
	if home, err := os.UserHomeDir(); err == nil {
		scanTemplateDir(filepath.Join(home, ".config", "moa", "prompts"), templates)
	}

	// Project templates (higher priority — overwrites global by name).
	scanTemplateDir(filepath.Join(cwd, ".moa", "prompts"), templates)

	result := make([]Template, 0, len(templates))
	for _, t := range templates {
		result = append(result, t)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// Render replaces {{placeholder}} with values from the map.
// Returns an error if any placeholder has no corresponding value.
func Render(t Template, values map[string]string) (string, error) {
	result := t.Content
	for _, p := range t.Placeholders {
		val, ok := values[p]
		if !ok {
			return "", fmt.Errorf("missing value for placeholder {{%s}}", p)
		}
		result = strings.ReplaceAll(result, "{{"+p+"}}", val)
	}
	return result, nil
}

func scanTemplateDir(dir string, out map[string]Template) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		content := string(data)
		absPath, _ := filepath.Abs(path)
		out[name] = Template{
			Name:         name,
			Path:         absPath,
			Placeholders: extractPlaceholders(content),
			Content:      content,
		}
	}
}

// extractPlaceholders returns unique placeholder names in order of first appearance.
func extractPlaceholders(content string) []string {
	matches := placeholderRe.FindAllStringSubmatch(content, -1)
	seen := make(map[string]bool)
	var result []string
	for _, m := range matches {
		name := m[1]
		if !seen[name] {
			seen[name] = true
			result = append(result, name)
		}
	}
	return result
}
