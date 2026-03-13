package skill

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const skillFile = "SKILL.md"

// FormatIndex returns a pre-formatted skills index for the system prompt.
// Returns empty string if there are no skills.
func FormatIndex(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Available skills (use the load_skill tool to load when relevant):\n")
	for _, s := range skills {
		sb.WriteString("- ")
		sb.WriteString(s.Name)
		sb.WriteString(": ")
		sb.WriteString(s.DisplayName)
		if s.Description != "" {
			sb.WriteString(" — ")
			sb.WriteString(s.Description)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// Skill represents a loadable knowledge pack.
type Skill struct {
	Name        string // directory name (e.g., "go-testing")
	DisplayName string // from first # heading in SKILL.md
	Description string // first paragraph after heading
	Dir         string // absolute path to skill directory
}

// Discover scans skill directories and returns available skills.
// Project-level skills (.moa/skills/) override global ones (~/.config/moa/skills/)
// when they share the same name. Results are sorted by name.
func Discover(cwd string) []Skill {
	skills := make(map[string]Skill)

	// Global skills (lower priority).
	if home, err := os.UserHomeDir(); err == nil {
		scanDir(filepath.Join(home, ".config", "moa", "skills"), skills)
	}

	// Project skills (higher priority — overwrites global by name).
	scanDir(filepath.Join(cwd, ".moa", "skills"), skills)

	result := make([]Skill, 0, len(skills))
	for _, s := range skills {
		result = append(result, s)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// Load reads the full SKILL.md content for a skill.
func Load(s Skill) (string, error) {
	data, err := os.ReadFile(filepath.Join(s.Dir, skillFile))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// scanDir reads all <dir>/<name>/SKILL.md entries and adds them to the map.
func scanDir(dir string, out map[string]Skill) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		path := filepath.Join(dir, name, skillFile)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		absDir, _ := filepath.Abs(filepath.Join(dir, name))
		displayName, desc := parseSkillHeader(path)
		if displayName == "" {
			displayName = name
		}
		out[name] = Skill{
			Name:        name,
			DisplayName: displayName,
			Description: desc,
			Dir:         absDir,
		}
	}
}

// parseSkillHeader reads the first # heading and the first paragraph after it.
func parseSkillHeader(path string) (displayName, description string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	foundHeading := false

	for scanner.Scan() {
		line := scanner.Text()

		if !foundHeading {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "# ") {
				displayName = strings.TrimSpace(trimmed[2:])
				foundHeading = true
			}
			continue
		}

		// After heading: skip blank lines, collect first non-blank paragraph.
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if description != "" {
				// End of first paragraph.
				break
			}
			continue
		}
		// Skip if the next content is another heading or a list — not a description.
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			break
		}
		if description != "" {
			description += " "
		}
		description += trimmed
	}

	return displayName, description
}
