package serve

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

// callbackReceiver is a test double for the caller's webhook endpoint. It
// records every delivery and answers with a caller-supplied status sequence.
type callbackReceiver struct {
	srv *httptest.Server

	mu        sync.Mutex
	payloads  []AutomationCallback
	bodies    [][]byte
	headers   []http.Header
	statuses  []int // consumed in order; the last one repeats
	delivered chan struct{}
}

func newCallbackReceiver(t *testing.T, statuses ...int) *callbackReceiver {
	t.Helper()
	if len(statuses) == 0 {
		statuses = []int{http.StatusOK}
	}
	rc := &callbackReceiver{statuses: statuses, delivered: make(chan struct{}, 16)}
	rc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload AutomationCallback
		_ = json.Unmarshal(body, &payload)
		rc.mu.Lock()
		rc.payloads = append(rc.payloads, payload)
		rc.bodies = append(rc.bodies, body)
		rc.headers = append(rc.headers, r.Header.Clone())
		status := rc.statuses[min(len(rc.payloads)-1, len(rc.statuses)-1)]
		rc.mu.Unlock()
		select {
		case rc.delivered <- struct{}{}:
		default:
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(rc.srv.Close)
	return rc
}

func (rc *callbackReceiver) count() int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return len(rc.payloads)
}

func (rc *callbackReceiver) all() []AutomationCallback {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return append([]AutomationCallback(nil), rc.payloads...)
}

// waitForCallbacks blocks until n deliveries landed (or fails the test).
func (rc *callbackReceiver) waitForCallbacks(t *testing.T, n int) []AutomationCallback {
	t.Helper()
	pollUntil(t, 10*time.Second, "callback deliveries", func() bool { return rc.count() >= n })
	return rc.all()
}

// fastCallbackBackoff shrinks the retry waits so the retry tests stay quick.
func fastCallbackBackoff(t *testing.T) {
	t.Helper()
	orig := callbackBackoff
	callbackBackoff = []time.Duration{5 * time.Millisecond, 10 * time.Millisecond, 15 * time.Millisecond}
	t.Cleanup(func() { callbackBackoff = orig })
}

// newCallbackSession creates an automation session pointed at url, without
// going through HTTP (the trigger logic is what is under test here).
func newCallbackSession(t *testing.T, mgr *Manager, url, secret string) *ManagedSession {
	t.Helper()
	meta := map[string]any{}
	if url != "" {
		meta[session.MetaCallbackURL] = url
	}
	if secret != "" {
		meta[session.MetaCallbackSecret] = secret
	}
	sess, err := mgr.CreateSession(CreateOpts{Origin: automationOriginDefault, extraMeta: meta})
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func TestCallbackDeliveredOnRunDone(t *testing.T) {
	rc := newCallbackReceiver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("all green")))
	sess := newCallbackSession(t, mgr, rc.srv.URL, "")

	if _, _, _, err := mgr.Send(sess.ID, "do it", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	got := rc.waitForCallbacks(t, 1)[0]
	if got.Status != callbackStatusDone {
		t.Errorf("status = %q, want done", got.Status)
	}
	if got.SessionID != sess.ID {
		t.Errorf("session_id = %q, want %q", got.SessionID, sess.ID)
	}
	if got.URL != "/?session="+sess.ID {
		t.Errorf("url = %q, want the relative session path", got.URL)
	}
	if got.Title != sess.title() {
		t.Errorf("title = %q, want %q", got.Title, sess.title())
	}
	if got.Summary != "all green" {
		t.Errorf("summary = %q, want the final assistant text", got.Summary)
	}
	if got.Error != "" {
		t.Errorf("error = %q, want empty on success", got.Error)
	}
	if _, err := time.Parse(time.RFC3339, got.Timestamp); err != nil {
		t.Errorf("timestamp %q is not RFC3339: %v", got.Timestamp, err)
	}

	rc.mu.Lock()
	hdr := rc.headers[0]
	rc.mu.Unlock()
	if ct := hdr.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
	if ua := hdr.Get("User-Agent"); ua != "moa-automation" {
		t.Errorf("user-agent = %q", ua)
	}
	if sig := hdr.Get("X-Moa-Signature"); sig != "" {
		t.Errorf("unsigned callback carries a signature: %q", sig)
	}
}

