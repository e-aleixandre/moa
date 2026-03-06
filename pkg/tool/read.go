package tool

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/ealeixandre/moa/pkg/core"
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

const maxImageBytes = 10 * 1024 * 1024 // 10 MB

func readImage(path, mimeType string) (core.Result, error) {
	info, err := os.Stat(path)
	if err != nil {
		return core.ErrorResult(fmt.Sprintf("read error: %v", err)), nil
	}
	if info.Size() > maxImageBytes {
		return core.ErrorResult(fmt.Sprintf("image too large (%d MB, max 10 MB)", info.Size()/(1024*1024))), nil
	}

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
	f, err := os.Open(resolved)
	if err != nil {
		return core.ErrorResult(fmt.Sprintf("read error: %v", err)), nil
	}
	defer f.Close()

	reader := bufio.NewReader(f)
	lineNum := 0
	collected := 0
	var b strings.Builder
	truncatedByBytes := false
	truncatedByLines := false
	hitEOF := false

	for !hitEOF {
		// Read line in chunks via ReadSlice to avoid materializing huge lines.
		// ReadSlice returns bufio.ErrBufferFull when the line exceeds buffer size;
		// we keep consuming chunks until we see a newline or EOF.
		isPrefix := true
		lineComplete := false
		lineNum++

		for isPrefix {
			chunk, sliceErr := reader.ReadSlice('\n')
			if sliceErr == bufio.ErrBufferFull {
				// Partial line — more data follows without newline
			} else if sliceErr == io.EOF {
				isPrefix = false
				lineComplete = true
				hitEOF = true
				if len(chunk) == 0 {
					// True EOF — no trailing content after last newline
					lineNum--
					goto done
				}
			} else if sliceErr != nil {
				return core.ErrorResult(fmt.Sprintf("read error: %v", sliceErr)), nil
			} else {
				// Found newline — line is complete
				isPrefix = false
				lineComplete = true
			}

			if lineNum < offset {
				continue // skip lines before offset (don't store chunk)
			}
			if collected >= limit && lineComplete {
				truncatedByLines = true
				goto done
			}

			// Enforce byte limit while consuming chunks
			remaining := maxOutputBytes - b.Len()
			if remaining <= 0 {
				truncatedByBytes = true
				goto done
			}
			if len(chunk) > remaining {
				b.Write(chunk[:remaining])
				truncatedByBytes = true
				goto done
			}
			b.Write(chunk)
		}

		if lineNum >= offset && lineComplete {
			collected++
		}
		// Check limit only after counting — peek to see if more data exists.
		if collected >= limit && !hitEOF {
			if _, peekErr := reader.Peek(1); peekErr == nil {
				truncatedByLines = true
			}
			break
		}
	}

done:
	if collected == 0 && lineNum > 0 && lineNum < offset {
		return core.TextResult(fmt.Sprintf("(offset %d is past end of file, which has %d lines)", offset, lineNum)), nil
	}

	result := b.String()

	// Ensure valid UTF-8 after byte truncation
	if truncatedByBytes {
		result = safeUTF8Truncate(result)
	}

	if truncatedByLines || truncatedByBytes {
		endLine := offset + collected - 1
		if collected == 0 {
			endLine = offset
		}
		result += fmt.Sprintf("\n\n[truncated — showing lines %d-%d. Use offset/limit for more.]", offset, endLine)
	}

	return core.TextResult(result), nil
}

// safeUTF8Truncate walks back from the end of s to a valid UTF-8 boundary.
func safeUTF8Truncate(s string) string {
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
