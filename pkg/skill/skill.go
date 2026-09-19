package skill

import (
	"io"
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

	// Builtin marks a skill shipped inside the binary rather than found on
	// disk, so Load reads it from the embedded filesystem and Dir stays empty.
	Builtin bool

	// DisableModelInvocation keeps the skill out of the system prompt index and
	// out of the model's reach: only the user invokes it, with "/<name>". Use it
	// for skills that are occasionally useful but would otherwise cost tokens on
	// every request.
	DisableModelInvocation bool
	// UserInvocable is false for skills that are background knowledge rather than
	// an action worth offering in the slash menu. Defaults to true.
	UserInvocable bool

	// Context is "fork" to run the skill in an isolated subagent with no
	// inherited messages. Any other value (including empty) loads the body
	// inline, which is how every skill behaved before this field existed.
	Context string
	// Background, when true with Context=="fork", launches the child without
	// blocking: the parent keeps working and receives the child's result later,
	// through the usual subagent completion notification. Ignored for inline
	// skills. Defaults to false.
	Background bool
	// ParentTranscript is "snapshot" to freeze the parent's active transcript
	// branch and hand the child a path to that file. Defaults to none.
	ParentTranscript string
}

// ModelInvocable reports whether the model may load this skill on its own.
func (s Skill) ModelInvocable() bool { return !s.DisableModelInvocation }

// IsFork reports whether the skill runs as an isolated subagent.
func (s Skill) IsFork() bool { return strings.EqualFold(s.Context, "fork") }

// WantsParentSnapshot reports whether a forked skill should receive a frozen
// copy of the parent's active transcript branch.
func (s Skill) WantsParentSnapshot() bool {
	return s.IsFork() && strings.EqualFold(s.ParentTranscript, "snapshot")
}

// Options select which sources Discover reads.
type Options struct {
	// Builtin includes the skills embedded in the binary. It is off by default:
	// a built-in skill answers one kind of session, and listing it everywhere
	// would spend every other session's prompt advertising something it has no
	// use for.
	Builtin bool
}

