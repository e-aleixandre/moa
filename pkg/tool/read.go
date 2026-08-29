package tool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/e-aleixandre/moa/pkg/attachment"
	"github.com/e-aleixandre/moa/pkg/core"
)

var imageExtensions = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// pdfToTextBinary is the binary name for pdftotext. Overridable in tests.
var pdfToTextBinary = "pdftotext"

const maxPDFBytes = 50 * 1024 * 1024 // 50 MB

// NewRead creates the read tool.
func NewRead(cfg ToolConfig) core.Tool {
	return core.Tool{
		Name:        "read",
		Label:       "Read",
		Description: "Read a file. Supports text, images (jpg, png, gif, webp), and PDF (requires pdftotext). Text/PDF truncated to 2000 lines or 50KB. Use offset/limit for large files. Images must be at most 8000 px per side.",
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
		Effect: core.EffectReadOnly,
		LockKey: func(args map[string]any) string {
			p := getString(args, "path", "")
			if p == "" {
				return ""
			}
			resolved, err := safePath(cfg, p)
			if err != nil {
				return ""
			}
			return resolved
		},
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			path := getString(params, "path", "")
			if path == "" {
				return core.ErrorResult("path is required"), nil
			}

			resolved, err := safePath(cfg, path)
			if err != nil {
				return core.ErrorResult(err.Error()), nil
			}

			info, err := os.Stat(resolved)
			if err != nil {
				return core.ErrorResult(fmt.Sprintf("cannot read %s: %v", path, err)), nil
			}
			if info.IsDir() {
				return listDirectory(resolved, cfg.WorkspaceRoot, "[Note: path is a directory, not a file. Showing directory listing instead. Use the ls tool for directory listings.]\n\n")
			}
			if !info.Mode().IsRegular() {
				return core.ErrorResult(fmt.Sprintf("cannot read %s: not a regular file", path)), nil
			}

			ext := strings.ToLower(filepath.Ext(resolved))

			// Parse pagination params early — used by text and PDF paths.
			offset := getInt(params, "offset", 1)
			limit := getInt(params, "limit", maxOutputLines)
			if offset < 1 {
				offset = 1
			}
			if limit < 1 {
				limit = maxOutputLines
			}

			// Image — no pagination
			if mimeType, ok := imageExtensions[ext]; ok {
				// ctx, not a captured value: the attachment capability belongs to
				// the agent making THIS call, not to the tool object. This same
				// core.Tool is shared with ephemeral agents (reviewers), which
				// must stay inline.
				result, err := readImage(ctx, resolved, mimeType)
				if err == nil && !result.IsError && cfg.FileTracker != nil {
					cfg.FileTracker.MarkRead(resolved)
				}
				return result, err
			}

			// PDF
			if ext == ".pdf" {
				result, err := readPDF(ctx, resolved, path, info.Size(), offset, limit)
				if err == nil && !result.IsError && cfg.FileTracker != nil {
					cfg.FileTracker.MarkRead(resolved)
				}
				return result, err
			}

			// Text file
			result, err := readTextFile(resolved, path, offset, limit)
			if err == nil && !result.IsError && cfg.FileTracker != nil {
				cfg.FileTracker.MarkRead(resolved)
			}
			return result, err
		},
	}
}

