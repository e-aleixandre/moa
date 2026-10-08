package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
)

// The runtime tests drive the real buildProvider wrapper, the real auth.Store
// on a temporary auth.json and the real concrete transports. Only the network
// is local: a test HTTP client routes the fixed production origins (and only
// those) to one httptest server, which records what each request carried.

var fixedTestOrigins = map[string]bool{
	"api.openai.com":        true,
	"chatgpt.com":           true,
	"auth.openai.com":       true,
	"api.anthropic.com":     true,
	"console.anthropic.com": true,
}

type upstreamCall struct {
	Origin  string
	Path    string
	Bearer  string
	APIKey  string
	Account string
	Refresh string
}

type fakeUpstream struct {
	srv *httptest.Server

	mu        sync.Mutex
	calls     []upstreamCall // inference requests
	tokens    []upstreamCall // token refresh requests
	inference func(w http.ResponseWriter, c upstreamCall, n int)
	token     func(w http.ResponseWriter, c upstreamCall, n int)
}

func newFakeUpstream(t *testing.T) *fakeUpstream {
	t.Helper()
	u := &fakeUpstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(u.serve))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *fakeUpstream) serve(w http.ResponseWriter, r *http.Request) {
	c := upstreamCall{
		Origin:  r.Header.Get("X-Test-Origin"),
		Path:    r.URL.Path,
		Bearer:  strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "),
		APIKey:  r.Header.Get("X-API-Key"),
		Account: r.Header.Get("chatgpt-account-id"),
	}
	if c.Origin == "auth.openai.com" || c.Origin == "console.anthropic.com" {
		_ = r.ParseForm()
		c.Refresh = r.PostForm.Get("refresh_token")
		u.mu.Lock()
		n := len(u.tokens)
		u.tokens = append(u.tokens, c)
		h := u.token
		u.mu.Unlock()
		if h == nil {
			writeStatus(w, http.StatusBadRequest, `{"error":"invalid_grant"}`)
			return
		}
		h(w, c, n)
		return
	}
	_, _ = io.Copy(io.Discard, r.Body)
	u.mu.Lock()
	n := len(u.calls)
	u.calls = append(u.calls, c)
	h := u.inference
	u.mu.Unlock()
	if h == nil {
		writeOK(w, c)
		return
	}
	h(w, c, n)
}

func (u *fakeUpstream) snapshot() (calls, tokens []upstreamCall) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]upstreamCall(nil), u.calls...), append([]upstreamCall(nil), u.tokens...)
}

// client routes only the fixed upstream origins to the fake server and
// refuses everything else, so a test can never reach a real endpoint.
func (u *fakeUpstream) client() *http.Client {
	target, _ := url.Parse(u.srv.URL)
	return &http.Client{Transport: routeTransport{target: target, base: u.srv.Client().Transport}}
}

type routeTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (rt routeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || !fixedTestOrigins[r.URL.Host] {
		return nil, errors.New("test transport: origin is not a fixed provider origin")
	}
	out := r.Clone(r.Context())
	out.URL.Scheme = rt.target.Scheme
	out.URL.Host = rt.target.Host
	out.Host = rt.target.Host
	out.Header.Set("X-Test-Origin", r.URL.Host)
	return rt.base.RoundTrip(out)
}

func writeStatus(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, body)
}

func writeOK(w http.ResponseWriter, c upstreamCall) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	if c.Origin == "api.anthropic.com" {
		for _, ev := range [][2]string{
			{"message_start", `{"type":"message_start","message":{"id":"m1","model":"claude-sonnet-4-6","usage":{"input_tokens":1,"output_tokens":0}}}`},
			{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`},
			{"content_block_stop", `{"type":"content_block_stop","index":0}`},
			{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`},
			{"message_stop", `{"type":"message_stop"}`},
		} {
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev[0], ev[1])
		}
		return
	}
	for _, data := range []string{
		`{"type":"response.output_item.added","item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":""}],"status":"in_progress"}}`,
		`{"type":"response.output_text.delta","delta":"ok"}`,
		`{"type":"response.output_item.done","item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"ok"}],"status":"completed"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
	} {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	}
}

func writeTokens(w http.ResponseWriter, access, refresh string) {
	writeStatus(w, http.StatusOK, fmt.Sprintf(`{"access_token":%q,"refresh_token":%q,"expires_in":3600}`, access, refresh))
}

// openaiAccess builds a ChatGPT-shaped access JWT for account; marker is its
// signature segment, so the token is recognizable in any output.
func openaiAccess(account, marker string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"https://api.openai.com/auth":{"chatgpt_account_id":%q}}`, account)))
	return header + "." + payload + "." + marker
}

func clearProviderEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "XAI_API_KEY", "META_API_KEY"} {
		t.Setenv(k, "")
	}
}

type runtimeFixture struct {
	u     *fakeUpstream
	path  string
	store *auth.Store // the serving process's store
	other *auth.Store // a separate process (CLI) on the same file
}

func newRuntimeFixture(t *testing.T) *runtimeFixture {
	t.Helper()
	clearProviderEnv(t)
	u := newFakeUpstream(t)
	path := filepath.Join(t.TempDir(), "cfg", "auth.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	return &runtimeFixture{
		u:     u,
		path:  path,
		store: auth.NewStoreWithHTTPClient(path, u.client()),
		other: auth.NewStore(path),
	}
}

func (f *runtimeFixture) commit(t *testing.T, s *auth.Store, provider string, cred auth.Credential) string {
	t.Helper()
	expected, err := s.StoredGeneration(provider)
	if err != nil {
		t.Fatal(err)
	}
	gen, err := s.CommitLogin(provider, expected, cred)
	if err != nil {
		t.Fatalf("CommitLogin(%s): %v", provider, err)
	}
	return gen
}

func (f *runtimeFixture) build(t *testing.T, model core.Model) core.Provider {
	t.Helper()
	built, err := buildProviderWithClient(model, f.store, f.u.client())
	if err != nil {
		t.Fatalf("buildProvider: %v", err)
	}
	return built.Provider
}

var (
	openaiModel    = core.Model{ID: "gpt-5.3-codex", Provider: "openai"}
	anthropicModel = core.Model{ID: "claude-sonnet-4-6", Provider: "anthropic"}
)

func oauthCred(access, refresh, account string) auth.Credential {
	return auth.Credential{Type: "oauth", Access: access, Refresh: refresh, AccountID: account, Expires: time.Now().Add(time.Hour).UnixMilli()}
}

func apiKeyCred(key string) auth.Credential {
	return auth.Credential{Type: "api_key", Key: key}
}

// streamText runs one logical request to completion.
func streamText(ctx context.Context, p core.Provider, model core.Model) (string, error) {
	ch, err := p.Stream(ctx, core.Request{Model: model, Messages: []core.Message{core.NewUserMessage("hi")}})
	if err != nil {
		return "", err
	}
	var text string
	var streamErr error
	for ev := range ch {
		switch ev.Type {
		case core.ProviderEventError:
			streamErr = ev.Error
		case core.ProviderEventDone:
			if ev.Message != nil {
				for _, c := range ev.Message.Content {
					text += c.Text
				}
			}
		}
	}
	return text, streamErr
}

type streamResult struct {
	text string
	err  error
}

func streamAsync(p core.Provider, model core.Model) <-chan streamResult {
	out := make(chan streamResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		text, err := streamText(ctx, p, model)
		out <- streamResult{text, err}
	}()
	return out
}

func waitResult(t *testing.T, ch <-chan streamResult) streamResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(30 * time.Second):
		t.Fatal("request did not finish")
		return streamResult{}
	}
}

func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// heldFirst makes the first inference request signal started and wait for
// release, then answer with respond; later requests answer OK.
func heldFirst(started, release chan struct{}, respond func(http.ResponseWriter, upstreamCall)) func(http.ResponseWriter, upstreamCall, int) {
	return func(w http.ResponseWriter, c upstreamCall, n int) {
		if n == 0 {
			close(started)
			<-release
			respond(w, c)
			return
		}
		writeOK(w, c)
	}
}

