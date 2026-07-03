package permission

import (
	"regexp"
	"strings"
)

// dangerousPatterns matches shell commands that download and immediately
// execute remote code — the classic `curl … | sh` shape an injected prompt
// can smuggle into a tool call. This is a heuristic mitigation against prompt
// injection, NOT a sandbox: it only forces an explicit user confirmation, and
// a false positive costs one extra prompt while a false negative would let
// downloaded code run unattended. Patterns are deliberately broad on that
// tradeoff.
var dangerousPatterns = []*regexp.Regexp{
	// (a) curl/wget output piped into a shell interpreter, allowing a
	//     sudo/env/command wrapper between the pipe and the shell.
	regexp.MustCompile(`(?:curl|wget)\b.*\|\s*(?:(?:sudo|env|command)\s+[^|]*?)?(?:bash|dash|zsh|ash|sh)\b`),
	// (b) process substitution of a download fed to a shell: bash <(curl …).
	regexp.MustCompile(`(?:bash|dash|zsh|ash|sh)\b[^<]*<\(\s*(?:curl|wget)\b`),
	// (c) command substitution of a download passed to sh -c / bash -c.
	regexp.MustCompile(`(?:bash|dash|zsh|ash|sh)\b[^;&|]*?-c\b[^;&|]*(?:\$\(|` + "`" + `)\s*(?:curl|wget)\b`),
}

// IsDangerousCommand reports whether cmd downloads and executes remote code.
// See dangerousPatterns for the exact shapes and the heuristic-not-sandbox
// caveat.
func IsDangerousCommand(cmd string) bool {
	for _, re := range dangerousPatterns {
		if re.MatchString(cmd) {
			return true
		}
	}
	return false
}

// isShellTool reports whether name is a shell tool whose primary argument is
// model-controlled command text (the prompt-injection vector). Only "bash"
// qualifies: script tools run config-defined commands, so their model-supplied
// input is arguments, not the command itself.
func isShellTool(name string) bool {
	return strings.EqualFold(name, "bash")
}
