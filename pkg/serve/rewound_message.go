package serve

import (
	"strconv"
	"strings"

	"github.com/e-aleixandre/moa/pkg/core"
)

// rewoundMessage is the user message a rewind hands back to the client for the
// composer: its text and the attachments it carried. Stored attachments are
// referenced by id (the client downloads them from the attachment endpoint);
// inline data only exists on legacy messages.
type rewoundMessage struct {
	Text        string              `json:"text"`
	Attachments []rewoundAttachment `json:"attachments,omitempty"`
	// Unavailable names files that were attached but cannot come back: they
	// live on disk (native budget full, oversized image) and the message only
	// kept a note with their path, not a stored attachment.
	Unavailable []string `json:"unavailable,omitempty"`
}

type rewoundAttachment struct {
	Type         string `json:"type"`
	AttachmentID string `json:"attachment_id,omitempty"`
	Filename     string `json:"filename,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	Size         int64  `json:"size,omitempty"`
	Data         string `json:"data,omitempty"`
}

func newRewoundMessage(msg core.Message) *rewoundMessage {
	out := &rewoundMessage{}
	for _, c := range msg.Content {
		switch c.Type {
		case "text":
			if name, ok := diskAdvisoryName(c.Text); ok {
				out.Unavailable = append(out.Unavailable, name)
				continue
			}
			out.Text += c.Text
		case "image", "document":
			out.Attachments = append(out.Attachments, rewoundAttachment{
				Type:         c.Type,
				AttachmentID: c.AttachmentID,
				Filename:     c.Filename,
				MimeType:     c.MimeType,
				Size:         c.AttachmentSize,
				Data:         c.Data,
			})
		}
	}
	return out
}

// diskAdvisoryName recognises the note buildAttachmentContent writes for a file
// it kept on disk and returns the file's name.
func diskAdvisoryName(text string) (string, bool) {
	rest, ok := strings.CutPrefix(text, strings.TrimSuffix(diskAdvisoryFormat, "%q"))
	if !ok {
		return "", false
	}
	quoted, err := strconv.QuotedPrefix(rest)
	if err != nil {
		return "", false
	}
	name, err := strconv.Unquote(quoted)
	if err != nil {
		return "", false
	}
	return name, true
}
