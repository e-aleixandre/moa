package skill

import (
	"bufio"
	"os"
	"strings"
)

// frontmatter is the YAML-ish header of a SKILL.md, kept as raw key/value pairs.
// Skills follow the Agent Skills convention, so the file may carry fields moa
// does not understand; unknown keys are preserved-by-ignoring rather than
// rejected, and a skill written for another harness still loads here.
type frontmatter map[string]string

// boolField reads a boolean field, falling back to def when absent or
// unparseable. The convention accepts more than Go's strconv: yes/no/on/off/1/0
// in any case, so a skill written that way behaves as its author intended
// instead of silently taking the default.
func (f frontmatter) boolField(key string, def bool) bool {
	raw, ok := f[key]
	if !ok {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "yes", "on", "1":
		return true
	case "false", "no", "off", "0":
		return false
	}
	return def
}

// stripFrontmatter removes the leading "---" block from skill content. The
// opening marker must be the file's first line, so a document that starts with
// a horizontal rule keeps all of its text.
func stripFrontmatter(content string) string {
	rest, ok := strings.CutPrefix(content, "---\n")
	if !ok {
		return content
	}
	_, body, ok := strings.Cut(rest, "\n---")
	if !ok {
		// Unterminated block: it was never frontmatter, keep the text as is.
		return content
	}
	if _, after, ok := strings.Cut(body, "\n"); ok {
		return after
	}
	return ""
}

// parseFrontmatter reads the YAML block delimited by "---" at the very top of
// the file. Per the convention the opening marker must be the first line;
// otherwise the whole file — markers included — is skill content, so a document
// that merely uses a horizontal rule is not mistaken for configuration.
func parseFrontmatter(path string) frontmatter {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "---" {
		return nil
	}

	fm := frontmatter{}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "---" {
			return fm
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		fm[key] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	// Unterminated block: treat what was read as content, not configuration.
	return nil
}
