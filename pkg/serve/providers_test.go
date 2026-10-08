package serve

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
)

// The Providers API tests drive the real NewServer chain (Host check,
// browser/device authentication, route policy, CSRF) with a real device
// store, a real auth.Store on a temporary auth.json and the real login
// manager. Only the network is local: a client routes the fixed provider
// origins, and only those, to one httptest server that counts every call.
// Every secret a fake upstream issues contains "SENTINEL".

var providerTestOrigins = map[string]bool{
	"auth.openai.com":       true,
	"console.anthropic.com": true,
	"auth.x.ai":             true,
}

type providerUpstream struct {
	srv *httptest.Server

	mu         sync.Mutex
	calls      []string // origin + path of every request
	verifiers  []string // PKCE verifiers the token endpoints received
	xaiApprove bool
}

func newProviderUpstream(t *testing.T) *providerUpstream {
	t.Helper()
	u := &providerUpstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(u.serve))
	t.Cleanup(u.srv.Close)
	return u
}

func providerTestJWT(account, marker string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"https://api.openai.com/auth":{"chatgpt_account_id":%q}}`, account)))
	return header + "." + payload + "." + marker
}

func writeProviderTestJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func (u *providerUpstream) serve(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("X-Test-Origin")
	u.mu.Lock()
	u.calls = append(u.calls, origin+r.URL.Path)
	approve := u.xaiApprove
	u.mu.Unlock()
	switch origin + r.URL.Path {
	case "auth.openai.com/oauth/token":
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") == "authorization_code" {
			u.addVerifier(r.PostForm.Get("code_verifier"))
		}
		writeProviderTestJSON(w, http.StatusOK, fmt.Sprintf(`{"access_token":%q,"refresh_token":"REFRESH-SENTINEL-OPENAI","expires_in":3600}`, providerTestJWT("acct-1", "ACCESS-SENTINEL-OPENAI")))
	case "console.anthropic.com/v1/oauth/token":
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		u.addVerifier(body["code_verifier"])
		writeProviderTestJSON(w, http.StatusOK, `{"access_token":"sk-`+`ant-oat01-ACCESS-SENTINEL-ANTHROPIC","refresh_token":"REFRESH-SENTINEL-ANTHROPIC","expires_in":3600}`)
	case "auth.x.ai/.well-known/openid-configuration":
		writeProviderTestJSON(w, http.StatusOK, `{"issuer":"https://auth.x.ai","token_endpoint":"https://auth.x.ai/oauth2/token","device_authorization_endpoint":"https://auth.x.ai/oauth2/device/code"}`)
	case "auth.x.ai/oauth2/device/code":
		writeProviderTestJSON(w, http.StatusOK, `{"device_code":"DEVICE-CODE-SENTINEL","user_code":"WXYZ-1234","verification_uri":"https://accounts.x.ai/device","verification_uri_complete":"https://accounts.x.ai/device?user_code=WXYZ-1234","expires_in":600,"interval":1}`)
	case "auth.x.ai/oauth2/token":
		if approve {
			writeProviderTestJSON(w, http.StatusOK, `{"access_token":"ACCESS-SENTINEL-XAI","refresh_token":"REFRESH-SENTINEL-XAI","expires_in":3600}`)
			return
		}
		writeProviderTestJSON(w, http.StatusBadRequest, `{"error":"authorization_pending"}`)
	default:
		writeProviderTestJSON(w, http.StatusNotFound, `{}`)
	}
}

func (u *providerUpstream) addVerifier(v string) {
	if v == "" {
		return
	}
	u.mu.Lock()
	u.verifiers = append(u.verifiers, v)
	u.mu.Unlock()
}

func (u *providerUpstream) callCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.calls)
}

func (u *providerUpstream) client() *http.Client {
	target, _ := url.Parse(u.srv.URL)
	return &http.Client{Transport: providerRouteTransport{target: target, base: u.srv.Client().Transport}}
}

type providerRouteTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (rt providerRouteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || !providerTestOrigins[r.URL.Host] {
		return nil, errors.New("test transport: origin is not a fixed provider origin")
	}
	out := r.Clone(r.Context())
	out.URL.Scheme, out.URL.Host, out.Host = rt.target.Scheme, rt.target.Host, rt.target.Host
	out.Header.Set("X-Test-Origin", r.URL.Host)
	return rt.base.RoundTrip(out)
}

type providerFixture struct {
	t       *testing.T
	up      *providerUpstream
	path    string
	store   *auth.Store
	logins  *auth.ProviderLoginManager
	mgr     *Manager
	opts    []ServerOption
	handler http.Handler
	// responses records every Providers response for the secret scan.
	responses []*httptest.ResponseRecorder
}

func clearProviderTestEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "XAI_API_KEY", "META_API_KEY"} {
		t.Setenv(k, "")
	}
}

// newProviderFixture builds a server with the Providers API on a fresh
// auth.json. tokenMode adds owner token authentication and an Automation
// token; otherwise the network boundary is the owner.
func newProviderFixture(t *testing.T, tokenMode bool) *providerFixture {
	t.Helper()
	if !deviceStoreLockSupported() {
		t.Skip("device auth fails closed where advisory process locks are unavailable")
	}
	clearProviderTestEnv(t)
	f := &providerFixture{t: t, up: newProviderUpstream(t)}
	dir := t.TempDir()
	f.path = filepath.Join(dir, "cfg", "auth.json")
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		t.Fatal(err)
	}
	f.mgr = newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	f.opts = []ServerOption{WithDeviceStorePath(filepath.Join(dir, "devices.json"))}
	if tokenMode {
		f.opts = append(f.opts, WithAuthToken("owner-token", false), WithAutomationToken("automation-token"))
	}
	f.start()
	return f
}

// start (re)creates the store, login manager and server on the same files,
// which is what a server restart looks like.
func (f *providerFixture) start() {
	if f.logins != nil {
		f.logins.Close()
	}
	f.store = auth.NewStoreWithHTTPClient(f.path, f.up.client())
	f.logins = auth.NewProviderLoginManagerWithHTTPClient(context.Background(), f.store, f.up.client())
	logins := f.logins
	f.t.Cleanup(logins.Close)
	f.handler = NewServer(f.mgr, append(append([]ServerOption(nil), f.opts...), WithProviderCredentials(f.store, f.logins))...)
}

func (f *providerFixture) seed(provider string, cred auth.Credential) string {
	f.t.Helper()
	gen, err := f.store.StoredGeneration(provider)
	if err != nil {
		f.t.Fatal(err)
	}
	next, err := f.store.CommitLogin(provider, gen, cred)
	if err != nil {
		f.t.Fatal(err)
	}
	return next
}

func (f *providerFixture) generation(provider string) string {
	f.t.Helper()
	gen, err := f.store.StoredGeneration(provider)
	if err != nil {
		f.t.Fatal(err)
	}
	return gen
}

func (f *providerFixture) fileBytes() []byte {
	f.t.Helper()
	data, err := os.ReadFile(f.path)
	if err != nil && !os.IsNotExist(err) {
		f.t.Fatal(err)
	}
	return data
}

type reqOpt func(*http.Request)

func withCookie(c *http.Cookie) reqOpt { return func(r *http.Request) { r.AddCookie(c) } }
func withHeader(name string, values ...string) reqOpt {
	return func(r *http.Request) {
		r.Header.Del(name)
		for _, v := range values {
			r.Header.Add(name, v)
		}
	}
}
func withoutHeader(name string) reqOpt { return func(r *http.Request) { r.Header.Del(name) } }
func withRemote(addr string) reqOpt    { return func(r *http.Request) { r.RemoteAddr = addr } }
func withHost(host string) reqOpt      { return func(r *http.Request) { r.Host = host } }
func withTLS() reqOpt                  { return func(r *http.Request) { r.TLS = &tls.ConnectionState{} } }
func withDevice(credential string) reqOpt {
	return withHeader("Authorization", deviceAuthorizationScheme+" "+credential)
}

var ownerCookie = &http.Cookie{Name: authCookieName, Value: "owner-token"}

// do sends a request as moa's own page would: same Host and Origin,
// X-Moa-Request and a JSON body. Options then alter it.
func (f *providerFixture) do(method, path, body string, opts ...reqOpt) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = "localhost:8080"
	req.RemoteAddr = "127.0.0.1:40000"
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("X-Moa-Request", "1")
		req.Header.Set("Origin", "http://localhost:8080")
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, opt := range opts {
		opt(req)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	f.responses = append(f.responses, rec)
	return rec
}

func decodeProviderJSON[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %d %q: %v", rec.Code, rec.Body.String(), err)
	}
	return v
}

type testProviderRow struct {
	ID                   string   `json:"id"`
	Source               string   `json:"source"`
	Kind                 string   `json:"kind"`
	CredentialGeneration string   `json:"credential_generation"`
	State                string   `json:"state"`
	Attention            bool     `json:"attention"`
	Action               string   `json:"action"`
	ChangedAt            string   `json:"changed_at"`
	RenewAt              string   `json:"renew_at"`
	LastUseOKAt          string   `json:"last_use_ok_at"`
	OAuthEnabled         *bool    `json:"oauth_enabled"`
	APIKeyEnabled        *bool    `json:"api_key_enabled"`
	Actions              []string `json:"actions"`
	PendingSave          *bool    `json:"pending_save"`
}

type testProvidersStatus struct {
	Version        int               `json:"version"`
	CanAdmin       bool              `json:"can_admin"`
	AttentionCount int               `json:"attention_count"`
	Providers      []testProviderRow `json:"providers"`
}

func (s testProvidersStatus) row(t *testing.T, id string) testProviderRow {
	t.Helper()
	for _, r := range s.Providers {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no %s row in %+v", id, s)
	return testProviderRow{}
}

func beginBody(gen string) string  { return fmt.Sprintf(`{"expected_generation":%q}`, gen) }
func attemptJSON(id string) string { return fmt.Sprintf(`{"attempt_id":%q}`, id) }

// R01: active own devices have the same administration as token/network.
func TestProviders_UserIdentityAdministration(t *testing.T) {
	type caller struct {
		name    string
		allowed bool
		// statusOK: GET /api/providers/status is readable (sanitized).
		statusOK bool
		opts     func(f *providerFixture) []reqOpt
	}
	deviceHeader := func(f *providerFixture, owner *http.Cookie) []reqOpt {
		d := pairedDevice(f.t, f.handler, owner, "phone")
		return []reqOpt{withDevice(d.Credential)}
	}
	deviceCookie := func(f *providerFixture, owner *http.Cookie) []reqOpt {
		d := pairedDevice(f.t, f.handler, owner, "webview")
		return []reqOpt{withCookie(deviceBrowserSession(f.t, f.handler, d.Credential))}
	}
	modes := []struct {
		name    string
		token   bool
		callers []caller
	}{
		{"network", false, []caller{
			{"none", true, true, func(*providerFixture) []reqOpt { return nil }},
			{"automation_bearer", true, true, func(*providerFixture) []reqOpt {
				return []reqOpt{withHeader("Authorization", "Bearer automation-token")}
			}},
			{"device_header", true, true, func(f *providerFixture) []reqOpt { return deviceHeader(f, nil) }},
			{"device_cookie", true, true, func(f *providerFixture) []reqOpt { return deviceCookie(f, nil) }},
		}},
		{"token", true, []caller{
			{"owner", true, true, func(*providerFixture) []reqOpt { return []reqOpt{withCookie(ownerCookie)} }},
			{"device_header", true, true, func(f *providerFixture) []reqOpt { return deviceHeader(f, ownerCookie) }},
			{"device_cookie", true, true, func(f *providerFixture) []reqOpt { return deviceCookie(f, ownerCookie) }},
			{"automation_bearer", false, false, func(*providerFixture) []reqOpt {
				return []reqOpt{withHeader("Authorization", "Bearer automation-token")}
			}},
			{"hook_secret", false, false, func(*providerFixture) []reqOpt {
				return []reqOpt{withHeader("Authorization", "Bearer hook-path-secret")}
			}},
			{"none", false, false, func(*providerFixture) []reqOpt { return nil }},
		}},
	}
	for _, mode := range modes {
		for _, c := range mode.callers {
			t.Run(mode.name+"/"+c.name, func(t *testing.T) {
				f := newProviderFixture(t, mode.token)
				gen := f.seed("openai", auth.Credential{Type: "api_key", Key: "sk-" + "old-KEY-SENTINEL"})
				opts := c.opts(f)

				status := f.do(http.MethodGet, "/api/providers/status", "", opts...)
				if c.statusOK {
					if status.Code != http.StatusOK {
						t.Fatalf("GET status = %d: %s", status.Code, status.Body.String())
					}
					got := decodeProviderJSON[testProvidersStatus](t, status)
					if got.CanAdmin != c.allowed {
						t.Errorf("can_admin = %v, want %v", got.CanAdmin, c.allowed)
					}
					if row := got.row(t, "openai"); !c.allowed && (row.Actions != nil || row.PendingSave != nil || row.OAuthEnabled != nil) {
						t.Errorf("device status carries owner fields: %+v", row)
					}
				} else if status.Code != http.StatusUnauthorized && status.Code != http.StatusForbidden {
					t.Fatalf("GET status = %d, want denied", status.Code)
				}

				admin := []struct {
					method, path, body string
					ownerCode          int
				}{
					{http.MethodGet, "/api/providers", "", http.StatusOK},
					{http.MethodPost, "/api/providers/xai/oauth/begin", beginBody(""), http.StatusAccepted},
					{http.MethodPost, "/api/providers/openai/oauth/complete", `{"attempt_id":"x","input":"y"}`, http.StatusNotFound},
					{http.MethodPost, "/api/providers/openai/oauth/progress", attemptJSON("x"), http.StatusNotFound},
					{http.MethodPost, "/api/providers/openai/oauth/cancel", attemptJSON("x"), http.StatusNotFound},
					{http.MethodPost, "/api/providers/openai/retry-save", `{}`, http.StatusOK},
					{http.MethodGet, "/api/providers/openai/oauth/begin", "", http.StatusMethodNotAllowed},
					{http.MethodPut, "/api/providers/status", "", http.StatusMethodNotAllowed},
					{http.MethodDelete, "/api/providers", "", http.StatusMethodNotAllowed},
					{http.MethodGet, "/api/providers/openai", "", http.StatusNotFound},
					{http.MethodPost, "/api/providers/openai/api-key", fmt.Sprintf(`{"key":"sk-`+`new-KEY-SENTINEL-0123","expected_generation":%q}`, gen), http.StatusOK},
				}
				before, calls := f.fileBytes(), f.up.callCount()
				for _, a := range admin {
					rec := f.do(a.method, a.path, a.body, opts...)
					if c.allowed {
						if rec.Code != a.ownerCode {
							t.Errorf("%s %s = %d, want %d: %s", a.method, a.path, rec.Code, a.ownerCode, rec.Body.String())
						}
						if rec.Code == http.StatusMethodNotAllowed && rec.Header().Get("Allow") == "" {
							t.Errorf("%s %s: 405 without Allow", a.method, a.path)
						}
						continue
					}
					if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
						t.Errorf("%s %s = %d, want 401/403: %s", a.method, a.path, rec.Code, rec.Body.String())
					}
				}
				if c.allowed {
					return
				}
				if n := f.up.callCount() - calls; n != 0 {
					t.Errorf("denied requests made %d upstream calls", n)
				}
				if after := f.fileBytes(); !bytes.Equal(before, after) {
					t.Errorf("denied requests changed auth.json")
				}
			})
		}
	}
}

// R02: every Providers POST needs moa's exact origin, the CSRF header and a
// strict JSON body; a rejection starts, exchanges and saves nothing.
func TestProviders_StrictBrowserMutations(t *testing.T) {
	f := newProviderFixture(t, false)
	f.seed("openai", auth.Credential{Type: "api_key", Key: "sk-" + "old-KEY-SENTINEL"})
	big := `{"expected_generation":"` + strings.Repeat("a", providersBodyLimit) + `"}`
	rejected := []struct {
		name string
		code int
		body string // "" = the default begin body
		opts []reqOpt
	}{
		{"no_origin", http.StatusForbidden, "", []reqOpt{withoutHeader("Origin")}},
		{"null_origin", http.StatusForbidden, "", []reqOpt{withHeader("Origin", "null")}},
		{"empty_origin", http.StatusForbidden, "", []reqOpt{withHeader("Origin", "")}},
		{"other_host", http.StatusForbidden, "", []reqOpt{withHeader("Origin", "http://evil.example:8080")}},
		{"other_port", http.StatusForbidden, "", []reqOpt{withHeader("Origin", "http://localhost:9090")}},
		{"no_port", http.StatusForbidden, "", []reqOpt{withHeader("Origin", "http://localhost")}},
		{"other_scheme", http.StatusForbidden, "", []reqOpt{withHeader("Origin", "https://localhost:8080")}},
		{"with_path", http.StatusForbidden, "", []reqOpt{withHeader("Origin", "http://localhost:8080/")}},
		{"with_query", http.StatusForbidden, "", []reqOpt{withHeader("Origin", "http://localhost:8080?x=1")}},
		{"with_userinfo", http.StatusForbidden, "", []reqOpt{withHeader("Origin", "http://u@localhost:8080")}},
		{"list", http.StatusForbidden, "", []reqOpt{withHeader("Origin", "http://localhost:8080, http://evil.example")}},
		{"space_list", http.StatusForbidden, "", []reqOpt{withHeader("Origin", "http://localhost:8080 http://evil.example")}},
		{"two_headers", http.StatusForbidden, "", []reqOpt{withHeader("Origin", "http://localhost:8080", "http://localhost:8080")}},
		{"no_csrf", http.StatusForbidden, "", []reqOpt{withoutHeader("X-Moa-Request")}},
		{"tls_http_origin", http.StatusForbidden, "", []reqOpt{withTLS()}},
		{"loopback_xfp_https_http_origin", http.StatusForbidden, "", []reqOpt{withHeader("X-Forwarded-Proto", "https")}},
		{"loopback_two_xfp", http.StatusForbidden, "", []reqOpt{withHeader("X-Forwarded-Proto", "https", "http")}},
		{"loopback_list_xfp", http.StatusForbidden, "", []reqOpt{withHeader("X-Forwarded-Proto", "https, http"), withHeader("Origin", "https://localhost:8080")}},
		{"loopback_bad_xfp", http.StatusForbidden, "", []reqOpt{withHeader("X-Forwarded-Proto", "gopher")}},
		{"remote_spoofed_xfp", http.StatusForbidden, "", []reqOpt{withRemote("100.64.0.9:40000"), withHeader("X-Forwarded-Proto", "https"), withHeader("Origin", "https://localhost:8080")}},
		{"forwarded_host_ignored", http.StatusForbidden, "", []reqOpt{withHeader("X-Forwarded-Host", "evil.example:8080"), withHeader("Origin", "http://evil.example:8080")}},
		{"text_plain", http.StatusUnsupportedMediaType, "", []reqOpt{withHeader("Content-Type", "text/plain")}},
		{"form", http.StatusUnsupportedMediaType, "", []reqOpt{withHeader("Content-Type", "application/x-www-form-urlencoded")}},
		{"unknown_field", http.StatusBadRequest, `{"expected_generation":"","issuer":"https://evil.example"}`, nil},
		{"trailing", http.StatusBadRequest, `{"expected_generation":""}{}`, nil},
		{"array", http.StatusBadRequest, `[]`, nil},
		{"missing_generation", http.StatusBadRequest, `{}`, nil},
		{"too_large", http.StatusRequestEntityTooLarge, big, nil},
	}
	before, calls := f.fileBytes(), f.up.callCount()
	for _, c := range rejected {
		t.Run(c.name, func(t *testing.T) {
			body := c.body
			if body == "" {
				body = beginBody("")
			}
			if rec := f.do(http.MethodPost, "/api/providers/xai/oauth/begin", body, c.opts...); rec.Code != c.code {
				t.Errorf("begin = %d, want %d: %s", rec.Code, c.code, rec.Body.String())
			}
			keyBody := fmt.Sprintf(`{"key":"sk-`+`new-KEY-SENTINEL-0123","expected_generation":%q}`, f.generation("openai"))
			if c.body != "" {
				keyBody = c.body
			}
			if rec := f.do(http.MethodPost, "/api/providers/openai/api-key", keyBody, c.opts...); rec.Code != c.code {
				t.Errorf("api-key = %d, want %d: %s", rec.Code, c.code, rec.Body.String())
			}
		})
	}
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodOptions, http.MethodPatch} {
		rec := f.do(m, "/api/providers/openai/api-key", `{"key":"sk-`+`new-KEY-SENTINEL-0123","expected_generation":""}`)
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
			t.Errorf("%s api-key = %d Allow=%q, want 405 Allow: POST", m, rec.Code, rec.Header().Get("Allow"))
		}
		if v := rec.Header().Get("Access-Control-Allow-Origin"); v != "" {
			t.Errorf("%s api-key grants CORS %q", m, v)
		}
	}
	if n := f.up.callCount() - calls; n != 0 {
		t.Errorf("rejected requests made %d upstream calls", n)
	}
	if !bytes.Equal(before, f.fileBytes()) {
		t.Errorf("rejected requests changed auth.json")
	}

	accepted := []struct {
		name string
		opts []reqOpt
	}{
		{"same_origin", nil},
		{"direct_tls", []reqOpt{withTLS(), withHeader("Origin", "https://localhost:8080")}},
		{"loopback_proxy_https", []reqOpt{withHeader("X-Forwarded-Proto", "https"), withHeader("Origin", "https://localhost:8080")}},
		{"tailnet_name", []reqOpt{withHost("100.100.1.2:8080"), withHeader("Origin", "http://100.100.1.2:8080")}},
		{"ipv6_host", []reqOpt{withHost("[::1]:8080"), withHeader("Origin", "http://[::1]:8080")}},
	}
	for _, c := range accepted {
		rec := f.do(http.MethodPost, "/api/providers/openai/api-key", fmt.Sprintf(`{"key":"sk-`+`new-KEY-SENTINEL-0123","expected_generation":%q}`, f.generation("openai")), c.opts...)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: api-key = %d: %s", c.name, rec.Code, rec.Body.String())
		}
	}
}

// R02: no Providers response is cached or leaks a referrer, including the
// errors written by the outer Host/authentication/route boundaries.
func TestProviders_EveryResponseIsNoStore(t *testing.T) {
	network := newProviderFixture(t, false)
	device := pairedDevice(t, network.handler, nil, "phone")
	token := newProviderFixture(t, true)
	cases := []struct {
		name string
		f    *providerFixture
		code int
		run  func(f *providerFixture) *httptest.ResponseRecorder
	}{
		{"status", network, 200, func(f *providerFixture) *httptest.ResponseRecorder {
			return f.do(http.MethodGet, "/api/providers/status", "")
		}},
		{"list", network, 200, func(f *providerFixture) *httptest.ResponseRecorder { return f.do(http.MethodGet, "/api/providers", "") }},
		{"begin", network, 201, func(f *providerFixture) *httptest.ResponseRecorder {
			return f.do(http.MethodPost, "/api/providers/openai/oauth/begin", beginBody(""))
		}},
		{"bad_json", network, 400, func(f *providerFixture) *httptest.ResponseRecorder {
			return f.do(http.MethodPost, "/api/providers/openai/oauth/begin", `{`)
		}},
		{"not_found", network, 404, func(f *providerFixture) *httptest.ResponseRecorder {
			return f.do(http.MethodGet, "/api/providers/meta/nothing", "")
		}},
		{"method", network, 405, func(f *providerFixture) *httptest.ResponseRecorder {
			return f.do(http.MethodPost, "/api/providers/status", `{}`)
		}},
		{"origin", network, 403, func(f *providerFixture) *httptest.ResponseRecorder {
			return f.do(http.MethodPost, "/api/providers/openai/oauth/begin", beginBody(""), withoutHeader("Origin"))
		}},
		{"device_admin", network, 200, func(f *providerFixture) *httptest.ResponseRecorder {
			return f.do(http.MethodGet, "/api/providers", "", withDevice(device.Credential))
		}},
		{"host", network, 403, func(f *providerFixture) *httptest.ResponseRecorder {
			return f.do(http.MethodGet, "/api/providers/status", "", withHost("evil.example"))
		}},
		{"unauthenticated", token, 401, func(f *providerFixture) *httptest.ResponseRecorder {
			return f.do(http.MethodGet, "/api/providers/status", "")
		}},
		{"unauthenticated_subtree", token, 401, func(f *providerFixture) *httptest.ResponseRecorder {
			return f.do(http.MethodPost, "/api/providers/x/y/z", `{}`)
		}},
	}
	for _, c := range cases {
		rec := c.run(c.f)
		if rec.Code != c.code {
			t.Errorf("%s = %d, want %d: %s", c.name, rec.Code, c.code, rec.Body.String())
		}
		h := rec.Header()
		if h.Get("Cache-Control") != "no-store" || h.Get("Referrer-Policy") != "no-referrer" || h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s headers: Cache-Control=%q Referrer-Policy=%q X-Content-Type-Options=%q", c.name, h.Get("Cache-Control"), h.Get("Referrer-Policy"), h.Get("X-Content-Type-Options"))
		}
	}
}

type testAttempt struct {
	Provider                string `json:"provider"`
	Flow                    string `json:"flow"`
	AttemptID               string `json:"attempt_id"`
	AuthorizeURL            string `json:"authorize_url"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresAt               string `json:"expires_at"`
	State                   string `json:"state"`
}

func browserCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == providerBrowserCookie {
			return c
		}
	}
	t.Fatalf("no %s cookie in %v", providerBrowserCookie, rec.Header().Values("Set-Cookie"))
	return nil
}

func authorizeState(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("state")
}

// R04 (HTTP): an attempt is bound to the browser that began it, used once,
// and lost (with the previous credential intact) on restart.
func TestProviders_AttemptsAreBoundToTheBrowser(t *testing.T) {
	f := newProviderFixture(t, false)
	f.seed("openai", auth.Credential{Type: "api_key", Key: "sk-" + "old-KEY-SENTINEL"})

	begin := f.do(http.MethodPost, "/api/providers/openai/oauth/begin", beginBody(f.generation("openai")))
	if begin.Code != http.StatusCreated {
		t.Fatalf("begin = %d: %s", begin.Code, begin.Body.String())
	}
	attempt := decodeProviderJSON[testAttempt](t, begin)
	if attempt.Flow != "paste_url" || attempt.AttemptID == "" || attempt.AuthorizeURL == "" {
		t.Fatalf("begin view = %+v", attempt)
	}
	cookie := browserCookie(t, begin)
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil || len(raw) < 32 {
		t.Errorf("binding cookie is not >=32 random bytes: %q", cookie.Value)
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/api/providers" || cookie.MaxAge <= 0 || cookie.MaxAge > 15*60 || cookie.Secure {
		t.Errorf("binding cookie attributes = %+v", cookie)
	}
	if cookie.Value == attempt.AttemptID {
		t.Error("attempt id and browser binding are the same value")
	}
	// Behind the loopback TLS proxy the cookie is Secure; a valid one is reused.
	again := f.do(http.MethodPost, "/api/providers/anthropic/oauth/begin", beginBody(""), withCookie(cookie), withHeader("X-Forwarded-Proto", "https"), withHeader("Origin", "https://localhost:8080"))
	if again.Code != http.StatusCreated {
		t.Fatalf("second begin = %d: %s", again.Code, again.Body.String())
	}
	if c := browserCookie(t, again); c.Value != cookie.Value || !c.Secure {
		t.Errorf("second begin cookie = %+v, want the same value, Secure", c)
	}

	paste := fmt.Sprintf("http://localhost:1455/auth/callback?code=CODE-SENTINEL&state=%s", authorizeState(t, attempt.AuthorizeURL))
	complete := func(id string, opts ...reqOpt) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"attempt_id": id, "input": paste})
		return f.do(http.MethodPost, "/api/providers/openai/oauth/complete", string(body), opts...)
	}
	otherBrowser := &http.Cookie{Name: providerBrowserCookie, Value: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))}
	before, calls := f.fileBytes(), f.up.callCount()
	unknown := complete("not-an-attempt", withCookie(cookie))
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown attempt = %d: %s", unknown.Code, unknown.Body.String())
	}
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"no_cookie":              complete(attempt.AttemptID),
		"other_browser":          complete(attempt.AttemptID, withCookie(otherBrowser)),
		"progress_other_browser": f.do(http.MethodPost, "/api/providers/openai/oauth/progress", attemptJSON(attempt.AttemptID), withCookie(otherBrowser)),
		"cancel_other_browser":   f.do(http.MethodPost, "/api/providers/openai/oauth/cancel", attemptJSON(attempt.AttemptID), withCookie(otherBrowser)),
		"other_provider":         f.do(http.MethodPost, "/api/providers/anthropic/oauth/complete", fmt.Sprintf(`{"attempt_id":%q,"input":"x#y"}`, attempt.AttemptID), withCookie(cookie)),
	} {
		if rec.Code != http.StatusNotFound || rec.Body.String() != unknown.Body.String() && name != "other_provider" {
			t.Errorf("%s = %d %s, want 404 like an unknown attempt (%s)", name, rec.Code, rec.Body.String(), unknown.Body.String())
		}
	}
	if dup := complete(attempt.AttemptID, withCookie(cookie), withCookie(otherBrowser)); dup.Code != http.StatusBadRequest {
		t.Errorf("duplicate binding cookies = %d, want 400", dup.Code)
	}
	if n := f.up.callCount() - calls; n != 0 {
		t.Errorf("unbound completions made %d upstream calls", n)
	}
	if !bytes.Equal(before, f.fileBytes()) {
		t.Error("unbound completions changed auth.json")
	}

	progress := f.do(http.MethodPost, "/api/providers/openai/oauth/progress", attemptJSON(attempt.AttemptID), withCookie(cookie))
	if progress.Code != http.StatusOK || !strings.Contains(progress.Body.String(), `"state":"waiting"`) {
		t.Fatalf("progress = %d: %s", progress.Code, progress.Body.String())
	}
	ok := complete(attempt.AttemptID, withCookie(cookie))
	if ok.Code != http.StatusOK {
		t.Fatalf("complete = %d: %s", ok.Code, ok.Body.String())
	}
	var done struct {
		Provider   testProviderRow `json:"provider"`
		NextAction string          `json:"next_action"`
	}
	if err := json.Unmarshal(ok.Body.Bytes(), &done); err != nil {
		t.Fatal(err)
	}
	if done.NextAction != "return_to_session" || done.Provider.State != auth.StatusSaved || done.Provider.Kind != "oauth" || done.Provider.CredentialGeneration == "" {
		t.Errorf("complete body = %s", ok.Body.String())
	}
	if again := complete(attempt.AttemptID, withCookie(cookie)); again.Code != http.StatusGone {
		t.Errorf("second complete = %d, want 410: %s", again.Code, again.Body.String())
	}
	if c := f.do(http.MethodPost, "/api/providers/openai/oauth/cancel", attemptJSON(attempt.AttemptID), withCookie(cookie)); c.Code != http.StatusNoContent {
		t.Errorf("cancel after completion = %d, want 204 (idempotent)", c.Code)
	}

	// Restart: a fresh attempt is lost, the saved credential is not.
	next := f.do(http.MethodPost, "/api/providers/openai/oauth/begin", beginBody(f.generation("openai")), withCookie(cookie))
	pending := decodeProviderJSON[testAttempt](t, next)
	saved, calls := f.fileBytes(), f.up.callCount()
	f.start()
	paste = fmt.Sprintf("http://localhost:1455/auth/callback?code=CODE-SENTINEL&state=%s", authorizeState(t, pending.AuthorizeURL))
	if rec := complete(pending.AttemptID, withCookie(cookie)); rec.Code != http.StatusNotFound {
		t.Errorf("complete after restart = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if n := f.up.callCount() - calls; n != 0 {
		t.Errorf("completion after restart made %d upstream calls", n)
	}
	if !bytes.Equal(saved, f.fileBytes()) {
		t.Error("completion after restart changed auth.json")
	}
	if st := f.store.ProviderStatus("openai"); st.Kind != "oauth" || st.State != auth.StatusSaved {
		t.Errorf("after restart status = %+v, want the saved login, not checked", st)
	}
}

