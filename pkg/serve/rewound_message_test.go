package serve

import (
	"github.com/e-aleixandre/moa/pkg/attachment"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestNewRewoundMessageKeepsTextAndAttachments(t *testing.T) {
	msg := core.Message{Role: "user", Content: []core.Content{
		{Type: "image", AttachmentID: "att-1", MimeType: "image/png", Filename: "shot.png", AttachmentSize: 42},
		{Type: "document", AttachmentID: "att-2", MimeType: "text/plain", Filename: "notes.txt"},
		{Type: "text", Text: "look at "},
		{Type: "text", Text: "this"},
	}}
	got := newRewoundMessage(msg)
	if got.Text != "look at this" {
		t.Fatalf("text = %q", got.Text)
	}
	if len(got.Attachments) != 2 {
		t.Fatalf("attachments = %+v", got.Attachments)
	}
	if a := got.Attachments[0]; a.Type != "image" || a.AttachmentID != "att-1" || a.Size != 42 || a.Filename != "shot.png" {
		t.Fatalf("first attachment = %+v", a)
	}
	if a := got.Attachments[1]; a.Type != "document" || a.MimeType != "text/plain" {
		t.Fatalf("second attachment = %+v", a)
	}
}

// A file kept on disk (native budget full, oversized image) is not a stored
// attachment: only an advisory text with its path is persisted. The recall must
// not paste that advisory into the composer; it names the file as unavailable
// (review finding 5).
func TestNewRewoundMessageDiskFallbackIsReportedNotPasted(t *testing.T) {
	store, err := attachment.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOA_ATTACHMENTS_DIR", t.TempDir())
	content, _, _, err := buildAttachmentContent([]Attachment{{Name: "shot.png", Mime: "image/png", Data: b64(pngBytes(64))}}, "abcdef0123456789", nil, maxSessionNativeDocBytes, store)
	if err != nil {
		t.Fatal(err)
	}
	content = append(content, core.TextContent("please review"))

	got := newRewoundMessage(core.NewUserMessageWithContent(content))
	if got.Text != "please review" {
		t.Fatalf("text = %q", got.Text)
	}
	if len(got.Attachments) != 0 || len(got.Unavailable) != 1 || got.Unavailable[0] != "shot.png" {
		t.Fatalf("attachments=%+v unavailable=%v", got.Attachments, got.Unavailable)
	}
}