func credClass(t *testing.T, err error) *core.ProviderCredentialError {
	t.Helper()
	pe, ok := core.AsProviderCredentialError(err)
	if !ok {
		t.Fatalf("err = %v (%T), want a classified *core.ProviderCredentialError", err, err)
	}
	return pe
}

// R12: a provider built once follows the selection of every new request as a
// whole (transport, endpoint, account, token), while a request already in
// flight completes with the selection it started with.
func TestRuntime_NextRequestAppliesWholeSelection(t *testing.T) {
	accessA := openaiAccess("acct-a", "SIG-A")
	accessB := openaiAccess("acct-b", "SIG-B")
	cases := []struct {
		name          string
		first, second auth.Credential
		wantFirst     upstreamCall
		wantSecond    upstreamCall
		docsAfter     bool
	}{
		{
			name:       "oauth_to_api_key",
			first:      oauthCred(accessA, "refresh-a", "acct-a"),
			second:     apiKeyCred("sk-" + "proj-key-b"),
			wantFirst:  upstreamCall{Origin: "chatgpt.com", Path: "/backend-api/codex/responses", Bearer: accessA, Account: "acct-a"},
			wantSecond: upstreamCall{Origin: "api.openai.com", Path: "/v1/responses", Bearer: "sk-" + "proj-key-b"},
			docsAfter:  true,
		},
		{
			name:       "api_key_to_oauth",
			first:      apiKeyCred("sk-" + "proj-key-a"),
			second:     oauthCred(accessB, "refresh-b", "acct-b"),
			wantFirst:  upstreamCall{Origin: "api.openai.com", Path: "/v1/responses", Bearer: "sk-" + "proj-key-a"},
			wantSecond: upstreamCall{Origin: "chatgpt.com", Path: "/backend-api/codex/responses", Bearer: accessB, Account: "acct-b"},
			docsAfter:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRuntimeFixture(t)
			f.commit(t, f.store, "openai", tc.first)
			p := f.build(t, openaiModel)

			started, release := make(chan struct{}), make(chan struct{})
			f.u.inference = heldFirst(started, release, writeOK)
			first := streamAsync(p, openaiModel)
			waitClosed(t, started, "first request")

			// A separate process (the CLI) commits the new selection.
			f.commit(t, f.other, "openai", tc.second)
			if got := core.ProviderSupportsDocuments(p); got != tc.docsAfter {
				t.Errorf("SupportsDocuments after switch = %v, want %v", got, tc.docsAfter)
			}
			if r := waitResult(t, streamAsync(p, openaiModel)); r.err != nil || r.text != "ok" {
				t.Fatalf("second request = %q, %v", r.text, r.err)
			}
			close(release)
			if r := waitResult(t, first); r.err != nil || r.text != "ok" {
				t.Fatalf("in-flight request = %q, %v; it must complete with its own selection", r.text, r.err)
			}

			calls, tokens := f.u.snapshot()
			if len(calls) != 2 || len(tokens) != 0 {
				t.Fatalf("calls = %+v, token calls = %d", calls, len(tokens))
			}
			if calls[0] != tc.wantFirst {
				t.Errorf("in-flight request = %+v, want %+v", calls[0], tc.wantFirst)
			}
			if calls[1] != tc.wantSecond {
				t.Errorf("next request = %+v, want %+v (mixed selection)", calls[1], tc.wantSecond)
			}
		})
	}
}

