package serve

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/tool"
)

func b64(data []byte) string { return base64.StdEncoding.EncodeToString(data) }

// pngBytes returns a byte slice that starts with a valid PNG signature + IHDR
// so http.DetectContentType classifies it as image/png. The trailing padding
// lets callers reach a desired total size for limit tests. This exists because
// buildAttachmentContent now sniffs magic bytes and refuses to forward a
// mislabeled binary to the provider as an image.
func pngBytes(total int) []byte {
	sig := []byte{
		0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, // PNG signature
		0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R', // IHDR chunk header
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, // 1x1
		0x08, 0x02, 0x00, 0x00, 0x00, // bit depth/color/etc.
	}
	if total <= len(sig) {
		return sig
	}
	out := make([]byte, total)
	copy(out, sig)
	return out
}

func sendBody(t *testing.T, text string, atts []Attachment) string {
	t.Helper()
	body := struct {
		Text        string       `json:"text"`
		Attachments []Attachment `json:"attachments,omitempty"`
	}{Text: text, Attachments: atts}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSend_WithImageAttachment(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()

	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	atts := []Attachment{{Name: "captura.png", Mime: "image/png", Data: b64(pngBytes(64))}}
	resp := apiReq(t, srv, "POST", "/api/sessions/"+sess.ID+"/send", sendBody(t, "mira esta captura", atts))
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != 202 {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}
	var out map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["action"] != "send" {
		t.Fatalf("expected action=send, got %q", out["action"])
	}

	pollUntil(t, 5*time.Second, "session idle after send", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sessState(sess) == StateIdle
	})

	// The first user message must carry the attachment block before the text
	// block (per the API's recommended ordering for image/document + text).
	msgs := sess.History()
	var userContent []core.Content
	for _, m := range msgs {
		if m.Role == "user" {
			userContent = m.Content
			break
		}
	}
	if userContent == nil {
		t.Fatal("expected a user message in history")
	}
	if len(userContent) != 2 {
		t.Fatalf("expected 2 content blocks (image, text), got %d", len(userContent))
	}
	if userContent[0].Type != "image" {
		t.Fatalf("expected first block to be image, got %q", userContent[0].Type)
	}
	if userContent[1].Type != "text" || userContent[1].Text != "mira esta captura" {
		t.Fatalf("expected second block to be the user text, got %+v", userContent[1])
	}
}

func TestSend_AttachmentOnlyNoText(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Delay the run's response so the crude first-message title (set
	// synchronously in Manager.Send) can be observed before the async
	// auto-title pass (which fires only after the run ends) overwrites it.
	prov := newMockProvider(delayedResponseHandler(300*time.Millisecond, "done"))
	mgr := newTestManager(t, ctx, prov)
	httpSrv := httptest.NewServer(NewServer(mgr))
	defer httpSrv.Close()

	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	atts := []Attachment{{Name: "ventas.csv", Mime: "text/csv", Data: b64([]byte("a,b\n1,2\n"))}}
	resp := apiReq(t, httpSrv, "POST", "/api/sessions/"+sess.ID+"/send", sendBody(t, "", atts))
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != 202 {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}

	if got := sess.title(); got != "ventas.csv" {
		t.Fatalf("expected title=ventas.csv, got %q", got)
	}

	pollUntil(t, 5*time.Second, "session idle after send", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sessState(sess) == StateIdle
	})
}

