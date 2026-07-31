package core

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/jpeg"
	"testing"
)

func jpegOfSize(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h)), &jpeg.Options{Quality: 1}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

func TestImageDimensions(t *testing.T) {
	w, h := ImageDimensions(jpegOfSize(t, 40, 90))
	if w != 40 || h != 90 {
		t.Fatalf("got %dx%d, want 40x90", w, h)
	}
}

func TestImageDimensions_Unreadable(t *testing.T) {
	if w, h := ImageDimensions([]byte("not an image")); w != 0 || h != 0 {
		t.Fatalf("got %dx%d, want 0x0 for undecodable input", w, h)
	}
}

func TestImageExceedsMaxDimension(t *testing.T) {
	tall := base64.StdEncoding.EncodeToString(jpegOfSize(t, 100, MaxImageDimension+1))
	w, h, over := ImageExceedsMaxDimension(tall)
	if !over || w != 100 || h != MaxImageDimension+1 {
		t.Fatalf("tall image: got %dx%d over=%v, want oversize", w, h, over)
	}

	ok := base64.StdEncoding.EncodeToString(jpegOfSize(t, 100, MaxImageDimension))
	if _, _, over := ImageExceedsMaxDimension(ok); over {
		t.Fatal("image exactly at the limit must be accepted")
	}

	if _, _, over := ImageExceedsMaxDimension("!!!not base64!!!"); over {
		t.Fatal("undecodable payload must not be reported as oversized")
	}
}

// A payload larger than the header window must still be measured: only the
// header is decoded, and truncating base64 must not break decoding.
func TestImageExceedsMaxDimension_LargePayload(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 200, MaxImageDimension+1))
	for i := range img.Pix {
		img.Pix[i] = byte(i * 7)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if buf.Len() <= imageHeaderBytes {
		t.Fatalf("payload %d bytes is not larger than the header window", buf.Len())
	}
	w, h, over := ImageExceedsMaxDimension(base64.StdEncoding.EncodeToString(buf.Bytes()))
	if !over || w != 200 || h != MaxImageDimension+1 {
		t.Fatalf("got %dx%d over=%v, want oversize", w, h, over)
	}
}

// buildWebP assembles a minimal WebP container of the given variant.
func buildWebP(variant string, w, h int) []byte {
	var chunk []byte
	switch variant {
	case "VP8 ":
		chunk = make([]byte, 18)
		copy(chunk, []byte{0x9d, 0x01, 0x2a}[:0]) // placed below at the right offset
		chunk[3], chunk[4], chunk[5] = 0x9d, 0x01, 0x2a
		binary.LittleEndian.PutUint16(chunk[6:8], uint16(w))
		binary.LittleEndian.PutUint16(chunk[8:10], uint16(h))
	case "VP8L":
		chunk = make([]byte, 9)
		chunk[0] = 0x2f
		bits := uint32(w-1) | uint32(h-1)<<14
		binary.LittleEndian.PutUint32(chunk[1:5], bits)
	case "VP8X":
		chunk = make([]byte, 10)
		cw, ch := uint32(w-1), uint32(h-1)
		chunk[4], chunk[5], chunk[6] = byte(cw), byte(cw>>8), byte(cw>>16)
		chunk[7], chunk[8], chunk[9] = byte(ch), byte(ch>>8), byte(ch>>16)
	}
	out := []byte("RIFF")
	out = binary.LittleEndian.AppendUint32(out, uint32(12+len(chunk)))
	out = append(out, "WEBP"...)
	out = append(out, variant...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(chunk)))
	return append(out, chunk...)
}

// WebP has no decoder in the standard library, so before this it measured as
// 0x0 — i.e. "not oversized" — and an 12000px WebP sailed through to a provider
// that rejects it, breaking every later turn.
func TestImageDimensions_WebP(t *testing.T) {
	for _, tc := range []struct {
		variant string
		w, h    int
	}{
		{"VP8 ", 12000, 300},
		{"VP8L", 9000, 200},
		{"VP8X", 16384, 100},
	} {
		w, h := ImageDimensions(buildWebP(tc.variant, tc.w, tc.h))
		if w != tc.w || h != tc.h {
			t.Errorf("%s: got %dx%d, want %dx%d", tc.variant, w, h, tc.w, tc.h)
		}
		if w <= MaxImageDimension {
			t.Errorf("%s: %dx%d should read as oversized", tc.variant, w, h)
		}
	}
}

func TestImageExceedsMaxDimension_WebP(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString(buildWebP("VP8 ", 12000, 300))
	w, h, exceeds := ImageExceedsMaxDimension(b64)
	if !exceeds {
		t.Fatalf("expected oversized, got %dx%d exceeds=%v", w, h, exceeds)
	}
}

// A JPEG may carry megabytes of EXIF/ICC before its frame header. Truncating
// the read at a fixed window made DecodeConfig fail, which read as 0x0 and let
// an oversized image through.
func TestImageDimensions_JPEGWithLateSOF(t *testing.T) {
	buf := []byte{0xff, 0xd8}
	for i := 0; i < 6; i++ { // ~390 KB of APP1, past imageHeaderBytes
		seg := make([]byte, 65535)
		buf = append(buf, 0xff, 0xe1)
		binary.BigEndian.PutUint16(seg[0:2], 65535)
		buf = append(buf, seg...)
	}
	sof := make([]byte, 11)
	binary.BigEndian.PutUint16(sof[0:2], 11)
	sof[2] = 8
	binary.BigEndian.PutUint16(sof[3:5], 9001) // height
	binary.BigEndian.PutUint16(sof[5:7], 40)   // width
	buf = append(buf, 0xff, 0xc0)
	buf = append(buf, sof...)

	w, h := ImageDimensions(buf)
	if w != 40 || h != 9001 {
		t.Fatalf("got %dx%d, want 40x9001", w, h)
	}

	if _, _, exceeds := ImageExceedsMaxDimension(base64.StdEncoding.EncodeToString(buf)); !exceeds {
		t.Error("a 40x9001 JPEG with a late SOF should read as oversized")
	}
}

