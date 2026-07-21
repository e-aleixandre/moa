package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/attachment"
	"github.com/ealeixandre/moa/pkg/core"
)

func TestGetAttachment(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	const sessionID = "0123456789abcdef0123456789abcdef"
	if _, ok := mgr.Get(sessionID); ok {
		t.Fatal("test session unexpectedly loaded")
	}
	data := pngBytes(64)
	descriptor, err := mgr.attachStore.PutRef(sessionID, data, attachment.PutMeta{
		Name: "screen.png", Mime: "image/png", Kind: "image", Width: 1, Height: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewServer(mgr))
	defer server.Close()
	path := "/api/sessions/" + sessionID + "/attachments/" + descriptor.ID

	resp := apiReq(t, server, http.MethodGet, path, "")
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close() //nolint:errcheck
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !bytes.Equal(body, data) {
		t.Fatalf("GET = %d, body=%d bytes; want 200 and stored bytes", resp.StatusCode, len(body))
	}
	if got := resp.Header.Get("Content-Type"); got != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", got)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
	if got := resp.Header.Get("Cross-Origin-Resource-Policy"); got != "same-origin" {
		t.Fatalf("Cross-Origin-Resource-Policy = %q", got)
	}
	if got := resp.Header.Get("Content-Security-Policy"); got != "sandbox" {
		t.Fatalf("Content-Security-Policy = %q", got)
	}
	if got := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(got, "inline;") {
		t.Fatalf("Content-Disposition = %q, want inline", got)
	}
	etag := `"sha256-` + descriptor.SHA256 + `"`
	if got := resp.Header.Get("ETag"); got != etag {
		t.Fatalf("ETag = %q, want %q", got, etag)
	} else if !regexp.MustCompile(`^"sha256-[0-9a-f]{64}"$`).MatchString(got) {
		t.Fatalf("ETag = %q, want quoted SHA-256 ETag", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "private, max-age=0, must-revalidate" {
		t.Fatalf("Cache-Control = %q", got)
	}

	head := apiReq(t, server, http.MethodHead, path, "")
	headBody, err := io.ReadAll(head.Body)
	head.Body.Close() //nolint:errcheck
	if err != nil {
		t.Fatal(err)
	}
	if head.StatusCode != http.StatusOK || len(headBody) != 0 || head.Header.Get("ETag") != etag {
		t.Fatalf("HEAD = %d, body=%d bytes, ETag=%q", head.StatusCode, len(headBody), head.Header.Get("ETag"))
	}

	conditional, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	conditional.Header.Set("If-None-Match", etag)
	resp, err = http.DefaultClient.Do(conditional)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional GET = %d, want 304", resp.StatusCode)
	}

	for _, deniedPath := range []string{
		"/api/sessions/fedcba9876543210fedcba9876543210/attachments/" + descriptor.ID,
		"/api/sessions/" + sessionID + "/attachments/att_000000000000000000000000",
	} {
		denied := apiReq(t, server, http.MethodGet, deniedPath, "")
		deniedBody, _ := io.ReadAll(denied.Body)
		denied.Body.Close() //nolint:errcheck
		if denied.StatusCode != http.StatusNotFound || string(deniedBody) != "not found\n" {
			t.Fatalf("denied GET %q = %d, %q; want generic 404", deniedPath, denied.StatusCode, deniedBody)
		}
	}

	file, err := mgr.attachStore.PutRef(sessionID, []byte("file bytes"), attachment.PutMeta{
		Name: "report.bin", Mime: "application/octet-stream", Kind: "file",
	})
	if err != nil {
		t.Fatal(err)
	}
	fileResp := apiReq(t, server, http.MethodGet, "/api/sessions/"+sessionID+"/attachments/"+file.ID, "")
	fileResp.Body.Close() //nolint:errcheck
	if got := fileResp.Header.Get("Content-Disposition"); !strings.HasPrefix(got, "attachment;") {
		t.Fatalf("non-raster Content-Disposition = %q, want attachment", got)
	}

	mgr.attachStore = nil
	nilStore := apiReq(t, server, http.MethodGet, path, "")
	nilStoreBody, err := io.ReadAll(nilStore.Body)
	nilStore.Body.Close() //nolint:errcheck
	if err != nil {
		t.Fatal(err)
	}
	if nilStore.StatusCode != http.StatusNotFound || string(nilStoreBody) != "not found\n" {
		t.Fatalf("nil store GET = %d, %q; want generic 404", nilStore.StatusCode, nilStoreBody)
	}
}

func TestGetAttachmentDenialsHaveIdenticalNotFoundResponses(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	const ownerID = "0123456789abcdef0123456789abcdef"
	data := []byte("private attachment bytes")
	descriptor, err := mgr.attachStore.PutRef(ownerID, data, attachment.PutMeta{
		Name: "screen.png", Mime: "image/png", Kind: "image",
	})
	if err != nil {
		t.Fatal(err)
	}
	const releasedID = "11111111111111111111111111111111"
	released, err := mgr.attachStore.PutRef(releasedID, data, attachment.PutMeta{
		Name: "released.png", Mime: "image/png", Kind: "image",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.attachStore.ReleaseSession(releasedID); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(NewServer(mgr))
	defer server.Close()
	unknownPath := "/api/sessions/" + ownerID + "/attachments/att_000000000000000000000000"
	unknown := apiReq(t, server, http.MethodGet, unknownPath, "")
	unknownBody, err := io.ReadAll(unknown.Body)
	unknown.Body.Close() //nolint:errcheck
	if err != nil {
		t.Fatal(err)
	}
	if unknown.StatusCode != http.StatusNotFound || string(unknownBody) != "not found\n" || bytes.Contains(unknownBody, data) {
		t.Fatalf("unknown attachment = %d, %q; want generic 404", unknown.StatusCode, unknownBody)
	}

	cases := []struct {
		name string
		path string
	}{
		{"foreign session", "/api/sessions/fedcba9876543210fedcba9876543210/attachments/" + descriptor.ID},
		{"released session", "/api/sessions/" + releasedID + "/attachments/" + released.ID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := apiReq(t, server, http.MethodGet, tc.path, "")
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close() //nolint:errcheck
			if readErr != nil {
				t.Fatal(readErr)
			}
			if resp.StatusCode != unknown.StatusCode || !bytes.Equal(body, unknownBody) || bytes.Contains(body, data) {
				t.Fatalf("GET %q = %d, %q; want identical generic 404", tc.path, resp.StatusCode, body)
			}
		})
	}

	mgr.attachStore = nil
	nilStore := apiReq(t, server, http.MethodGet, "/api/sessions/"+ownerID+"/attachments/"+descriptor.ID, "")
	nilStoreBody, err := io.ReadAll(nilStore.Body)
	nilStore.Body.Close() //nolint:errcheck
	if err != nil {
		t.Fatal(err)
	}
	if nilStore.StatusCode != unknown.StatusCode || !bytes.Equal(nilStoreBody, unknownBody) || bytes.Contains(nilStoreBody, data) {
		t.Fatalf("nil store GET = %d, %q; want identical generic 404", nilStore.StatusCode, nilStoreBody)
	}
}

func TestGetAttachmentMalformedIDNeverServesBytes(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	const sessionID = "0123456789abcdef0123456789abcdef"
	data := []byte("private attachment bytes")
	descriptor, err := mgr.attachStore.PutRef(sessionID, data, attachment.PutMeta{
		Name: "screen.png", Mime: "image/png", Kind: "image",
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewServer(mgr))
	defer server.Close()

	unknown := apiReq(t, server, http.MethodGet, "/api/sessions/"+sessionID+"/attachments/att_000000000000000000000000", "")
	unknownBody, err := io.ReadAll(unknown.Body)
	unknown.Body.Close() //nolint:errcheck
	if err != nil {
		t.Fatal(err)
	}
	for _, attID := range []string{"att_nothex", "att_", "att_00000000000000000000000"} {
		resp := apiReq(t, server, http.MethodGet, "/api/sessions/"+sessionID+"/attachments/"+attID, "")
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close() //nolint:errcheck
		if readErr != nil {
			t.Fatal(readErr)
		}
		if resp.StatusCode != http.StatusNotFound || !bytes.Equal(body, unknownBody) || bytes.Contains(body, data) {
			t.Fatalf("malformed ID %q = %d, %q; want generic 404 without bytes", attID, resp.StatusCode, body)
		}
	}

	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	traversal, err := http.NewRequest(http.MethodGet, server.URL+"/api/sessions/"+sessionID+"/attachments/../"+descriptor.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(traversal)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close() //nolint:errcheck
	if err != nil {
		t.Fatal(err)
	}
	// ServeMux cleans this path with a redirect; it must not serve attachment bytes.
	if resp.StatusCode == http.StatusOK || bytes.Contains(body, data) {
		t.Fatalf("traversal path = %d, %q; must not serve attachment bytes", resp.StatusCode, body)
	}
}

func TestAttachmentDispositionAllowsOnlyRasterMIMEs(t *testing.T) {
	for _, mediaType := range []string{"image/svg+xml", "text/html", "application/pdf"} {
		if inlineAttachmentMIMEs[mediaType] {
			t.Fatalf("%s is inline; want attachment disposition", mediaType)
		}
	}
	for _, mediaType := range []string{"image/jpeg", "image/png", "image/gif", "image/webp"} {
		if !inlineAttachmentMIMEs[mediaType] {
			t.Fatalf("%s is not inline; want raster inline disposition", mediaType)
		}
	}
}

func TestGetAttachmentSanitizesDispositionFilename(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	const sessionID = "0123456789abcdef0123456789abcdef"
	descriptor, err := mgr.attachStore.PutRef(sessionID, pngBytes(64), attachment.PutMeta{
		Name: "../ev\"il\n.png", Mime: "image/png", Kind: "image",
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewServer(mgr))
	defer server.Close()
	resp := apiReq(t, server, http.MethodGet, "/api/sessions/"+sessionID+"/attachments/"+descriptor.ID, "")
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d, want 200", resp.StatusCode)
	}
	disposition := resp.Header.Get("Content-Disposition")
	_, params, err := mime.ParseMediaType(disposition)
	if err != nil {
		t.Fatalf("Content-Disposition = %q: %v", disposition, err)
	}
	if params["filename"] != `ev"il.png` || strings.Contains(disposition, "\r") || strings.Contains(disposition, "\n") || strings.Contains(disposition, "../") {
		t.Fatalf("unsafe Content-Disposition = %q, filename=%q", disposition, params["filename"])
	}
}

func TestGetAttachmentRequiresSharedTokenAuthentication(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	const sessionID = "0123456789abcdef0123456789abcdef"
	descriptor, err := mgr.attachStore.PutRef(sessionID, pngBytes(64), attachment.PutMeta{
		Name: "screen.png", Mime: "image/png", Kind: "image",
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(mgr, WithAuthToken("owner", false))
	path := "/api/sessions/" + sessionID + "/attachments/" + descriptor.ID
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			unauthenticated := httptest.NewRequest(method, path, nil)
			unauthenticated.Host = "localhost"
			unauthenticatedRec := httptest.NewRecorder()
			handler.ServeHTTP(unauthenticatedRec, unauthenticated)
			if unauthenticatedRec.Code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated %s = %d, want 401", method, unauthenticatedRec.Code)
			}

			authenticated := httptest.NewRequest(method, path, nil)
			authenticated.Host = "localhost"
			authenticated.AddCookie(&http.Cookie{Name: authCookieName, Value: "owner"})
			authenticatedRec := httptest.NewRecorder()
			handler.ServeHTTP(authenticatedRec, authenticated)
			if authenticatedRec.Code != http.StatusOK {
				t.Fatalf("authenticated %s = %d, want 200", method, authenticatedRec.Code)
			}
		})
	}
}

func TestSanitizeHistoryMessagePreservesAttachmentDescriptor(t *testing.T) {
	input := core.AgentMessage{Message: core.Message{Role: "user", Content: []core.Content{{
		Type: "image", AttachmentID: "att_0123456789abcdef01234567", AttachmentSize: 42,
		MimeType: "image/png", Filename: "screen.png",
	}}}}
	got, _ := sanitizeHistoryMessage(input)
	block := got.Content[0]
	if block.AttachmentID != input.Content[0].AttachmentID ||
		block.AttachmentSize != input.Content[0].AttachmentSize ||
		block.MimeType != input.Content[0].MimeType ||
		block.Filename != input.Content[0].Filename || block.Data != "" {
		t.Fatalf("sanitized descriptor = %+v, want preserved byte-free descriptor", block)
	}
}

func TestLimitInitHistoryPreservesDocumentDescriptor(t *testing.T) {
	input := core.AgentMessage{Message: core.Message{Role: "user", Content: []core.Content{{
		Type: "document", AttachmentID: "att_0123456789abcdef01234567", AttachmentSize: 42,
		MimeType: "application/pdf", Filename: "report.pdf",
	}}}}
	got, _ := limitInitHistory([]core.AgentMessage{input})
	if len(got) != 1 || len(got[0].Content) != 1 {
		t.Fatalf("sanitized history = %#v, want one document message", got)
	}
	block := got[0].Content[0]
	if block.Type != "document" || block.AttachmentID != input.Content[0].AttachmentID ||
		block.AttachmentSize != input.Content[0].AttachmentSize || block.MimeType != input.Content[0].MimeType ||
		block.Filename != input.Content[0].Filename || block.Data != "" {
		t.Fatalf("sanitized document descriptor = %+v, want preserved byte-free descriptor", block)
	}
}

func TestConversationMessagesProjectsAttachmentDescriptors(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := mgr.attachStore.PutRef(sess.ID, pngBytes(64), attachment.PutMeta{
		Name: "screen.png", Mime: "image/png", Kind: "image", Width: 1, Height: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	appendConversationTestMessage(sess, "image", "user", "look", nil, core.Content{
		Type: "image", AttachmentID: descriptor.ID, AttachmentSize: descriptor.Size,
		MimeType: descriptor.Mime, Filename: descriptor.Name,
	})

	handler := NewServer(mgr)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID+"/messages", nil)
	req.Host = "localhost"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("messages = %d: %s", rec.Code, rec.Body.String())
	}
	var response conversationResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.Messages) != 1 {
		t.Fatalf("messages = %#v", response.Messages)
	}
	message := response.Messages[0]
	if message.OmittedBlocks != 0 || len(message.Attachments) != 1 {
		t.Fatalf("message projection = %#v", message)
	}
	attachment := message.Attachments[0]
	if attachment.ID != descriptor.ID || attachment.Name != descriptor.Name || attachment.Mime != descriptor.Mime ||
		attachment.Size != descriptor.Size || attachment.Kind != "image" || attachment.Width != 1 || attachment.Height != 1 ||
		attachment.URL != "/api/sessions/"+sess.ID+"/attachments/"+descriptor.ID {
		t.Fatalf("attachment projection = %#v", attachment)
	}
	if strings.Contains(rec.Body.String(), "iVBOR") {
		t.Fatalf("attachment bytes leaked in response: %s", rec.Body.String())
	}
}

func TestConversationMessagesProjectsDocumentDescriptor(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := mgr.attachStore.PutRef(sess.ID, []byte("%PDF-1.4"), attachment.PutMeta{
		Name: "report.pdf", Mime: "application/pdf", Kind: "file",
	})
	if err != nil {
		t.Fatal(err)
	}
	appendConversationTestMessage(sess, "document", "user", "review", nil, core.Content{
		Type: "document", AttachmentID: descriptor.ID, AttachmentSize: descriptor.Size,
		MimeType: descriptor.Mime, Filename: descriptor.Name,
	})

	handler := NewServer(mgr)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID+"/messages", nil)
	req.Host = "localhost"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("messages = %d: %s", rec.Code, rec.Body.String())
	}
	var response conversationResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.Messages) != 1 {
		t.Fatalf("messages = %#v", response.Messages)
	}
	message := response.Messages[0]
	if message.OmittedBlocks != 0 || len(message.Attachments) != 1 {
		t.Fatalf("message projection = %#v", message)
	}
	got := message.Attachments[0]
	if got.ID != descriptor.ID || got.Name != descriptor.Name || got.Mime != descriptor.Mime ||
		got.Size != descriptor.Size || got.Kind != "file" ||
		got.URL != "/api/sessions/"+sess.ID+"/attachments/"+descriptor.ID {
		t.Fatalf("document projection = %#v", got)
	}
}

