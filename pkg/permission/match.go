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
func matchPolicy(patterns []string, toolName string, args map[string]any) bool {
	primary := primaryArg(toolName, args)

	for _, pat := range patterns {
		if matchPattern(pat, toolName, primary) {
			return true
		}
	}
	return false
}

// matchPattern checks a single pattern against a tool call.
func matchPattern(pattern, toolName, primaryArg string) bool {
	// Parse "Tool(argPattern)" or just "tool"
	patTool, argPat, hasArg := parsePattern(pattern)

	if !strings.EqualFold(patTool, toolName) {
		return false
	}

	if !hasArg {
		return true // tool name match = any args
	}

	// Match the arg pattern against the primary arg.
	// Use filepath.Match for glob support (*, ?).
	matched, _ := filepath.Match(argPat, primaryArg)
	if matched {
		return true
	}

	// Also try prefix match for "cmd:*" style patterns.
	// filepath.Match doesn't handle "npm install:*" matching "npm install --save foo"
	// because * only matches within a single path segment. Handle colon-star explicitly.
	if strings.HasSuffix(argPat, ":*") {
		prefix := strings.TrimSuffix(argPat, ":*")
		if primaryArg == prefix || strings.HasPrefix(primaryArg, prefix+" ") || strings.HasPrefix(primaryArg, prefix+"\t") {
			return true
		}
	}

	return false
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

// primaryArg extracts the most relevant argument for matching.
func primaryArg(toolName string, args map[string]any) string {
	if args == nil {
		return ""
	}
	switch strings.ToLower(toolName) {
	case "bash":
		if cmd, ok := args["command"].(string); ok {
			return cmd
		}
	case "write", "edit", "read":
		if path, ok := args["path"].(string); ok {
			return path
		}
	}
	return ""
}

// GenerateAllowPattern creates an allow pattern for the "always allow" shortcut.
// For bash, generates "Bash(firstWord:*)" from the command.
// For other tools, generates just the tool name.
func GenerateAllowPattern(toolName string, args map[string]any) string {
	if strings.ToLower(toolName) == "bash" {
		if cmd, ok := args["command"].(string); ok {
			first := strings.Fields(cmd)
			if len(first) > 0 {
				return "Bash(" + first[0] + ":*)"
			}
		}
	}
	return toolName
}