// Whatever cannot be measured must stay measurable-as-fine, or unknown formats
// would start being rejected.
func TestImageDimensions_UnknownStaysZero(t *testing.T) {
	for name, data := range map[string][]byte{
		"empty":         {},
		"garbage":       []byte("not an image at all"),
		"truncatedRIFF": []byte("RIFF\x00\x00\x00\x00WEBPVP8 "),
		"jpegNoSOF":     {0xff, 0xd8, 0xff, 0xda, 0x00, 0x02},
	} {
		if w, h := ImageDimensions(data); w != 0 || h != 0 {
			t.Errorf("%s: got %dx%d, want 0x0", name, w, h)
		}
	}
}

// The entry points (read tool, attachments) hand over decoded bytes, and those
// must be measured however far in the frame header sits — a cap there would be
// a way in for an image with enough metadata in front of it.
func TestImageDimensions_JPEGWithVeryLateSOF(t *testing.T) {
	buf := []byte{0xff, 0xd8}
	// ~5 MB of APP1 segments, past both imageHeaderBytes and jpegScanBytes.
	for len(buf) < 5<<20 {
		seg := make([]byte, 65535)
		binary.BigEndian.PutUint16(seg[0:2], 65535)
		buf = append(buf, 0xff, 0xe1)
		buf = append(buf, seg...)
	}
	sof := make([]byte, 11)
	binary.BigEndian.PutUint16(sof[0:2], 11)
	sof[2] = 8
	binary.BigEndian.PutUint16(sof[3:5], 9001)
	binary.BigEndian.PutUint16(sof[5:7], 40)
	buf = append(buf, 0xff, 0xc0)
	buf = append(buf, sof...)

	w, h := ImageDimensions(buf)
	if w != 40 || h != 9001 {
		t.Fatalf("got %dx%d, want 40x9001 (the guarded paths must not cap the scan)", w, h)
	}
}

func TestImageMimeFromBytes(t *testing.T) {
	gif := append([]byte("GIF89a"), make([]byte, 20)...)
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 20)...)
	webp := append(append([]byte("RIFF"), []byte{0, 0, 0, 0}...), []byte("WEBPVP8 ")...)
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"gif", gif, "image/gif"},
		{"png", png, "image/png"},
		{"jpeg", jpegOfSize(t, 4, 4), "image/jpeg"},
		{"webp", webp, "image/webp"},
		{"unknown", []byte("not an image at all"), ""},
		{"short", []byte("GI"), ""},
		// "GIF" alone is a prefix, not a signature: the version bytes are part
		// of the magic, so an arbitrary payload starting with it is not a GIF.
		{"gif prefix only", []byte("GIFT for you, not an image"), ""},
	}
	for _, c := range cases {
		if got := ImageMimeFromBytes(c.data); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// A block recorded with the wrong media type (a GIF read from a .png) is what
// Anthropic rejects with a hard 400; the correction must win over the label.
func TestCorrectImageMime(t *testing.T) {
	gif := base64.StdEncoding.EncodeToString(append([]byte("GIF89a"), make([]byte, 4096)...))
	if got := CorrectImageMime(gif, "image/png"); got != "image/gif" {
		t.Errorf("mislabeled GIF: got %q, want image/gif", got)
	}
	jpg := base64.StdEncoding.EncodeToString(jpegOfSize(t, 10, 10))
	if got := CorrectImageMime(jpg, "image/jpeg"); got != "image/jpeg" {
		t.Errorf("honest JPEG: got %q", got)
	}
	// Unrecognized payloads keep the declared type rather than being guessed at.
	if got := CorrectImageMime(base64.StdEncoding.EncodeToString([]byte("mystery bytes")), "image/png"); got != "image/png" {
		t.Errorf("unknown format: got %q, want the declared type", got)
	}
	if got := CorrectImageMime("!!not base64!!", "image/png"); got != "image/png" {
		t.Errorf("undecodable payload: got %q, want the declared type", got)
	}
}

// Standard base64 tolerates CR/LF, so a line-wrapped payload must still be
// recognized: failing to decode here would leave a poisoned history declaring
// the wrong media type forever.
func TestCorrectImageMime_LineWrappedPayload(t *testing.T) {
	raw := base64.StdEncoding.EncodeToString(append([]byte("GIF89a"), make([]byte, 256)...))
	wrapped := ""
	for i := 0; i < len(raw); i += 4 {
		end := i + 4
		if end > len(raw) {
			end = len(raw)
		}
		wrapped += raw[i:end] + "\r\n"
	}
	if got := CorrectImageMime(wrapped, "image/png"); got != "image/gif" {
		t.Errorf("wrapped GIF: got %q, want image/gif", got)
	}
	// Newlines landing mid-quantum are the case an index cut would break.
	if got := CorrectImageMime(raw[:2]+"\n"+raw[2:], "image/png"); got != "image/gif" {
		t.Errorf("GIF with a newline inside the first quantum: got %q, want image/gif", got)
	}
}
