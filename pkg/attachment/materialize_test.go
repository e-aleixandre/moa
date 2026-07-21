package attachment

import (
	"context"
	"encoding/base64"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
)

func TestMaterializeMessages(t *testing.T) {
	store := newTestStore(t)
	bytes := []byte("stored image bytes")
	descriptor, err := store.PutRef(sessionOne, bytes, PutMeta{
		Name: "photo.png",
		Mime: "image/png",
		Kind: "image",
	})
	if err != nil {
		t.Fatal(err)
	}

	msgs := []core.Message{
		{
			Role: "user",
			Content: []core.Content{
				{
					Type:           "image",
					AttachmentID:   descriptor.ID,
					AttachmentSize: descriptor.Size,
					MimeType:       "image/custom",
				},
				{Type: "image", Data: "legacy-inline", MimeType: "image/gif"},
			},
		},
	}

	got, err := store.MaterializeMessages(sessionOne, msgs)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Content[0].Data != base64.StdEncoding.EncodeToString(bytes) {
		t.Fatalf("materialized data = %q", got[0].Content[0].Data)
	}
	if got[0].Content[0].MimeType != "image/custom" {
		t.Fatalf("materialized MIME type = %q, want preserved value", got[0].Content[0].MimeType)
	}
	if got[0].Content[1].Data != "legacy-inline" {
		t.Fatalf("legacy inline data = %q, want unchanged", got[0].Content[1].Data)
	}
	if msgs[0].Content[0].Data != "" {
		t.Fatalf("input descriptor data = %q, want empty", msgs[0].Content[0].Data)
	}
	if &got[0] == &msgs[0] || &got[0].Content[0] == &msgs[0].Content[0] {
		t.Fatal("materialized message was not cloned")
	}

	materializer := store.MaterializerFor(sessionOne)
	got, err = materializer(context.Background(), msgs)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Content[0].Data != base64.StdEncoding.EncodeToString(bytes) {
		t.Fatal("MaterializerFor did not materialize the attachment")
	}
}

func TestMaterializeMessagesMissingOrForeignAttachmentFails(t *testing.T) {
	store := newTestStore(t)
	descriptor, err := store.PutRef(sessionOne, []byte("owned bytes"), PutMeta{Mime: "image/png", Kind: "image"})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name      string
		sessionID string
		attID     string
	}{
		{name: "foreign", sessionID: sessionTwo, attID: descriptor.ID},
		{name: "unknown", sessionID: sessionOne, attID: "att_aaaaaaaaaaaaaaaaaaaaaaaa"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msgs := []core.Message{{
				Role: "user",
				Content: []core.Content{{
					Type:         "image",
					AttachmentID: tc.attID,
				}},
			}}
			_, err := store.MaterializeMessages(tc.sessionID, msgs)
			if !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("MaterializeMessages error = %v, want fs.ErrNotExist", err)
			}
		})
	}
}

func TestMaterializeMessagesDocumentBecomesDurableViewAdvisory(t *testing.T) {
	store := newTestStore(t)
	descriptor, err := store.PutRef(sessionOne, []byte("a,b\n1,2\n"), PutMeta{
		Name: "report.csv", Mime: "text/csv", Kind: "file",
	})
	if err != nil {
		t.Fatal(err)
	}
	msgs := []core.Message{{Role: "user", Content: []core.Content{{
		Type: "document", AttachmentID: descriptor.ID, AttachmentSize: descriptor.Size,
		MimeType: descriptor.Mime, Filename: descriptor.Name,
	}}}}

	got, err := store.MaterializeMessages(sessionOne, msgs)
	if err != nil {
		t.Fatal(err)
	}
	view, err := store.EnsureView(sessionOne, descriptor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Content[0].Type != "text" || !containsAll(got[0].Content[0].Text, "The user attached the file", view, "untrusted user-provided") {
		t.Fatalf("document advisory = %+v, want durable English advisory with %q", got[0].Content[0], view)
	}
	if msgs[0].Content[0].Type != "document" || msgs[0].Content[0].AttachmentID != descriptor.ID {
		t.Fatalf("stored history was mutated: %+v", msgs[0].Content[0])
	}
}

func TestMaterializeMessagesUnavailableDocumentFallsBackToAdvisory(t *testing.T) {
	store := newTestStore(t)
	descriptor, err := store.PutRef(sessionOne, []byte("gone"), PutMeta{Name: "gone.txt", Mime: "text/plain", Kind: "file"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveRef(sessionOne, descriptor.ID); err != nil {
		t.Fatal(err)
	}
	msgs := []core.Message{{Role: "user", Content: []core.Content{{Type: "document", AttachmentID: descriptor.ID, Filename: "gone.txt"}}}}
	got, err := store.MaterializeMessages(sessionOne, msgs)
	if err != nil {
		t.Fatalf("unavailable document should not fail materialization: %v", err)
	}
	if got[0].Content[0].Type != "text" || !containsAll(got[0].Content[0].Text, "gone.txt", "no longer available") {
		t.Fatalf("unavailable document advisory = %+v", got[0].Content[0])
	}
}

func containsAll(text string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(text, part) {
			return false
		}
	}
	return true
}

func TestMaterializeMessagesWithoutAttachmentReturnsInput(t *testing.T) {
	store := newTestStore(t)
	msgs := []core.Message{{Role: "user", Content: []core.Content{core.TextContent("hello")}}}

	got, err := store.MaterializeMessages(sessionOne, msgs)
	if err != nil {
		t.Fatal(err)
	}
	if &got[0] != &msgs[0] {
		t.Fatal("no-attachment messages should retain their original backing array")
	}
}