// No Providers response (body or headers) and no log line carries an access,
// refresh, API key, device code, auth code or PKCE verifier.
func TestProviders_ResponsesCarryNoSecret(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	f := newProviderFixture(t, false)
	for _, p := range []string{"openai", "anthropic"} {
		begin := f.do(http.MethodPost, "/api/providers/"+p+"/oauth/begin", beginBody(""))
		a := decodeProviderJSON[testAttempt](t, begin)
		cookie := browserCookie(t, begin)
		state := authorizeState(t, a.AuthorizeURL)
		input := "http://localhost:1455/auth/callback?code=CODE-SENTINEL&state=" + state
		if p == "anthropic" {
			input = "CODE-SENTINEL#" + state
		}
		body, _ := json.Marshal(map[string]string{"attempt_id": a.AttemptID, "input": input})
		if rec := f.do(http.MethodPost, "/api/providers/"+p+"/oauth/complete", string(body), withCookie(cookie)); rec.Code != http.StatusOK {
			t.Fatalf("%s complete = %d: %s", p, rec.Code, rec.Body.String())
		}
		// A wrong paste is never echoed.
		_ = f.do(http.MethodPost, "/api/providers/"+p+"/oauth/complete", string(body), withCookie(cookie))
	}
	f.up.mu.Lock()
	f.up.xaiApprove = true
	f.up.mu.Unlock()
	begin := f.do(http.MethodPost, "/api/providers/xai/oauth/begin", beginBody(""))
	if begin.Code != http.StatusAccepted {
		t.Fatalf("xai begin = %d: %s", begin.Code, begin.Body.String())
	}
	a := decodeProviderJSON[testAttempt](t, begin)
	if a.UserCode != "WXYZ-1234" || a.VerificationURI != "https://accounts.x.ai/device" || a.State != "waiting" {
		t.Errorf("xai begin view = %+v", a)
	}
	cookie := browserCookie(t, begin)
	deadline := time.Now().Add(15 * time.Second)
	for {
		rec := f.do(http.MethodPost, "/api/providers/xai/oauth/progress", attemptJSON(a.AttemptID), withCookie(cookie))
		if strings.Contains(rec.Body.String(), `"state":"saved"`) {
			if !strings.Contains(rec.Body.String(), `"provider_status"`) {
				t.Errorf("saved progress without provider status: %s", rec.Body.String())
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("xai never saved: %s", rec.Body.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = f.do(http.MethodPost, "/api/providers/openai/api-key", fmt.Sprintf(`{"key":"sk-`+`proj-KEY-SENTINEL","expected_generation":%q}`, f.generation("openai")))
	_ = f.do(http.MethodPost, "/api/providers/openai/api-key", `{"key":"sk-`+`ant-oat01-KEY-SENTINEL","expected_generation":"stale"}`)
	_ = f.do(http.MethodGet, "/api/providers", "")
	_ = f.do(http.MethodGet, "/api/providers/status", "")

	f.up.mu.Lock()
	verifiers := append([]string(nil), f.up.verifiers...)
	f.up.mu.Unlock()
	if len(verifiers) != 2 {
		t.Fatalf("token endpoints saw %d verifiers, want 2", len(verifiers))
	}
	secrets := append([]string{"SENTINEL"}, verifiers...)
	for i, rec := range f.responses {
		var headers strings.Builder
		_ = rec.Header().Write(&headers)
		for _, s := range secrets {
			if strings.Contains(rec.Body.String(), s) || strings.Contains(headers.String(), s) {
				t.Errorf("response %d (%d) carries a secret: %s %s", i, rec.Code, headers.String(), rec.Body.String())
			}
		}
	}
	for _, s := range secrets {
		if strings.Contains(logs.String(), s) {
			t.Errorf("logs carry a secret: %s", logs.String())
		}
	}
}

// R17: status reports what is known — saved is not ready, expiry renews on
// use, timestamps are measured — and reading it has no side effects.
func TestProviders_StatusIsHonestAndSideEffectFree(t *testing.T) {
	f := newProviderFixture(t, false)
	expires := time.Now().Add(-time.Minute).Truncate(time.Second)
	f.seed("openai", auth.Credential{Type: "oauth", Access: providerTestJWT("acct-1", "ACCESS-SENTINEL-OLD"), Refresh: "REFRESH-SENTINEL-OLD", AccountID: "acct-1", Expires: expires.UnixMilli()})
	f.seed("anthropic", auth.Credential{Type: "api_key", Key: "sk-" + "ant-api-KEY-SENTINEL"})
	device := pairedDevice(t, f.handler, nil, "phone")

	before, calls := f.fileBytes(), f.up.callCount()
	info, _ := os.Stat(f.path)
	get := func(path string, opts ...reqOpt) testProvidersStatus {
		t.Helper()
		rec := f.do(http.MethodGet, path, "", opts...)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
		}
		return decodeProviderJSON[testProvidersStatus](t, rec)
	}
	owner := get("/api/providers")
	_ = get("/api/providers/status")
	if n := f.up.callCount() - calls; n != 0 {
		t.Errorf("status GETs made %d upstream calls (refresh)", n)
	}
	after, _ := os.Stat(f.path)
	if !bytes.Equal(before, f.fileBytes()) || !after.ModTime().Equal(info.ModTime()) {
		t.Error("status GETs wrote auth.json")
	}
	oa := owner.row(t, "openai")
	if oa.State != auth.StatusRenewOnUse || oa.Attention || oa.RenewAt != expires.UTC().Format(time.RFC3339) || oa.LastUseOKAt != "" {
		t.Errorf("expired OAuth row = %+v, want renew_on_use at the real expiry, no attention, no use", oa)
	}
	an := owner.row(t, "anthropic")
	if an.State != auth.StatusSaved || an.RenewAt != "" || an.LastUseOKAt != "" || an.Kind != "api_key" {
		t.Errorf("unused key row = %+v, want saved (not ready) without invented times", an)
	}
	if x := owner.row(t, "xai"); x.State != auth.StatusMissing || x.Attention || x.Action != "connect" {
		t.Errorf("missing row = %+v", x)
	}
	if owner.AttentionCount != 0 || !owner.CanAdmin || an.OAuthEnabled == nil || !*an.OAuthEnabled {
		t.Errorf("owner status = %+v", owner)
	}

	// A real successful use makes it ready, with a measured time.
	anSnap, _ := f.store.PeekSnapshot("anthropic")
	f.store.RecordUse(anSnap, nil)
	if r := get("/api/providers/status").row(t, "anthropic"); r.State != auth.StatusReady || r.LastUseOKAt == "" || r.ChangedAt == "" {
		t.Errorf("after success = %+v, want ready with last_use_ok_at", r)
	}

	// The same rejection seen by several sessions counts once; devices see
	// it too, with nothing to do but ask the owner.
	rejected := core.NewProviderCredentialError("anthropic", core.CredentialSourceStore, "inference", core.CredentialKeyRejected)
	for i := 0; i < 3; i++ {
		f.store.RecordUse(anSnap, rejected)
	}
	dev := get("/api/providers/status", withDevice(device.Credential))
	if dev.AttentionCount != 1 || !dev.CanAdmin || dev.row(t, "anthropic").State != auth.StatusKeyRejected || dev.row(t, "anthropic").Action != "replace_key" {
		t.Errorf("device status after rejection = %+v", dev)
	}
	if o := get("/api/providers"); o.AttentionCount != 1 || o.row(t, "anthropic").Action != "replace_key" {
		t.Errorf("owner status after rejection = %+v", o)
	}
	if r := get("/api/providers/status").row(t, "anthropic"); r.Action != "replace_key" || r.Actions != nil {
		t.Errorf("owner badge row = %+v, want the owner action without admin fields", r)
	}

	// A real success of the same key clears the warning.
	f.store.RecordUse(anSnap, nil)
	if get("/api/providers/status").AttentionCount != 0 {
		t.Error("a later success of the same key did not clear the warning")
	}

	// Temporary, quota and permission failures never ask for sign-in.
	for _, class := range []string{core.CredentialTemporary, core.CredentialQuota, core.CredentialPermissions, core.CredentialChanged} {
		f.store.RecordUse(anSnap, core.NewProviderCredentialError("anthropic", core.CredentialSourceStore, "inference", class))
	}
	if s := get("/api/providers/status"); s.AttentionCount != 0 || s.row(t, "anthropic").State != auth.StatusTemporary {
		t.Errorf("after temporary/quota/permissions = %+v, want temporary without attention", s)
	}

	// A replacement starts clean, and results of the old key cannot set or
	// clear the new one's state.
	rec := f.do(http.MethodPost, "/api/providers/anthropic/api-key", fmt.Sprintf(`{"key":"sk-`+`ant-api-NEW-KEY-SENTINEL","expected_generation":%q}`, f.generation("anthropic")))
	if rec.Code != http.StatusOK {
		t.Fatalf("api-key = %d: %s", rec.Code, rec.Body.String())
	}
	row := decodeProviderJSON[testProviderRow](t, rec)
	if row.State != auth.StatusSaved || row.ChangedAt == "" || row.LastUseOKAt != "" {
		t.Errorf("after replacement = %+v, want saved with changed_at", row)
	}
	f.store.RecordUse(anSnap, rejected)
	if s := get("/api/providers/status"); s.AttentionCount != 0 || s.row(t, "anthropic").State != auth.StatusSaved {
		t.Errorf("an old key's rejection reached the new key: %+v", s)
	}
	newSnap, _ := f.store.PeekSnapshot("anthropic")
	f.store.RecordUse(newSnap, rejected)
	f.store.RecordUse(anSnap, nil)
	if s := get("/api/providers/status"); s.AttentionCount != 1 || s.row(t, "anthropic").State != auth.StatusKeyRejected {
		t.Errorf("an old key's success cleared the new key's rejection: %+v", s)
	}

	// The projection lives for the server's lifetime; a restart honestly
	// knows nothing about use.
	f.start()
	if s := get("/api/providers/status"); s.AttentionCount != 0 || s.row(t, "anthropic").State != auth.StatusSaved || s.row(t, "anthropic").ChangedAt != "" {
		t.Errorf("after restart = %+v, want saved, not checked", s)
	}
}

// R17: an unsaved rotation is a save problem the owner can retry without any
// provider call, and it is never reported as connected.
func TestProviders_SaveFailureAndRetrySave(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permissions do not stop root")
	}
	f := newProviderFixture(t, false)
	f.seed("openai", auth.Credential{Type: "oauth", Access: providerTestJWT("acct-1", "ACCESS-SENTINEL-OLD"), Refresh: "REFRESH-SENTINEL-OLD", AccountID: "acct-1", Expires: time.Now().Add(-time.Minute).UnixMilli()})
	dir := filepath.Dir(f.path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if _, err := f.store.ResolveSnapshot(context.Background(), "openai"); err == nil {
		t.Fatal("rotation save into a read-only directory succeeded")
	}
	calls := f.up.callCount()
	s := decodeProviderJSON[testProvidersStatus](t, f.do(http.MethodGet, "/api/providers", ""))
	row := s.row(t, "openai")
	if row.State != auth.StatusSaveFailed || !row.Attention || row.PendingSave == nil || !*row.PendingSave || s.AttentionCount != 1 || row.Action != "retry_save" {
		t.Errorf("pending rotation row = %+v (count %d)", row, s.AttentionCount)
	}
	if !containsString(row.Actions, "retry_save") {
		t.Errorf("actions = %v, want retry_save", row.Actions)
	}
	fail := f.do(http.MethodPost, "/api/providers/openai/retry-save", `{}`)
	if fail.Code != http.StatusServiceUnavailable || !strings.Contains(fail.Body.String(), `"action":"retry_save"`) {
		t.Errorf("retry-save while still failing = %d: %s", fail.Code, fail.Body.String())
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ok := f.do(http.MethodPost, "/api/providers/openai/retry-save", `{}`)
	if ok.Code != http.StatusOK {
		t.Fatalf("retry-save = %d: %s", ok.Code, ok.Body.String())
	}
	if r := decodeProviderJSON[testProviderRow](t, ok); r.State != auth.StatusSaved || r.Attention {
		t.Errorf("after retry-save = %+v, want saved", r)
	}
	if n := f.up.callCount() - calls; n != 0 {
		t.Errorf("status and retry-save made %d upstream calls", n)
	}
}

// Without WithProviderCredentials the routes answer unavailable: they never
// fall back to the default credential file.
func TestProviders_UnavailableWithoutCredentials(t *testing.T) {
	clearProviderTestEnv(t)
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	f := &providerFixture{t: t, handler: NewServer(mgr)}
	for _, rec := range []*httptest.ResponseRecorder{
		f.do(http.MethodGet, "/api/providers/status", ""),
		f.do(http.MethodGet, "/api/providers", ""),
		f.do(http.MethodPost, "/api/providers/openai/oauth/begin", beginBody("")),
	} {
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("unwired = %d, want 503: %s", rec.Code, rec.Body.String())
		}
	}
}
