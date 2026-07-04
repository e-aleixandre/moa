package serve

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ealeixandre/moa/pkg/core"
)

// Attachment is a file uploaded inline (base64) with a /send request.
type Attachment struct {
	Name string `json:"name"`
	Mime string `json:"mime"`
	Data string `json:"data"` // base64 standard encoding
}

// ErrBadAttachment wraps any attachment validation failure; the wrapping error
// message names the offending attachment and is safe to surface as a 400.
var ErrBadAttachment = errors.New("bad attachment")

// ErrAttachmentsWhileRunning is returned when attachments are sent while the
// agent is running or awaiting a permission decision — steering is text-only.
var ErrAttachmentsWhileRunning = errors.New("attachments cannot be sent while the agent is running")

const (
	maxAttachments        = 8
	maxImageBytes         = 5 << 20   // 5 MB decoded, per-image API limit
	maxAttachmentTextSize = 256 << 10 // 256 KiB decoded
)

var allowedImageMimes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/gif":  true,
	"image/webp": true,
}

// buildAttachmentContent validates and converts uploaded attachments into
// core.Content blocks: images become "image" blocks (reusing the original
// base64 string), everything else is treated as text and wrapped in a
// <attachment> sentinel. PDFs are not supported yet (Phase 2).
func buildAttachmentContent(atts []Attachment) ([]core.Content, error) {
	if len(atts) > maxAttachments {
		return nil, fmt.Errorf("%w: too many attachments (max %d)", ErrBadAttachment, maxAttachments)
	}

	content := make([]core.Content, 0, len(atts))
	for _, a := range atts {
		decoded, err := base64.StdEncoding.DecodeString(a.Data)
		if err != nil {
			return nil, fmt.Errorf("%w: attachment %q: invalid base64", ErrBadAttachment, a.Name)
		}

		if allowedImageMimes[a.Mime] {
			if len(decoded) > maxImageBytes {
				return nil, fmt.Errorf("%w: attachment %q: image exceeds %d MB", ErrBadAttachment, a.Name, maxImageBytes>>20)
			}
			content = append(content, core.ImageContent(a.Data, a.Mime))
			continue
		}

		if a.Mime == "application/pdf" {
			return nil, fmt.Errorf("%w: attachment %q: PDF attachments are not supported yet", ErrBadAttachment, a.Name)
		}

		if len(decoded) > maxAttachmentTextSize {
			return nil, fmt.Errorf("%w: attachment %q: text exceeds %d KiB", ErrBadAttachment, a.Name, maxAttachmentTextSize>>10)
		}
		if !utf8.Valid(decoded) || bytes.IndexByte(decoded, 0) != -1 {
			return nil, fmt.Errorf("%w: attachment %q: unsupported type", ErrBadAttachment, a.Name)
		}

		name := strings.ReplaceAll(a.Name, `"`, `\"`)
		text := fmt.Sprintf("<attachment name=\"%s\">\n%s\n</attachment>", name, decoded)
		content = append(content, core.TextContent(text))
	}
	return content, nil
}
