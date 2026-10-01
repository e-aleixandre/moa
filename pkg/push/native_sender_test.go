package push

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRelay checks what a real relay checks that the server is responsible
// for (the signature over the exact bytes) and opens the envelope as the NSE
// would, to prove what was sent.
type fakeRelay struct {
	t      *testing.T
	keys   DeviceKeys
	mu     sync.Mutex
	got    []sendRequest
	status int
	reply  string
}

func (f *fakeRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if r.Method != http.MethodPost || r.URL.Path != "/v1/send" || r.URL.RawQuery != "" {
		f.t.Errorf("relay got %s %s", r.Method, r.URL)
	}
	if got := r.Header.Get("X-Moa-Sig"); got != SignSend(f.keys.Send, body) {
		f.t.Errorf("signature does not cover the body")
	}
	var req sendRequest
	if err := json.Unmarshal(body, &req); err != nil {
		f.t.Errorf("body: %v", err)
	}
	f.mu.Lock()
	f.got = append(f.got, req)
	status, reply := f.status, f.reply
	f.mu.Unlock()
	if status == 0 {
		status, reply = http.StatusOK, `{"ok":true}`
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, reply)
}

func (f *fakeRelay) requests() []sendRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sendRequest(nil), f.got...)
}

func nativeFixture(t *testing.T) (*NativeSender, *fakeRelay, *httptest.Server, NativeRegistration) {
	t.Helper()
	secret := seq(3, 32)
	keys, _ := DeriveDeviceKeys(secret)
	relay := &fakeRelay{t: t, keys: keys}
	srv := httptest.NewServer(relay)
	t.Cleanup(srv.Close)
	store, err := NewNativeStore(filepath.Join(t.TempDir(), "push_native.json"))
	if err != nil {
		t.Fatal(err)
	}
	reg := NativeRegistration{DeviceID: "dev1", RelayURL: srv.URL, Handle: strings.Repeat("h", 64), ExpiresAt: time.Now().Add(24 * time.Hour), Secret: b64u.EncodeToString(secret), Env: "sandbox"}
	if err := store.Put(reg); err != nil {
		t.Fatal(err)
	}
	s := NewNativeSender(store, srv.URL)
	s.SetActive(func(string) bool { return true })
	return s, relay, srv, reg
}

