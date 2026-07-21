package attachment

import (
	"context"
	"encoding/base64"
	"errors"
	"io/fs"
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
	descriptor, err := store.PutRef(sessionOne, []byte("owned bytes"), PutMeta{Kind: "image"})
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
