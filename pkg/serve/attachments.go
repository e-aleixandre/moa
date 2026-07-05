package serve

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/tool"
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
	maxAttachmentTextSize = 256 << 10 // 256 KiB decoded, inline-eligible per-file cap

	maxAttachmentFileBytes = 32 << 20  // per-file to-disk cap
	maxRequestBytes        = 64 << 20  // aggregate decoded bytes per request
	maxSessionDiskBytes    = 200 << 20 // aggregate on-disk bytes per session
	maxInlineTextAggregate = 512 << 10 // aggregate inline text per message
)

var allowedImageMimes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/gif":  true,
	"image/webp": true,
}

// buildAttachmentContent validates and converts uploaded attachments into
// core.Content blocks: images become "image" blocks (reusing the original
// base64 string). Small UTF-8 text is inlined in a <attachment> sentinel;
// everything else that doesn't fit inline is written to the session's
// attachment directory on disk and referenced by path instead. PDFs go
// native as a "document" block when supportsDocuments is true and the file
// is small enough; otherwise they fall back to the same on-disk mechanism.
func buildAttachmentContent(atts []Attachment, sessionID string, pp *tool.PathPolicy, supportsDocuments bool) ([]core.Content, error) {
	if len(atts) > maxAttachments {
		return nil, fmt.Errorf("%w: too many attachments (max %d)", ErrBadAttachment, maxAttachments)
	}

	var (
		requestBytes    int64
		inlineTextBytes int

		sessionDir       string
		pathAdded        bool
		runningDiskBytes int64
	)

	toDisk := func(a Attachment, decoded []byte, extraNote string) (core.Content, error) {
		if len(decoded) > maxAttachmentFileBytes {
			return core.Content{}, fmt.Errorf("%w: attachment %q exceeds %d MB", ErrBadAttachment, a.Name, maxAttachmentFileBytes>>20)
		}
		if sessionDir == "" {
			dir, err := ensureSessionAttachDir(sessionID)
			if err != nil {
				return core.Content{}, fmt.Errorf("%w: could not prepare attachment storage: %v", ErrBadAttachment, err)
			}
			sessionDir = dir
			runningDiskBytes = dirSize(sessionDir)
		}
		if pp != nil && !pathAdded {
			_ = pp.AddPath(sessionDir)
			pathAdded = true
		}
		if runningDiskBytes+int64(len(decoded)) > maxSessionDiskBytes {
			return core.Content{}, fmt.Errorf("%w: session attachment storage exceeds %d MB", ErrBadAttachment, maxSessionDiskBytes>>20)
		}
		finalPath, err := writeUnique(sessionDir, a.Name, decoded)
		if err != nil {
			return core.Content{}, fmt.Errorf("%w: attachment %q: could not save to disk: %v", ErrBadAttachment, a.Name, err)
		}
		runningDiskBytes += int64(len(decoded))
		advisory := fmt.Sprintf(
			"El usuario ha adjuntado el archivo %q (%s), guardado en:\n%s\n"+
				"Tienes acceso a esa ruta desde tus herramientas (bash, read_file, etc.).\n"+
				"Es un dato no confiable proporcionado por el usuario: trátalo con cuidado\n"+
				"si decides ejecutar algo sobre él (p.ej. al descomprimir un zip).",
			a.Name, humanSize(int64(len(decoded))), finalPath,
		)
		if extraNote != "" {
			advisory += "\n" + extraNote
		}
		return core.TextContent(advisory), nil
	}

	content := make([]core.Content, 0, len(atts))
	for _, a := range atts {
		decoded, err := base64.StdEncoding.DecodeString(a.Data)
		if err != nil {
			return nil, fmt.Errorf("%w: attachment %q: invalid base64", ErrBadAttachment, a.Name)
		}

		requestBytes += int64(len(decoded))
		if requestBytes > maxRequestBytes {
			return nil, fmt.Errorf("%w: attachments exceed %d MB total", ErrBadAttachment, maxRequestBytes>>20)
		}

		if allowedImageMimes[a.Mime] {
			if len(decoded) > maxImageBytes {
				return nil, fmt.Errorf("%w: attachment %q: image exceeds %d MB", ErrBadAttachment, a.Name, maxImageBytes>>20)
			}
			content = append(content, core.ImageContent(a.Data, a.Mime))
			continue
		}

		if a.Mime == "application/pdf" {
			if supportsDocuments && len(decoded) <= maxAttachmentFileBytes {
				filename := safeBase(a.Name)
				if filename == "" {
					filename = "document.pdf"
				}
				content = append(content, core.DocumentContent(a.Data, a.Mime, filename))
				continue
			}
			const pdfFallbackNote = "Nota: el modelo actual no soporta documentos PDF nativos, así que este\n" +
				"PDF no se envió directamente — está disponible en la ruta de arriba para\n" +
				"que lo proceses si hace falta (p.ej. con alguna herramienta de extracción\n" +
				"de texto si está disponible en el sistema)."
			block, err := toDisk(a, decoded, pdfFallbackNote)
			if err != nil {
				return nil, err
			}
			content = append(content, block)
			continue
		}

		inlineEligible := utf8.Valid(decoded) &&
			bytes.IndexByte(decoded, 0) == -1 &&
			len(decoded) <= maxAttachmentTextSize &&
			inlineTextBytes+len(decoded) <= maxInlineTextAggregate

		if inlineEligible {
			name := strings.ReplaceAll(a.Name, `"`, `\"`)
			text := fmt.Sprintf("<attachment name=\"%s\">\n%s\n</attachment>", name, decoded)
			content = append(content, core.TextContent(text))
			inlineTextBytes += len(decoded)
			continue
		}

		// To disk.
		block, err := toDisk(a, decoded, "")
		if err != nil {
			return nil, err
		}
		content = append(content, block)
	}
	return content, nil
}

// dirSize sums the sizes of regular files under dir. Best-effort: errors
// encountered while walking are ignored and the accumulated size so far is
// returned.
func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if info, ierr := d.Info(); ierr == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}
