package core

import (
	"bytes"
	"encoding/base64"
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
