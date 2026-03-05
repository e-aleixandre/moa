package tool

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/ealeixandre/go-agent/pkg/core"
)

var imageExtensions = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// NewRead creates the read tool.
func NewRead(cfg ToolConfig) core.Tool {
	return core.Tool{
		Name:        "read",
		Label:       "Read",
		Description: "Read a file. Supports text and images (jpg, png, gif, webp). Text files truncated to 2000 lines or 50KB. Use offset/limit for large files.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {
					"type": "string",
					"description": "Path to the file to read"
				},
				"offset": {
					"type": "integer",
					"description": "Line number to start reading from (1-indexed)"
				},
				"limit": {
					"type": "integer",
					"description": "Maximum number of lines to read"
				}
			},
			"required": ["path"]
		}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			path := getString(params, "path", "")
			if path == "" {
				return core.ErrorResult("path is required"), nil
			}

			resolved, err := safePath(cfg.WorkspaceRoot, path)
			if err != nil {
				return core.ErrorResult(err.Error()), nil
			}

			info, err := os.Stat(resolved)
			if err != nil {
				return core.ErrorResult(fmt.Sprintf("cannot read %s: %v", path, err)), nil
			}
			if info.IsDir() {
				return core.ErrorResult(fmt.Sprintf("%s is a directory, use ls instead", path)), nil
			}

			// Check if image
			ext := strings.ToLower(filepath.Ext(resolved))
			if mimeType, ok := imageExtensions[ext]; ok {
				return readImage(resolved, mimeType)
			}

			// Text file
			offset := getInt(params, "offset", 1)
			limit := getInt(params, "limit", maxOutputLines)
			if offset < 1 {
				offset = 1
			}
			if limit < 1 {
				limit = maxOutputLines
			}

			return readTextFile(resolved, path, offset, limit)
		},
	}
}

func readImage(path, mimeType string) (core.Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return core.ErrorResult(fmt.Sprintf("read error: %v", err)), nil
	}

	// Auto-detect mime if needed
	if mimeType == "" {
		mimeType = mime.TypeByExtension(filepath.Ext(path))
	}

	encoded := base64.StdEncoding.EncodeToString(data)
	return core.Result{
		Content: []core.Content{core.ImageContent(encoded, mimeType)},
	}, nil
}

func readTextFile(resolved, displayPath string, offset, limit int) (core.Result, error) {
	data, err := os.ReadFile(resolved)
	if err != nil {
		return core.ErrorResult(fmt.Sprintf("read error: %v", err)), nil
	}

	content := string(data)
	lines := strings.Split(content, "\n")
	totalLines := len(lines)

	// Apply offset (1-indexed)
	startIdx := offset - 1
	if startIdx >= totalLines {
		return core.TextResult(fmt.Sprintf("(file has %d lines, offset %d is past end)", totalLines, offset)), nil
	}
	if startIdx < 0 {
		startIdx = 0
	}

	endIdx := startIdx + limit
	truncated := false
	if endIdx > totalLines {
		endIdx = totalLines
	} else if endIdx < totalLines {
		truncated = true
	}

	result := strings.Join(lines[startIdx:endIdx], "\n")

	// Also truncate by bytes
	if len(result) > maxOutputBytes {
		result = result[:maxOutputBytes]
		truncated = true
	}

	if truncated {
		result += fmt.Sprintf("\n\n[truncated — showing lines %d-%d of %d. Use offset/limit for more.]", offset, startIdx+endIdx-startIdx, totalLines)
	}

	return core.TextResult(result), nil
}