func TestConversationMessagesOmitsLegacyInlineImages(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	appendConversationTestMessage(sess, "legacy-image", "user", "look", nil, core.Content{
		Type: "image", Data: "aGVsbG8=", MimeType: "image/png", Filename: "legacy.png",
	})

	handler := NewServer(mgr)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID+"/messages", nil)
	req.Host = "localhost"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("messages = %d: %s", rec.Code, rec.Body.String())
	}
	var response conversationResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.Messages) != 1 {
		t.Fatalf("messages = %#v", response.Messages)
	}
	message := response.Messages[0]
	if len(message.Attachments) != 0 || message.OmittedBlocks != 1 {
		t.Fatalf("legacy inline projection = %#v; want no attachment DTO and one omitted block", message)
	}
}

func TestGetAttachmentDeletedSessionIgnoresCachedETag(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	data := pngBytes(64)
	descriptor, err := mgr.attachStore.PutRef(sess.ID, data, attachment.PutMeta{
		Name: "screen.png", Mime: "image/png", Kind: "image",
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewServer(mgr))
	defer server.Close()
	path := "/api/sessions/" + sess.ID + "/attachments/" + descriptor.ID
	first := apiReq(t, server, http.MethodGet, path, "")
	first.Body.Close() //nolint:errcheck
	if first.StatusCode != http.StatusOK || first.Header.Get("ETag") == "" {
		t.Fatalf("initial GET = %d, ETag=%q; want 200 with ETag", first.StatusCode, first.Header.Get("ETag"))
	}
	etag := first.Header.Get("ETag")
	if err := mgr.Delete(sess.ID); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("If-None-Match", etag)
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close() //nolint:errcheck
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound || bytes.Contains(body, data) {
		t.Fatalf("conditional GET after delete = %d, %q; want 404 without attachment bytes", resp.StatusCode, body)
	}
}

func TestConversationMessagesProjectsAttachmentOnlyMessage(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	content := []core.Content{{
		Type: "image", AttachmentID: "att_0123456789abcdef01234567", AttachmentSize: 42,
		MimeType: "image/png", Filename: "screen.png",
	}}
	projection := mgr.safeConversationMessages("0123456789abcdef", []core.AgentMessage{{
		Message: core.Message{MsgID: "image-only", Role: "user", Timestamp: time.Now().Unix(), Content: content},
	}})
	if len(projection.messages) != 1 || len(projection.messages[0].Attachments) != 1 || projection.messages[0].OmittedBlocks != 0 {
		t.Fatalf("attachment-only projection = %#v", projection.messages)
	}
}
