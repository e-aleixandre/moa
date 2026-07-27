package core

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
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
// DecodeConfig only needs the header, which for JPEG means everything up to the
// SOF marker. That marker sits after any EXIF/ICC segments, and those are
// bounded only by the format itself, so a header can legitimately be larger
// than this — scanJPEGSize handles that case without buffering the whole file.
const imageHeaderBytes = 256 << 10

// jpegScanBytes bounds the segment walk for a JPEG whose SOF sits past
// imageHeaderBytes. The walk jumps segment to segment rather than reading them,
// so this only bounds how far a deliberately padded file can push the frame
// header before it is treated as unmeasurable.
const jpegScanBytes = 4 << 20

// ImageDimensions reports the pixel size from an image header. Returns 0,0 when
// the format is unsupported or the header is unreadable — an unknown size is
// never treated as oversized, so callers fall through to normal handling.
func ImageDimensions(data []byte) (width, height int) {
	defer func() {
		if recover() != nil {
			width, height = 0, 0
		}
	}()
	// WebP is accepted as an inline image but has no decoder in the standard
	// library, so image.DecodeConfig reports "unknown format" and the size
	// silently reads as 0x0 — which every caller treats as "fine to send".
	// The header carries the size in a fixed layout, so read it directly
	// rather than pull in a full decoder for four integers.
	if w, h, ok := webPSize(data); ok {
		return w, h
	}
	head := data
	if len(head) > imageHeaderBytes {
		head = head[:imageHeaderBytes]
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(head))
	if err != nil {
		// A JPEG whose SOF sits past the truncation point fails here, and
		// falling through would report 0x0 (i.e. "not oversized"). Walk the
		// segment chain instead, which skips over EXIF/ICC without decoding.
		if w, h, ok := scanJPEGSize(data); ok {
			return w, h
		}
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// webPSize reads the pixel size out of a WebP header. Covers the three
// container variants (lossy VP8, lossless VP8L, extended VP8X); returns false
// for anything else, including a truncated header.
func webPSize(data []byte) (width, height int, ok bool) {
	if len(data) < 21 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return 0, 0, false
	}
	switch string(data[12:16]) {
	case "VP8 ":
		// Lossy: 3-byte frame tag, 3-byte start code, then 14-bit dimensions.
		if len(data) < 30 || data[23] != 0x9d || data[24] != 0x01 || data[25] != 0x2a {
			return 0, 0, false
		}
		w := int(binary.LittleEndian.Uint16(data[26:28]) & 0x3fff)
		h := int(binary.LittleEndian.Uint16(data[28:30]) & 0x3fff)
		return w, h, true
	case "VP8L":
		// Lossless: signature byte, then 14-bit width and height minus one,
		// packed across a little-endian 32-bit field.
		if len(data) < 25 || data[20] != 0x2f {
			return 0, 0, false
		}
		bits := binary.LittleEndian.Uint32(data[21:25])
		return int(bits&0x3fff) + 1, int((bits>>14)&0x3fff) + 1, true
	case "VP8X":
		// Extended: 24-bit canvas size minus one.
		if len(data) < 30 {
			return 0, 0, false
		}
		w := int(uint32(data[24]) | uint32(data[25])<<8 | uint32(data[26])<<16)
		h := int(uint32(data[27]) | uint32(data[28])<<8 | uint32(data[29])<<16)
		return w + 1, h + 1, true
	}
	return 0, 0, false
}

// scanJPEGSize walks the JPEG segment chain to the frame header, skipping
// metadata segments by their declared length instead of decoding them. Used
// when the SOF marker sits beyond imageHeaderBytes, which happens with large
// EXIF or embedded colour profiles.
func scanJPEGSize(data []byte) (width, height int, ok bool) {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return 0, 0, false
	}
	if len(data) > jpegScanBytes {
		data = data[:jpegScanBytes]
	}
	for i := 2; i+3 < len(data); {
		if data[i] != 0xff {
			return 0, 0, false
		}
		marker := data[i+1]
		// Padding and standalone markers carry no length field.
		if marker == 0xff {
			i++
			continue
		}
		if marker == 0x01 || (marker >= 0xd0 && marker <= 0xd9) {
			i += 2
			continue
		}
		if i+3 >= len(data) {
			return 0, 0, false
		}
		length := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
		if length < 2 {
			return 0, 0, false
		}
		// SOF0..SOF15, excluding the DHT/JPG/DAC markers interleaved in that
		// range, carry the frame dimensions.
		if marker >= 0xc0 && marker <= 0xcf && marker != 0xc4 && marker != 0xc8 && marker != 0xcc {
			if i+9 >= len(data) {
				return 0, 0, false
			}
			h := int(binary.BigEndian.Uint16(data[i+5 : i+7]))
			w := int(binary.BigEndian.Uint16(data[i+7 : i+9]))
			return w, h, true
		}
		// Entropy-coded data follows the scan header; the size is not here.
		if marker == 0xda {
			return 0, 0, false
		}
		i += 2 + length
	}
	return 0, 0, false
}

// ImageExceedsMaxDimension reports whether a base64 image payload has a side
// above MaxImageDimension. Only the header is decoded, so the cost is bounded
// regardless of image size.
func ImageExceedsMaxDimension(b64 string) (width, height int, exceeds bool) {
	// Try the small window first: almost every image puts its size in the first
	// few hundred bytes, and this runs for every image block on every request,
	// so the common case must not pay for the pathological one.
	if w, h, ok := decodePrefix(b64, imageHeaderBytes); ok {
		return w, h, w > MaxImageDimension || h > MaxImageDimension
	}
	// Nothing readable in the prefix: either an unsupported format, or a JPEG
	// whose frame header sits behind a lot of metadata. Only that second case
	// justifies the wider window.
	w, h, _ := decodePrefix(b64, jpegScanBytes)
	return w, h, w > MaxImageDimension || h > MaxImageDimension
}

// decodePrefix measures the image from the first `limit` bytes of a base64
// payload. ok reports whether a size was actually read.
func decodePrefix(b64 string, limit int) (width, height int, ok bool) {
	head := b64
	// base64 is 4 chars per 3 bytes; decode only what the header needs, cut on a
	// 4-char group boundary so the truncated string decodes without padding.
	if max := (limit/3 + 1) * 4; len(head) > max {
		head = head[:max-max%4]
	}
	decoded, err := base64.StdEncoding.DecodeString(head)
	if err != nil {
		return 0, 0, false
	}
	w, h := ImageDimensions(decoded)
	return w, h, w > 0 && h > 0
}