// R12: every HTTP retry of one logical request carries the same snapshot, even
// when a new login is committed between attempts.
func TestRuntime_RetriesKeepTheRequestSnapshot(t *testing.T) {
	f := newRuntimeFixture(t)
	accessA := openaiAccess("acct-a", "SIG-A")
	f.commit(t, f.store, "openai", oauthCred(accessA, "refresh-a", "acct-a"))
	p := f.build(t, openaiModel)

	started, proceed := make(chan struct{}), make(chan struct{})
	f.u.inference = func(w http.ResponseWriter, c upstreamCall, n int) {
		if n == 0 {
			close(started)
			<-proceed
			writeStatus(w, http.StatusServiceUnavailable, `{"error":"busy"}`)
			return
		}
		writeOK(w, c)
	}
	res := streamAsync(p, openaiModel)
	waitClosed(t, started, "first attempt")
	f.commit(t, f.other, "openai", apiKeyCred("sk-"+"proj-key-b"))
	close(proceed)
	if r := waitResult(t, res); r.err != nil {
		t.Fatalf("request: %v", r.err)
	}
	calls, _ := f.u.snapshot()
	want := upstreamCall{Origin: "chatgpt.com", Path: "/backend-api/codex/responses", Bearer: accessA, Account: "acct-a"}
	if len(calls) != 2 || calls[1] != want {
		t.Fatalf("attempts = %+v, retry want %+v", calls, want)
	}
}

// R12: the stored type selects the transport; a pasted key is never
// re-detected as a subscription token by its shape.
func TestRuntime_StoredAPIKeyKeepsItsTransport(t *testing.T) {
	t.Run("anthropic", func(t *testing.T) {
		f := newRuntimeFixture(t)
		f.commit(t, f.store, "anthropic", apiKeyCred("sk-"+"ant-oat01-pasted-as-key"))
		if r := waitResult(t, streamAsync(f.build(t, anthropicModel), anthropicModel)); r.err != nil {
			t.Fatal(r.err)
		}
		calls, _ := f.u.snapshot()
		if len(calls) != 1 || calls[0].APIKey != "sk-"+"ant-oat01-pasted-as-key" || calls[0].Bearer != "" {
			t.Fatalf("calls = %+v, want X-API-Key transport", calls)
		}
	})
	t.Run("openai", func(t *testing.T) {
		f := newRuntimeFixture(t)
		jwtShaped := openaiAccess("acct-x", "KEYSIG")
		f.commit(t, f.store, "openai", apiKeyCred(jwtShaped))
		if r := waitResult(t, streamAsync(f.build(t, openaiModel), openaiModel)); r.err != nil {
			t.Fatal(r.err)
		}
		calls, _ := f.u.snapshot()
		if len(calls) != 1 || calls[0].Origin != "api.openai.com" || calls[0].Account != "" {
			t.Fatalf("calls = %+v, want the API-key transport", calls)
		}
	})
}

// R13: a 401 for selection A that arrives after B was committed never
// refreshes or retries with B and is reported as credentials_changed.
func TestRuntime_Late401NeverUsesNewSelection(t *testing.T) {
	accessA := openaiAccess("acct-a", "SIG-A")
	accessB := openaiAccess("acct-b", "SIG-B")
	cases := []struct {
		name          string
		model         core.Model
		provider      string
		first, second auth.Credential
	}{
		{"openai_oauth_to_oauth", openaiModel, "openai", oauthCred(accessA, "refresh-a", "acct-a"), oauthCred(accessB, "refresh-b", "acct-b")},
		{"openai_oauth_to_api_key", openaiModel, "openai", oauthCred(accessA, "refresh-a", "acct-a"), apiKeyCred("sk-" + "proj-key-b")},
		{"openai_api_key_to_oauth", openaiModel, "openai", apiKeyCred("sk-" + "proj-key-a"), oauthCred(accessB, "refresh-b", "acct-b")},
		{"anthropic_oauth_to_oauth", anthropicModel, "anthropic", oauthCred("sk-"+"ant-oat01-a", "refresh-a", ""), oauthCred("sk-"+"ant-oat01-b", "refresh-b", "")},
		{"anthropic_api_key_to_api_key", anthropicModel, "anthropic", apiKeyCred("sk-" + "ant-api03-a"), apiKeyCred("sk-" + "ant-api03-b")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRuntimeFixture(t)
			genA := f.commit(t, f.store, tc.provider, tc.first)
			p := f.build(t, tc.model)

			started, release := make(chan struct{}), make(chan struct{})
			f.u.inference = heldFirst(started, release, func(w http.ResponseWriter, _ upstreamCall) {
				writeStatus(w, http.StatusUnauthorized, `{"error":{"code":"token_expired"}}`)
			})
			res := streamAsync(p, tc.model)
			waitClosed(t, started, "request A")
			f.commit(t, f.other, tc.provider, tc.second)
			before, _ := f.other.Get(tc.provider)
			close(release)
			r := waitResult(t, res)

			calls, tokens := f.u.snapshot()
			if len(tokens) != 0 {
				t.Errorf("late 401 of A caused %d refreshes", len(tokens))
			}
			if len(calls) != 1 {
				t.Errorf("late 401 of A was retried: calls = %+v", calls)
			}
			pe := credClass(t, r.err)
			if pe.Class != core.CredentialChanged || pe.Action != "send_again" || pe.Generation != genA {
				t.Errorf("error = %+v, want credentials_changed for generation %s", pe, genA)
			}
			if after, _ := f.other.Get(tc.provider); after.Access != before.Access || after.Key != before.Key || after.Expires != before.Expires || after.Generation != before.Generation {
				t.Errorf("selection B was modified: %+v -> %+v", before, after)
			}
		})
	}
}

