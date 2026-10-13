package serve

import (
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