func TestCallbackDeliveredOnRunFailed(t *testing.T) {
	rc := newCallbackReceiver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(errorHandler(errors.New("provider exploded"))))
	sess := newCallbackSession(t, mgr, rc.srv.URL, "")

	if _, _, _, err := mgr.Send(sess.ID, "do it", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	got := rc.waitForCallbacks(t, 1)[0]
	if got.Status != callbackStatusFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if !strings.Contains(got.Error, "provider exploded") {
		t.Errorf("error = %q, want the run error", got.Error)
	}
}

// A permission request fires needs_input exactly once per run, no matter how
// many times the agent asks, and the run's eventual end still reports.
func TestCallbackNeedsInputFiresOncePerRun(t *testing.T) {
	rc := newCallbackReceiver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("hi")))
	sess := newCallbackSession(t, mgr, rc.srv.URL, "")

	b := sess.runtime.Bus
	b.Publish(bus.RunStarted{SessionID: sess.ID, RunGen: 1})
	b.Publish(bus.PermissionRequested{SessionID: sess.ID, ID: "p1", ToolName: "bash"})
	b.Publish(bus.PermissionRequested{SessionID: sess.ID, ID: "p2", ToolName: "bash"})
	b.Publish(bus.AskUserRequested{SessionID: sess.ID, ID: "a1"})
	rc.waitForCallbacks(t, 1)
	b.Drain(2 * time.Second)
	if n := rc.count(); n != 1 {
		t.Fatalf("needs_input delivered %d times, want 1", n)
	}
	if got := rc.all()[0].Status; got != callbackStatusNeedsInput {
		t.Fatalf("status = %q, want needs_input", got)
	}

	// The run ending still reports, and the next run may ask again.
	b.Publish(bus.RunEnded{SessionID: sess.ID, RunGen: 1, FinalText: "done now"})
	all := rc.waitForCallbacks(t, 2)
	if all[1].Status != callbackStatusDone || all[1].Summary != "done now" {
		t.Fatalf("second callback = %+v, want done/'done now'", all[1])
	}
	b.Publish(bus.RunStarted{SessionID: sess.ID, RunGen: 2})
	b.Publish(bus.PermissionRequested{SessionID: sess.ID, ID: "p3", ToolName: "bash"})
	all = rc.waitForCallbacks(t, 3)
	if all[2].Status != callbackStatusNeedsInput {
		t.Fatalf("third callback = %+v, want needs_input for the new run", all[2])
	}
}

// A needs_input callback carries what the run is blocked on, so the caller can
// answer it through the scoped interaction endpoints.
func TestCallbackNeedsInputCarriesPendingAsk(t *testing.T) {
	rc := newCallbackReceiver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("hi")))
	sess := newCallbackSession(t, mgr, rc.srv.URL, "")

	sess.runtime.Bus.Publish(bus.AskUserRequested{SessionID: sess.ID, ID: "a1", Questions: []bus.AskQuestion{
		{Text: "Ship it?", Options: []string{"yes", "no"}},
		{Text: "Which branch?"},
	}})
	got := rc.waitForCallbacks(t, 1)[0]
	if got.Status != callbackStatusNeedsInput {
		t.Fatalf("status = %q, want needs_input", got.Status)
	}
	if got.Pending == nil {
		t.Fatal("needs_input callback carries no pending object")
	}
	if got.Pending.Kind != pendingKindQuestion || got.Pending.ID != "a1" {
		t.Fatalf("pending = %+v, want a question with id a1", got.Pending)
	}
	if len(got.Pending.Questions) != 2 {
		t.Fatalf("questions = %+v, want 2", got.Pending.Questions)
	}
	if got.Pending.Questions[0].Text != "Ship it?" ||
		len(got.Pending.Questions[0].Options) != 2 ||
		got.Pending.Questions[0].Options[0] != "yes" {
		t.Errorf("first question = %+v, want the event's question and options", got.Pending.Questions[0])
	}
	if got.Pending.Tool != "" || got.Pending.Summary != "" {
		t.Errorf("question pending carries permission fields: %+v", got.Pending)
	}
}