func TestNativeSenderSendsSealedEnvelopeWithoutClearContent(t *testing.T) {
	s, relay, _, reg := nativeFixture(t)
	n := Notification{Title: "moa necesita tu decisión", Body: "Proyecto secreto", SessionID: "sess_9", Tag: "req:sess_9", Kind: KindAsk, Level: LevelUrgent}
	s.Notify(context.Background(), n)
	got := relay.requests()
	if len(got) != 1 {
		t.Fatalf("relay got %d requests", len(got))
	}
	raw, _ := json.Marshal(got[0])
	// Base64 can spell short words by chance: check the long ones, and that
	// nothing travels outside the opaque fields.
	for _, clear := range []string{"secreto", "sess_9", "decisi"} {
		if strings.Contains(string(raw), clear) {
			t.Fatalf("relay request carries %q in clear: %s", clear, raw)
		}
	}
	var shape map[string]json.RawMessage
	_ = json.Unmarshal(raw, &shape)
	if len(shape) != 4 {
		t.Fatalf("relay request has fields beyond h, t, c, e: %s", raw)
	}
	if got[0].Handle != reg.Handle || got[0].Collapse != collapseID(relay.keys, n.Tag) {
		t.Fatalf("handle/collapse = %q/%q", got[0].Handle, got[0].Collapse)
	}
	if d := time.Since(time.Unix(got[0].Time, 0)); d < 0 || d > 5*time.Second {
		t.Fatalf("t is not now: %v", d)
	}
	p, err := OpenEnvelope(relay.keys, got[0].Envelope, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if p.Dest != "session" || p.Sess != "sess_9" || p.Kind != KindAsk || p.Level != LevelUrgent || p.Title != n.Title || p.Body != n.Body {
		t.Fatalf("envelope = %+v", p)
	}
	if r, _ := s.LastResult("dev1"); r.Result != "ok" {
		t.Fatalf("last result = %+v", r)
	}
}

func TestNativeSenderSkipsInactiveForeignRelayAndExpiredHandles(t *testing.T) {
	s, relay, _, reg := nativeFixture(t)
	n := Notification{Title: "t", Kind: KindDone, Level: LevelActive}

	s.SetActive(nil) // device lifecycle unknown: send nothing
	s.Notify(context.Background(), n)
	s.SetActive(func(string) bool { return false })
	s.Notify(context.Background(), n)
	s.SetActive(func(string) bool { return true })

	other := reg
	other.DeviceID, other.RelayURL = "dev2", "https://elsewhere.example"
	if err := s.Store().Put(other); err != nil {
		t.Fatal(err)
	}
	expired := reg
	expired.DeviceID, expired.ExpiresAt = "dev3", time.Now().Add(-time.Second)
	if err := s.Store().Put(expired); err != nil {
		t.Fatal(err)
	}
	if err := s.Store().Remove("dev1"); err != nil {
		t.Fatal(err)
	}
	s.Notify(context.Background(), n)
	if got := relay.requests(); len(got) != 0 {
		t.Fatalf("relay got %d requests, want none", len(got))
	}
	if r, _ := s.LastResult("dev3"); r.Result != "handle_expired" {
		t.Fatalf("expired handle result = %+v", r)
	}
}

func TestNativeSenderReactsToRelayAnswers(t *testing.T) {
	cases := []struct {
		status int
		reply  string
		result string
		kept   bool
	}{
		{http.StatusGone, `{"error":"unregistered"}`, "unregistered", false},
		{http.StatusUnauthorized, `{"error":"handle_expired"}`, "handle_expired", true},
		{http.StatusBadGateway, `{"error":"apns","status":400,"reason":"BadDeviceToken"}`, "rejected", true},
		{http.StatusUnauthorized, `{"error":"unauthorized"}`, "rejected", true},
	}
	for _, c := range cases {
		s, relay, _, _ := nativeFixture(t)
		relay.status, relay.reply = c.status, c.reply
		s.Notify(context.Background(), Notification{Title: "t", Kind: KindDone, Level: LevelActive})
		r, _ := s.LastResult("dev1")
		if r.Result != c.result {
			t.Errorf("%d %s: result %+v, want %s", c.status, c.reply, r, c.result)
		}
		if _, ok := s.Store().Get("dev1"); ok != c.kept {
			t.Errorf("%d %s: kept=%v, want %v", c.status, c.reply, ok, c.kept)
		}
		if c.result == "rejected" && c.status == http.StatusBadGateway && !strings.Contains(r.Reason, "BadDeviceToken") {
			t.Errorf("APNs reason lost: %+v", r)
		}
	}
}

func TestNativeSenderGoneKeepsANewerRegistration(t *testing.T) {
	s, _, _, reg := nativeFixture(t)
	newer := reg
	newer.Handle = strings.Repeat("n", 64)
	// The device re-registers while the old send is answered 410.
	if err := s.Store().Put(newer); err != nil {
		t.Fatal(err)
	}
	if err := s.store.removeIf(reg); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Store().Get("dev1"); !ok || got.Handle != newer.Handle {
		t.Fatal("a 410 for an old handle dropped the new registration")
	}
}

func TestNativeSenderDoesNotFollowRedirects(t *testing.T) {
	var hits int
	var mu sync.Mutex
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/send", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	store, _ := NewNativeStore(filepath.Join(t.TempDir(), "n.json"))
	_ = store.Put(NativeRegistration{DeviceID: "d", RelayURL: redirect.URL, Handle: strings.Repeat("h", 64), ExpiresAt: time.Now().Add(time.Hour), Secret: b64u.EncodeToString(seq(1, 32)), Env: "sandbox"})
	s := NewNativeSender(store, redirect.URL)
	s.SetActive(func(string) bool { return true })
	s.Notify(context.Background(), Notification{Title: "t"})
	mu.Lock()
	defer mu.Unlock()
	if hits != 0 {
		t.Fatal("the sender followed a redirect away from the relay")
	}
}

func TestNativeStorePersistsPrivately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push_native.json")
	store, _ := NewNativeStore(path)
	reg := NativeRegistration{DeviceID: "d", RelayURL: DefaultRelayURL, Handle: "h", ExpiresAt: time.Unix(2000000000, 0).UTC(), Secret: "s", Env: "production"}
	if err := store.Put(reg); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", info.Mode().Perm())
	}
	again, err := NewNativeStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := again.Get("d"); !ok || got != reg {
		t.Fatalf("reloaded %+v", got)
	}
	if err := again.Remove("d"); err != nil {
		t.Fatal(err)
	}
	third, _ := NewNativeStore(path)
	if len(third.All()) != 0 {
		t.Fatal("removal not persisted")
	}
}

type recordingSender struct{ got []Notification }

func (r *recordingSender) Notify(_ context.Context, n Notification) { r.got = append(r.got, n) }

func TestSendersFanOut(t *testing.T) {
	a, b := &recordingSender{}, &recordingSender{}
	Senders{a, b}.Notify(context.Background(), Notification{Title: "x"})
	if len(a.got) != 1 || len(b.got) != 1 {
		t.Fatal("every transport gets the notification")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	Senders{a, b}.Notify(ctx, Notification{Title: "y"})
	if len(a.got) != 1 {
		t.Fatal("a cancelled delivery goes nowhere")
	}
}
