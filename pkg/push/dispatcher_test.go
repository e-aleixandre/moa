package push

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// validSub builds a subscription with a real P-256 public key + auth secret so
// webpush's RFC 8291 payload encryption succeeds against the test endpoint.
func validSub(t *testing.T, endpoint string) webpush.Subscription {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ecdh key: %v", err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatalf("auth: %v", err)
	}
	return webpush.Subscription{
		Endpoint: endpoint,
		Keys: webpush.Keys{
			P256dh: base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
			Auth:   base64.RawURLEncoding.EncodeToString(auth),
		},
	}
}

func newTestDispatcher(t *testing.T, store *Store) *Dispatcher {
	t.Helper()
	v, err := LoadOrGenerateVAPID(filepath.Join(t.TempDir(), "vapid.json"))
	if err != nil {
		t.Fatalf("vapid: %v", err)
	}
	return NewDispatcher(store, v, "mailto:test@example.com")
}

func TestDispatcher_NotifySends(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	store, err := NewStore(filepath.Join(t.TempDir(), "subs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Add(validSub(t, srv.URL)); err != nil {
		t.Fatal(err)
	}

	newTestDispatcher(t, store).Notify(Notification{Title: "moa", Body: "test"})

	if hits.Load() != 1 {
		t.Fatalf("expected 1 push delivered, got %d", hits.Load())
	}
	if store.Len() != 1 {
		t.Fatalf("subscription should survive success, got len %d", store.Len())
	}
}

func TestDispatcher_PrunesGoneSubscription(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone) // 410: subscription permanently gone
	}))
	defer srv.Close()

	store, err := NewStore(filepath.Join(t.TempDir(), "subs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Add(validSub(t, srv.URL)); err != nil {
		t.Fatal(err)
	}

	newTestDispatcher(t, store).Notify(Notification{Title: "moa"})

	if store.Len() != 0 {
		t.Fatalf("gone subscription should be pruned, got len %d", store.Len())
	}
}