// R13: a rotation already done by a sibling request of the same login is
// reused: one safe retry with the rotated token, no second refresh.
func TestRuntime_SameGenerationSiblingRotationRetriesOnce(t *testing.T) {
	f := newRuntimeFixture(t)
	access1 := openaiAccess("acct-a", "SIG-A1")
	access2 := openaiAccess("acct-a", "SIG-A2")
	f.commit(t, f.store, "openai", oauthCred(access1, "refresh-a1", "acct-a"))
	p := f.build(t, openaiModel)

	f.u.token = func(w http.ResponseWriter, c upstreamCall, _ int) {
		writeTokens(w, access2, "refresh-a2")
	}
	started, release := make(chan struct{}), make(chan struct{})
	f.u.inference = func(w http.ResponseWriter, c upstreamCall, n int) {
		if n == 0 {
			close(started)
			<-release
		}
		if c.Bearer == access1 {
			writeStatus(w, http.StatusUnauthorized, `{}`)
			return
		}
		writeOK(w, c)
	}
	first := streamAsync(p, openaiModel)
	waitClosed(t, started, "first request")
	if r := waitResult(t, streamAsync(p, openaiModel)); r.err != nil {
		t.Fatalf("sibling request: %v", r.err)
	}
	close(release)
	if r := waitResult(t, first); r.err != nil {
		t.Fatalf("first request after sibling rotation: %v", r.err)
	}
	calls, tokens := f.u.snapshot()
	if len(tokens) != 1 {
		t.Errorf("refreshes = %d, want 1", len(tokens))
	}
	if len(calls) != 4 || calls[3].Bearer != access2 || calls[3].Account != "acct-a" {
		t.Errorf("calls = %+v, want the first request retried once with the sibling's rotated token", calls)
	}
}

// R14: an environment OAuth token is never refreshed from, retried with or
// given the account of the stored login.
func TestRuntime_EnvironmentOAuthNeverBorrowsStore(t *testing.T) {
	f := newRuntimeFixture(t)
	f.commit(t, f.store, "openai", oauthCred(openaiAccess("acct-b", "SIG-B"), "refresh-b", "acct-b"))
	envToken := openaiAccess("acct-env", "SIG-ENV")
	t.Setenv("OPENAI_API_KEY", envToken)
	p := f.build(t, openaiModel)
	f.u.inference = func(w http.ResponseWriter, _ upstreamCall, _ int) {
		writeStatus(w, http.StatusUnauthorized, `{}`)
	}
	r := waitResult(t, streamAsync(p, openaiModel))
	calls, tokens := f.u.snapshot()
	if len(tokens) != 0 || len(calls) != 1 || calls[0].Bearer != envToken || calls[0].Account != "acct-env" {
		t.Fatalf("calls = %+v, refreshes = %d", calls, len(tokens))
	}
	pe := credClass(t, r.err)
	if pe.Source != core.CredentialSourceEnv || pe.Class != core.CredentialReconnect || pe.Action != "manage_environment" {
		t.Fatalf("error = %+v, want env reconnect with manage_environment", pe)
	}
}