func TestCallbackNeedsInputCarriesPendingPermission(t *testing.T) {
	rc := newCallbackReceiver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("hi")))
	sess := newCallbackSession(t, mgr, rc.srv.URL, "")

	sess.runtime.Bus.Publish(bus.PermissionRequested{
		SessionID: sess.ID, ID: "p1", ToolName: "bash",
		Args: map[string]any{"command": "rm -rf /tmp/x"},
	})
	got := rc.waitForCallbacks(t, 1)[0]
	if got.Pending == nil {
		t.Fatal("needs_input callback carries no pending object")
	}
	if got.Pending.Kind != pendingKindPermission || got.Pending.ID != "p1" {
		t.Fatalf("pending = %+v, want a permission with id p1", got.Pending)
	}
	if got.Pending.Tool != "bash" {
		t.Errorf("tool = %q, want bash", got.Pending.Tool)
	}
	if got.Pending.Summary != "rm -rf /tmp/x" {
		t.Errorf("summary = %q, want the command", got.Pending.Summary)
	}
	if len(got.Pending.Questions) != 0 {
		t.Errorf("permission pending carries questions: %+v", got.Pending.Questions)
	}
}

// done/failed payloads are unchanged: no pending object.
func TestCallbackDonePayloadHasNoPending(t *testing.T) {
	rc := newCallbackReceiver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("all green")))
	sess := newCallbackSession(t, mgr, rc.srv.URL, "")

	if _, _, _, err := mgr.Send(sess.ID, "do it", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	if got := rc.waitForCallbacks(t, 1)[0]; got.Pending != nil {
		t.Fatalf("done payload carries pending: %+v", got.Pending)
	}
}

func TestPermissionArgsSummary(t *testing.T) {
	tests := []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{"bash command", "bash", map[string]any{"command": "go test ./..."}, "go test ./..."},
		{"file path", "write", map[string]any{"path": "/tmp/x", "content": "y"}, "/tmp/x"},
		{"fallback is deterministic", "fetch_content", map[string]any{"url": "https://x", "raw": true}, "raw=true url=https://x"},
		{"no args", "noop", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := permissionArgsSummary(tt.tool, tt.args); got != tt.want {
				t.Fatalf("summary = %q, want %q", got, tt.want)
			}
		})
	}
}

// The per-run guard is exact because every trigger is handled by a single
// SubscribeAll subscriber, in publication order: a RunStarted published between
// two blocking events resets the guard exactly there, no earlier and no later.
// With separate typed subscriptions this sequence could deliver one or three
// callbacks depending on goroutine scheduling.
func TestCallbackNeedsInputRespectsPublicationOrder(t *testing.T) {
	rc := newCallbackReceiver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("hi")))
	sess := newCallbackSession(t, mgr, rc.srv.URL, "")

	b := sess.runtime.Bus
	b.Publish(bus.RunStarted{SessionID: sess.ID, RunGen: 1})
	b.Publish(bus.PermissionRequested{SessionID: sess.ID, ID: "p1", ToolName: "bash"})
	b.Publish(bus.PermissionRequested{SessionID: sess.ID, ID: "p2", ToolName: "bash"})
	// A RunStarted that lands after a delivered needs_input opens the guard for
	// the new run only — it must not resurrect the previous run's events.
	b.Publish(bus.RunStarted{SessionID: sess.ID, RunGen: 2})
	b.Publish(bus.AskUserRequested{SessionID: sess.ID, ID: "a1"})
	b.Publish(bus.AskUserRequested{SessionID: sess.ID, ID: "a2"})

	rc.waitForCallbacks(t, 2)
	b.Drain(2 * time.Second)
	if n := rc.count(); n != 2 {
		t.Fatalf("needs_input delivered %d times, want exactly 2 (one per run)", n)
	}
	for i, cbk := range rc.all() {
		if cbk.Status != callbackStatusNeedsInput {
			t.Errorf("callback %d status = %q, want needs_input", i, cbk.Status)
		}
	}
}

func TestCallbackSignatureIsVerifiable(t *testing.T) {
	rc := newCallbackReceiver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("signed")))
	const secret = "s3cret"
	sess := newCallbackSession(t, mgr, rc.srv.URL, secret)

	if _, _, _, err := mgr.Send(sess.ID, "do it", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	rc.waitForCallbacks(t, 1)
	rc.mu.Lock()
	body, sig := rc.bodies[0], rc.headers[0].Get("X-Moa-Signature")
	rc.mu.Unlock()

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if sig != want {
		t.Fatalf("signature = %q, want %q", sig, want)
	}
	if hmac.Equal([]byte(sig), []byte(callbackSignature("other", body))) {
		t.Error("signature verifies under the wrong secret")
	}
}

func TestCallbackRetriesOn5xxThenSucceeds(t *testing.T) {
	fastCallbackBackoff(t)
	rc := newCallbackReceiver(t, http.StatusInternalServerError, http.StatusOK)
	payload := AutomationCallback{SessionID: "s1", Status: callbackStatusDone}
	if err := postAutomationCallback(context.Background(), callbackTarget{url: rc.srv.URL}, payload); err != nil {
		t.Fatalf("delivery failed: %v", err)
	}
	if n := rc.count(); n != 2 {
		t.Fatalf("attempts = %d, want 2 (one failure + one success)", n)
	}
}