// TestSend_BinaryGoesToDisk covers the case of a non-text, non-image, non-PDF
// attachment: it no longer errors, it's routed to disk under the session's
// attachment dir and the agent gets a text advisory pointing at the path.
func TestSend_BinaryGoesToDisk(t *testing.T) {
	t.Setenv("MOA_ATTACHMENTS_DIR", t.TempDir())

	srv, mgr, cancel := newTestServer(t)
	defer cancel()

	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	badBytes := []byte{0xff, 0xfe, 0x00, 0x01}
	atts := []Attachment{{Name: "weird.bin", Mime: "application/x-msdownload", Data: b64(badBytes)}}
	resp := apiReq(t, srv, "POST", "/api/sessions/"+sess.ID+"/send", sendBody(t, "hi", atts))
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != 202 {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}

	pollUntil(t, 5*time.Second, "session idle after send", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sessState(sess) == StateIdle
	})

	dir, err := sessionAttachDir(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("expected attach dir to exist: %v", err)
	}
	if len(entries) < 1 {
		t.Fatalf("expected at least 1 file in %q, got %d", dir, len(entries))
	}

	msgs := sess.History()
	var found bool
	for _, m := range msgs {
		if m.Role != "user" {
			continue
		}
		for _, c := range m.Content {
			if c.Type == "text" && strings.Contains(c.Text, "guardado en:") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("expected user message to contain a text block mentioning the saved path")
	}
}

func TestSend_AttachmentTooLarge(t *testing.T) {
	t.Setenv("MOA_ATTACHMENTS_DIR", t.TempDir())

	srv, mgr, cancel := newTestServer(t)
	defer cancel()

	t.Run("image", func(t *testing.T) {
		sess, err := mgr.CreateSession(CreateOpts{})
		if err != nil {
			t.Fatal(err)
		}
		big := pngBytes(maxImageBytes + 1)
		atts := []Attachment{{Name: "huge.png", Mime: "image/png", Data: b64(big)}}
		resp := apiReq(t, srv, "POST", "/api/sessions/"+sess.ID+"/send", sendBody(t, "hi", atts))
		defer resp.Body.Close() //nolint:errcheck
		if resp.StatusCode != 400 {
			t.Fatalf("expected 400, got %d", resp.StatusCode)
		}
	})

	t.Run("text", func(t *testing.T) {
		sess, err := mgr.CreateSession(CreateOpts{})
		if err != nil {
			t.Fatal(err)
		}
		// A >256KiB text file no longer errors — it overflows the inline
		// per-file cap and is routed to disk instead.
		big := bytes.Repeat([]byte("a"), maxAttachmentTextSize+1)
		atts := []Attachment{{Name: "huge.txt", Mime: "text/plain", Data: b64(big)}}
		resp := apiReq(t, srv, "POST", "/api/sessions/"+sess.ID+"/send", sendBody(t, "hi", atts))
		defer resp.Body.Close() //nolint:errcheck
		if resp.StatusCode != 202 {
			t.Fatalf("expected 202, got %d", resp.StatusCode)
		}
	})
}

