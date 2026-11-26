package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/e-aleixandre/moa/pkg/core"
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
			stdout.SpillDir = SpillOutputDir()
			stderr.headMax = maxOutputBytes / 2
			stderr.tailMax = maxOutputBytes / 2
			stderr.SpillDir = SpillOutputDir()
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
			output = truncateLinesHeadTail(output, 1000, stdout.SpillPath, stdout.spillIncomplete)

			return core.TextResult(output), nil
		},
	}
}

// globEscape makes a path literal inside a glob (fd and find -path).
func globEscape(p string) string {
	var sb strings.Builder
	for _, r := range p {
		if strings.ContainsRune(`\*?[]{}`, r) {
			sb.WriteByte('\\')
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

func buildFindArgs(pattern, searchPath, fileType string) []string {
	// The search root only matters as a literal prefix: fd and find match
	// path globs against absolute paths, so an ancestor of the workspace
	// called like the pattern's first directory must not match.
	root := searchPath
	if abs, err := filepath.Abs(searchPath); err == nil {
		root = abs
	}
	root = globEscape(strings.TrimRight(root, "/"))

	// Try fd first (respects .gitignore by default), then fdfind (the
	// Debian/Ubuntu binary name), fallback to find
	fdPath, err := exec.LookPath("fd")
	if err != nil {
		fdPath, err = exec.LookPath("fdfind")
	}
	if err == nil {
		args := []string{fdPath, "--glob", "--color=never"}
		switch fileType {
		case "f":
			args = append(args, "--type", "f")
		case "d":
			args = append(args, "--type", "d")
		}
		if strings.Contains(pattern, "/") {
			// A bare glob only matches the file name; with a slash the user
			// means a path relative to the search root.
			args = append(args, "--full-path")
			pattern = root + "/**/" + pattern
		}
		args = append(args, "--", pattern, searchPath)
		return args
	}

	// Fallback to find
	args := []string{"find", searchPath}
	switch fileType {
	case "f":
		args = append(args, "-type", "f")
	case "d":
		args = append(args, "-type", "d")
	}
	if strings.Contains(pattern, "/") {
		// find's * already crosses slashes but "/**/" needs a directory in
		// between, unlike fd where it may match none: accept both.
		// -path matches the path as printed, i.e. as given.
		root := globEscape(strings.TrimRight(searchPath, "/"))
		p := "/" + pattern
		if strings.Contains(p, "/**/") {
			args = append(args, "(",
				"-path", root+strings.ReplaceAll(p, "/**/", "/*/"),
				"-o", "-path", root+strings.ReplaceAll(p, "/**/", "/"),
				")")
		} else {
			args = append(args, "-path", root+p)
		}
	} else {
		args = append(args, "-name", pattern)
	}
	return args
}
