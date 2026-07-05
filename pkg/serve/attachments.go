package serve

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
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
	// maxNativeDocBytes bounds the total bytes of native PDF "document" blocks
	// in ONE message. Native documents (unlike to-disk files) are embedded in
	// the conversation as base64 and re-sent every turn, so an unbounded native
	// PDF payload would inflate persistence/memory/request size across turns.
	// A single message is capped here; larger PDFs fall back to disk (which is
	// governed by the per-session disk quota instead).
	maxNativeDocBytes = 24 << 20 // 24 MB of native PDF per message
	// maxSessionNativeDocBytes bounds the CUMULATIVE bytes of native document
	// blocks already present in the conversation history plus the new message.
	// Because native documents are re-sent every turn, without this a user could
	// accumulate ~maxNativeDocBytes per message indefinitely, bypassing the
	// on-disk session quota and growing memory/persistence/requests unboundedly.
	// Once history already holds this many native-doc bytes, further PDFs fall
	// back to disk instead of going native.
	maxSessionNativeDocBytes = 48 << 20 // 48 MB of native PDF across the whole session
)

var allowedImageMimes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/gif":  true,
	"image/webp": true,
}

// pdfFallbackNote is appended when a PDF is saved to disk because the active
// provider can't accept native documents.
const pdfFallbackNote = "Nota: el modelo actual no soporta documentos PDF nativos, así que este\n" +
	"PDF no se envió directamente — está disponible en la ruta de arriba para\n" +
	"que lo proceses si hace falta (p.ej. con alguna herramienta de extracción\n" +
	"de texto si está disponible en el sistema)."

// mimeMismatchNote is appended when an attachment's declared MIME (image/PDF)
// does not match its actual bytes, so it is saved to disk instead of being
// forwarded natively to the provider (which would reject it).
const mimeMismatchNote = "Nota: el tipo declarado del archivo no coincide con su contenido real,\n" +
	"así que no se envió como imagen/PDF nativo — está guardado en disco para\n" +
	"que lo inspecciones."

// bytesLookLikeImage reports whether data's magic bytes match the declared
// image MIME. Uses net/http content sniffing plus explicit checks so a binary
// mislabeled as image/png isn't forwarded to the provider as an image.
func bytesLookLikeImage(data []byte, declaredMime string) bool {
	if len(data) < 12 {
		return false
	}
	sniffed := http.DetectContentType(data) // e.g. "image/png", "image/jpeg"
	switch declaredMime {
	case "image/jpeg":
		return sniffed == "image/jpeg"
	case "image/png":
		return sniffed == "image/png"
	case "image/gif":
		return sniffed == "image/gif"
	case "image/webp":
		// http.DetectContentType returns "image/webp" for RIFF....WEBP.
		return sniffed == "image/webp"
	default:
		return false
	}
}

// bytesLookLikePDF reports whether data begins with the PDF magic header
// "%PDF-". A short run of leading whitespace or a UTF-8/UTF-16 BOM is tolerated
// (some producers prepend those), but the magic must appear at the START —
// finding "%PDF-" anywhere in the file is NOT sufficient (an arbitrary binary
// could contain that sequence and would then be mis-sent as a native document).
func bytesLookLikePDF(data []byte) bool {
	// Strip a leading BOM if present.
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}): // UTF-8 BOM
		data = data[3:]
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}), bytes.HasPrefix(data, []byte{0xFF, 0xFE}): // UTF-16 BOM
		data = data[2:]
	}
	// Skip a small amount of leading ASCII whitespace.
	i := 0
	for i < len(data) && i < 8 && (data[i] == ' ' || data[i] == '\t' || data[i] == '\r' || data[i] == '\n' || data[i] == '\f' || data[i] == 0x00) {
		i++
	}
	return bytes.HasPrefix(data[i:], []byte("%PDF-"))
}

