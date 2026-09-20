package serve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func voiceLiveCloseRequest(t *testing.T, handler http.HandlerFunc, path, body string, identity *authIdentity) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if identity != nil {
		req = withAuthIdentity(req, *identity)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// Point 4: the browser's session.close is the normal path, and it is the one
// that does not survive a locked screen. This is what closes the session when
// that path never runs.
func TestVoiceLiveRegistryClosesOnHangupAndIsIdempotent(t *testing.T) {
	reg, closed := newTestVoiceLiveRegistry()
	reg.track("live-1", "sess-1", "device:a")

	if !reg.hangup("live-1", "sess-1", "device:a") {
		t.Fatal("the caller could not close his own call")
	}
	// Both paths closing is the expected case, not an error: the browser
	// already sent session.close before this request arrived.
	if !reg.hangup("live-1", "sess-1", "device:a") {
		t.Fatal("a second hangup was rejected")
	}
	if !reg.hangup("never-existed", "sess-1", "device:a") {
		t.Fatal("hanging up an unknown call was rejected")
	}
	if len(*closed) != 1 || (*closed)[0] != "live-1" {
		t.Fatalf("upstream closes = %v, want exactly one", *closed)
	}
	if len(reg.calls) != 0 {
		t.Fatalf("the registry kept a closed call: %#v", reg.calls)
	}
}

func TestVoiceLiveRegistryRefusesAnotherCallersSession(t *testing.T) {
	reg, closed := newTestVoiceLiveRegistry()
	reg.track("live-1", "sess-1", "device:a")

	if reg.hangup("live-1", "sess-1", "device:b") {
		t.Fatal("a different principal closed a call that was not his")
	}
	if reg.hangup("live-1", "other-session", "device:a") {
		t.Fatal("a call was closed from a session it does not belong to")
	}
	if _, allowed := reg.touch("live-1", "sess-1", "device:b"); allowed {
		t.Fatal("a different principal kept somebody else's call alive")
	}
	if len(*closed) != 0 {
		t.Fatalf("a refused request still closed the session: %v", *closed)
	}
}

// The bounded sweeper: what the locked screen, the closed tab and the dropped
// network all look like from the server.
func TestVoiceLiveRegistrySweepsCallsNobodyClosed(t *testing.T) {
	reg, closed := newTestVoiceLiveRegistry()
	now := time.Unix(1000, 0).UTC()
	reg.now = func() time.Time { return now }
	reg.track("abandoned", "sess-1", "device:a")
	reg.track("alive", "sess-2", "device:a")

	now = now.Add(voiceLiveIdleTTL / 2)
	if done := reg.sweep(); done {
		t.Fatal("the sweeper stopped while calls were still tracked")
	}
	if len(*closed) != 0 {
		t.Fatalf("a call within its heartbeat window was closed: %v", *closed)
	}

	// Only one of the two is still being heard from.
	now = now.Add(voiceLiveIdleTTL)
	if _, allowed := reg.touch("alive", "sess-2", "device:a"); !allowed {
		t.Fatal("the heartbeat was refused")
	}
	if done := reg.sweep(); done {
		t.Fatal("the sweeper stopped with a live call tracked")
	}
	if len(*closed) != 1 || (*closed)[0] != "abandoned" {
		t.Fatalf("sweep closed %v, want the abandoned call only", *closed)
	}

	// A client that keeps pinging forever still cannot hold a session past the
	// hard ceiling.
	now = now.Add(voiceLiveMaxCallAge)
	reg.touch("alive", "sess-2", "device:a")
	if done := reg.sweep(); !done {
		t.Fatal("the sweeper did not stop after the last call was closed")
	}
	if len(*closed) != 2 || (*closed)[1] != "alive" {
		t.Fatalf("sweep closed %v, want the aged call too", *closed)
	}
	if reg.sweeping {
		t.Fatal("the sweeper goroutine stays marked as running with nothing to sweep")
	}
}

func TestVoiceLiveRegistryDoesNotCloseALiveCallToCapItsBookkeeping(t *testing.T) {
	reg, closed := newTestVoiceLiveRegistry()
	// Starts are rate limited, but Live sessions last longer than the rate
	// window. A registry cap must never turn its accounting limit into a
	// surprise hangup for the oldest still-heartbeating call.
	for i := 0; i < 20; i++ {
		reg.now = func() time.Time { return time.Unix(int64(1000+i), 0).UTC() }
		reg.track(fmt.Sprintf("live-%02d", i), "sess-1", "device:a")
	}
	if len(reg.calls) != 20 {
		t.Fatalf("tracked = %d, want every live call", len(reg.calls))
	}
	if len(*closed) != 0 {
		t.Fatalf("a still-live call was closed to cap bookkeeping: %v", *closed)
	}
}

// The sweeper runs only while there is something to sweep, and one is enough.
func TestVoiceLiveRegistrySweeperStartsOnceAndStopsWhenEmpty(t *testing.T) {
	reg, _ := newTestVoiceLiveRegistry()
	spawned := 0
	reg.spawn = func(func()) { spawned++ }
	reg.track("live-1", "sess-1", "device:a")
	reg.track("live-2", "sess-1", "device:a")
	if spawned != 1 {
		t.Fatalf("sweepers spawned = %d, want 1", spawned)
	}
	reg.hangup("live-1", "sess-1", "device:a")
	reg.hangup("live-2", "sess-1", "device:a")
	if done := reg.sweep(); !done {
		t.Fatal("an empty registry did not end its sweeper")
	}
	reg.track("live-3", "sess-1", "device:a")
	if spawned != 2 {
		t.Fatalf("a new call after the sweeper stopped left nothing sweeping: %d", spawned)
	}
}

func TestVoiceLiveCloseAndHeartbeatRoutes(t *testing.T) {
	reg, closed := newTestVoiceLiveRegistry()
	identity := authIdentity{Kind: "device", DeviceID: "a"}
	reg.track("live-1", "sess-1", identity.auditID())
	closeRoute := handleVoiceLiveClose(reg)
	beat := handleVoiceLiveHeartbeat(reg)

	for _, body := range []string{`{}`, `{"session_id":"sess-1"}`, `{"session_id":"sess-1","live_session_id":""}`, `not json`} {
		rec := voiceLiveCloseRequest(t, closeRoute, "/api/voice/live/close", body, &identity)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d", body, rec.Code)
		}
	}
	if rec := voiceLiveCloseRequest(t, beat, "/api/voice/live/heartbeat", `{"session_id":"sess-1","live_session_id":"live-1"}`, &identity); rec.Code != http.StatusNoContent {
		t.Fatalf("heartbeat = %d: %s", rec.Code, rec.Body.String())
	}
	// A heartbeat for a call this server does not know is not an error the
	// owner sees, but the client must learn to stop pinging.
	rec := voiceLiveCloseRequest(t, beat, "/api/voice/live/heartbeat", `{"session_id":"sess-1","live_session_id":"gone"}`, &identity)
	if rec.Code != http.StatusNotFound || voiceLiveJSONBody(t, rec)["cause"] != voiceLiveCauseUnknownCall {
		t.Fatalf("unknown call = %d: %s", rec.Code, rec.Body.String())
	}
	other := authIdentity{Kind: "device", DeviceID: "b"}
	rec = voiceLiveCloseRequest(t, closeRoute, "/api/voice/live/close", `{"session_id":"sess-1","live_session_id":"live-1"}`, &other)
	if rec.Code != http.StatusForbidden || voiceLiveJSONBody(t, rec)["cause"] != voiceLiveCauseWrongCaller {
		t.Fatalf("another caller = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := voiceLiveCloseRequest(t, closeRoute, "/api/voice/live/close", `{"session_id":"sess-1","live_session_id":"live-1"}`, &identity); rec.Code != http.StatusNoContent {
		t.Fatalf("close = %d: %s", rec.Code, rec.Body.String())
	}
	if len(*closed) != 1 {
		t.Fatalf("upstream closes = %v", *closed)
	}
}

// The endpoint and its idempotency, against a fake transport: no network, but
// the exact request shape the Live API documents for ending a session.
func TestVoiceLiveUpstreamHangupUsesTheDocumentedEndpoint(t *testing.T) {
	var mu sync.Mutex
	var seen []*http.Request
	status := http.StatusOK
	body := ""
	client := &http.Client{Transport: voiceLiveRoundTripper(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		seen = append(seen, req)
		mu.Unlock()
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	hangup := voiceLiveUpstreamHangup(func() (string, bool) { return "key", true }, client)

	if err := hangup(context.Background(), "live_abc/../evil"); err != nil {
		t.Fatalf("hangup = %v", err)
	}
	req := seen[0]
	if req.Method != http.MethodPost || req.URL.String() != "https://api.openai.com/v1/live/sessions/live_abc%2F..%2Fevil/hangup" {
		t.Fatalf("request = %s %s", req.Method, req.URL)
	}
	if req.Header.Get("Authorization") != "Bearer key" || req.ContentLength != 0 {
		t.Fatalf("auth %q, body length %d", req.Header.Get("Authorization"), req.ContentLength)
	}

	// The session was already closed by the browser: closing twice is the
	// designed outcome of having two paths, so it cannot be a failure.
	status = http.StatusNotFound
	if err := hangup(context.Background(), "live_abc"); err != nil {
		t.Fatalf("closing an already closed session = %v", err)
	}
	status = http.StatusInternalServerError
	body = `{"error":{"message":"credential key was reflected"}}`
	if err := hangup(context.Background(), "live_abc"); err == nil {
		t.Fatal("an upstream failure was reported as a successful close")
	} else if strings.Contains(err.Error(), "key") {
		t.Fatalf("the failed close leaked its credential into the error: %v", err)
	}

	// Without a key there is nothing to authenticate with, and the failure
	// says so instead of reaching the network.
	noKey := voiceLiveUpstreamHangup(nil, client)
	if err := noKey(context.Background(), "live_abc"); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("no key = %v", err)
	}
}

func TestVoiceLiveRegistryLogsAFailedUpstreamClose(t *testing.T) {
	reg, _ := newTestVoiceLiveRegistry()
	now := time.Unix(1000, 0).UTC()
	reg.now = func() time.Time { return now }
	attempts := 0
	reg.closer = func(context.Context, string) error {
		attempts++
		if attempts == 1 {
			return errors.New("status 500: upstream exploded")
		}
		return nil
	}
	reg.track("live-1", "sess-1", "device:a")
	if !reg.hangup("live-1", "sess-1", "device:a") {
		t.Fatal("hangup reported a caller mismatch")
	}
	if len(reg.calls) != 1 {
		t.Fatalf("a failed close lost the record needed for retry: %#v", reg.calls)
	}
	now = now.Add(voiceLiveIdleTTL)
	if done := reg.sweep(); !done {
		t.Fatal("the successful retry did not empty the registry")
	}
	if attempts != 2 || len(reg.calls) != 0 {
		t.Fatalf("close attempts=%d calls=%#v, want one retry and no leak", attempts, reg.calls)
	}
}
