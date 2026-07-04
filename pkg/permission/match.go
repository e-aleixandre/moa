package permission

import (
	"path/filepath"
	"strings"
)

// matchPolicy checks whether a tool call matches any pattern in the list.
// Pattern format (compatible with Claude Code):
//
//	"bash"             — matches tool name (any args)
//	"Bash(npm:*)"      — matches bash commands starting with "npm"
//	"Bash(npm install)" — matches exact command
//	"Write(*.go)"      — matches write to paths ending in .go
//	"Edit(pkg/*)"      — matches edit on paths under pkg/
//
// Tool names are case-insensitive. The arg inside parens is matched against
// the tool's primary argument (command for bash, path for write/edit).
//
// isDeny selects the matching semantics for bash prefix/glob rules: an allow
// rule must never green-light shell chaining, but a deny rule must still catch
// it (a broader match only denies more), so the chaining guard is skipped for
// deny lists.
func matchPolicy(patterns []string, toolName string, args map[string]any, isDeny bool) bool {
	primaries := primaryArgs(toolName, args)

	// Tools with no matchable argument: only bare tool-name patterns apply.
	if len(primaries) == 0 {
		for _, pat := range patterns {
			if matchPattern(pat, toolName, "", isDeny) {
				return true
			}
		}
		return false
	}

	if isDeny {
		// Any forbidden target blocks the whole call. For a multi-file tool
		// (apply_patch) that means a single denied path stops the entire patch.
		for _, pat := range patterns {
			for _, p := range primaries {
				if matchPattern(pat, toolName, p, true) {
					return true
				}
			}
		}
		return false
	}

	// Allow: auto-approve only when EVERY target is covered by some pattern —
	// otherwise a multi-file patch touching one allowed path would smuggle in
	// writes to unlisted paths. For single-arg tools this is the same as before.
	for _, p := range primaries {
		covered := false
		for _, pat := range patterns {
			if matchPattern(pat, toolName, p, false) {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

// matchPattern checks a single pattern against a tool call.
func matchPattern(pattern, toolName, primaryArg string, isDeny bool) bool {
	// Parse "Tool(argPattern)" or just "tool"
	patTool, argPat, hasArg := parsePattern(pattern)

	if !strings.EqualFold(patTool, toolName) {
		return false
	}

	if !hasArg {
		return true // tool name match = any args
	}

	// Exact match always wins — even for commands containing glob or shell
	// metacharacters that filepath.Match would misinterpret (e.g. an "always
	// allow" rule generated for a command with pipes or a subshell).
	if argPat == primaryArg {
		return true
	}

	// Match the arg pattern against the primary arg.
	// Use filepath.Match for glob support (*, ?). A glob like "git *" matches
	// across shell metacharacters (* spans anything but '/'), so an allow rule
	// must not let it green-light chaining either.
	matched, _ := filepath.Match(argPat, primaryArg)
	if matched {
		if !isDeny && strings.EqualFold(patTool, "bash") && hasShellChaining(primaryArg) {
			return false
		}
		return true
	}

	// Also try prefix match for "cmd:*" style patterns.
	// filepath.Match doesn't handle "npm install:*" matching "npm install --save foo"
	// because * only matches within a single path segment. Handle colon-star explicitly.
	if strings.HasSuffix(argPat, ":*") {
		prefix := strings.TrimSuffix(argPat, ":*")
		if primaryArg == prefix {
			return true
		}
		if strings.HasPrefix(primaryArg, prefix+" ") || strings.HasPrefix(primaryArg, prefix+"\t") {
			// A bash prefix rule must never green-light shell chaining:
			// approving "git status" must not approve "git status; rm -rf ~".
			if !isDeny && strings.EqualFold(patTool, "bash") && hasShellChaining(primaryArg) {
				return false
			}
			return true
		}
	}

	// A deny rule with a bare filename pattern (no '/') must block that file at
	// any depth. The gate sees the model's raw path — often absolute or nested —
	// and filepath.Match's * never crosses '/', so "*.env" alone would miss
	// "backend/.env" or "/repo/.env". Widen deny (only) to the basename; allow
	// rules stay strict so they don't over-approve nested paths.
	if isDeny && !strings.EqualFold(patTool, "bash") && !strings.Contains(argPat, "/") {
		if base := filepath.Base(primaryArg); base != primaryArg {
			if matched, _ := filepath.Match(argPat, base); matched {
				return true
			}
		}
	}

	return false
}

// hasShellChaining reports whether a bash command uses metacharacters that can
// run additional commands or redirect output. Prefix-based allow patterns must
// not auto-approve such commands. We deliberately do not parse quotes: a false
// positive (re-prompting for `echo "a;b"`) is a safe, minor annoyance, whereas
// a false negative is a privilege-escalation hole.
func hasShellChaining(cmd string) bool {
	if strings.ContainsAny(cmd, ";|&`><\n\r") {
		return true
	}
	return strings.Contains(cmd, "$(")
}

// parsePattern splits "Tool(argGlob)" into (tool, argGlob, true)
// or "tool" into (tool, "", false).
func parsePattern(pattern string) (tool, argPat string, hasArg bool) {
	idx := strings.IndexByte(pattern, '(')
	if idx < 0 {
		return pattern, "", false
	}

	tool = pattern[:idx]
	rest := strings.TrimSuffix(pattern[idx+1:], ")")
	return tool, rest, true
}

// primaryArg extracts the single most relevant argument for matching. Multi-path
// tools (apply_patch) have no single primary arg — use primaryArgs for matching.
func primaryArg(toolName string, args map[string]any) string {
	if args == nil {
		return ""
	}
	switch strings.ToLower(toolName) {
	case "bash":
		if cmd, ok := args["command"].(string); ok {
			return cmd
		}
	// Path-scoped tools: the file/dir they operate on. Without these entries
	// primaryArg returned "" and no arg-scoped deny/allow rule could match them.
	case "write", "edit", "read", "grep", "find", "ls", "multiedit", "send_file":
		if path, ok := args["path"].(string); ok {
			return path
		}
	case "fetch_content":
		if u, ok := args["url"].(string); ok {
			return u
		}
	}
	return ""
}

// primaryArgs returns every argument a deny/allow rule can match against. Most
// tools have exactly one (see primaryArg); apply_patch touches multiple files,
// so each target path embedded in the patch is returned for path scoping.
func primaryArgs(toolName string, args map[string]any) []string {
	if strings.EqualFold(toolName, "apply_patch") {
		return patchTargetPaths(args)
	}
	if p := primaryArg(toolName, args); p != "" {
		return []string{p}
	}
	return nil
}

// patchTargetPaths extracts every file path an apply_patch call would touch by
// scanning the *** Begin Patch markers. A single primaryArg cannot represent a
// multi-file patch, so deny/allow path rules need the full set.
func patchTargetPaths(args map[string]any) []string {
	text, ok := args["patch"].(string)
	if !ok {
		return nil
	}
	markers := []string{"*** Add File:", "*** Update File:", "*** Delete File:", "*** Move to:"}
	var paths []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		for _, m := range markers {
			if strings.HasPrefix(line, m) {
				if p := strings.TrimSpace(strings.TrimPrefix(line, m)); p != "" {
					paths = append(paths, p)
				}
			}
		}
	}
	return paths
}

// GenerateAllowPattern creates an allow pattern for the "always allow" shortcut.
// For bash, generates "Bash(firstWord:*)" from the command.
// For other tools, generates just the tool name.
func GenerateAllowPattern(toolName string, args map[string]any) string {
	if strings.ToLower(toolName) == "bash" {
		if cmd, ok := args["command"].(string); ok {
			// Commands with shell chaining can't be safely generalized to a
			// prefix rule — allow only this exact command instead.
			if hasShellChaining(cmd) {
				return "Bash(" + cmd + ")"
			}
			first := strings.Fields(cmd)
			if len(first) > 0 {
				return "Bash(" + first[0] + ":*)"
			}
		}
	}
	return toolName
}
