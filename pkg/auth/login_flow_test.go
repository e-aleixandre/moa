package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// Distinct sentinels: any of them on an output surface is a leak.
const (
	codeSentinel    = "CODE-SENTINEL-41c7"
	accessSentinel  = "sk-" + "ant-oat01-ACCESS-SENTINEL-9e2d"
	refreshSentinel = "REFRESH-SENTINEL-55af"
	bodySentinel    = "UPSTREAM-BODY-SENTINEL-0b13"
)

// codeTokenServer is an httptest authorization-code token endpoint. It
// records every request body (JSON for Anthropic, form for OpenAI).
type codeTokenServer struct {
	*httptest.Server
	mu    sync.Mutex
	calls int
	reqs  []map[string]string
	// account, when set, issues OpenAI JWT access tokens for that account.
	account string
	// started receives one value per request before gate is consulted.
	started chan struct{}
	// gate, when set, blocks each request until the test closes or feeds it.
	gate chan struct{}
	// respond, when set, replaces the success response.
	respond func(w http.ResponseWriter, r *http.Request)
}

func newCodeTokenServer(t *testing.T) *codeTokenServer {
	t.Helper()
	ts := &codeTokenServer{started: make(chan struct{}, 16)}
	ts.Server = httptest.NewServer(http.HandlerFunc(ts.handle))
	t.Cleanup(ts.Close)
	return ts
}

func (ts *codeTokenServer) handle(w http.ResponseWriter, r *http.Request) {
	fields := map[string]string{}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		_ = json.Unmarshal(body, &fields)
	} else if form, err := url.ParseQuery(string(body)); err == nil {
		for k := range form {
			fields[k] = form.Get(k)
		}
	}
	ts.mu.Lock()
	ts.calls++
	ts.reqs = append(ts.reqs, fields)
	n := ts.calls
	gate, respond, account := ts.gate, ts.respond, ts.account
	ts.mu.Unlock()
	ts.started <- struct{}{}
	if gate != nil {
		<-gate
	}
	if respond != nil {
		respond(w, r)
		return
	}
	access := accessSentinel
	if account != "" {
		access = testOpenAIJWT(account, n)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": refreshSentinel, "expires_in": 3600})
}

func (ts *codeTokenServer) callCount() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.calls
}

func (ts *codeTokenServer) lastRequest() map[string]string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if len(ts.reqs) == 0 {
		return nil
	}
	return ts.reqs[len(ts.reqs)-1]
}

func (ts *codeTokenServer) endpoints() tokenEndpoints {
	return tokenEndpoints{client: ts.Client(), anthropic: ts.URL, openai: ts.URL}
}

func attemptState(t *testing.T, authorize string) string {
	t.Helper()
	u, err := url.Parse(authorize)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("state")
}

const (
	anthropicCallback = "https://console.anthropic.com/oauth/code/callback"
	openaiCallback    = "http://localhost:1455/auth/callback"
)

func anthropicPaste(state string) string { return codeSentinel + "#" + state }
func openaiPaste(state string) string {
	return openaiCallback + "?code=" + codeSentinel + "&state=" + url.QueryEscape(state)
}

// noEcho fails if err's text contains any distinctive part of the input.
func noEcho(t *testing.T, err error, parts ...string) {
	t.Helper()
	for _, p := range append(parts, codeSentinel, "evil.example", bodySentinel, accessSentinel, refreshSentinel) {
		if p != "" && strings.Contains(err.Error(), p) {
			t.Fatalf("error echoes %q: %v", p, err)
		}
	}
}