func readImage(ctx context.Context, path, mimeType string) (core.Result, error) {
	info, err := os.Stat(path)
	if err != nil {
		return core.ErrorResult(fmt.Sprintf("read error: %v", err)), nil
	}
	encodedSize := base64.StdEncoding.EncodedLen(int(info.Size()))
	if encodedSize > core.MaxImageBase64Bytes {
		return core.ErrorResult(fmt.Sprintf("image too large after base64 encoding (%d MB, max %d MB)", encodedSize/(1024*1024), core.MaxImageBase64Bytes/(1024*1024))), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return core.ErrorResult(fmt.Sprintf("read error: %v", err)), nil
	}

	// An oversized image is rejected by the provider with a hard 400, and since
	// history is replayed every turn it would leave the whole conversation
	// unsendable. Fail here instead, with an actionable message for the model.
	width, height := core.ImageDimensions(data)
	if width > core.MaxImageDimension || height > core.MaxImageDimension {
		return core.ErrorResult(fmt.Sprintf(
			"image is %dx%d px; both sides must be at most %d px (provider limit). "+
				"Resize or crop it first (e.g. split a tall screenshot into sections) and read the smaller file.",
			width, height, core.MaxImageDimension)), nil
	}

	// Auto-detect mime if needed
	if mimeType == "" {
		mimeType = mime.TypeByExtension(filepath.Ext(path))
	}
	// The type so far comes from the extension, which lies often enough (a GIF
	// saved as .png). Anthropic rejects a mismatch between the declared media
	// type and the bytes with a hard 400, and history is replayed every turn,
	// so trust the magic bytes over the name.
	if actual := core.ImageMimeFromBytes(data); actual != "" {
		mimeType = actual
	}

	// Externalize only when the invoking agent brought the capability. The
	// scope is resolved from the context on every call, never captured in
	// NewRead's closure: tools are shared objects (the plan/code reviewers and
	// subagents reuse the parent's `read`), so a captured scope would make an
	// agent that must not externalize write references into the parent's index.
	if scope := attachment.ScopeFromContext(ctx); scope != nil {
		descriptor, err := scope.Put(data, attachment.PutMeta{
			Name:   filepath.Base(path),
			Mime:   mimeType,
			Kind:   "image",
			Width:  width,
			Height: height,
		})
		if err != nil {
			// A valid screenshot must never become a user-visible failure just
			// because the blob store misbehaved: fall back to the inline bytes.
			// No partial descriptor is returned — a reference without a blob
			// behind it would reach the provider as an empty image.
			slog.Warn("read: could not externalize image, falling back to inline", "path", path, "error", err)
		} else {
			return core.Result{
				Content: []core.Content{{
					Type:           "image",
					MimeType:       descriptor.Mime,
					AttachmentID:   descriptor.ID,
					AttachmentSize: descriptor.Size,
				}},
			}, nil
		}
	}

	encoded := base64.StdEncoding.EncodeToString(data)
	return core.Result{
		Content: []core.Content{core.ImageContent(encoded, mimeType)},
	}, nil
}

func readPDF(ctx context.Context, resolved, displayPath string, fileSize int64, offset, limit int) (core.Result, error) {
	if fileSize > maxPDFBytes {
		return core.ErrorResult(fmt.Sprintf("PDF too large (%d MB, max %d MB)", fileSize/(1024*1024), maxPDFBytes/(1024*1024))), nil
	}

	binPath, err := exec.LookPath(pdfToTextBinary)
	if err != nil {
		return core.ErrorResult("pdftotext not found. Install poppler:\n  macOS:  brew install poppler\n  Ubuntu: sudo apt install poppler-utils\n  Fedora: sudo dnf install poppler-utils"), nil
	}

	cmd := exec.CommandContext(ctx, binPath, "-layout", resolved, "-")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return core.ErrorResult(fmt.Sprintf("read error: %v", err)), nil
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return core.ErrorResult(fmt.Sprintf("pdftotext failed to start: %v", err)), nil
	}

	result, paginateErr := paginateReader(stdout, displayPath, offset, limit)

	// Drain remaining stdout to prevent pipe deadlock — paginateReader may stop
	// early on limit/byte truncation, leaving the child blocked on a full pipe buffer.
	io.Copy(io.Discard, stdout) //nolint:errcheck

	// Must wait for command to finish after draining stdout.
	waitErr := cmd.Wait()
	if paginateErr != nil {
		return core.Result{}, paginateErr
	}
	if waitErr != nil {
		errText := strings.TrimSpace(stderrBuf.String())
		if errText == "" {
			errText = waitErr.Error()
		}
		return core.ErrorResult(fmt.Sprintf("pdftotext failed: %s", errText)), nil
	}

	return result, nil
}

func readTextFile(resolved, displayPath string, offset, limit int) (core.Result, error) {
	f, err := os.Open(resolved)
	if err != nil {
		return core.ErrorResult(fmt.Sprintf("read error: %v", err)), nil
	}
	defer f.Close() //nolint:errcheck

	return paginateReader(f, displayPath, offset, limit)
}

// paginateReader reads from r with offset/limit pagination and byte limits.
// Shared by readTextFile and readPDF.
func paginateReader(r io.Reader, displayPath string, offset, limit int) (core.Result, error) {
	reader := bufio.NewReader(r)
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
