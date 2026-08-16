package attachment

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestNewScopeRejectsHalfBuiltValues(t *testing.T) {
	store := newTestStore(t)

	if _, err := NewScope(nil, sessionOne); err == nil {
		t.Fatal("NewScope with a nil store must fail: a scope without a store could produce nothing and resolve nothing")
	}
	for _, bad := range []string{"", "has spaces", "bad/slash"} {
		if _, err := NewScope(store, bad); err == nil {
			t.Fatalf("NewScope(store, %q) must fail: an invalid owner cannot own anything", bad)
		}
	}
	scope, err := NewScope(store, sessionOne)
	if err != nil {
		t.Fatal(err)
	}
	if scope.SessionID() != sessionOne {
		t.Fatalf("scope.SessionID() = %q, want %q", scope.SessionID(), sessionOne)
	}
}

func TestScopePutAndMaterializerShareTheOwner(t *testing.T) {
	store := newTestStore(t)
	scope, err := NewScope(store, sessionOne)
	if err != nil {
		t.Fatal(err)
	}

	data := []byte("scoped image bytes")
	descriptor, err := scope.Put(data, PutMeta{Name: "photo.png", Mime: "image/png", Kind: "image"})
	if err != nil {
		t.Fatal(err)
	}
	// The reference must be registered under the scope's own session, not
	// anywhere else: ownership is what keeps the blob alive until the session
	// is deleted.
	if _, ok := store.Lookup(sessionOne, descriptor.ID); !ok {
		t.Fatalf("descriptor %s is not referenced by %s", descriptor.ID, sessionOne)
	}

	materialize := scope.Materializer()
	if materialize == nil {
		t.Fatal("a non-nil scope must always yield a materializer")
	}
	msgs := []core.Message{{
		Role:    "user",
		Content: []core.Content{{Type: "image", AttachmentID: descriptor.ID, MimeType: "image/png"}},
	}}
	got, err := materialize(context.Background(), msgs)
	if err != nil {
		t.Fatalf("materializer failed, so the scope resolves under the wrong session: %v", err)
	}
	if want := base64.StdEncoding.EncodeToString(data); got[0].Content[0].Data != want {
		t.Fatalf("materialized data = %q, want the bytes stored through the same scope", got[0].Content[0].Data)
	}
}

func TestNilScopeIsInertButUsable(t *testing.T) {
	var scope *Scope
	if scope.SessionID() != "" {
		t.Fatalf("nil scope SessionID = %q, want empty", scope.SessionID())
	}
	if scope.Materializer() != nil {
		t.Fatal("a nil scope must not yield a materializer")
	}
	if _, err := scope.Put([]byte("x"), PutMeta{Kind: "image"}); err == nil {
		t.Fatal("a nil scope must refuse to produce references")
	}
}

func TestScopeContextRoundTripAndShadowing(t *testing.T) {
	store := newTestStore(t)
	scope, err := NewScope(store, sessionOne)
	if err != nil {
		t.Fatal(err)
	}

	base := context.Background()
	if ScopeFromContext(base) != nil {
		t.Fatal("a bare context must carry no capability")
	}
	withScope := WithScope(base, scope)
	if ScopeFromContext(withScope) != scope {
		t.Fatal("WithScope/ScopeFromContext round-trip failed")
	}
	// Writing nil must HIDE an inherited capability, not leave it visible.
	if got := ScopeFromContext(WithScope(withScope, nil)); got != nil {
		t.Fatalf("WithScope(ctx, nil) left scope %v visible; nil must shadow the inherited value", got)
	}
}
