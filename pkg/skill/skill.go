package skill

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/e-aleixandre/moa/pkg/core"
)

const skillFile = "SKILL.md"

// FormatIndex returns a pre-formatted skills index for the system prompt.
// Returns empty string if there are no skills.
//
// Skills the model may not invoke are left out entirely: listing one would spend
// prompt tokens on every request of the session to advertise something the model
// cannot call. Those are reached by the user typing "/<name>".
func FormatIndex(skills []Skill) string {
	var sb strings.Builder
	for _, s := range skills {
		if !s.ModelInvocable() {
			continue
		}
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
	if sb.Len() == 0 {
		return ""
	}
	return "Available skills (use the load_skill tool to load when relevant):\n" + sb.String()
}

// Skill represents a loadable knowledge pack.
type Skill struct {
	Name        string // directory name (e.g., "go-testing")
	DisplayName string // from first # heading in SKILL.md
	Description string // first paragraph after heading
	Dir         string // absolute path to skill directory

	// DisableModelInvocation keeps the skill out of the system prompt index and
	// out of the model's reach: only the user invokes it, with "/<name>". Use it
	// for skills that are occasionally useful but would otherwise cost tokens on
	// every request.
	DisableModelInvocation bool
	// UserInvocable is false for skills that are background knowledge rather than
	// an action worth offering in the slash menu. Defaults to true.
	UserInvocable bool
}

// ModelInvocable reports whether the model may load this skill on its own.
func (s Skill) ModelInvocable() bool { return !s.DisableModelInvocation }

// Discover scans skill directories and returns available skills.
// Project-level skills (.moa/skills/) override global ones (~/.config/moa/skills/)
// when they share the same name. Results are sorted by name.
func Discover(cwd string) []Skill {
	skills := make(map[string]Skill)

	// Global skills (lower priority).
	if dir := core.ConfigSubdir("skills"); dir != "" {
		scanDir(dir, skills)
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

// Load reads the full SKILL.md content for a skill, without its frontmatter:
// the header configures moa, and feeding it to the model would spend tokens on
// keys it cannot act on.
func Load(s Skill) (string, error) {
	data, err := os.ReadFile(filepath.Join(s.Dir, skillFile))
	if err != nil {
		return "", err
	}
	return stripFrontmatter(string(data)), nil
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
		fm := parseFrontmatter(path)
		out[name] = Skill{
			Name:                   name,
			DisplayName:            displayName,
			Description:            desc,
			Dir:                    absDir,
			DisableModelInvocation: fm.boolField("disable-model-invocation", false),
			UserInvocable:          fm.boolField("user-invocable", true),
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

	// A frontmatter block is configuration, not content: skip past it so the
	// heading below it is still found.
	if scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "---" {
			for scanner.Scan() {
				if strings.TrimSpace(scanner.Text()) == "---" {
					break
				}
			}
		} else if trimmed := strings.TrimSpace(scanner.Text()); strings.HasPrefix(trimmed, "# ") {
			displayName = strings.TrimSpace(trimmed[2:])
			foundHeading = true
		}
	}

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
