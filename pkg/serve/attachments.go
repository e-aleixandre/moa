package serve

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/ealeixandre/moa/pkg/attachment"
	"github.com/ealeixandre/moa/pkg/bus"
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

const (
	maxAttachments         = 8
	maxImageBytes          = 5 << 20   // 5 MB decoded, per-image API limit
	maxImageDimension      = 100_000   // metadata guard; DecodeConfig never allocates pixels
	maxAttachmentFileBytes = 32 << 20  // per-file durable-file cap
	maxRequestBytes        = 64 << 20  // aggregate decoded bytes per request
	maxSessionDiskBytes    = 200 << 20 // aggregate on-disk bytes per session in degraded mode
	// maxSessionNativeDocBytes bounds native image content retained in history.
	// A per-file image cap alone would still permit unbounded session memory and
	// request growth across turns.
	maxSessionNativeDocBytes = 48 << 20 // 48 MB across the whole session
)

var allowedImageMimes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/gif":  true,
	"image/webp": true,
}

const nativeContentFallbackNote = "Nota: este adjunto supera el límite acumulado de contenido binario nativo de la sesión, así que se\n" +
	"guardó en disco para evitar que el historial y las solicitudes al modelo crezcan sin límite."

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
// core.Content blocks. Images use durable references when a store is available
// (otherwise legacy inline base64); every non-image is a byte-free durable
// document descriptor. Without a store, non-images retain the legacy temporary
// disk-path behavior so a degraded server remains usable.
func buildAttachmentContent(atts []Attachment, sessionID string, pp *tool.PathPolicy, priorNativeDocBytes int64, store *attachment.Store) (result []core.Content, writtenFiles []string, descriptors []attachment.Descriptor, retErr error) {
	if len(atts) > maxAttachments {
		return nil, nil, nil, fmt.Errorf("%w: too many attachments (max %d)", ErrBadAttachment, maxAttachments)
	}

	var (
		requestBytes      int64
		nativeBinaryBytes int64 // all native binary blocks this message

		sessionDir         string
		pathAdded          bool
		runningDiskBytes   int64
		writtenPaths       []string // files written during THIS call (for rollback)
		createdDescriptors []attachment.Descriptor
	)

	// On any error return after partial success, remove the files written
	// during THIS call so a failed /send does not leave orphan files counting
	// against the session quota. On success the paths are returned to the caller
	// so it can also roll them back if a downstream step (Bus.Execute) fails.
	defer func() {
		if retErr != nil {
			for _, p := range writtenPaths {
				_ = os.Remove(p)
			}
			if store != nil {
				for _, d := range createdDescriptors {
					_ = store.RemoveRef(sessionID, d.ID)
				}
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

	storeDurable := func(a Attachment, decoded []byte, meta attachment.PutMeta) (core.Content, error) {
		d, err := store.PutRef(sessionID, decoded, meta)
		if err != nil {
			return core.Content{}, fmt.Errorf("%w: attachment %q: could not store attachment: %v", ErrBadAttachment, a.Name, err)
		}
		blockType := "document"
		if meta.Kind == "image" {
			blockType = "image"
		}
		createdDescriptors = append(createdDescriptors, d)
		descriptors = append(descriptors, d)
		return core.Content{Type: blockType, AttachmentID: d.ID, AttachmentSize: d.Size, MimeType: d.Mime, Filename: d.Name}, nil
	}

	storeDocument := func(a Attachment, decoded []byte, mediaType string) (core.Content, error) {
		if len(decoded) > maxAttachmentFileBytes {
			return core.Content{}, fmt.Errorf("%w: attachment %q exceeds %d MB", ErrBadAttachment, a.Name, maxAttachmentFileBytes>>20)
		}
		if store == nil {
			return toDisk(a, decoded, "")
		}
		return storeDurable(a, decoded, attachment.PutMeta{Name: a.Name, Mime: mediaType, Kind: "file"})
	}

	content := make([]core.Content, 0, len(atts))
	for _, a := range atts {
		decoded, err := base64.StdEncoding.DecodeString(a.Data)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("%w: attachment %q: invalid base64", ErrBadAttachment, a.Name)
		}

		requestBytes += int64(len(decoded))
		if requestBytes > maxRequestBytes {
			return nil, nil, nil, fmt.Errorf("%w: attachments exceed %d MB total", ErrBadAttachment, maxRequestBytes>>20)
		}

		if allowedImageMimes[a.Mime] {
			// Trust the bytes, not the client-declared MIME: a mislabeled
			// binary must not be forwarded to the provider as an image.
			if !bytesLookLikeImage(decoded, a.Mime) {
				block, derr := storeDocument(a, decoded, safeAttachmentMime(http.DetectContentType(decoded)))
				if derr != nil {
					return nil, nil, nil, derr
				}
				content = append(content, block)
				continue
			}
			if len(decoded) > maxImageBytes {
				return nil, nil, nil, fmt.Errorf("%w: attachment %q: image exceeds %d MB", ErrBadAttachment, a.Name, maxImageBytes>>20)
			}
			if priorNativeDocBytes+nativeBinaryBytes+int64(len(decoded)) > maxSessionNativeDocBytes {
				block, derr := toDisk(a, decoded, nativeContentFallbackNote)
				if derr != nil {
					return nil, nil, nil, derr
				}
				content = append(content, block)
				continue
			}
			if store == nil {
				content = append(content, core.ImageContent(a.Data, a.Mime))
			} else {
				w, h := imageDimensions(decoded)
				block, err := storeDurable(a, decoded, attachment.PutMeta{
					Name: a.Name, Mime: a.Mime, Kind: "image", Width: w, Height: h,
				})
				if err != nil {
					return nil, nil, nil, err
				}
				content = append(content, block)
			}
			nativeBinaryBytes += int64(len(decoded))
			continue
		}

		if a.Mime == "application/pdf" {
			block, err := storeDocument(a, decoded, "application/pdf")
			if err != nil {
				return nil, nil, nil, err
			}
			content = append(content, block)
			continue
		}

		block, err := storeDocument(a, decoded, safeAttachmentMime(a.Mime))
		if err != nil {
			return nil, nil, nil, err
		}
		content = append(content, block)
	}
	return content, writtenPaths, descriptors, nil
}

func safeAttachmentMime(declared string) string {
	mediaType, _, err := mime.ParseMediaType(declared)
	if err != nil || mediaType == "" {
		return "application/octet-stream"
	}
	return mediaType
}

func imageDimensions(data []byte) (width, height int) {
	defer func() {
		if recover() != nil {
			width, height = 0, 0
		}
	}()
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width < 0 || config.Height < 0 || config.Width > maxImageDimension || config.Height > maxImageDimension {
		return 0, 0
	}
	return config.Width, config.Height
}

// priorNativeDocBytes returns the native image bytes already committed to (or
// in flight to) the session, for the per-session budget check.
// It reads the undelivered total (queued + inflight steers) BEFORE history: a
// steer only leaves the undelivered count once it is visible in history, so
// reading queue-side first then history can never miss bytes in the delivery
// window (at worst it double-counts an item transiting between the two reads,
// which fails closed — a spurious rejection the caller can retry — rather than
// underestimating and admitting content past the cap). History is recomputed
// live so compaction/clear naturally shrink the total.
func priorNativeDocBytes(sess *ManagedSession) int64 {
	undelivered, _ := bus.QueryTyped[bus.GetUndeliveredNativeBytes, int64](sess.runtime.Bus, bus.GetUndeliveredNativeBytes{})
	return undelivered + countNativeDocBytes(sess.History())
}

// countNativeDocBytes returns the total decoded byte size of native binary
// content blocks already present in history. Used to enforce the cumulative
// session cap for images, which are re-sent every turn.
// The Data field holds standard base64; its decoded length is ~len*3/4.
func countNativeDocBytes(msgs []core.AgentMessage) int64 {
	var total int64
	for _, m := range msgs {
		for _, c := range m.Content {
			if c.Type == "image" {
				if c.AttachmentSize > 0 {
					total += c.AttachmentSize
				} else {
					total += int64(base64.StdEncoding.DecodedLen(len(c.Data)))
				}
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