// buildAttachmentContent validates and converts uploaded attachments into
// core.Content blocks: images become "image" blocks (reusing the original
// base64 string). Small UTF-8 text is inlined in a <attachment> sentinel;
// everything else that doesn't fit inline is written to the session's
// attachment directory on disk and referenced by path instead. PDFs go
// native as a "document" block when supportsDocuments is true and the file
// is small enough; otherwise they fall back to the same on-disk mechanism.
func buildAttachmentContent(atts []Attachment, sessionID string, pp *tool.PathPolicy, supportsDocuments bool, priorNativeDocBytes int64) (result []core.Content, retErr error) {
	if len(atts) > maxAttachments {
		return nil, fmt.Errorf("%w: too many attachments (max %d)", ErrBadAttachment, maxAttachments)
	}

	var (
		requestBytes    int64
		inlineTextBytes int
		nativeDocBytes  int64 // total bytes of native PDF blocks this message

		sessionDir       string
		pathAdded        bool
		runningDiskBytes int64
		writtenPaths     []string // files written during THIS call (for rollback)
	)

	// On any error return after partial success, remove the files written
	// during THIS call so a failed /send does not leave orphan files counting
	// against the session quota (the path-allowlist entry is idempotent and
	// harmless, so it is intentionally left in place).
	defer func() {
		if retErr != nil {
			for _, p := range writtenPaths {
				_ = os.Remove(p)
			}
		}
	}()

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
			if err := pp.AddPath(sessionDir); err != nil {
				// Don't tell the agent it has access if the policy rejected the
				// path — that would be a false promise. Surface it instead.
				return core.Content{}, fmt.Errorf("%w: could not grant access to attachment storage: %v", ErrBadAttachment, err)
			}
			pathAdded = true
		}
		if runningDiskBytes+int64(len(decoded)) > maxSessionDiskBytes {
			return core.Content{}, fmt.Errorf("%w: session attachment storage exceeds %d MB", ErrBadAttachment, maxSessionDiskBytes>>20)
		}
		finalPath, err := writeUnique(sessionDir, a.Name, decoded)
		if err != nil {
			return core.Content{}, fmt.Errorf("%w: attachment %q: could not save to disk: %v", ErrBadAttachment, a.Name, err)
		}
		writtenPaths = append(writtenPaths, finalPath)
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
			// Trust the bytes, not the client-declared MIME: a mislabeled
			// binary must not be forwarded to the provider as an image.
			if !bytesLookLikeImage(decoded, a.Mime) {
				block, derr := toDisk(a, decoded, mimeMismatchNote)
				if derr != nil {
					return nil, derr
				}
				content = append(content, block)
				continue
			}
			if len(decoded) > maxImageBytes {
				return nil, fmt.Errorf("%w: attachment %q: image exceeds %d MB", ErrBadAttachment, a.Name, maxImageBytes>>20)
			}
			content = append(content, core.ImageContent(a.Data, a.Mime))
			continue
		}

		if a.Mime == "application/pdf" {
			// Only send natively if the bytes are actually a PDF (%PDF- magic),
			// the provider supports documents, the file fits the per-file cap,
			// AND adding it stays within the per-message native-doc budget
			// (native PDFs are re-sent every turn, so they can't be unbounded).
			isPDF := bytesLookLikePDF(decoded)
			if isPDF && supportsDocuments &&
				len(decoded) <= maxAttachmentFileBytes &&
				nativeDocBytes+int64(len(decoded)) <= maxNativeDocBytes &&
				priorNativeDocBytes+nativeDocBytes+int64(len(decoded)) <= maxSessionNativeDocBytes {
				filename := safeBase(a.Name)
				if filename == "" {
					filename = "document.pdf"
				}
				content = append(content, core.DocumentContent(a.Data, a.Mime, filename))
				nativeDocBytes += int64(len(decoded))
				continue
			}
			note := pdfFallbackNote
			if !isPDF {
				note = mimeMismatchNote
			}
			block, err := toDisk(a, decoded, note)
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

// countNativeDocBytes returns the total decoded byte size of native "document"
// content blocks already present in the conversation history. Used to enforce a
// cumulative per-session cap on native PDFs (which are re-sent every turn).
// The Data field holds standard base64; its decoded length is ~len*3/4.
func countNativeDocBytes(msgs []core.AgentMessage) int64 {
	var total int64
	for _, m := range msgs {
		for _, c := range m.Content {
			if c.Type == "document" {
				total += int64(base64.StdEncoding.DecodedLen(len(c.Data)))
			}
		}
	}
	return total
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
