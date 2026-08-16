package agent

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/e-aleixandre/moa/pkg/attachment"
	"github.com/e-aleixandre/moa/pkg/core"
)

// TestOldInlineSessionsStayValid is the upgrade-compatibility check: a session
// written by an older moa (base64 inline, no AttachmentID) must keep working
// under the new code, with no migration and no store.
//
// This is what a user who just upgraded has on disk, so a regression here means
// broken conversations in the wild, not merely a missed optimization.
func TestOldInlineSessionsStayValid(t *testing.T) {
	oldStyle := []core.Message{{
		Role: "user",
		Content: []core.Content{
			{Type: "text", Text: "look at this"},
			// Exactly what pre-upgrade moa persisted: bytes inline, no reference.
			{Type: "image", MimeType: "image/png", Data: base64.StdEncoding.EncodeToString([]byte("PNGDATA"))},
		},
	}}

	if err := checkResolvedAttachments(oldStyle); err != nil {
		t.Fatalf("the barrier rejected a pre-upgrade session: %v", err)
	}

	// And a document, same shape.
	oldDoc := []core.Message{{
		Role: "user",
		Content: []core.Content{
			{Type: "document", MimeType: "application/pdf", Data: base64.StdEncoding.EncodeToString([]byte("%PDF"))},
		},
	}}
	if err := checkResolvedAttachments(oldDoc); err != nil {
		t.Fatalf("the barrier rejected a pre-upgrade document: %v", err)
	}
}

// TestNoScopeBehavesExactlyLikeBeforeUpgrade covers the user who never gets a
// scope wired (CLI paths, ephemeral agents): nothing externalizes, so nothing
// can dangle.
func TestNoScopeBehavesExactlyLikeBeforeUpgrade(t *testing.T) {
	if got := attachment.ScopeFromContext(context.Background()); got != nil {
		t.Fatalf("a bare context must carry no capability, got %v", got)
	}
}
