package core

import (
	"bytes"
	"encoding/base64"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// MaxImageDimension is the largest side, in pixels, accepted for an inline
// image. It is Anthropic's limit: it rejects anything above 8000 px per side
// with a hard 400, and because history is replayed on every turn a single
// oversized image makes the whole conversation unsendable until the block is
// removed.
//
// OpenAI has no equivalent hard limit — it caps the request payload (512 MB)
// and the image count (1500), and oversized images are resized server-side to
// a patch/pixel budget rather than rejected. The one exception is GPT-5.6 with
// detail "original"/"auto" (what convertUserContent sends), which preserves
// the input dimensions and bills every patch: a 395x8239 screenshot is not an
// error there, just expensive. So the limit is enforced where images enter
// history (the read tool and attachments), which keeps a session portable
// across providers — a history built under OpenAI must not become unsendable
// the moment the user switches to Anthropic mid-session.
const MaxImageDimension = 8000

// imageHeaderBytes bounds how much of an image is decoded to read its size.
// DecodeConfig only needs the header; for JPEG the SOF marker sits after any
// EXIF/ICC segments, which this comfortably covers.
const imageHeaderBytes = 256 << 10

// ImageDimensions reports the pixel size from an image header. Returns 0,0 when
// the format is unsupported or the header is unreadable — an unknown size is
// never treated as oversized, so callers fall through to normal handling.
func ImageDimensions(data []byte) (width, height int) {
	defer func() {
		if recover() != nil {
			width, height = 0, 0
		}
	}()
	if len(data) > imageHeaderBytes {
		data = data[:imageHeaderBytes]
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// ImageExceedsMaxDimension reports whether a base64 image payload has a side
// above MaxImageDimension. Only the header is decoded, so the cost is bounded
// regardless of image size.
func ImageExceedsMaxDimension(b64 string) (width, height int, exceeds bool) {
	head := b64
	// base64 is 4 chars per 3 bytes; decode only what the header needs, cut on a
	// 4-char group boundary so the truncated string decodes without padding.
	if max := (imageHeaderBytes/3 + 1) * 4; len(head) > max {
		head = head[:max-max%4]
	}
	decoded, err := base64.StdEncoding.DecodeString(head)
	if err != nil {
		return 0, 0, false
	}
	w, h := ImageDimensions(decoded)
	return w, h, w > MaxImageDimension || h > MaxImageDimension
}