func TestCallbackGivesUpAfterThreeAttempts(t *testing.T) {
	fastCallbackBackoff(t)
	rc := newCallbackReceiver(t, http.StatusBadGateway)
	err := postAutomationCallback(context.Background(), callbackTarget{url: rc.srv.URL}, AutomationCallback{})
	if err == nil {
		t.Fatal("delivery reported success against a permanently failing receiver")
	}
	if n := rc.count(); n != 3 {
		t.Fatalf("attempts = %d, want 3", n)
	}
}

// A 4xx means the receiver understood and refused: retrying it is noise.
func TestCallbackDoesNotRetryClientError(t *testing.T) {
	fastCallbackBackoff(t)
	rc := newCallbackReceiver(t, http.StatusBadRequest)
	if err := postAutomationCallback(context.Background(), callbackTarget{url: rc.srv.URL}, AutomationCallback{}); err == nil {
		t.Fatal("a 400 was reported as delivered")
	}
	if n := rc.count(); n != 1 {
		t.Fatalf("attempts = %d, want 1", n)
	}
}

func TestCallbackDoesNotFollowRedirects(t *testing.T) {
	fastCallbackBackoff(t)
	final := newCallbackReceiver(t)
	var redirects atomic.Int32
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirects.Add(1)
		http.Redirect(w, r, final.srv.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	err := postAutomationCallback(context.Background(), callbackTarget{url: redirector.URL}, AutomationCallback{})
	if err == nil {
		t.Fatal("a redirect was followed")
	}
	if !errors.Is(err, errCallbackRedirect) {
		t.Errorf("error = %v, want the redirect sentinel", err)
	}
	if n := final.count(); n != 0 {
		t.Fatalf("redirect target received %d deliveries, want 0", n)
	}
	// A refused redirect is permanent: retrying would re-POST the signed payload
	// to the redirector.
	if n := redirects.Load(); n != 1 {
		t.Fatalf("redirector received %d requests, want 1", n)
	}
}

// Shutdown must not leave a delivery goroutine behind: a RunEnded drained while
// the runtime closes spawns a WaitQuiescent waiter, which only gives up because
// Shutdown cancels the session context.
func TestCallbackDoesNotOutliveShutdown(t *testing.T) {
	rc := newCallbackReceiver(t)
	ctx, cancel := context.WithCancel(context.Background()) // still live during Shutdown
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("hi")))
	sess := newCallbackSession(t, mgr, rc.srv.URL, "")

	b := sess.runtime.Bus
	// A background bash job keeps the session non-quiescent, so the "done"
	// waiter is still parked when Shutdown runs.
	b.Publish(bus.BashJobStarted{SessionID: sess.ID, JobID: "b1", Command: "sleep"})
	b.Publish(bus.RunEnded{SessionID: sess.ID, RunGen: 1, FinalText: "finished"})

	mgr.Shutdown()

	if err := sess.infra.sessionCtx.Err(); err == nil {
		t.Fatal("Shutdown left the session context live")
	}
	time.Sleep(100 * time.Millisecond) // let any stray delivery land
	if n := rc.count(); n != 0 {
		t.Fatalf("received %d callbacks after Shutdown, want 0", n)
	}
}

// The delivery log must never carry the callback URL: it can embed credentials
// in its userinfo or query, and *url.Error stringifies the whole URL.
func TestCallbackFailureLogHidesCredentials(t *testing.T) {
	fastCallbackBackoff(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("hi")))

	// A closed server gives a stable "connection refused" from a real dial.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	host := strings.TrimPrefix(dead.URL, "http://")
	dead.Close()
	const (
		user  = "automation-user"
		pass  = "sup3rs3cret"
		token = "qtok3n"
	)
	url := "http://" + user + ":" + pass + "@" + host + "/hooks/moa?token=" + token
	sess := newCallbackSession(t, mgr, url, "")

	var buf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(orig) })

	deliverAutomationCallback(sess, callbackTarget{url: url}, callbackStatusDone, "", "", nil)

	logged := buf.String()
	if !strings.Contains(logged, "automation callback delivery failed") {
		t.Fatalf("delivery failure was not logged: %q", logged)
	}
	for _, secret := range []string{user, pass, token, "/hooks/moa"} {
		if strings.Contains(logged, secret) {
			t.Errorf("log leaked %q: %s", secret, logged)
		}
	}
	if !strings.Contains(logged, host) {
		t.Errorf("log lost the sanitized destination %q: %s", host, logged)
	}
}

