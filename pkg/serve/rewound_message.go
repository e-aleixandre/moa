package serve

import "github.com/e-aleixandre/moa/pkg/core"

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
	// SessionMentions are the sessions the original message explicitly
	// selected, from its server-minted receipt only (never from its text).
	// Handing them back does not grant anything: the resend is a new message.
	SessionMentions []string `json:"session_mentions,omitempty"`
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
			// A text block with a Filename and no stored attachment is the note moa wrote for a file
			// kept on disk (see toDisk). Only that mark identifies it: text
			// is never classified by its wording.
			if c.Filename != "" && c.AttachmentID == "" {
				out.Unavailable = append(out.Unavailable, c.Filename)
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