// R03: the pasted value must carry the attempt's state and come from the exact
// destination; anything else is refused before any token request.
func TestCodePaste_StrictFormsBeforeExchange(t *testing.T) {
	type pasteCase struct {
		name string
		in   func(state string) string
		ok   bool
	}
	cases := map[string][]pasteCase{
		"anthropic": {
			{"code#state", anthropicPaste, true},
			{"callback query", func(s string) string { return anthropicCallback + "?code=" + codeSentinel + "&state=" + s }, true},
			{"callback fragment query", func(s string) string { return anthropicCallback + "#code=" + codeSentinel + "&state=" + s }, true},
			{"callback fragment code#state", func(s string) string { return anthropicCallback + "#" + codeSentinel + "#" + s }, true},
			{"surrounding whitespace", func(s string) string { return "  " + anthropicPaste(s) + "\n" }, true},
			{"code only", func(string) string { return codeSentinel }, false},
			{"empty", func(string) string { return "" }, false},
			{"empty code", func(s string) string { return "#" + s }, false},
			{"empty state", func(string) string { return codeSentinel + "#" }, false},
			{"wrong state", func(string) string { return codeSentinel + "#wrong-state" }, false},
			{"extra hash", func(s string) string { return codeSentinel + "#" + s + "#x" }, false},
			{"space inside", func(s string) string { return codeSentinel + " x#" + s }, false},
			{"url without state", func(string) string { return anthropicCallback + "?code=" + codeSentinel }, false},
			{"url duplicate state", func(s string) string {
				return anthropicCallback + "?code=" + codeSentinel + "&state=" + s + "&state=" + s
			}, false},
			{"url duplicate code", func(s string) string { return anthropicCallback + "?code=" + codeSentinel + "&code=other&state=" + s }, false},
			{"url empty code", func(s string) string { return anthropicCallback + "?code=&state=" + s }, false},
			{"wrong host", func(s string) string {
				return "https://evil.example/oauth/code/callback?code=" + codeSentinel + "&state=" + s
			}, false},
			{"lookalike host", func(s string) string {
				return "https://console.anthropic.com.evil.example/oauth/code/callback?code=" + codeSentinel + "&state=" + s
			}, false},
			{"http scheme", func(s string) string {
				return "http://console.anthropic.com/oauth/code/callback?code=" + codeSentinel + "&state=" + s
			}, false},
			{"explicit port", func(s string) string {
				return "https://console.anthropic.com:443/oauth/code/callback?code=" + codeSentinel + "&state=" + s
			}, false},
			{"wrong path", func(s string) string {
				return "https://console.anthropic.com/oauth/code/other?code=" + codeSentinel + "&state=" + s
			}, false},
			{"userinfo", func(s string) string {
				return "https://evil.example@console.anthropic.com/oauth/code/callback?code=" + codeSentinel + "&state=" + s
			}, false},
			{"malformed encoding", func(s string) string { return anthropicCallback + "?code=" + codeSentinel + "%zz&state=" + s }, false},
			{"semicolon separator", func(s string) string { return anthropicCallback + "?code=" + codeSentinel + ";state=" + s }, false},
			{"error with code", func(s string) string {
				return anthropicCallback + "?error=access_denied&code=" + codeSentinel + "&state=" + s
			}, false},
			{"query and fragment", func(s string) string {
				return anthropicCallback + "?code=" + codeSentinel + "&state=" + s + "#code=other&state=" + s
			}, false},
			{"foreign issuer", func(s string) string {
				return anthropicCallback + "?code=" + codeSentinel + "&state=" + s + "&iss=https://evil.example"
			}, false},
		},
		"openai": {
			{"full callback", openaiPaste, true},
			{"pinned issuer", func(s string) string { return openaiPaste(s) + "&iss=https%3A%2F%2Fauth.openai.com" }, true},
			{"harmless extra param", func(s string) string { return openaiPaste(s) + "&scope=openid" }, true},
			{"code#state", func(s string) string { return codeSentinel + "#" + s }, false},
			{"code only", func(string) string { return codeSentinel }, false},
			{"loopback IP", func(s string) string { return strings.Replace(openaiPaste(s), "localhost", "127.0.0.1", 1) }, false},
			{"other port", func(s string) string { return strings.Replace(openaiPaste(s), ":1455", ":1456", 1) }, false},
			{"no port", func(s string) string { return strings.Replace(openaiPaste(s), ":1455", "", 1) }, false},
			{"https scheme", func(s string) string { return strings.Replace(openaiPaste(s), "http:", "https:", 1) }, false},
			{"other path", func(s string) string { return strings.Replace(openaiPaste(s), "/auth/callback", "/auth/other", 1) }, false},
			{"other host", func(s string) string { return strings.Replace(openaiPaste(s), "localhost", "evil.example", 1) }, false},
			{"userinfo", func(s string) string {
				return strings.Replace(openaiPaste(s), "localhost", "evil.example@localhost", 1)
			}, false},
			{"fragment only", func(s string) string { return openaiCallback + "#code=" + codeSentinel + "&state=" + s }, false},
			{"query and fragment", func(s string) string { return openaiPaste(s) + "#code=other" }, false},
			{"missing state", func(string) string { return openaiCallback + "?code=" + codeSentinel }, false},
			{"wrong state", func(string) string { return openaiCallback + "?code=" + codeSentinel + "&state=wrong" }, false},
			{"duplicate state", func(s string) string { return openaiPaste(s) + "&state=" + s }, false},
			{"duplicate code", func(s string) string { return openaiPaste(s) + "&code=other" }, false},
			{"malformed encoding", func(s string) string { return openaiCallback + "?code=" + codeSentinel + "%zz&state=" + s }, false},
			{"error with code", func(s string) string { return openaiPaste(s) + "&error=access_denied" }, false},
			{"foreign issuer", func(s string) string { return openaiPaste(s) + "&iss=https%3A%2F%2Fevil.example" }, false},
		},
	}
	for provider, list := range cases {
		for _, tc := range list {
			t.Run(provider+"/"+tc.name, func(t *testing.T) {
				ts := newCodeTokenServer(t)
				ts.account = "acct-1"
				a, err := beginCodeAttempt(provider, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				state := attemptState(t, a.AuthorizeURL())
				creds, err := completeCodeAttempt(context.Background(), ts.endpoints(), a, tc.in(state))
				if tc.ok {
					if err != nil || creds == nil || creds.Refresh != refreshSentinel {
						t.Fatalf("valid paste refused: %v", err)
					}
					if got := ts.lastRequest()["code"]; got != codeSentinel {
						t.Fatalf("exchanged code %q, want %q", got, codeSentinel)
					}
					return
				}
				if err == nil {
					t.Fatalf("accepted invalid paste (exchange calls %d)", ts.callCount())
				}
				if n := ts.callCount(); n != 0 {
					t.Fatalf("invalid paste reached the token endpoint (%d calls)", n)
				}
				noEcho(t, err, "wrong-state")
			})
		}
	}
}

// R05 (owner option a): Anthropic state is random and independent of the
// PKCE verifier; the verifier never appears in the authorize URL, and the
// token request carries code, state and code_verifier separately to the
// pinned endpoint.
func TestLoginAnthropic_StateIndependentOfVerifier(t *testing.T) {
	oldClient := oauthClient
	defer func() { oauthClient = oldClient }()
	var payload map[string]string
	oauthClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.String() != tokenURL {
			t.Fatalf("request %s %s, want POST %s", req.Method, req.URL, tokenURL)
		}
		if ct := req.Header.Get("Content-Type"); ct != "application/json" {
			t.Fatalf("content type %q", ct)
		}
		body, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{"access_token":"acc","refresh_token":"ref","expires_in":3600}`))}, nil
	})}

	var authURL string
	creds, err := LoginAnthropic(func(u string) { authURL = u }, func() (string, error) {
		return anthropicPaste(attemptState(t, authURL)), nil
	})
	if err != nil || creds == nil || creds.Access != "acc" {
		t.Fatalf("LoginAnthropic: %+v, %v", creds, err)
	}
	u, _ := url.Parse(authURL)
	q := u.Query()
	verifier := payload["code_verifier"]
	if len(verifier) < 43 {
		t.Fatalf("verifier %q too short", verifier)
	}
	if q.Get("state") == verifier {
		t.Fatal("authorize state equals the PKCE verifier")
	}
	if strings.Contains(authURL, verifier) {
		t.Fatal("authorize URL contains the PKCE verifier")
	}
	if len(q.Get("state")) < 43 {
		t.Fatalf("state %q is not 32 random bytes", q.Get("state"))
	}
	sum := sha256.Sum256([]byte(verifier))
	if q.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(sum[:]) || q.Get("code_challenge_method") != "S256" {
		t.Fatal("code_challenge is not S256 of the verifier")
	}
	want := map[string]string{"grant_type": "authorization_code", "client_id": clientID, "code": codeSentinel,
		"state": q.Get("state"), "redirect_uri": redirectURI}
	for k, v := range want {
		if payload[k] != v {
			t.Fatalf("token request %s = %q, want %q", k, payload[k], v)
		}
	}
	if q.Get("client_id") != clientID || q.Get("redirect_uri") != redirectURI {
		t.Fatal("authorize URL client/redirect changed")
	}
}

// R06: exchange failures are classified with fixed copy; no upstream body,
// URL or code reaches the error, redirects are not followed with the code,
// and malformed or oversized success responses are not success.
func TestCodeExchange_SafeFailures(t *testing.T) {
	for _, provider := range []string{"anthropic", "openai"} {
		paste := anthropicPaste
		if provider == "openai" {
			paste = openaiPaste
		}
		run := func(t *testing.T, ts *codeTokenServer, ep tokenEndpoints) error {
			t.Helper()
			a, err := beginCodeAttempt(provider, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			creds, err := completeCodeAttempt(context.Background(), ep, a, paste(attemptState(t, a.AuthorizeURL())))
			if err == nil {
				t.Fatalf("exchange succeeded: %+v", creds)
			}
			noEcho(t, err, ep.anthropic, "127.0.0.1")
			return err
		}
		t.Run(provider+"/invalid_grant", func(t *testing.T) {
			ts := newCodeTokenServer(t)
			ts.respond = func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprintf(w, `{"error":"invalid_grant","error_description":%q}`, bodySentinel)
			}
			wantCredClass(t, run(t, ts, ts.endpoints()), "reconnect")
		})
		t.Run(provider+"/server_error", func(t *testing.T) {
			ts := newCodeTokenServer(t)
			ts.respond = func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = io.WriteString(w, bodySentinel)
			}
			wantCredClass(t, run(t, ts, ts.endpoints()), "temporary")
		})
		t.Run(provider+"/network", func(t *testing.T) {
			ts := newCodeTokenServer(t)
			ep := ts.endpoints()
			ts.Close()
			wantCredClass(t, run(t, ts, ep), "temporary")
		})
		t.Run(provider+"/empty_access", func(t *testing.T) {
			ts := newCodeTokenServer(t)
			ts.respond = func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `{"access_token":"","refresh_token":%q,"expires_in":3600}`, refreshSentinel)
			}
			wantCredClass(t, run(t, ts, ts.endpoints()), "provider_unavailable")
		})
		t.Run(provider+"/malformed", func(t *testing.T) {
			ts := newCodeTokenServer(t)
			ts.respond = func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "{"+bodySentinel) }
			wantCredClass(t, run(t, ts, ts.endpoints()), "provider_unavailable")
		})
		t.Run(provider+"/oversized", func(t *testing.T) {
			ts := newCodeTokenServer(t)
			ts.account = "acct-1"
			ts.respond = func(w http.ResponseWriter, _ *http.Request) {
				access := accessSentinel
				if provider == "openai" {
					access = testOpenAIJWT("acct-1", 1)
				}
				_, _ = fmt.Fprintf(w, `{"access_token":%q,"refresh_token":%q,"expires_in":3600,"pad":%q}`,
					access, refreshSentinel, strings.Repeat("x", maxOAuthResponse))
			}
			wantCredClass(t, run(t, ts, ts.endpoints()), "provider_unavailable")
		})
		t.Run(provider+"/redirect_not_followed", func(t *testing.T) {
			var leaked int
			var mu sync.Mutex
			other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				leaked++
				mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": accessSentinel, "refresh_token": refreshSentinel, "expires_in": 3600})
			}))
			defer other.Close()
			ts := newCodeTokenServer(t)
			ts.respond = func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, other.URL+"/steal", http.StatusTemporaryRedirect)
			}
			_ = run(t, ts, ts.endpoints())
			mu.Lock()
			defer mu.Unlock()
			if leaked != 0 {
				t.Fatal("token request was replayed to the redirect target")
			}
		})
		t.Run(provider+"/context_cancel", func(t *testing.T) {
			ts := newCodeTokenServer(t)
			ts.gate = make(chan struct{})
			defer close(ts.gate)
			a, _ := beginCodeAttempt(provider, time.Now())
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				_, err := completeCodeAttempt(ctx, ts.endpoints(), a, paste(attemptState(t, a.AuthorizeURL())))
				done <- err
			}()
			<-ts.started
			cancel()
			select {
			case err := <-done:
				wantCredClass(t, err, "temporary")
				noEcho(t, err, "127.0.0.1")
			case <-time.After(5 * time.Second):
				t.Fatal("exchange ignored context cancellation")
			}
		})
	}
}

// R06: a refresh POST must not be replayed (with the refresh token) to a
// redirect target either.
func TestRefresh_RedirectNotFollowed(t *testing.T) {
	var leaked int
	var mu sync.Mutex
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		leaked++
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "x", "refresh_token": "y", "expires_in": 3600})
	}))
	defer other.Close()
	ts := newCodeTokenServer(t)
	ts.respond = func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusTemporaryRedirect)
	}
	for _, provider := range []string{"anthropic", "openai"} {
		if _, err := ts.endpoints().refresh(context.Background(), provider, refreshSentinel); err == nil {
			t.Fatalf("%s: redirected refresh succeeded", provider)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if leaked != 0 {
		t.Fatalf("refresh token replayed to the redirect target %d times", leaked)
	}
}

// The CLI loopback listener accepts only what a paste would: matching state,
// no error parameter smuggled past it, and the rebuilt URL then goes through
// CompleteOpenAI's strict parser.
func TestOpenAICallbackHandler_StrictAsPaste(t *testing.T) {
	a, err := beginCodeAttempt("openai", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	state := attemptState(t, a.AuthorizeURL())
	for _, tc := range []struct {
		query   string
		forward bool
	}{
		{"code=" + codeSentinel + "&state=" + state, true},
		{"code=" + codeSentinel, false},
		{"code=" + codeSentinel + "&state=wrong", false},
		{"code=" + codeSentinel + "&state=" + state + "&state=" + state, false},
		{"error=access_denied&state=" + state, true},
	} {
		ch := make(chan string, 1)
		rec := httptest.NewRecorder()
		callbackHandler(a, ch).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1455/auth/callback?"+tc.query, nil))
		select {
		case got := <-ch:
			if !tc.forward {
				t.Fatalf("%s: forwarded", tc.query)
			}
			if got != openaiCallback+"?"+tc.query {
				t.Fatalf("forwarded %q", got)
			}
			ts := newCodeTokenServer(t)
			ts.account = "acct-1"
			_, err := completeCodeAttempt(context.Background(), ts.endpoints(), a, got)
			if strings.HasPrefix(tc.query, "error=") {
				var le *LoginError
				if !errors.As(err, &le) || le.Class != LoginDenied || ts.callCount() != 0 {
					t.Fatalf("denial: %v (%d calls)", err, ts.callCount())
				}
			} else if err != nil {
				t.Fatal(err)
			}
		default:
			if tc.forward {
				t.Fatalf("%s: not forwarded (HTTP %d)", tc.query, rec.Code)
			}
			if rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), codeSentinel) {
				t.Fatalf("%s: HTTP %d %q", tc.query, rec.Code, rec.Body.String())
			}
		}
	}
}
