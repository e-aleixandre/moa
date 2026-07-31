package serve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/session"
)

const testAutomationToken = "automation-secret"

// newAutomationTestServer builds a Serve instance with the Automation API
// enabled (or disabled when token is empty).
func newAutomationTestServer(t *testing.T, token string) (*httptest.Server, *Manager) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	prov := newMockProvider(simpleResponseHandler("test reply"))
	mgr := newTestManagerWithRoot(t, ctx, prov, "/tmp")
	srv := httptest.NewServer(NewServer(mgr, WithAutomationToken(token)))
	t.Cleanup(srv.Close)
	return srv, mgr
}

// automationReq posts to the Automation API. An empty bearer omits the
// Authorization header; csrf adds the browser CSRF header.
func automationReq(t *testing.T, srv *httptest.Server, path, bearer, body string, csrf bool) *http.Response {
	t.Helper()
	req, err := http.NewRequest("POST", srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if csrf {
		req.Header.Set("X-Moa-Request", "1")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func decodeRun(t *testing.T, resp *http.Response) AutomationRunResponse {
	t.Helper()
	var out AutomationRunResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return out
}

func TestAutomationAuth(t *testing.T) {
	tests := []struct {
		name       string
		token      string // configured on the server
		bearer     string
		csrfHeader bool
		wantStatus int
	}{
		// No CSRF header: a bearer request is exempt (a cross-site form cannot
		// set Authorization), which is the point of the separate surface.
		{"happy path without CSRF header", testAutomationToken, testAutomationToken, false, http.StatusCreated},
		{"CSRF header is also accepted", testAutomationToken, testAutomationToken, true, http.StatusCreated},
		{"disabled without configured token", "", testAutomationToken, false, http.StatusNotFound},
		{"disabled route ignores missing bearer too", "", "", false, http.StatusNotFound},
		{"missing bearer", testAutomationToken, "", false, http.StatusUnauthorized},
		{"wrong bearer", testAutomationToken, "nope", false, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := newAutomationTestServer(t, tt.token)
			resp := automationReq(t, srv, "/api/automation/runs", tt.bearer, `{"prompt":"hello"}`, tt.csrfHeader)
			defer resp.Body.Close() //nolint:errcheck
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
		})
	}
}

// The Automation API is not reachable through the browser surface: a token
// cookie must not open it, and the automation token must not open the rest.
func TestAutomationTokenIsScopedToAutomationRoutes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prov := newMockProvider(simpleResponseHandler("test reply"))
	mgr := newTestManagerWithRoot(t, ctx, prov, "/tmp")
	srv := httptest.NewServer(NewServer(mgr,
		WithAuthToken("browser-secret", false),
		WithAutomationToken(testAutomationToken)))
	defer srv.Close()

	// Automation bearer on a regular route → rejected by browser auth.
	req, err := http.NewRequest("GET", srv.URL+"/api/sessions", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testAutomationToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("automation bearer on /api/sessions: status = %d, want 401", resp.StatusCode)
	}

	// Browser cookie on the automation route → still needs the bearer.
	req2, err := http.NewRequest("POST", srv.URL+"/api/automation/runs", strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	req2.Header.Set("X-Moa-Request", "1")
	req2.AddCookie(&http.Cookie{Name: authCookieName, Value: "browser-secret"})
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close() //nolint:errcheck
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("browser cookie on automation route: status = %d, want 401", resp2.StatusCode)
	}
}

// Bearer auth skips CSRF but not the anti DNS-rebinding Host check.
func TestAutomationHostCheckStillApplies(t *testing.T) {
	srv, _ := newAutomationTestServer(t, testAutomationToken)
	req, err := http.NewRequest("POST", srv.URL+"/api/automation/runs", strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "evil.example.com"
	req.Header.Set("Authorization", "Bearer "+testAutomationToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestAutomationRunCreatesSessionAndSendsPrompt(t *testing.T) {
	srv, mgr := newAutomationTestServer(t, testAutomationToken)
	body := `{"prompt":"ship it","title":"deploy","origin":"linear-webhook","callback_url":"https://hooks.example.com/moa"}`
	resp := automationReq(t, srv, "/api/automation/runs", testAutomationToken, body, false)
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	run := decodeRun(t, resp)
	if run.SessionID == "" || !run.Created {
		t.Fatalf("unexpected response: %#v", run)
	}
	if run.URL != "/?session="+run.SessionID {
		t.Fatalf("url = %q, want a relative session path", run.URL)
	}

	sess, ok := mgr.Get(run.SessionID)
	if !ok {
		t.Fatal("session not registered in the manager")
	}
	if sess.Origin != "linear-webhook" {
		t.Errorf("origin = %q, want linear-webhook", sess.Origin)
	}

	// The prompt reached the agent: the run completes and the conversation
	// holds the user message.
	pollUntil(t, 5*time.Second, "automation run finished", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sessState(sess) == StateIdle
	})
	var sawPrompt bool
	for _, msg := range sess.History() {
		if msg.Role != "user" {
			continue
		}
		for _, c := range msg.Content {
			if strings.Contains(c.Text, "ship it") {
				sawPrompt = true
			}
		}
	}
	if !sawPrompt {
		t.Error("prompt was not sent to the session")
	}

	saved, _, err := session.FindSession(mgr.sessionBaseDir, run.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := saved.Metadata[session.MetaCallbackURL]; got != "https://hooks.example.com/moa" {
		t.Errorf("callback_url metadata = %v, want the request value", got)
	}
	if saved.Origin() != "linear-webhook" {
		t.Errorf("persisted origin = %q, want linear-webhook", saved.Origin())
	}
}

func TestAutomationRunDefaultsOriginToAutomation(t *testing.T) {
	srv, mgr := newAutomationTestServer(t, testAutomationToken)
	resp := automationReq(t, srv, "/api/automation/runs", testAutomationToken, `{"prompt":"hi"}`, false)
	defer resp.Body.Close() //nolint:errcheck
	run := decodeRun(t, resp)
	sess, ok := mgr.Get(run.SessionID)
	if !ok {
		t.Fatal("session not found")
	}
	if sess.Origin != automationOriginDefault {
		t.Errorf("origin = %q, want %q", sess.Origin, automationOriginDefault)
	}
}

func TestAutomationRunValidation(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	tests := []struct {
		name string
		body string
	}{
		{"missing prompt", `{"title":"x"}`},
		{"blank prompt", `{"prompt":"   "}`},
		{"invalid JSON", `{`},
		{"callback scheme file", `{"prompt":"hi","callback_url":"file:///etc/passwd"}`},
		{"callback scheme ftp", `{"prompt":"hi","callback_url":"ftp://example.com/x"}`},
		{"callback relative", `{"prompt":"hi","callback_url":"/relative"}`},
		{"callback without host", `{"prompt":"hi","callback_url":"http://"}`},
		{"callback secret without url", `{"prompt":"hi","callback_secret":"s3cret"}`},
		{"title too long", `{"prompt":"hi","title":"` + long(maxAutomationTitleBytes+1) + `"}`},
		{"origin too long", `{"prompt":"hi","origin":"` + long(maxAutomationOriginBytes+1) + `"}`},
		{"idempotency key too long", `{"prompt":"hi","idempotency_key":"` + long(maxAutomationIdempotencyKeyBytes+1) + `"}`},
		{"callback url too long", `{"prompt":"hi","callback_url":"https://example.com/` + long(maxAutomationCallbackURLBytes) + `"}`},
		{"callback secret too long", `{"prompt":"hi","callback_url":"https://example.com/x","callback_secret":"` + long(maxAutomationCallbackSecretBytes+1) + `"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := newAutomationTestServer(t, testAutomationToken)
			resp := automationReq(t, srv, "/api/automation/runs", testAutomationToken, tt.body, false)
			defer resp.Body.Close() //nolint:errcheck
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
}

func TestAutomationRunIdempotency(t *testing.T) {
	srv, mgr := newAutomationTestServer(t, testAutomationToken)
	body := `{"prompt":"do the thing","idempotency_key":"issue-42"}`

	first := automationReq(t, srv, "/api/automation/runs", testAutomationToken, body, false)
	defer first.Body.Close() //nolint:errcheck
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first status = %d, want 201", first.StatusCode)
	}
	firstRun := decodeRun(t, first)

	second := automationReq(t, srv, "/api/automation/runs", testAutomationToken, body, false)
	defer second.Body.Close() //nolint:errcheck
	if second.StatusCode != http.StatusOK {
		t.Fatalf("retry status = %d, want 200", second.StatusCode)
	}
	secondRun := decodeRun(t, second)
	if secondRun.SessionID != firstRun.SessionID {
		t.Fatalf("retry created a new session %q (first %q)", secondRun.SessionID, firstRun.SessionID)
	}
	if secondRun.Created {
		t.Error("retry reported created=true")
	}

	// A different key still creates its own session.
	other := automationReq(t, srv, "/api/automation/runs", testAutomationToken, `{"prompt":"other","idempotency_key":"issue-43"}`, false)
	defer other.Body.Close() //nolint:errcheck
	if decodeRun(t, other).SessionID == firstRun.SessionID {
		t.Error("distinct idempotency key reused the session")
	}

	if _, ok := mgr.automation.lookup("issue-42"); !ok {
		t.Error("idempotency key missing from the index")
	}
}

func TestAutomationIdempotencyIsConcurrencySafe(t *testing.T) {
	srv, _ := newAutomationTestServer(t, testAutomationToken)
	const n = 4
	ids := make([]string, n)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp := automationReq(t, srv, "/api/automation/runs", testAutomationToken,
				`{"prompt":"retry storm","idempotency_key":"webhook-1"}`, false)
			defer resp.Body.Close() //nolint:errcheck
			var out AutomationRunResponse
			_ = json.NewDecoder(resp.Body).Decode(&out)
			ids[i] = out.SessionID
		}(i)
	}
	wg.Wait()
	for _, id := range ids {
		if id == "" || id != ids[0] {
			t.Fatalf("concurrent retries produced different sessions: %v", ids)
		}
	}
}

// The index is rebuilt from persisted metadata, so a restart still deduplicates.
func TestAutomationIndexRebuiltFromMetadata(t *testing.T) {
	idx := &automationIndex{keys: make(map[string]string)}
	idx.load([]session.Summary{
		{ID: "s1", Metadata: map[string]any{session.MetaIdempotencyKey: "k1"}},
		{ID: "s2", Metadata: map[string]any{"model": "m"}},
	})
	if id, ok := idx.lookup("k1"); !ok || id != "s1" {
		t.Fatalf("lookup(k1) = %q, %v; want s1, true", id, ok)
	}
	if _, ok := idx.lookup(""); ok {
		t.Error("empty key should never resolve")
	}
	idx.forget("s1")
	if _, ok := idx.lookup("k1"); ok {
		t.Error("forget did not drop the key of a deleted session")
	}
}

func TestBearerTokenEqual(t *testing.T) {
	tests := []struct {
		name   string
		header string
		token  string
		want   bool
	}{
		{"exact", "Bearer secret", "secret", true},
		{"scheme is case-insensitive", "bearer secret", "secret", true},
		{"wrong token", "Bearer other", "secret", false},
		{"wrong scheme", "Moa-Device secret", "secret", false},
		{"no scheme", "secret", "secret", false},
		{"empty header", "", "secret", false},
		{"empty value", "Bearer ", "secret", false},
		{"empty configured token", "Bearer secret", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bearerTokenEqual(tt.header, tt.token); got != tt.want {
				t.Errorf("bearerTokenEqual(%q, %q) = %v, want %v", tt.header, tt.token, got, tt.want)
			}
		})
	}
}

// A failed first Send commits nothing: the session may survive, but it carries
// no idempotency key (neither on disk nor in the index), so the caller's retry
// starts a clean run in a fresh session — and a restart rebuilding the index
// from disk cannot resolve the key to the promptless one either.
func TestAutomationRunCommitsNoKeyWhenSendFails(t *testing.T) {
	srv, mgr := newAutomationTestServer(t, testAutomationToken)
	var failedID string
	orig := sendFirstPrompt
	sendFirstPrompt = func(m *Manager, sessionID, prompt string) error {
		failedID = sessionID
		return errors.New("boom")
	}
	t.Cleanup(func() { sendFirstPrompt = orig })

	body := `{"prompt":"do it","idempotency_key":"issue-99"}`
	resp := automationReq(t, srv, "/api/automation/runs", testAutomationToken, body, false)
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	if failedID == "" {
		t.Fatal("send was never attempted")
	}
	// The session is allowed to remain (nothing is rolled back), but it must be
	// keyless: no metadata key, no index entry.
	if saved, _, err := session.FindSession(mgr.sessionBaseDir, failedID); err == nil {
		if key, _ := saved.Metadata[session.MetaIdempotencyKey].(string); key != "" {
			t.Errorf("promptless session carries idempotency key %q", key)
		}
	}
	if _, ok := mgr.automation.lookup("issue-99"); ok {
		t.Error("idempotency key indexed for a run that never got its prompt")
	}

	// The retry works and produces a different, real session.
	sendFirstPrompt = orig
	retry := automationReq(t, srv, "/api/automation/runs", testAutomationToken, body, false)
	defer retry.Body.Close() //nolint:errcheck
	if retry.StatusCode != http.StatusCreated {
		t.Fatalf("retry status = %d, want 201", retry.StatusCode)
	}
	run := decodeRun(t, retry)
	if run.SessionID == failedID || !run.Created {
		t.Fatalf("retry did not create a fresh session: %#v", run)
	}
}

// The key is written after the send, and the persistence reactor rebuilds
// Metadata from scratch on every snapshot, so it must survive a save cycle the
// same way origin does — otherwise deduplication would silently stop working
// after the first turn.
func TestAutomationKeySurvivesSnapshot(t *testing.T) {
	srv, mgr := newAutomationTestServer(t, testAutomationToken)
	resp := automationReq(t, srv, "/api/automation/runs", testAutomationToken,
		`{"prompt":"hello","idempotency_key":"durable-1"}`, false)
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	run := decodeRun(t, resp)

	// Right after the call the key is already on disk.
	saved, _, err := session.FindSession(mgr.sessionBaseDir, run.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if key, _ := saved.Metadata[session.MetaIdempotencyKey].(string); key != "durable-1" {
		t.Fatalf("persisted key = %q, want durable-1", key)
	}

	// And it is still there once the run's snapshot lands.
	pollUntil(t, 5*time.Second, "idempotency key survives the snapshot", func() bool {
		saved, _, err := session.FindSession(mgr.sessionBaseDir, run.SessionID)
		if err != nil || len(saved.Entries) == 0 {
			return false
		}
		key, _ := saved.Metadata[session.MetaIdempotencyKey].(string)
		return key == "durable-1"
	})

	// A rebuilt index (as after a restart) resolves the key to the same session.
	idx := newAutomationIndex(mgr.sessionBaseDir)
	if id, ok := idx.lookup("durable-1"); !ok || id != run.SessionID {
		t.Fatalf("rebuilt index lookup = %q, %v; want %q, true", id, ok, run.SessionID)
	}
}

// Fail closed: while the index could not be rebuilt from disk, a keyed request
// is refused as retryable instead of silently losing deduplication. Unkeyed
// requests are unaffected, and a later successful rebuild unblocks keys.
func TestAutomationIndexRebuildFailureFailsClosed(t *testing.T) {
	srv, mgr := newAutomationTestServer(t, testAutomationToken)
	// A regular file is not a session base dir, so every rebuild errors.
	broken := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(broken, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	idx := newAutomationIndex(broken)
	if idx.ready {
		t.Fatal("index reported ready despite a failing rebuild")
	}
	mgr.automation = idx

	keyed := automationReq(t, srv, "/api/automation/runs", testAutomationToken,
		`{"prompt":"hi","idempotency_key":"k1"}`, false)
	defer keyed.Body.Close() //nolint:errcheck
	if keyed.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("keyed status = %d, want 503", keyed.StatusCode)
	}

	unkeyed := automationReq(t, srv, "/api/automation/runs", testAutomationToken, `{"prompt":"hi"}`, false)
	defer unkeyed.Body.Close() //nolint:errcheck
	if unkeyed.StatusCode != http.StatusCreated {
		t.Fatalf("unkeyed status = %d, want 201", unkeyed.StatusCode)
	}

	// Point the index at a readable directory: the next keyed request retries
	// the rebuild lazily and succeeds.
	idx.baseDir = mgr.sessionBaseDir
	retried := automationReq(t, srv, "/api/automation/runs", testAutomationToken,
		`{"prompt":"hi","idempotency_key":"k1"}`, false)
	defer retried.Body.Close() //nolint:errcheck
	if retried.StatusCode != http.StatusCreated {
		t.Fatalf("keyed status after recovery = %d, want 201", retried.StatusCode)
	}
	if _, ok := idx.lookup("k1"); !ok {
		t.Error("key missing from the recovered index")
	}
}

// A partial ListAll (some project stores unreadable) keeps the keys it could
// read, but does not mark the index ready.
func TestAutomationIndexRebuildKeepsPartialResults(t *testing.T) {
	base := t.TempDir()
	good := filepath.Join(base, "good")
	if err := os.MkdirAll(good, 0o755); err != nil {
		t.Fatal(err)
	}
	store := &automationIndex{baseDir: base, keys: map[string]string{}}
	store.load([]session.Summary{{ID: "s1", Metadata: map[string]any{session.MetaIdempotencyKey: "k1"}}})
	if err := os.Mkdir(filepath.Join(base, "unreadable"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(base, "unreadable"), 0o755) })
	if err := store.rebuild(); err == nil {
		t.Fatal("rebuild over an unreadable store returned no error")
	}
	if _, ok := store.lookup("k1"); !ok {
		t.Error("partial rebuild dropped previously known keys")
	}
	if store.ready {
		t.Error("index marked ready after a partial rebuild")
	}
}

// Delete and CreateAutomationRun must not interleave: a key registered by a run
// cannot be forgotten by a delete that started before the run finished.
func TestAutomationDeleteDoesNotRaceRunCreation(t *testing.T) {
	srv, mgr := newAutomationTestServer(t, testAutomationToken)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		key := "race-" + strconv.Itoa(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := automationReq(t, srv, "/api/automation/runs", testAutomationToken,
				`{"prompt":"race","idempotency_key":"`+key+`"}`, false)
			defer resp.Body.Close() //nolint:errcheck
			var out AutomationRunResponse
			_ = json.NewDecoder(resp.Body).Decode(&out)
			if out.SessionID != "" {
				_ = mgr.Delete(out.SessionID)
			}
		}()
	}
	wg.Wait()
	for i := 0; i < 4; i++ {
		if id, ok := mgr.automation.lookup("race-" + strconv.Itoa(i)); ok {
			t.Errorf("key race-%d still maps to deleted session %q", i, id)
		}
	}
}
