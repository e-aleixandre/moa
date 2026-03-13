package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/ealeixandre/moa/pkg/core"
)

// NewFind creates the find tool.
func NewFind(cfg ToolConfig) core.Tool {
	return core.Tool{
		Name:        "find",
		Label:       "Find",
		Description: "Search for files by glob pattern. Respects .gitignore when fd is available. Truncated to 1000 results or 50KB.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"pattern": {
					"type": "string",
					"description": "Glob pattern to search for (e.g. '*.go', 'main.*')"
				},
				"path": {
					"type": "string",
					"description": "Directory to search in (default: workspace root)"
				},
				"type": {
					"type": "string",
					"enum": ["f", "d"],
					"description": "Filter by type: f=files, d=directories"
				}
			},
			"required": ["pattern"]
		}`),
		Effect: core.EffectReadOnly,
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			pattern := getString(params, "pattern", "")
			if pattern == "" {
				return core.ErrorResult("pattern is required"), nil
			}

			searchPath := getString(params, "path", ".")
			if cfg.WorkspaceRoot != "" {
				validPath, err := safePath(cfg, searchPath)
				if err != nil {
					return core.ErrorResult(fmt.Sprintf("invalid path: %v", err)), nil
				}
				searchPath = validPath
			}
			fileType := getString(params, "type", "")

			args := buildFindArgs(pattern, searchPath, fileType)

			cmd := exec.CommandContext(ctx, args[0], args[1:]...)
			cmd.Dir = cfg.WorkspaceRoot

			var stdout, stderr headTailBuffer
			stdout.headMax = maxOutputBytes / 2
			stdout.tailMax = maxOutputBytes / 2
			stdout.SpillDir = spillOutputDir
			stderr.headMax = maxOutputBytes / 2
			stderr.tailMax = maxOutputBytes / 2
			stderr.SpillDir = spillOutputDir
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			err := cmd.Run()
			stdout.Close()
			stderr.Close()

			output := stdout.String()
			if output == "" {
				if err != nil {
					errMsg := stderr.String()
					if errMsg != "" {
						return core.ErrorResult(errMsg), nil
					}
				}
				return core.TextResult("No files found."), nil
			}

			// Truncate by lines
			output = truncateLines(output, 1000)

			return core.TextResult(output), nil
		},
	}
}

func buildFindArgs(pattern, searchPath, fileType string) []string {
	// Try fd first (respects .gitignore by default), fallback to find
	if fdPath, err := exec.LookPath("fd"); err == nil {
		args := []string{fdPath, "--glob", "--color=never"}
		if fileType == "f" {
			args = append(args, "--type", "f")
		} else if fileType == "d" {
			args = append(args, "--type", "d")
		}
		args = append(args, "--", pattern, searchPath)
		return args
	}

	// Fallback to find
	args := []string{"find", searchPath}
	if fileType == "f" {
		args = append(args, "-type", "f")
	} else if fileType == "d" {
		args = append(args, "-type", "d")
	}
	args = append(args, "-name", pattern)
	return args
}