// R15: credential failures are classified through the real wrapper, store and
// transports, for both the date refresh and the reactive 401 paths.
func TestRuntime_CredentialFailuresAreClassified(t *testing.T) {
	accessA := openaiAccess("acct-a", "SIG-A")
	accessA2 := openaiAccess("acct-a", "SIG-A2")
	reject := func(w http.ResponseWriter, c upstreamCall, _ int) {
		writeStatus(w, http.StatusUnauthorized, `{}`)
	}
	rejectA := func(w http.ResponseWriter, c upstreamCall, _ int) {
		if c.Bearer == accessA {
			writeStatus(w, http.StatusUnauthorized, `{}`)
			return
		}
		writeOK(w, c)
	}
	tokenStatus := func(code int, body string) func(http.ResponseWriter, upstreamCall, int) {
		return func(w http.ResponseWriter, _ upstreamCall, _ int) { writeStatus(w, code, body) }
	}
	rotate := func(w http.ResponseWriter, _ upstreamCall, _ int) { writeTokens(w, accessA2, "refresh-a2") }

	cases := []struct {
		name       string
		model      core.Model
		provider   string
		cred       auth.Credential
		inference  func(http.ResponseWriter, upstreamCall, int)
		token      func(http.ResponseWriter, upstreamCall, int)
		setup      func(t *testing.T, f *runtimeFixture)
		wantClass  string
		wantAction string
		wantCalls  int
		wantTokens int
		quota      bool
	}{
		{name: "reactive_invalid_grant", model: openaiModel, provider: "openai", cred: oauthCred(accessA, "refresh-a", "acct-a"),
			inference: rejectA, token: tokenStatus(400, `{"error":"invalid_grant"}`), wantClass: core.CredentialReconnect, wantAction: "reconnect", wantCalls: 1, wantTokens: 1},
		{name: "still_rejected_after_refresh", model: openaiModel, provider: "openai", cred: oauthCred(accessA, "refresh-a", "acct-a"),
			inference: reject, token: rotate, wantClass: core.CredentialReconnect, wantAction: "reconnect", wantCalls: 2, wantTokens: 1},
		{name: "reactive_refresh_unavailable", model: openaiModel, provider: "openai", cred: oauthCred(accessA, "refresh-a", "acct-a"),
			inference: rejectA, token: tokenStatus(503, `oops`), wantClass: core.CredentialTemporary, wantAction: "retry", wantCalls: 1, wantTokens: 1},
		{name: "reactive_refresh_rate_limited", model: openaiModel, provider: "openai", cred: oauthCred(accessA, "refresh-a", "acct-a"),
			inference: rejectA, token: tokenStatus(429, `slow`), wantClass: core.CredentialTemporary, wantAction: "retry", wantCalls: 1, wantTokens: 1},
		{name: "date_refresh_invalid_grant", model: openaiModel, provider: "openai",
			cred:  auth.Credential{Type: "oauth", Access: accessA, Refresh: "refresh-a", AccountID: "acct-a", Expires: time.Now().Add(-time.Hour).UnixMilli()},
			token: tokenStatus(400, `{"error":"invalid_grant"}`), wantClass: core.CredentialReconnect, wantAction: "reconnect", wantCalls: 0, wantTokens: 1},
		{name: "api_key_rejected", model: openaiModel, provider: "openai", cred: apiKeyCred("sk-" + "proj-key"),
			inference: reject, wantClass: core.CredentialKeyRejected, wantAction: "replace_key", wantCalls: 1},
		{name: "anthropic_oauth_rejected", model: anthropicModel, provider: "anthropic", cred: oauthCred("sk-"+"ant-oat01-a", "refresh-a", ""),
			inference: reject, token: rotate, wantClass: core.CredentialReconnect, wantAction: "reconnect", wantCalls: 1},
		{name: "entitlement_denied", model: openaiModel, provider: "openai", cred: apiKeyCred("sk-" + "proj-key"),
			inference: func(w http.ResponseWriter, _ upstreamCall, _ int) {
				writeStatus(w, 403, `{"error":{"message":"no access"}}`)
			},
			wantClass: core.CredentialPermissions, wantAction: "check_plan", wantCalls: 1},
		{name: "usage_quota", model: openaiModel, provider: "openai", cred: oauthCred(accessA, "refresh-a", "acct-a"),
			inference: func(w http.ResponseWriter, _ upstreamCall, _ int) {
				writeStatus(w, 429, `{"error":{"type":"usage_limit_reached","message":"limit","resets_in_seconds":60}}`)
			},
			quota: true, wantCalls: 1},
		{name: "store_corrupt", model: openaiModel, provider: "openai", cred: apiKeyCred("sk-" + "proj-key"),
			setup: func(t *testing.T, f *runtimeFixture) {
				if err := os.WriteFile(f.path, []byte("{truncated"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantClass: core.CredentialStoreUnavailable, wantAction: "repair_store"},
		{name: "reactive_save_failure", model: openaiModel, provider: "openai", cred: oauthCred(accessA, "refresh-a", "acct-a"),
			inference: rejectA, token: rotate,
			setup: func(t *testing.T, f *runtimeFixture) {
				if os.Geteuid() == 0 {
					t.Skip("directory permissions do not bind root")
				}
				dir := filepath.Dir(f.path)
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
			},
			wantClass: core.CredentialPersistenceFailed, wantAction: "retry_save", wantCalls: 1, wantTokens: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRuntimeFixture(t)
			f.commit(t, f.store, tc.provider, tc.cred)
			p := f.build(t, tc.model)
			f.u.inference, f.u.token = tc.inference, tc.token
			if tc.setup != nil {
				tc.setup(t, f)
			}
			r := waitResult(t, streamAsync(p, tc.model))
			calls, tokens := f.u.snapshot()
			if len(calls) != tc.wantCalls || len(tokens) != tc.wantTokens {
				t.Errorf("inference calls = %d (want %d), refreshes = %d (want %d)", len(calls), tc.wantCalls, len(tokens), tc.wantTokens)
			}
			if tc.quota {
				if _, ok := core.AsQuotaExceeded(r.err); !ok {
					t.Fatalf("err = %v, want the typed quota error", r.err)
				}
				if _, ok := core.AsProviderCredentialError(r.err); ok {
					t.Fatalf("quota turned into a credential class: %v", r.err)
				}
				return
			}
			pe := credClass(t, r.err)
			if pe.Class != tc.wantClass || pe.Action != tc.wantAction || pe.Provider != tc.provider {
				t.Fatalf("error = %+v, want class %s action %s", pe, tc.wantClass, tc.wantAction)
			}
		})
	}
}

type weeklyRuntimeTransport struct {
	mu        sync.Mutex
	headers   http.Header
	bearers   []string
	generic   bool
	refreshes int
}

func (tr *weeklyRuntimeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.String() != "https://api.anthropic.com/v1/messages" {
		tr.mu.Lock()
		tr.refreshes++
		tr.mu.Unlock()
		return nil, errors.New("test refuses auth, refresh, polling and network")
	}
	_, _ = io.Copy(io.Discard, r.Body)
	_ = r.Body.Close()
	tr.mu.Lock()
	n := len(tr.bearers)
	tr.bearers = append(tr.bearers, r.Header.Get("Authorization"))
	tr.mu.Unlock()
	code, body, h := 429, `{"error":{"type":"rate_limit_error","message":"generic"}}`, tr.headers.Clone()
	if tr.generic {
		h = http.Header{"Retry-After": []string{"3600"}}
	}
	if n > 0 {
		code = 200
		body = "event: message_start\ndata: {\"message\":{\"id\":\"fake\",\"model\":\"claude-opus-5-5\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {}\n\n"
	}
	return &http.Response{StatusCode: code, Header: h, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func TestWeeklyRuntimeNormalStoreResolvesNextAttemptAfterHours(t *testing.T) {
	clearProviderEnv(t)
	raw, err := os.ReadFile("../../pkg/provider/anthropic/testdata/weekly-oauth-capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Headers http.Header `json:"rate_limit_headers"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		tr := &weeklyRuntimeTransport{headers: fixture.Headers}
		client := &http.Client{Transport: tr}
		path := filepath.Join(t.TempDir(), "auth.json")
		store := auth.NewStoreWithHTTPClient(path, client)
		other := auth.NewStoreWithHTTPClient(path, client)
		first := auth.Credential{Type: "oauth", Access: "offline-weekly-token-one", Refresh: "offline-refresh-unused", Expires: time.Now().Add(72 * time.Hour).UnixMilli()}
		if _, err := store.CommitLogin("anthropic", "", first); err != nil {
			t.Fatal(err)
		}
		model, _ := core.ResolveModel("opus")
		p := &snapshotProvider{model: model, authStore: store, httpClient: client}
		req := core.Request{Model: model, Messages: []core.Message{core.NewUserMessage("offline")}, Options: core.StreamOptions{ThinkingLevel: "high"}}
		ch, err := p.Stream(context.Background(), req)
		quota, ok := core.AsQuotaExceeded(err)
		if !ok || ch != nil || quota.Wait == nil || quota.Wait.Scope != "seven_day" {
			t.Fatalf("wrapper lost classification: %v/%v", ch, err)
		}
		if _, ok := core.AsProviderCredentialError(err); ok {
			t.Fatal("quota classified as auth")
		}
		if strings.Contains(err.Error(), first.Access) {
			t.Fatal("credential in quota error")
		}
		time.Sleep(18 * time.Hour)
		gen, err := other.StoredGeneration("anthropic")
		if err != nil {
			t.Fatal(err)
		}
		second := first
		second.Access = "offline-weekly-token-two"
		second.Expires = time.Now().Add(72 * time.Hour).UnixMilli()
		if _, err := other.CommitLogin("anthropic", gen, second); err != nil {
			t.Fatal(err)
		}
		ch, err = p.Stream(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		for range ch {
		}
		tr.mu.Lock()
		defer tr.mu.Unlock()
		if len(tr.bearers) != 2 || tr.bearers[0] != "Bearer "+first.Access || tr.bearers[1] != "Bearer "+second.Access || tr.refreshes != 0 {
			t.Fatalf("normal Store binding=%v refreshes=%d", tr.bearers, tr.refreshes)
		}
	})
}

func TestWeeklyRuntimeRetryControlUnwrapIsNotAuthentication(t *testing.T) {
	clearProviderEnv(t)
	synctest.Test(t, func(t *testing.T) {
		tr := &weeklyRuntimeTransport{generic: true}
		client := &http.Client{Transport: tr}
		store := auth.NewStoreWithHTTPClient(filepath.Join(t.TempDir(), "auth.json"), client)
		if _, err := store.CommitLogin("anthropic", "", auth.Credential{Type: "oauth", Access: "offline-control-token", Expires: time.Now().Add(time.Hour).UnixMilli()}); err != nil {
			t.Fatal(err)
		}
		model, _ := core.ResolveModel("opus")
		p := &snapshotProvider{model: model, authStore: store, httpClient: client}
		start := time.Now()
		_, err := p.Stream(context.Background(), core.Request{Model: model, Messages: []core.Message{core.NewUserMessage("offline")}, Options: core.StreamOptions{OnProviderRetry: func(ctx context.Context, w core.ProviderWait) error {
			return &core.ProviderRetryReady{Attempt: w.Attempt, Wait: &w}
		}}})
		var ready *core.ProviderRetryReady
		if !errors.As(err, &ready) || ready.Wait == nil || ready.Wait.Kind != "transport_retry" || ready.Attempt != 1 {
			t.Fatalf("control lost=%v", err)
		}
		if _, ok := core.AsProviderCredentialError(err); ok {
			t.Fatal("control classified as auth")
		}
		if time.Since(start) != 0 {
			t.Fatal("wrapper retained credentials during retry sleep")
		}
		tr.mu.Lock()
		defer tr.mu.Unlock()
		if len(tr.bearers) != 1 || tr.refreshes != 0 {
			t.Fatal("control performed another request")
		}
	})
}
