package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/ealeixandre/moa/pkg/core"
)

// NewGrep creates the grep tool.
func NewGrep(cfg ToolConfig) core.Tool {
	return core.Tool{
		Name:        "grep",
		Label:       "Grep",
		Description: "Search file contents for a pattern (regex or literal). Respects .gitignore. Returns matching lines with file paths and line numbers. Truncated to 100 matches or 50KB.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"pattern": {
					"type": "string",
					"description": "Search pattern (regex by default, literal with --fixed-strings)"
				},
				"path": {
					"type": "string",
					"description": "Directory or file to search in (default: workspace root)"
				},
				"include": {
					"type": "string",
					"description": "Glob pattern to include files (e.g. '*.go')"
				},
				"fixed_strings": {
					"type": "boolean",
					"description": "Treat pattern as literal string instead of regex"
				}
			},
			"required": ["pattern"]
		}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			pattern := getString(params, "pattern", "")
			if pattern == "" {
				return core.ErrorResult("pattern is required"), nil
			}

			searchPath := getString(params, "path", ".")
			if cfg.WorkspaceRoot != "" {
				validPath, err := safePath(cfg.WorkspaceRoot, searchPath)
				if err != nil {
					return core.ErrorResult(fmt.Sprintf("invalid path: %v", err)), nil
				}
				searchPath = validPath
			}

			// Build grep command — prefer ripgrep if available, fallback to grep -r
			args := buildGrepArgs(params, pattern, searchPath)

			cmd := exec.CommandContext(ctx, args[0], args[1:]...)
			cmd.Dir = cfg.WorkspaceRoot

			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			err := cmd.Run()

			output := stdout.String()
			if output == "" && err != nil {
				// grep returns exit 1 for no matches
				if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
					return core.TextResult("No matches found."), nil
				}
				errMsg := stderr.String()
				if errMsg != "" {
					return core.ErrorResult(errMsg), nil
				}
				return core.ErrorResult(fmt.Sprintf("grep failed: %v", err)), nil
			}

			// Truncate
			output = truncateLines(output, 100)
			output = truncateOutput(output, maxOutputBytes)

			return core.TextResult(output), nil
		},
	}
}

func buildGrepArgs(params map[string]any, pattern, searchPath string) []string {
	// Try ripgrep first (rg), fallback to grep
	fixedStrings := getBool(params, "fixed_strings", false)
	include := getString(params, "include", "")

	if rgPath, err := exec.LookPath("rg"); err == nil {
		args := []string{rgPath, "--no-heading", "--line-number", "--color=never", "-m", "100"}
		if fixedStrings {
			args = append(args, "--fixed-strings")
		}
		if include != "" {
			args = append(args, "--glob", include)
		}
		args = append(args, pattern, searchPath)
		return args
	}

	// Fallback to grep
	args := []string{"grep", "-rn", "--color=never"}
	if fixedStrings {
		args = append(args, "-F")
	}
	if include != "" {
		args = append(args, "--include="+include)
	}
	args = append(args, pattern, searchPath)
	return args
}

// truncateGrepLines is like truncateLines but uses the first N non-empty lines that contain matches.
func truncateGrepLines(s string, maxLines int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	return strings.Join(lines[:maxLines], "\n") + fmt.Sprintf("\n\n[truncated — showing %d of %d matches]", maxLines, len(lines))
}
