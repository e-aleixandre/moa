package serve

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/push"
)

// withPushEndpoint gives mgr a real dispatcher whose only subscription is a
// local endpoint, and returns the number of pushes it has received.
func withPushEndpoint(t *testing.T, mgr *Manager) *atomic.Int32 {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)

	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatal(err)
	}
	store, err := push.NewStore(filepath.Join(t.TempDir(), "subs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Add(webpush.Subscription{
		Endpoint: srv.URL,
		Keys: webpush.Keys{
			P256dh: base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
			Auth:   base64.RawURLEncoding.EncodeToString(auth),
		},
	}); err != nil {
		t.Fatal(err)
	}
	vapid, err := push.LoadOrGenerateVAPID(filepath.Join(t.TempDir(), "vapid.json"))
	if err != nil {
		t.Fatal(err)
	}
	mgr.pushDispatcher = push.NewDispatcher(store, vapid, "mailto:test@example.com")
	mgr.pushPolicy = push.NewPolicy(mgr.pushDispatcher, push.PolicyConfig{})
	return &hits
}

// Sessions an owner launched report to that owner, not to the user's phone. A
// session the user opened in the owner's codebase, and the owner's own
// conversation, still push.
func TestPushSkipsSessionsTheOwnerLaunched(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	hits := withPushEndpoint(t, mgr)
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")
	userSess := ownerChild(t, mgr, root, "opened by the user")
	launched, err := mgr.CreateSession(CreateOpts{CWD: root, Title: "launched by the owner", Origin: "owner"})
	if err != nil {
		t.Fatal(err)
	}

	ask := func(sess *ManagedSession) int32 {
		t.Helper()
		before := hits.Load()
		sess.runtime.Bus.Publish(bus.AskUserRequested{SessionID: sess.ID, RunGen: 1, ID: "ask-" + sess.ID})
		sess.runtime.Bus.Publish(bus.PermissionRequested{SessionID: sess.ID, RunGen: 1, ID: "perm-" + sess.ID})
		sess.runtime.Bus.Publish(bus.StateChanged{SessionID: sess.ID, State: string(bus.StateError)})
		sess.runtime.Bus.Drain(5 * time.Second)
		return hits.Load() - before
	}

	if got := ask(launched); got != 0 {
		t.Fatalf("a session the owner launched pushed %d notifications to the user", got)
	}
	if got := ask(userSess); got != 3 {
		t.Fatalf("a user session in an owner's codebase pushed %d notifications, want 3", got)
	}
	if got := ask(ownerSess); got != 3 {
		t.Fatalf("the owner's own conversation pushed %d notifications, want 3", got)
	}
}