// Discover scans skill directories and returns available skills.
// Project-level skills (.moa/skills/) override global ones (~/.config/moa/skills/)
// when they share the same name; both override a built-in of the same name, so
// a user can iterate on a shipped skill by copying it into their own directory.
// Results are sorted by name.
func Discover(cwd string, opts ...Options) []Skill {
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	skills := make(map[string]Skill)

	// Built-in skills (lowest priority).
	if o.Builtin {
		for name, s := range builtinSkills() {
			skills[name] = s
		}
	}

	// Global skills.
	if dir := core.ConfigSubdir("skills"); dir != "" {
		scanDir(dir, skills, true)
	}

	// Project skill entries remain ordinary directories. Activation links are
	// reserved for the user-controlled global skills directory.
	scanDir(filepath.Join(cwd, ".moa", "skills"), skills, false)

	result := make([]Skill, 0, len(skills))
	for _, s := range skills {
		result = append(result, s)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// maxSkillBytes caps how much of a SKILL.md is loaded. A skill's content is
// copied into the conversation and stays there, so an oversized file would
// silently eat the context window. The cap matches the read tool's text
// ceiling; skills are meant to be concise, and reference material belongs in
// supporting files the agent reads on demand.
const maxSkillBytes = 50 * 1024

// Load reads the full SKILL.md content for a skill, without its frontmatter:
// the header configures moa, and feeding it to the model would spend tokens on
// keys it cannot act on.
//
// Content past the cap is truncated with a marker rather than refused, so an
// oversized skill still works instead of failing at the moment it is invoked.
func Load(s Skill) (string, error) {
	var (
		data []byte
		err  error
	)
	if s.Builtin {
		data, err = builtinFS.ReadFile(builtinPath(s.Name))
	} else {
		data, err = os.ReadFile(filepath.Join(s.Dir, skillFile))
	}
	if err != nil {
		return "", err
	}
	body := stripFrontmatter(string(data))
	if len(body) > maxSkillBytes {
		body = body[:maxSkillBytes] + "\n\n[skill truncated: over 50KB]\n"
	}
	return body, nil
}

// RenderBody substitutes the invocation's arguments into the skill.
//
// A skill that declares no placeholder still receives what the user typed,
// appended as a trailing line: dropping it silently would lose the only part of
// the invocation the user wrote by hand.
func RenderBody(body string, args []string) string {
	joined := strings.Join(args, " ")
	if strings.Contains(body, "$ARGUMENTS") {
		return strings.ReplaceAll(body, "$ARGUMENTS", joined)
	}
	if joined == "" {
		return body
	}
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return body + "\nARGUMENTS: " + joined + "\n"
}

// scanDir reads all <dir>/<name>/SKILL.md entries and adds them to the map.
// Global skill directories may be symlinks so ~/.config/moa/skills can act as
// an activation registry without mixing third-party checkouts into a personal
// skills repository. Project symlinks are deliberately not followed.
func scanDir(dir string, out map[string]Skill, followSymlinks bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		mode := e.Type()
		// Some filesystems report DT_UNKNOWN. Info performs the fallback stat
		// needed to distinguish a real directory from a symlink there.
		if mode == 0 {
			info, err := e.Info()
			if err != nil {
				continue
			}
			mode = info.Mode()
		}
		isDir := mode.IsDir()
		if !isDir && followSymlinks && mode&os.ModeSymlink != 0 {
			info, err := os.Stat(filepath.Join(dir, e.Name()))
			isDir = err == nil && info.IsDir()
		}
		if !isDir {
			continue
		}
		name := e.Name()
		path := filepath.Join(dir, name, skillFile)
		data, err := readHead(path)
		if err != nil {
			continue
		}
		absDir, _ := filepath.Abs(filepath.Join(dir, name))
		s := newSkill(name, data)
		s.Dir = absDir
		out[name] = s
	}
}

// readHead reads as much of a skill file as discovery can possibly need: the
// frontmatter and the first paragraph both live at the top, and Load caps the
// body at the same ceiling anyway.
//
// Discovery runs when a session is built and on every reload, for every skill
// on disk. Reading each file whole would make one oversized SKILL.md — a
// vendored document dropped into the skills directory — expensive on a path
// that never uses more than its first lines.
func readHead(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxSkillBytes))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// newSkill builds a skill from the raw SKILL.md content. Name is the directory
// the file was found in, which is what the agent and the user type.
func newSkill(name, content string) Skill {
	displayName, desc := parseSkillHeader(content)
	if displayName == "" {
		displayName = name
	}
	fm := parseFrontmatter(content)
	return Skill{
		Name:                   name,
		DisplayName:            displayName,
		Description:            desc,
		DisableModelInvocation: fm.boolField("disable-model-invocation", false),
		UserInvocable:          fm.boolField("user-invocable", true),
		Context:                fm.field("context"),
		Background:             fm.boolField("background", false),
		ParentTranscript:       fm.field("parent-transcript"),
	}
}

// parseSkillHeader reads the first # heading and the first paragraph after it.
func parseSkillHeader(content string) (displayName, description string) {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	foundHeading := false
	start := 0

	// A frontmatter block is configuration, not content: skip past it so the
	// heading below it is still found.
	if len(lines) > 0 {
		start = 1
		if strings.TrimSpace(lines[0]) == "---" {
			start = len(lines)
			for i := 1; i < len(lines); i++ {
				if strings.TrimSpace(lines[i]) == "---" {
					start = i + 1
					break
				}
			}
		} else if trimmed := strings.TrimSpace(lines[0]); strings.HasPrefix(trimmed, "# ") {
			displayName = strings.TrimSpace(trimmed[2:])
			foundHeading = true
		}
	}

	for _, line := range lines[min(start, len(lines)):] {
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
