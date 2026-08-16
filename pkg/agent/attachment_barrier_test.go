package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/e-aleixandre/moa/pkg/attachment"
	"github.com/e-aleixandre/moa/pkg/core"
)

// providerSpy records whether the provider was ever reached. The whole point of
// the barrier is that a request carrying an unresolved reference NEVER gets
// there: AttachmentID is a moa-internal identifier and no provider can resolve
// it, so the model would simply not see the image.
type providerSpy struct {
	called atomic.Bool
}

func (p *providerSpy) Stream(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	p.called.Store(true)
	return simpleTextResponse("done")(req)
}

func runWithContent(t *testing.T, cfg AgentConfig, content []core.Content) error {
	t.Helper()
	ag, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ag.SendWithContent(context.Background(), content)
	return err
}

func TestBarrierRejectsEmptyImageDescriptor(t *testing.T) {
	spy := &providerSpy{}
	err := runWithContent(t, AgentConfig{
		Provider: spy,
		Model:    core.Model{ID: "test-model", Provider: "mock"},
	}, []core.Content{{Type: "image", AttachmentID: "att_aaaaaaaaaaaaaaaaaaaaaaaa", MimeType: "image/png"}})

	if !errors.Is(err, ErrUnresolvedAttachment) {
		t.Fatalf("err = %v, want ErrUnresolvedAttachment", err)
	}
	if spy.called.Load() {
		t.Error("provider was called with an image the model could not see")
	}
}

func TestBarrierRejectsEmptyDocumentDescriptor(t *testing.T) {
	spy := &providerSpy{}
	err := runWithContent(t, AgentConfig{
		Provider: spy,
		Model:    core.Model{ID: "test-model", Provider: "mock"},
	}, []core.Content{{Type: "document", AttachmentID: "att_bbbbbbbbbbbbbbbbbbbbbbbb", Filename: "spec.pdf"}})

	if !errors.Is(err, ErrUnresolvedAttachment) {
		t.Fatalf("err = %v, want ErrUnresolvedAttachment", err)
	}
	if spy.called.Load() {
		t.Error("provider was called with a document the model could not see")
	}
}

// A descriptor whose blob is gone: the materializer errors out, so the run must
// fail before the provider is reached — never send a half-empty history.
func TestBarrierMissingBlobNeverReachesProvider(t *testing.T) {
	scope := newTestScope(t, testOwnerSession)
	spy := &providerSpy{}
	err := runWithContent(t, AgentConfig{
		Provider:        spy,
		Model:           core.Model{ID: "test-model", Provider: "mock"},
		AttachmentScope: scope,
	}, []core.Content{{Type: "image", AttachmentID: "att_cccccccccccccccccccccccc", MimeType: "image/png"}})

	if err == nil {
		t.Fatal("run succeeded with a descriptor whose blob does not exist")
	}
	if spy.called.Load() {
		t.Error("provider was called although the attachment could not be materialized")
	}
}

// A defective materializer that returns the descriptor untouched is exactly the
// silent failure the barrier exists for: no error anywhere, and the model gets
// an empty image.
func TestBarrierCatchesDefectiveMaterializer(t *testing.T) {
	store, err := attachment.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := store.PutRef(testOwnerSession, []byte("real image bytes"), attachment.PutMeta{
		Name: "photo.png", Mime: "image/png", Kind: "image",
	})
	if err != nil {
		t.Fatal(err)
	}

	spy := &providerSpy{}
	err = runWithContent(t, AgentConfig{
		Provider: spy,
		Model:    core.Model{ID: "test-model", Provider: "mock"},
		// Pass-through: it "materializes" nothing but reports success.
		MaterializeContent: func(_ context.Context, msgs []core.Message) ([]core.Message, error) {
			return msgs, nil
		},
	}, []core.Content{{Type: "image", AttachmentID: descriptor.ID, MimeType: "image/png"}})

	if !errors.Is(err, ErrUnresolvedAttachment) {
		t.Fatalf("err = %v, want ErrUnresolvedAttachment", err)
	}
	if spy.called.Load() {
		t.Error("provider was called with a descriptor a defective materializer left unresolved")
	}
}

// The mirror image of the above: a descriptor WITH bytes is legitimate and must
// not trip the barrier.
func TestBarrierAllowsMaterializedDescriptor(t *testing.T) {
	scope := newTestScope(t, testOwnerSession)
	descriptor, err := scope.Put([]byte("real image bytes"), attachment.PutMeta{
		Name: "photo.png", Mime: "image/png", Kind: "image",
	})
	if err != nil {
		t.Fatal(err)
	}
	spy := &providerSpy{}
	if err := runWithContent(t, AgentConfig{
		Provider:        spy,
		Model:           core.Model{ID: "test-model", Provider: "mock"},
		AttachmentScope: scope,
	}, []core.Content{{Type: "image", AttachmentID: descriptor.ID, MimeType: "image/png"}}); err != nil {
		t.Fatal(err)
	}
	if !spy.called.Load() {
		t.Error("provider was never called for a properly materialized attachment")
	}
}
