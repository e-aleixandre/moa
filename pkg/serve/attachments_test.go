package serve

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/attachment"
	"github.com/ealeixandre/moa/pkg/bus"
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
	var out struct {
		Action      string          `json:"action"`
		Attachments []AttachmentDTO `json:"attachments"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.Action != "send" {
		t.Fatalf("expected action=send, got %q", out.Action)
	}
	if len(out.Attachments) != 1 || out.Attachments[0].URL == "" {
		t.Fatalf("expected descriptor response, got %+v", out.Attachments)
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
	if userContent[0].AttachmentID == "" || userContent[0].AttachmentSize <= 0 || userContent[0].Data != "" {
		t.Fatalf("expected byte-free image descriptor, got %+v", userContent[0])
	}
	f, d, err := mgr.attachStore.Open(sess.ID, userContent[0].AttachmentID)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck
	got, err := io.ReadAll(f)
	if err != nil || d.Size != int64(len(got)) || !bytes.Equal(got, pngBytes(64)) {
		t.Fatalf("stored bytes or descriptor mismatch: bytes=%d descriptor=%+v error=%v", len(got), d, err)
	}
	if userContent[1].Type != "text" || userContent[1].Text != "mira esta captura" {
		t.Fatalf("expected second block to be the user text, got %+v", userContent[1])
	}
}

func TestBuildAttachmentContentImageStoreAndDegradedFallback(t *testing.T) {
	sessionID := "0123456789abcdef"
	store, err := attachment.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	image := pngBytes(64)
	atts := []Attachment{{Name: "image.png", Mime: "image/png", Data: b64(image)}}
	content, _, descriptors, err := buildAttachmentContent(atts, sessionID, nil, 0, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 1 || len(descriptors) != 1 || content[0].AttachmentID == "" || content[0].Data != "" || content[0].AttachmentSize <= 0 {
		t.Fatalf("expected stored descriptor block, got content=%+v descriptors=%+v", content, descriptors)
	}
	f, _, err := store.Open(sessionID, content[0].AttachmentID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil || !bytes.Equal(got, image) {
		t.Fatalf("stored image mismatch: %v", err)
	}
	content, _, descriptors, err = buildAttachmentContent(atts, sessionID, nil, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(descriptors) != 0 || content[0].AttachmentID != "" || content[0].Data != atts[0].Data {
		t.Fatalf("expected inline fallback, got content=%+v descriptors=%+v", content, descriptors)
	}
}

func TestBuildAttachmentContentStoreRollbackAfterPartialFailure(t *testing.T) {
	sessionID := "0123456789abcdef"
	root := t.TempDir()
	store, err := attachment.New(root)
	if err != nil {
		t.Fatal(err)
	}
	first := []byte("one,two\n1,2\n")
	_, _, _, err = buildAttachmentContent([]Attachment{
		{Name: "first.csv", Mime: "text/csv", Data: b64(first)},
		{Name: "too-big.bin", Mime: "application/octet-stream", Data: b64(make([]byte, maxAttachmentFileBytes+1))},
	}, sessionID, nil, 0, store)
	if !errors.Is(err, ErrBadAttachment) {
		t.Fatalf("buildAttachmentContent error = %v, want ErrBadAttachment", err)
	}

	// PutRef creates the first document occurrence before the second file is rejected;
	// the failed build must remove that occurrence and its now-unreferenced blob.
	hash := fmt.Sprintf("%x", sha256.Sum256(first))
	blobPath := filepath.Join(root, "blobs", "sha256", hash[:2], hash[2:4], hash)
	if _, err := os.Stat(blobPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial-build blob still exists or stat failed: %v", err)
	}
}

func TestSessionPathPolicyAllowsOnlyItsAttachmentViews(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManagerWithConfig(t, ctx, newMockProvider(simpleResponseHandler("done")), t.TempDir(), core.MoaConfig{PathScope: "workspace"})
	store, err := attachment.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mgr.attachStore = store
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	d, err := store.PutRef(sess.ID, []byte("private"), attachment.PutMeta{Name: "private.txt", Mime: "text/plain", Kind: "file"})
	if err != nil {
		t.Fatal(err)
	}
	view, err := store.EnsureView(sess.ID, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.SafePath(tool.ToolConfig{WorkspaceRoot: sess.CWD, PathPolicy: sess.pathPolicy}, view); err != nil {
		t.Fatalf("session path policy rejected its attachment view %q: %v", view, err)
	}
	otherID := "other-session-123"
	other, err := store.PutRef(otherID, []byte("other"), attachment.PutMeta{Name: "other.txt", Mime: "text/plain", Kind: "file"})
	if err != nil {
		t.Fatal(err)
	}
	otherView, err := store.EnsureView(otherID, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.SafePath(tool.ToolConfig{WorkspaceRoot: sess.CWD, PathPolicy: sess.pathPolicy}, otherView); err == nil {
		t.Fatalf("session path policy allowed another session's attachment view %q", otherView)
	}
}

func TestSendRollsBackStoredAttachmentWhenIdleEnqueueRejected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("done")))
	root := t.TempDir()
	store, err := attachment.New(root)
	if err != nil {
		t.Fatal(err)
	}
	mgr.attachStore = store
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	// A closed bus makes SendPromptWithContent reject after attachment ingestion.
	sess.runtime.Close()
	image := pngBytes(64)
	_, _, _, err = mgr.Send(sess.ID, "image", []Attachment{{Name: "image.png", Mime: "image/png", Data: b64(image)}}, "")
	if !errors.Is(err, bus.ErrClosed) {
		t.Fatalf("Send error = %v, want bus.ErrClosed", err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(image))
	blobPath := filepath.Join(root, "blobs", "sha256", hash[:2], hash[2:4], hash)
	if _, err := os.Stat(blobPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected idle-send blob still exists or stat failed: %v", err)
	}
}

func TestSendRollsBackStoredAttachmentWhenSteerEnqueueRejected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(delayedResponseHandler(3*time.Second, "slow")))
	root := t.TempDir()
	store, err := attachment.New(root)
	if err != nil {
		t.Fatal(err)
	}
	mgr.attachStore = store
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(sess.ID, "first", nil, ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 2*time.Second, "running", func() bool {
		return sessState(sess) == StateRunning
	})
	queueFull := false
	for i := 0; i < 64; i++ {
		_, _, _, err := mgr.Send(sess.ID, "queued", nil, "")
		if errors.Is(err, bus.ErrSteerQueueFull) {
			queueFull = true
			break
		}
		if err != nil {
			t.Fatalf("fill steer queue at %d: %v", i, err)
		}
	}
	if !queueFull {
		t.Fatal("steer queue did not fill")
	}

	image := pngBytes(64)
	_, _, _, err = mgr.Send(sess.ID, "image", []Attachment{{Name: "image.png", Mime: "image/png", Data: b64(image)}}, "")
	if !errors.Is(err, bus.ErrSteerQueueFull) {
		t.Fatalf("Send error = %v, want bus.ErrSteerQueueFull", err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(image))
	blobPath := filepath.Join(root, "blobs", "sha256", hash[:2], hash[2:4], hash)
	if _, err := os.Stat(blobPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected steer blob still exists or stat failed: %v", err)
	}
}

func TestCountNativeDocBytesOnlyCountsImages(t *testing.T) {
	msgs := []core.AgentMessage{{Message: core.Message{Content: []core.Content{{Type: "image", AttachmentSize: 123}, {Type: "document", Data: b64([]byte("abcd"))}}}}}
	if got, want := countNativeDocBytes(msgs), int64(123); got != want {
		t.Fatalf("countNativeDocBytes = %d, want %d", got, want)
	}
}

func TestDeleteReleasesAttachmentReferences(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("done")))
	root := t.TempDir()
	store, err := attachment.New(root)
	if err != nil {
		t.Fatal(err)
	}
	mgr.attachStore = store
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	d, err := store.PutRef(sess.ID, pngBytes(64), attachment.PutMeta{Name: "image.png", Mime: "image/png", Kind: "image"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Delete(sess.ID); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "blobs", "sha256", d.SHA256[:2], d.SHA256[2:4], d.SHA256)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("deleted session blob still exists or unexpected stat error: %v", err)
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

func TestSend_BinaryBecomesDurableDocument(t *testing.T) {
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
	var response struct {
		Attachments []AttachmentDTO `json:"attachments"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.Attachments) != 1 || response.Attachments[0].Kind != "file" || response.Attachments[0].ID == "" {
		t.Fatalf("expected durable document DTO from /send, got %+v", response.Attachments)
	}

	pollUntil(t, 5*time.Second, "session idle after send", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sessState(sess) == StateIdle
	})

	msgs := sess.History()
	var document core.Content
	for _, m := range msgs {
		if m.Role != "user" {
			continue
		}
		for _, c := range m.Content {
			if c.Type == "document" {
				document = c
			}
		}
	}
	if document.AttachmentID == "" || document.Data != "" || document.MimeType != "application/x-msdownload" {
		t.Fatalf("expected byte-free file descriptor, got %+v", document)
	}
	d, ok := mgr.attachStore.Lookup(sess.ID, document.AttachmentID)
	if !ok || d.Kind != "file" || d.Size != int64(len(badBytes)) {
		t.Fatalf("expected stored file descriptor, got %+v (ok=%v)", d, ok)
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
		big := bytes.Repeat([]byte("a"), 256<<10)
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

// TestSend_AttachmentsSteerWhileRunning verifies attachments sent mid-run are
// now accepted as a steer (with content) instead of being rejected: the unified
// queue rail carries image/content steers so a user can attach a file mid-run.
func TestSend_AttachmentsSteerWhileRunning(t *testing.T) {
	t.Setenv("MOA_ATTACHMENTS_DIR", t.TempDir())
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
	if resp2.StatusCode != 202 {
		t.Fatalf("expected 202 for attachments while running, got %d", resp2.StatusCode)
	}
	var out2 struct {
		Action  string `json:"action"`
		SteerID string `json:"steer_id"`
	}
	_ = json.NewDecoder(resp2.Body).Decode(&out2)
	if out2.Action != "steer" {
		t.Fatalf("expected action=steer for attachment mid-run, got %q", out2.Action)
	}
	if out2.SteerID == "" {
		t.Fatalf("expected a steer_id for the queued attachment chip")
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

func TestCancelSteersReleasesQueuedImageAttachment(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prov := newMockProvider(delayedResponseHandler(3*time.Second, "slow"))
	mgr := newTestManager(t, ctx, prov)
	httpSrv := httptest.NewServer(NewServer(mgr))
	defer httpSrv.Close()

	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	resp := apiReq(t, httpSrv, http.MethodPost, "/api/sessions/"+sess.ID+"/send", sendBody(t, "first", nil))
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("first send status = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
	pollUntil(t, 2*time.Second, "running", func() bool {
		return sessState(sess) == StateRunning
	})

	resp = apiReq(t, httpSrv, http.MethodPost, "/api/sessions/"+sess.ID+"/send", sendBody(t, "image steer", []Attachment{{
		Name: "queued.png", Mime: "image/png", Data: b64(pngBytes(64)),
	}}))
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("image steer status = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
	var queued struct {
		Action      string          `json:"action"`
		Attachments []AttachmentDTO `json:"attachments"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&queued); err != nil {
		t.Fatal(err)
	}
	if queued.Action != "steer" || len(queued.Attachments) != 1 {
		t.Fatalf("queued response = %+v, want one image steer", queued)
	}
	attachmentID := queued.Attachments[0].ID
	if _, ok := mgr.attachStore.Lookup(sess.ID, attachmentID); !ok {
		t.Fatal("queued attachment was not stored")
	}

	resp = apiReq(t, httpSrv, http.MethodPost, "/api/sessions/"+sess.ID+"/steers/cancel", "")
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("cancel steers status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
	pollUntil(t, 2*time.Second, "queued attachment release", func() bool {
		_, ok := mgr.attachStore.Lookup(sess.ID, attachmentID)
		return !ok
	})
	if _, _, err := mgr.attachStore.Open(sess.ID, attachmentID); err == nil {
		t.Fatal("canceled steer attachment is still openable")
	}
}

func TestRunErrorReleasesQueuedImageAttachment(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	providerErr := make(chan struct{})
	prov := newMockProvider(func(_ context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 1)
		go func() {
			defer close(ch)
			<-providerErr
			ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: errors.New("provider failed")}
		}()
		return ch, nil
	})
	mgr := newTestManager(t, ctx, prov)

	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if action, _, _, err := mgr.Send(sess.ID, "first", nil, ""); err != nil || action != "send" {
		t.Fatalf("first send = (%q, %v), want (send, nil)", action, err)
	}
	pollUntil(t, 2*time.Second, "running", func() bool {
		return sessState(sess) == StateRunning
	})

	action, _, descriptors, err := mgr.Send(sess.ID, "image steer", []Attachment{{
		Name: "queued.png", Mime: "image/png", Data: b64(pngBytes(64)),
	}}, "")
	if err != nil || action != "steer" || len(descriptors) != 1 {
		t.Fatalf("image steer = (%q, %+v, %v), want one queued image", action, descriptors, err)
	}
	attachmentID := descriptors[0].ID
	if _, ok := mgr.attachStore.Lookup(sess.ID, attachmentID); !ok {
		t.Fatal("queued attachment was not stored")
	}

	close(providerErr)
	pollUntil(t, 2*time.Second, "run error", func() bool {
		return sessState(sess) == StateError
	})
	pollUntil(t, 2*time.Second, "queued attachment release", func() bool {
		_, ok := mgr.attachStore.Lookup(sess.ID, attachmentID)
		return !ok
	})
	if _, _, err := mgr.attachStore.Open(sess.ID, attachmentID); err == nil {
		t.Fatal("run-error discarded steer attachment is still openable")
	}
}

func TestBuildAttachmentContent_TextBecomesDurableDocument(t *testing.T) {
	store, err := attachment.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	atts := []Attachment{
		{Name: `report "final".csv`, Mime: "text/csv", Data: b64([]byte("a,b\n1,2"))},
	}
	content, _, descriptors, err := buildAttachmentContent(atts, "abcdef0123456789", nil, 0, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 1 {
		t.Fatalf("expected 1 block, got %d", len(content))
	}
	if content[0].Type != "document" || content[0].AttachmentID == "" || content[0].Data != "" {
		t.Fatalf("expected byte-free document block, got %+v", content[0])
	}
	if len(descriptors) != 1 || descriptors[0].Kind != "file" || descriptors[0].Name != `report "final".csv` {
		t.Fatalf("expected stored file descriptor, got %+v", descriptors)
	}
}

func TestBuildAttachmentContent_OLE2BecomesDurableDocument(t *testing.T) {
	store, err := attachment.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	ole2 := []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1, 0x00, 0x01}
	content, written, descriptors, err := buildAttachmentContent([]Attachment{{
		Name: "informe.xls", Mime: "application/vnd.ms-excel", Data: b64(ole2),
	}}, "abcdef0123456789", nil, 0, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 1 || content[0].Type != "document" || content[0].AttachmentID == "" || content[0].Data != "" {
		t.Fatalf("expected byte-free OLE2 document descriptor, got %+v", content)
	}
	if len(written) != 0 || len(descriptors) != 1 || descriptors[0].Kind != "file" || descriptors[0].Mime != "application/vnd.ms-excel" {
		t.Fatalf("expected durable OLE2 file descriptor without disk advisory, written=%v descriptors=%+v", written, descriptors)
	}
}

func TestBuildAttachmentContent_LargeTextToDisk(t *testing.T) {
	t.Setenv("MOA_ATTACHMENTS_DIR", t.TempDir())
	pp := tool.NewPathPolicy(t.TempDir(), nil, false)

	big := bytes.Repeat([]byte("a"), 300<<10) // 300 KiB, valid UTF-8
	atts := []Attachment{{Name: "big.txt", Mime: "text/plain", Data: b64(big)}}

	sessionID := "0123456789abcdef"
	content, _, _, err := buildAttachmentContent(atts, sessionID, pp, 0, nil)
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
	content, _, _, err := buildAttachmentContent(atts, sessionID, pp, 0, nil)
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

func TestBuildAttachmentContent_DegradedTextFilesGoToDisk(t *testing.T) {
	t.Setenv("MOA_ATTACHMENTS_DIR", t.TempDir())
	pp := tool.NewPathPolicy(t.TempDir(), nil, false)

	chunk := bytes.Repeat([]byte("b"), 200<<10) // 200 KiB each, ≤256KiB per-file cap
	atts := []Attachment{
		{Name: "one.txt", Mime: "text/plain", Data: b64(chunk)},
		{Name: "two.txt", Mime: "text/plain", Data: b64(chunk)},
		{Name: "three.txt", Mime: "text/plain", Data: b64(chunk)},
	}

	sessionID := "aaaaaaaaaaaaaaaa"
	content, _, _, err := buildAttachmentContent(atts, sessionID, pp, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(content))
	}
	for i, block := range content {
		if block.Type != "text" || !strings.Contains(block.Text, "guardado en:") {
			t.Fatalf("expected degraded disk advisory at block %d, got %+v", i, block)
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
	if len(entries) != 3 {
		t.Fatalf("expected 3 files on disk, got %d", len(entries))
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

func TestDelete_ReleasesDurableAttachments(t *testing.T) {
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

	var attachmentID string
	for _, msg := range sess.History() {
		for _, block := range msg.Content {
			if block.Type == "document" {
				attachmentID = block.AttachmentID
			}
		}
	}
	if attachmentID == "" {
		t.Fatal("expected durable document in history")
	}
	if _, ok := mgr.attachStore.Lookup(sess.ID, attachmentID); !ok {
		t.Fatal("expected attachment reference before delete")
	}

	if err := mgr.Delete(sess.ID); err != nil {
		t.Fatal(err)
	}

	if _, ok := mgr.attachStore.Lookup(sess.ID, attachmentID); ok {
		t.Error("expected attachment reference to be released after Delete")
	}
}

func TestPDF_BecomesDurableDocument(t *testing.T) {
	store, err := attachment.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	pdfData := b64([]byte("%PDF-1.4 fake pdf bytes"))
	atts := []Attachment{{Name: "report.pdf", Mime: "application/pdf", Data: pdfData}}

	sessionID := "fedcba9876543210"
	content, _, descriptors, err := buildAttachmentContent(atts, sessionID, nil, 0, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 1 {
		t.Fatalf("expected 1 block, got %d", len(content))
	}
	if content[0].Type != "document" || content[0].AttachmentID == "" || content[0].Data != "" {
		t.Fatalf("expected byte-free PDF descriptor, got %+v", content[0])
	}
	if len(descriptors) != 1 || descriptors[0].Kind != "file" || descriptors[0].Mime != "application/pdf" {
		t.Fatalf("expected PDF file descriptor, got %+v", descriptors)
	}
}

func TestMimeMismatch_ImageBecomesDurableDocument(t *testing.T) {
	store, err := attachment.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// Not a real image — random binary claiming to be a PNG.
	atts := []Attachment{{Name: "fake.png", Mime: "image/png", Data: b64([]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d})}}

	sessionID := "aabbccdd11223344"
	content, _, descriptors, err := buildAttachmentContent(atts, sessionID, nil, 0, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 1 {
		t.Fatalf("expected 1 block, got %d", len(content))
	}
	if content[0].Type != "document" || content[0].AttachmentID == "" {
		t.Fatalf("expected mislabeled image document descriptor, got %+v", content[0])
	}
	if len(descriptors) != 1 || descriptors[0].Kind != "file" || descriptors[0].Mime == "image/png" {
		t.Fatalf("expected a safely typed file descriptor, got %+v", descriptors)
	}
}

func TestMimeMismatch_PDFBecomesDurableDocument(t *testing.T) {
	store, err := attachment.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	atts := []Attachment{{Name: "notreally.pdf", Mime: "application/pdf", Data: b64([]byte("this is not a pdf at all"))}}

	sessionID := "ddccbbaa44332211"
	content, _, descriptors, err := buildAttachmentContent(atts, sessionID, nil, 0, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 1 {
		t.Fatalf("expected 1 block, got %d", len(content))
	}
	if content[0].Type != "document" || content[0].AttachmentID == "" {
		t.Fatalf("expected PDF document descriptor, got %+v", content[0])
	}
	if len(descriptors) != 1 || descriptors[0].Kind != "file" {
		t.Fatalf("expected a file descriptor, got %+v", descriptors)
	}
}

func TestImage_SessionNativeBudgetFallsBackToDisk(t *testing.T) {
	t.Setenv("MOA_ATTACHMENTS_DIR", t.TempDir())
	pp := tool.NewPathPolicy(t.TempDir(), nil, false)
	image := pngBytes(1024)
	content, _, _, err := buildAttachmentContent([]Attachment{{Name: "x.png", Mime: "image/png", Data: b64(image)}}, "cafecafecafe0000", pp, maxSessionNativeDocBytes, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 1 || content[0].Type != "text" {
		t.Fatalf("image at exhausted native budget = %#v, want disk text fallback", content)
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

	_, _, _, err := buildAttachmentContent([]Attachment{good, tooBig}, sessionID, pp, 0, nil)
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