func TestSend_AttachmentBadBase64(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()

	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	atts := []Attachment{{Name: "bad.txt", Mime: "text/plain", Data: "not-valid-base64!!!"}}
	resp := apiReq(t, srv, "POST", "/api/sessions/"+sess.ID+"/send", sendBody(t, "hi", atts))
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != 400 {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestSend_TooManyAttachments(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()

	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	atts := make([]Attachment, 0, 9)
	for i := 0; i < 9; i++ {
		atts = append(atts, Attachment{
			Name: fmt.Sprintf("f%d.png", i), Mime: "image/png", Data: b64([]byte("x")),
		})
	}
	resp := apiReq(t, srv, "POST", "/api/sessions/"+sess.ID+"/send", sendBody(t, "hi", atts))
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != 400 {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestSend_AttachmentsWhileRunning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prov := newMockProvider(delayedResponseHandler(500*time.Millisecond, "slow"))
	mgr := newTestManager(t, ctx, prov)
	httpSrv := httptest.NewServer(NewServer(mgr))
	defer httpSrv.Close()

	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	resp := apiReq(t, httpSrv, "POST", "/api/sessions/"+sess.ID+"/send", sendBody(t, "first", nil))
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != 202 {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}

	pollUntil(t, 2*time.Second, "running", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sessState(sess) == StateRunning
	})

	atts := []Attachment{{Name: "x.png", Mime: "image/png", Data: b64([]byte("x"))}}
	resp2 := apiReq(t, httpSrv, "POST", "/api/sessions/"+sess.ID+"/send", sendBody(t, "second", atts))
	defer resp2.Body.Close() //nolint:errcheck
	if resp2.StatusCode != 409 {
		t.Fatalf("expected 409 for attachments while running, got %d", resp2.StatusCode)
	}

	// Text-only steer still works while running.
	resp3 := apiReq(t, httpSrv, "POST", "/api/sessions/"+sess.ID+"/send", sendBody(t, "steer text", nil))
	defer resp3.Body.Close() //nolint:errcheck
	if resp3.StatusCode != 202 {
		t.Fatalf("expected 202 for text-only steer, got %d", resp3.StatusCode)
	}
	var out map[string]string
	_ = json.NewDecoder(resp3.Body).Decode(&out)
	if out["action"] != "steer" {
		t.Fatalf("expected action=steer, got %q", out["action"])
	}

	pollUntil(t, 2*time.Second, "idle", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sessState(sess) == StateIdle || sessState(sess) == StateError
	})
}

func TestBuildAttachmentContent_TextHeader(t *testing.T) {
	t.Setenv("MOA_ATTACHMENTS_DIR", t.TempDir())
	pp := tool.NewPathPolicy(t.TempDir(), nil, false)

	atts := []Attachment{
		{Name: `report "final".csv`, Mime: "text/csv", Data: b64([]byte("a,b\n1,2"))},
	}
	content, err := buildAttachmentContent(atts, "abcdef0123456789", pp, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 1 {
		t.Fatalf("expected 1 block, got %d", len(content))
	}
	if content[0].Type != "text" {
		t.Fatalf("expected text block, got %q", content[0].Type)
	}
	want := "<attachment name=\"report \\\"final\\\".csv\">\na,b\n1,2\n</attachment>"
	if content[0].Text != want {
		t.Fatalf("unexpected sentinel:\n got: %q\nwant: %q", content[0].Text, want)
	}
}

func TestBuildAttachmentContent_LargeTextToDisk(t *testing.T) {
	t.Setenv("MOA_ATTACHMENTS_DIR", t.TempDir())
	pp := tool.NewPathPolicy(t.TempDir(), nil, false)

	big := bytes.Repeat([]byte("a"), 300<<10) // 300 KiB, valid UTF-8
	atts := []Attachment{{Name: "big.txt", Mime: "text/plain", Data: b64(big)}}

	sessionID := "0123456789abcdef"
	content, err := buildAttachmentContent(atts, sessionID, pp, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 1 {
		t.Fatalf("expected 1 block, got %d", len(content))
	}
	if content[0].Type != "text" {
		t.Fatalf("expected text block, got %q", content[0].Type)
	}
	if !strings.Contains(content[0].Text, "guardado en:") {
		t.Fatalf("expected advisory text, got %q", content[0].Text)
	}

	dir, err := sessionAttachDir(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("expected session dir to exist: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file on disk, got %d", len(entries))
	}

	found := false
	for _, p := range pp.AllowedPaths() {
		if p == dir {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected pp.AllowedPaths() to include %q, got %v", dir, pp.AllowedPaths())
	}
}

func TestBuildAttachmentContent_Collision(t *testing.T) {
	t.Setenv("MOA_ATTACHMENTS_DIR", t.TempDir())
	pp := tool.NewPathPolicy(t.TempDir(), nil, false)

	binary := []byte{0x00, 0x01, 0x02, 0x03}
	atts := []Attachment{
		{Name: "data.bin", Mime: "application/octet-stream", Data: b64(binary)},
		{Name: "data.bin", Mime: "application/octet-stream", Data: b64(binary)},
	}

	sessionID := "fedcba9876543210"
	content, err := buildAttachmentContent(atts, sessionID, pp, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(content))
	}
	for _, c := range content {
		if c.Type != "text" {
			t.Fatalf("expected text block, got %q", c.Type)
		}
	}

	dir, err := sessionAttachDir(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool, len(entries))
	for _, e := range entries {
		names[e.Name()] = true
	}
	if !names["data.bin"] || !names["data-2.bin"] {
		t.Fatalf("expected data.bin and data-2.bin, got %v", names)
	}
}

func TestBuildAttachmentContent_InlineAggregate(t *testing.T) {
	t.Setenv("MOA_ATTACHMENTS_DIR", t.TempDir())
	pp := tool.NewPathPolicy(t.TempDir(), nil, false)

	chunk := bytes.Repeat([]byte("b"), 200<<10) // 200 KiB each, ≤256KiB per-file cap
	atts := []Attachment{
		{Name: "one.txt", Mime: "text/plain", Data: b64(chunk)},
		{Name: "two.txt", Mime: "text/plain", Data: b64(chunk)},
		{Name: "three.txt", Mime: "text/plain", Data: b64(chunk)},
	}

	sessionID := "aaaaaaaaaaaaaaaa"
	content, err := buildAttachmentContent(atts, sessionID, pp, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(content))
	}
	// First two fit within the 512 KiB inline aggregate; the third overflows
	// to disk.
	if !strings.Contains(content[0].Text, "<attachment") {
		t.Fatalf("expected block 0 to be inline, got %.40q", content[0].Text)
	}
	if !strings.Contains(content[1].Text, "<attachment") {
		t.Fatalf("expected block 1 to be inline, got %.40q", content[1].Text)
	}
	if !strings.Contains(content[2].Text, "guardado en:") {
		t.Fatalf("expected block 2 to be the disk advisory, got %.40q", content[2].Text)
	}

	dir, err := sessionAttachDir(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file on disk, got %d", len(entries))
	}
}

func TestReapStaleAttachments(t *testing.T) {
	base := t.TempDir()
	t.Setenv("MOA_ATTACHMENTS_DIR", base)

	oldValid := "0123456789abcdef"
	recentValid := "fedcba9876543210"
	notASession := "notasession"

	for _, name := range []string{oldValid, recentValid, notASession} {
		if err := os.MkdirAll(filepath.Join(base, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	oldTime := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(base, oldValid), oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(base, notASession), oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	reapStaleAttachments()

	if _, err := os.Stat(filepath.Join(base, oldValid)); !os.IsNotExist(err) {
		t.Errorf("expected %q to be removed, err=%v", oldValid, err)
	}
	if _, err := os.Stat(filepath.Join(base, recentValid)); err != nil {
		t.Errorf("expected %q to be kept, err=%v", recentValid, err)
	}
	if _, err := os.Stat(filepath.Join(base, notASession)); err != nil {
		t.Errorf("expected %q to be kept (non-matching name), err=%v", notASession, err)
	}
}

func TestDelete_RemovesAttachments(t *testing.T) {
	t.Setenv("MOA_ATTACHMENTS_DIR", t.TempDir())

	srv, mgr, cancel := newTestServer(t)
	defer cancel()

	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	binary := []byte{0x00, 0x01, 0x02, 0x03}
	atts := []Attachment{{Name: "data.bin", Mime: "application/octet-stream", Data: b64(binary)}}
	resp := apiReq(t, srv, "POST", "/api/sessions/"+sess.ID+"/send", sendBody(t, "hi", atts))
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != 202 {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}

	pollUntil(t, 5*time.Second, "session idle after send", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sessState(sess) == StateIdle
	})

	dir, err := sessionAttachDir(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("expected attach dir to exist before delete: %v", err)
	}

	if err := mgr.Delete(sess.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("expected attach dir to be removed after Delete, err=%v", err)
	}
}

func TestPDF_NativeWhenSupported(t *testing.T) {
	t.Setenv("MOA_ATTACHMENTS_DIR", t.TempDir())
	pp := tool.NewPathPolicy(t.TempDir(), nil, false)

	pdfData := b64([]byte("%PDF-1.4 fake pdf bytes"))
	atts := []Attachment{{Name: "report.pdf", Mime: "application/pdf", Data: pdfData}}

	sessionID := "fedcba9876543210"
	content, err := buildAttachmentContent(atts, sessionID, pp, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 1 {
		t.Fatalf("expected 1 block, got %d", len(content))
	}
	if content[0].Type != "document" {
		t.Fatalf("expected document block, got %q", content[0].Type)
	}
	if content[0].Filename != "report.pdf" {
		t.Fatalf("expected filename report.pdf, got %q", content[0].Filename)
	}
	if content[0].Data != pdfData {
		t.Fatalf("expected data to match original base64")
	}

	if dir, err := sessionAttachDir(sessionID); err == nil {
		if _, statErr := os.Stat(dir); statErr == nil {
			t.Fatalf("expected no file on disk for native PDF")
		}
	}
}

func TestPDF_FallbackToDiskWhenUnsupported(t *testing.T) {
	t.Setenv("MOA_ATTACHMENTS_DIR", t.TempDir())
	pp := tool.NewPathPolicy(t.TempDir(), nil, false)

	pdfData := b64([]byte("%PDF-1.4 fake pdf bytes"))
	atts := []Attachment{{Name: "report.pdf", Mime: "application/pdf", Data: pdfData}}

	sessionID := "0123fedcba987654"
	content, err := buildAttachmentContent(atts, sessionID, pp, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 1 {
		t.Fatalf("expected 1 block, got %d", len(content))
	}
	if content[0].Type != "text" {
		t.Fatalf("expected text block, got %q", content[0].Type)
	}
	if !strings.Contains(content[0].Text, "guardado en:") {
		t.Fatalf("expected advisory text, got %q", content[0].Text)
	}
	if !strings.Contains(content[0].Text, "no soporta documentos PDF nativos") {
		t.Fatalf("expected fallback note, got %q", content[0].Text)
	}

	dir, err := sessionAttachDir(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("expected session dir to exist: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file on disk, got %d", len(entries))
	}
}

// TestMimeMismatch_ImageGoesToDisk verifies a binary mislabeled as image/png
// is NOT forwarded to the provider as an image (which would be rejected) but
// saved to disk with a mismatch note.
func TestMimeMismatch_ImageGoesToDisk(t *testing.T) {
	t.Setenv("MOA_ATTACHMENTS_DIR", t.TempDir())
	pp := tool.NewPathPolicy(t.TempDir(), nil, false)

	// Not a real image — random binary claiming to be a PNG.
	atts := []Attachment{{Name: "fake.png", Mime: "image/png", Data: b64([]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d})}}

	sessionID := "aabbccdd11223344"
	content, err := buildAttachmentContent(atts, sessionID, pp, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 1 {
		t.Fatalf("expected 1 block, got %d", len(content))
	}
	if content[0].Type != "text" {
		t.Fatalf("expected mislabeled image to go to disk (text block), got %q", content[0].Type)
	}
	if !strings.Contains(content[0].Text, "no coincide con su contenido real") {
		t.Fatalf("expected MIME-mismatch note, got %q", content[0].Text)
	}
}

// TestMimeMismatch_PDFGoesToDisk verifies a binary mislabeled as
// application/pdf is not sent as a native document but saved to disk.
func TestMimeMismatch_PDFGoesToDisk(t *testing.T) {
	t.Setenv("MOA_ATTACHMENTS_DIR", t.TempDir())
	pp := tool.NewPathPolicy(t.TempDir(), nil, false)

	atts := []Attachment{{Name: "notreally.pdf", Mime: "application/pdf", Data: b64([]byte("this is not a pdf at all"))}}

	sessionID := "ddccbbaa44332211"
	content, err := buildAttachmentContent(atts, sessionID, pp, true, 0) // supportsDocuments=true
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 1 {
		t.Fatalf("expected 1 block, got %d", len(content))
	}
	if content[0].Type != "text" {
		t.Fatalf("expected mislabeled PDF to go to disk (text block), got %q", content[0].Type)
	}
	if !strings.Contains(content[0].Text, "no coincide con su contenido real") {
		t.Fatalf("expected MIME-mismatch note, got %q", content[0].Text)
	}
}

// TestPDF_SessionNativeBudgetFallsBackToDisk verifies the CUMULATIVE per-session
// native-doc cap: if the conversation history already holds close to the session
// native budget, a new native-eligible PDF falls back to disk instead of being
// embedded natively (bounding cross-turn growth).
func TestPDF_SessionNativeBudgetFallsBackToDisk(t *testing.T) {
	t.Setenv("MOA_ATTACHMENTS_DIR", t.TempDir())
	pp := tool.NewPathPolicy(t.TempDir(), nil, false)
	sessionID := "cafecafecafe0000"

	pdf := make([]byte, 4<<20) // 4 MB
	copy(pdf, []byte("%PDF-1.7\n"))
	atts := []Attachment{{Name: "x.pdf", Mime: "application/pdf", Data: b64(pdf)}}

	// Simulate history already at the session budget.
	content, err := buildAttachmentContent(atts, sessionID, pp, true, maxSessionNativeDocBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 1 {
		t.Fatalf("expected 1 block, got %d", len(content))
	}
	if content[0].Type != "text" {
		t.Fatalf("expected disk fallback (text) when session native budget exhausted, got %q", content[0].Type)
	}

	// With no prior history, the same PDF goes native.
	content2, err := buildAttachmentContent(atts, sessionID, pp, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if content2[0].Type != "document" {
		t.Fatalf("expected native document with empty history, got %q", content2[0].Type)
	}
}

// TestBytesLookLikePDF verifies the magic check is prefix-anchored: "%PDF-"
// must be at the start (after an optional BOM/whitespace), not anywhere in the
// file, so an arbitrary binary containing "%PDF-" is not mis-sent as a document.
func TestBytesLookLikePDF(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want bool
	}{
		{"real prefix", []byte("%PDF-1.7\nstuff"), true},
		{"leading whitespace", append([]byte("  \n"), []byte("%PDF-1.4")...), true},
		{"utf8 bom", append([]byte{0xEF, 0xBB, 0xBF}, []byte("%PDF-1.5")...), true},
		{"magic in middle only", append(bytes.Repeat([]byte{0x00}, 40), []byte("%PDF-1.4")...), false},
		{"no magic", []byte("just some text"), false},
		{"empty", nil, false},
	}
	for _, c := range cases {
		if got := bytesLookLikePDF(c.data); got != c.want {
			t.Errorf("bytesLookLikePDF(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestPDF_NativeBudgetFallsBackToDisk verifies that once the per-message native
// PDF budget is exhausted, further PDFs fall back to disk instead of being
// embedded natively (which would be re-sent unbounded every turn).
func TestPDF_NativeBudgetFallsBackToDisk(t *testing.T) {
	t.Setenv("MOA_ATTACHMENTS_DIR", t.TempDir())
	pp := tool.NewPathPolicy(t.TempDir(), nil, false)
	sessionID := "beefbeefbeef0000"

	// Build a valid-ish PDF just over half the native budget, so the first goes
	// native and the second exceeds the aggregate and must fall back to disk.
	half := maxNativeDocBytes/2 + 1024
	mk := func() Attachment {
		b := make([]byte, half)
		copy(b, []byte("%PDF-1.7\n"))
		return Attachment{Name: "big.pdf", Mime: "application/pdf", Data: b64(b)}
	}
	content, err := buildAttachmentContent([]Attachment{mk(), mk()}, sessionID, pp, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(content))
	}
	if content[0].Type != "document" {
		t.Fatalf("expected first PDF native (document), got %q", content[0].Type)
	}
	if content[1].Type != "text" {
		t.Fatalf("expected second PDF to fall back to disk (text), got %q", content[1].Type)
	}
	if !strings.Contains(content[1].Text, "guardado en:") {
		t.Fatalf("expected disk advisory for the overflow PDF, got %q", content[1].Text)
	}
}

// TestBuildAttachmentContent_RollbackOnError verifies that when a later
// attachment in the same call fails (here: exceeds the per-file cap), the
// files already written to disk during THIS call are rolled back — a failed
// /send must not leave orphan files counting against the session quota.
func TestBuildAttachmentContent_RollbackOnError(t *testing.T) {
	t.Setenv("MOA_ATTACHMENTS_DIR", t.TempDir())
	pp := tool.NewPathPolicy(t.TempDir(), nil, false)
	sessionID := "abcabc1234567890"

	// First attachment: a valid binary that goes to disk.
	good := Attachment{Name: "ok.bin", Mime: "application/octet-stream", Data: b64([]byte{0x00, 0x01, 0x02, 0x03})}
	// Second attachment: a binary over the per-file cap → errors after the
	// first was written.
	tooBig := Attachment{Name: "big.bin", Mime: "application/octet-stream", Data: b64(make([]byte, maxAttachmentFileBytes+1))}

	_, err := buildAttachmentContent([]Attachment{good, tooBig}, sessionID, pp, false, 0)
	if err == nil {
		t.Fatal("expected error for oversized attachment")
	}
	dir, derr := sessionAttachDir(sessionID)
	if derr != nil {
		t.Fatal(derr)
	}
	if entries, rerr := os.ReadDir(dir); rerr == nil {
		if len(entries) != 0 {
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Fatalf("expected 0 files after rollback, got %d: %v", len(entries), names)
		}
	}
}

// TestRemoveSessionAttachDir_SymlinkSafety verifies that removing a session's
// attachment dir never follows a symlink into an arbitrary target: if the
// session path is a symlink, only the link is removed and the target survives.
func TestRemoveSessionAttachDir_SymlinkSafety(t *testing.T) {
	base := t.TempDir()
	t.Setenv("MOA_ATTACHMENTS_DIR", base)

	// A victim directory outside the base, with a file we must not delete.
	victimDir := t.TempDir()
	victimFile := filepath.Join(victimDir, "keep.txt")
	if err := os.WriteFile(victimFile, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Plant a session-id-shaped symlink in the base pointing at the victim dir.
	id := "0011223344556677"
	link := filepath.Join(base, id)
	if err := os.Symlink(victimDir, link); err != nil {
		t.Fatal(err)
	}

	if err := removeSessionAttachDir(id); err != nil {
		t.Fatalf("removeSessionAttachDir errored: %v", err)
	}
	// The symlink must be gone…
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("expected symlink removed, lstat err=%v", err)
	}
	// …but the victim file must survive (RemoveAll must NOT have followed it).
	if got, err := os.ReadFile(victimFile); err != nil || string(got) != "precious" {
		t.Fatalf("victim clobbered through symlink: err=%v content=%q", err, got)
	}
}

// TestRemoveSessionAttachDir_RemovesRealDir verifies the happy path: a real
// session dir with files is fully removed.
func TestRemoveSessionAttachDir_RemovesRealDir(t *testing.T) {
	base := t.TempDir()
	t.Setenv("MOA_ATTACHMENTS_DIR", base)
	id := "aabbccdd00112233"
	dir, err := ensureSessionAttachDir(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, werr := writeUnique(dir, "f.bin", []byte("x")); werr != nil {
		t.Fatal(werr)
	}
	if err := removeSessionAttachDir(id); err != nil {
		t.Fatalf("removeSessionAttachDir errored: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("expected session dir removed, stat err=%v", err)
	}
}

// TestWriteUnique_NoFollowSymlink verifies writeUnique refuses to write through
// a symlink planted at the target path (O_NOFOLLOW), instead of clobbering the
// symlink's target.
func TestWriteUnique_NoFollowSymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim.txt")
	if err := os.WriteFile(victim, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Plant a symlink "evil.txt" -> victim inside the attachment dir.
	link := filepath.Join(dir, "evil.txt")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	// writeUnique must NOT follow the symlink to overwrite victim. With O_EXCL
	// the existing symlink path collides, so it writes a suffixed regular file
	// instead — either way the victim's target is never clobbered.
	got, err := writeUnique(dir, "evil.txt", []byte("attacker"))
	if err != nil {
		t.Fatalf("writeUnique errored: %v", err)
	}
	// The written path must be a regular file inside dir, not the symlink.
	if fi, lerr := os.Lstat(got); lerr != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("writeUnique returned a symlink or bad path: %v (mode err %v)", got, lerr)
	}
	victimContent, _ := os.ReadFile(victim)
	if string(victimContent) != "original" {
		t.Fatalf("symlink target was clobbered: %q", victimContent)
	}
}
