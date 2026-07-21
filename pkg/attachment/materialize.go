package attachment

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"

	"github.com/ealeixandre/moa/pkg/core"
)

// MaterializeMessages returns a copy of msgs in which every attachment-reference
// content block (Type image/document with a non-empty AttachmentID and empty
// Data) has been expanded to inline base64 read from the blob store, verified to
// be owned by sessionID. Messages/blocks WITHOUT an attachment reference are
// passed through unchanged (legacy inline Data is left as-is). The input slice
// and its content are never mutated: only messages that actually need expansion
// are deep-cloned (copy-on-write), so the common no-attachment case allocates
// nothing new beyond the outer slice.
//
// A referenced blob that is missing/unreadable/owned by another session is a
// hard error (integrity) surfaced before the provider call — it must not
// silently drop the image or pretend the model saw it.
func (s *Store) MaterializeMessages(sessionID string, msgs []core.Message) ([]core.Message, error) {
	var out []core.Message
	for i, msg := range msgs {
		needsMaterialization := false
		for _, content := range msg.Content {
			if isAttachmentReference(content) {
				needsMaterialization = true
				break
			}
		}
		if !needsMaterialization {
			continue
		}

		if out == nil {
			out = append([]core.Message(nil), msgs...)
		}
		out[i].Content = core.CloneContent(msg.Content)
		for j := range out[i].Content {
			content := &out[i].Content[j]
			if !isAttachmentReference(*content) {
				continue
			}

			data, descriptor, err := s.readAttachment(sessionID, content.AttachmentID)
			if err != nil {
				return nil, err
			}
			content.Data = base64.StdEncoding.EncodeToString(data)
			if content.MimeType == "" {
				content.MimeType = descriptor.Mime
			}
			if content.Filename == "" {
				content.Filename = descriptor.Name
			}
		}
	}
	if out == nil {
		return msgs, nil
	}
	return out, nil
}

func isAttachmentReference(content core.Content) bool {
	return (content.Type == "image" || content.Type == "document") &&
		content.AttachmentID != "" && content.Data == ""
}

func (s *Store) readAttachment(sessionID, attachmentID string) ([]byte, Descriptor, error) {
	reader, descriptor, err := s.Open(sessionID, attachmentID)
	if err != nil {
		return nil, Descriptor{}, fmt.Errorf("materialize attachment %q for session %q: %w", attachmentID, sessionID, err)
	}
	data, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil {
		return nil, Descriptor{}, fmt.Errorf("materialize attachment %q for session %q: read blob: %w", attachmentID, sessionID, readErr)
	}
	if closeErr != nil {
		return nil, Descriptor{}, fmt.Errorf("materialize attachment %q for session %q: close blob: %w", attachmentID, sessionID, closeErr)
	}
	return data, descriptor, nil
}

// MaterializerFor returns a per-session materializer closure suitable for the
// agent's MaterializeContent hook. sessionID is baked in.
func (s *Store) MaterializerFor(sessionID string) func(context.Context, []core.Message) ([]core.Message, error) {
	return func(_ context.Context, msgs []core.Message) ([]core.Message, error) {
		return s.MaterializeMessages(sessionID, msgs)
	}
}
