package serve

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

func b64(data []byte) string { return base64.StdEncoding.EncodeToString(data) }

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

	atts := []Attachment{{Name: "captura.png", Mime: "image/png", Data: b64([]byte("fake-png-bytes"))}}
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

func TestSend_AttachmentInvalidMime(t *testing.T) {
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
	if resp.StatusCode != 400 {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	if !strings.Contains(buf.String(), "weird.bin") {
		t.Fatalf("expected error message to mention attachment name, got %q", buf.String())
	}
}

func TestSend_AttachmentTooLarge(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()

	t.Run("image", func(t *testing.T) {
		sess, err := mgr.CreateSession(CreateOpts{})
		if err != nil {
			t.Fatal(err)
		}
		big := bytes.Repeat([]byte{0}, maxImageBytes+1)
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
		big := bytes.Repeat([]byte("a"), maxAttachmentTextSize+1)
		atts := []Attachment{{Name: "huge.txt", Mime: "text/plain", Data: b64(big)}}
		resp := apiReq(t, srv, "POST", "/api/sessions/"+sess.ID+"/send", sendBody(t, "hi", atts))
		defer resp.Body.Close() //nolint:errcheck
		if resp.StatusCode != 400 {
			t.Fatalf("expected 400, got %d", resp.StatusCode)
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
	atts := []Attachment{
		{Name: `report "final".csv`, Mime: "text/csv", Data: b64([]byte("a,b\n1,2"))},
	}
	content, err := buildAttachmentContent(atts)
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