// A non-http(s) target is refused at delivery time too, not only at submission.
func TestCallbackNeverDialsNonHTTPURL(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "ftp://example.com/x", "/relative", ""} {
		err := postAutomationCallback(context.Background(), callbackTarget{url: raw}, AutomationCallback{})
		if !errors.Is(err, errAutomationInvalidCallback) {
			t.Errorf("postAutomationCallback(%q) error = %v, want the invalid-callback error", raw, err)
		}
	}
}

// An ordinary session has no callback_url, so nothing is ever delivered — and
// no subscription is even attached.
func TestNoCallbackForOrdinarySession(t *testing.T) {
	rc := newCallbackReceiver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("hello")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(sess.ID, "hi", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 5*time.Second, "run finished", func() bool { return sessState(sess) == StateIdle })
	sess.runtime.Bus.Drain(2 * time.Second)
	if n := rc.count(); n != 0 {
		t.Fatalf("ordinary session delivered %d callbacks", n)
	}
}

// A stored callback_url that is no longer valid disables delivery instead of
// dialing something unexpected.
func TestCallbackSubscriptionSkipsInvalidURL(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("hi")))
	sess := newCallbackSession(t, mgr, "file:///etc/passwd", "")
	before := len(sess.pushUnsubs)
	mgr.subscribeAutomationCallback(sess, map[string]any{session.MetaCallbackURL: "file:///etc/passwd"})
	if len(sess.pushUnsubs) != before {
		t.Error("an invalid callback_url still attached subscriptions")
	}
}

// The callback survives a restart: a resumed session re-reads callback_url from
// its persisted metadata.
func TestCallbackWiredOnResume(t *testing.T) {
	rc := newCallbackReceiver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("resumed reply")))
	sess := newCallbackSession(t, mgr, rc.srv.URL, "")
	id := sess.ID
	if _, _, _, err := mgr.Send(id, "first", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	rc.waitForCallbacks(t, 1)

	// Drop it from memory the way a restart would, then resume from disk.
	mgr.mu.Lock()
	delete(mgr.sessions, id)
	mgr.mu.Unlock()
	sess.runtime.Close()

	resumed, err := mgr.ResumeSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(id, "second", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	all := rc.waitForCallbacks(t, 2)
	if all[1].SessionID != resumed.ID || all[1].Status != callbackStatusDone {
		t.Fatalf("resumed callback = %+v, want done for %s", all[1], resumed.ID)
	}
}

func TestCallbackSummaryFallsBackToTranscript(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("transcript text")))
	sess := newCallbackSession(t, mgr, "", "")
	if _, _, _, err := mgr.Send(sess.ID, "hi", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 5*time.Second, "run finished", func() bool { return sessState(sess) == StateIdle })
	if got := callbackSummary(sess, ""); got != "transcript text" {
		t.Errorf("summary = %q, want the last assistant text", got)
	}
	if got := callbackSummary(sess, "final wins"); got != "final wins" {
		t.Errorf("summary = %q, want the run's final text", got)
	}
}

func TestTruncateSummary(t *testing.T) {
	short := "still short"
	if got := truncateSummary(short); got != short {
		t.Errorf("truncateSummary(short) = %q", got)
	}
	long := strings.Repeat("é", maxCallbackSummaryBytes) // 2 bytes per rune
	got := truncateSummary(long)
	if len(got) > maxCallbackSummaryBytes {
		t.Errorf("truncated length = %d, want <= %d", len(got), maxCallbackSummaryBytes)
	}
	if !strings.HasSuffix(got, "…") {
		t.Error("truncated summary lost its ellipsis marker")
	}
	for _, r := range got {
		if r == '\uFFFD' {
			t.Fatal("truncation split a UTF-8 rune")
		}
	}
}

func TestAssistantTextJoinsTextBlocks(t *testing.T) {
	msg := core.AgentMessage{Message: core.Message{Role: "assistant", Content: []core.Content{
		core.ThinkingContent("ignored"),
		core.TextContent("first"),
		core.TextContent("second"),
	}}}
	if got := assistantText(msg); got != "first\nsecond" {
		t.Errorf("assistantText = %q", got)
	}
}
