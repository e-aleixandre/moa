package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// fakeMCPAuth is a protected MCP resource plus its authorization server, both
// real HTTP servers: PRM at the resource, RFC 8414 metadata, DCR and a token
// endpoint on the AS.
type fakeMCPAuth struct {
	resource *httptest.Server
	as       *httptest.Server

	mu            sync.Mutex
	registrations []map[string]any
	tokenForms    []url.Values
	refreshes     atomic.Int32

	// tokenResponse, when set, answers the token endpoint instead of the
	// default happy path.
	tokenResponse func(w http.ResponseWriter, form url.Values)
	// registerResponse, when set, answers the registration endpoint.
	registerResponse func(w http.ResponseWriter)
}

func newFakeMCPAuth(t *testing.T) *fakeMCPAuth {
	t.Helper()
	f := &fakeMCPAuth{}
	f.as = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			writeTestJSON(w, http.StatusOK, map[string]any{
				"issuer":                           f.as.URL,
				"authorization_endpoint":           f.as.URL + "/authorize",
				"token_endpoint":                   f.as.URL + "/token",
				"registration_endpoint":            f.as.URL + "/register",
				"response_types_supported":         []string{"code"},
				"code_challenge_methods_supported": []string{"S256"},
			})
		case "/register":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.registrations = append(f.registrations, body)
			f.mu.Unlock()
			if f.registerResponse != nil {
				f.registerResponse(w)
				return
			}
			writeTestJSON(w, http.StatusCreated, map[string]any{
				"client_id":                  "client-1",
				"redirect_uris":              body["redirect_uris"],
				"token_endpoint_auth_method": "none",
			})
		case "/token":
			_ = r.ParseForm()
			f.mu.Lock()
			f.tokenForms = append(f.tokenForms, r.PostForm)
			f.mu.Unlock()
			if r.PostForm.Get("grant_type") == "refresh_token" {
				f.refreshes.Add(1)
			}
			if f.tokenResponse != nil {
				f.tokenResponse(w, r.PostForm)
				return
			}
			writeTestJSON(w, http.StatusOK, map[string]any{
				"access_token":  "at-1",
				"refresh_token": "rt-1",
				"token_type":    "Bearer",
				"expires_in":    3600,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.as.Close)
	f.resource = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/oauth-protected-resource/mcp" {
			writeTestJSON(w, http.StatusOK, map[string]any{
				"resource":              f.resource.URL + "/mcp",
				"authorization_servers": []string{f.as.URL},
				"scopes_supported":      []string{"read", "write"},
			})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(f.resource.Close)
	return f
}

func (f *fakeMCPAuth) serverURL() string { return f.resource.URL + "/mcp" }

func (f *fakeMCPAuth) lastTokenForm() url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.tokenForms) == 0 {
		return nil
	}
	return f.tokenForms[len(f.tokenForms)-1]
}

func writeTestJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func newTestMCPStore(t *testing.T) (*MCPOAuthStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mcp-oauth.json")
	return MCPOAuthStoreAt(path), path
}

// callbackFor simulates the browser landing on the loopback redirect after
// the user approved: it builds the URL the user would paste.
func callbackFor(t *testing.T, authorizeURL string, extra url.Values) (string, url.Values) {
	t.Helper()
	au, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	q := au.Query()
	cb, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		t.Fatal(err)
	}
	v := url.Values{"code": {"code-123"}, "state": {q.Get("state")}}
	for k, vals := range extra {
		v[k] = vals
	}
	cb.RawQuery = v.Encode()
	return cb.String(), q
}

// Test 1: discovery + dynamic client registration + authorize URL.
func TestMCPOAuthBeginDiscoveryAndRegistration(t *testing.T) {
	f := newFakeMCPAuth(t)
	store, _ := newTestMCPStore(t)

	authorizeURL, err := store.Begin(context.Background(), f.serverURL())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	f.mu.Lock()
	regs := f.registrations
	f.mu.Unlock()
	if len(regs) != 1 {
		t.Fatalf("registrations = %d, want 1", len(regs))
	}
	reg := regs[0]
	uris, _ := reg["redirect_uris"].([]any)
	if len(uris) != 1 {
		t.Fatalf("redirect_uris = %v", reg["redirect_uris"])
	}
	redirect, _ := url.Parse(uris[0].(string))
	if port, _ := strconv.Atoi(redirect.Port()); redirect.Scheme != "http" || redirect.Hostname() != "127.0.0.1" || redirect.Path != "/callback" || port < 49152 || port > 65535 {
		t.Fatalf("redirect uri = %v, want a loopback /callback", redirect)
	}
	if reg["token_endpoint_auth_method"] != "none" {
		t.Fatalf("auth method = %v, want none", reg["token_endpoint_auth_method"])
	}
	grants, _ := json.Marshal(reg["grant_types"])
	if string(grants) != `["authorization_code","refresh_token"]` {
		t.Fatalf("grant_types = %s", grants)
	}
	if reg["client_name"] != "moa" || reg["application_type"] != "native" || reg["scope"] != "read write" {
		t.Fatalf("registration = %v", reg)
	}

	au, _ := url.Parse(authorizeURL)
	if got := au.Scheme + "://" + au.Host + au.Path; got != f.as.URL+"/authorize" {
		t.Fatalf("authorize endpoint = %s", got)
	}
	q := au.Query()
	checks := map[string]string{
		"response_type":         "code",
		"client_id":             "client-1",
		"redirect_uri":          uris[0].(string),
		"code_challenge_method": "S256",
		"resource":              f.serverURL(),
		"scope":                 "read write",
	}
	for k, want := range checks {
		if got := q.Get(k); got != want {
			t.Errorf("authorize %s = %q, want %q", k, got, want)
		}
	}
	if len(q.Get("state")) < 43 || q.Get("code_challenge") == "" {
		t.Fatalf("state/code_challenge missing or short: %v", q)
	}
}

// Test 2: pasted URL → code exchange with verifier, resource and redirect_uri;
// the record is persisted 0600 and subscribers hear about it.
func TestMCPOAuthCompleteExchangesAndPersists(t *testing.T) {
	f := newFakeMCPAuth(t)
	store, path := newTestMCPStore(t)
	var notified []string
	unsubscribe := store.Subscribe(func(key string) { notified = append(notified, key) })
	defer unsubscribe()

	authorizeURL, err := store.Begin(context.Background(), f.serverURL())
	if err != nil {
		t.Fatal(err)
	}
	pasted, q := callbackFor(t, authorizeURL, url.Values{"iss": {f.as.URL}})
	if err := store.Complete(context.Background(), f.serverURL(), pasted); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	form := f.lastTokenForm()
	if form.Get("grant_type") != "authorization_code" || form.Get("code") != "code-123" {
		t.Fatalf("token form = %v", form)
	}
	if got := oauth2.S256ChallengeFromVerifier(form.Get("code_verifier")); got != q.Get("code_challenge") {
		t.Fatalf("code_verifier does not match the challenge")
	}
	if form.Get("resource") != f.serverURL() || form.Get("redirect_uri") != q.Get("redirect_uri") || form.Get("client_id") != "client-1" {
		t.Fatalf("token form = %v", form)
	}

	key := MCPOAuthKey(f.serverURL())
	if len(notified) != 1 || notified[0] != key {
		t.Fatalf("notified = %v, want [%s]", notified, key)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v, want 0600", info.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	var onDisk map[string]MCPOAuthRecord
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatal(err)
	}
	rec := onDisk[key]
	if rec.AccessToken != "at-1" || rec.RefreshToken != "rt-1" || rec.ClientID != "client-1" || rec.Issuer != f.as.URL || rec.NeedsReauth {
		t.Fatalf("record = %+v", rec)
	}
	if rec.ExpiresAt == 0 || rec.ObtainedAt == 0 {
		t.Fatalf("expiry/obtained not recorded: %+v", rec)
	}
	if strings.Contains(string(data), form.Get("code_verifier")) {
		t.Fatal("the PKCE verifier must never be persisted")
	}
	if exists, needs := store.Has(key); !exists || needs {
		t.Fatalf("Has = %v,%v", exists, needs)
	}
	tok, err := store.Token(context.Background(), key)
	if err != nil || tok.AccessToken != "at-1" {
		t.Fatalf("Token = %v, %v", tok, err)
	}
}

func seedMCPRecord(t *testing.T, store *MCPOAuthStore, key string, rec MCPOAuthRecord) {
	t.Helper()
	if err := store.withFileLock(context.Background(), func() error {
		store.put(key, rec)
		return store.save()
	}); err != nil {
		t.Fatal(err)
	}
}

func seededRecord(f *fakeMCPAuth, access, refresh string, expiresIn time.Duration) MCPOAuthRecord {
	return MCPOAuthRecord{
		ServerURL:     f.serverURL(),
		Resource:      f.serverURL(),
		Issuer:        f.as.URL,
		TokenEndpoint: f.as.URL + "/token",
		ClientID:      "client-1",
		AuthStyle:     int(oauth2.AuthStyleInParams),
		AccessToken:   access,
		RefreshToken:  refresh,
		ExpiresAt:     time.Now().Add(expiresIn).UnixMilli(),
		ObtainedAt:    time.Now().Add(-time.Hour).UnixMilli(),
	}
}

// Test 3: near-expiry access refreshes exactly once for N concurrent callers;
// the rotated refresh token is persisted, and a response without one keeps it.
func TestMCPOAuthRefreshSingleFlightAndRotation(t *testing.T) {
	f := newFakeMCPAuth(t)
	var seq atomic.Int32
	rotate := true
	f.tokenResponse = func(w http.ResponseWriter, form url.Values) {
		time.Sleep(50 * time.Millisecond) // widen the race window
		n := seq.Add(1)
		body := map[string]any{"access_token": "at-" + strconv.Itoa(int(n)+1), "token_type": "Bearer", "expires_in": 3600}
		if rotate {
			body["refresh_token"] = "rt-2"
		}
		writeTestJSON(w, http.StatusOK, body)
	}
	store, path := newTestMCPStore(t)
	key := MCPOAuthKey(f.serverURL())
	seedMCPRecord(t, store, key, seededRecord(f, "at-1", "rt-1", 30*time.Second))

	const n = 10
	var wg sync.WaitGroup
	got := make([]string, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := store.Token(context.Background(), key)
			errs[i] = err
			if tok != nil {
				got[i] = tok.AccessToken
			}
		}()
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil || got[i] != "at-2" {
			t.Fatalf("caller %d: token %q err %v, want at-2", i, got[i], errs[i])
		}
	}
	if c := f.refreshes.Load(); c != 1 {
		t.Fatalf("refreshes = %d, want 1 (single-flight)", c)
	}
	if form := f.lastTokenForm(); form.Get("refresh_token") != "rt-1" {
		t.Fatalf("refresh used %q, want rt-1", form.Get("refresh_token"))
	}
	data, _ := os.ReadFile(path)
	var onDisk map[string]MCPOAuthRecord
	_ = json.Unmarshal(data, &onDisk)
	if onDisk[key].RefreshToken != "rt-2" || onDisk[key].AccessToken != "at-2" {
		t.Fatalf("persisted = %+v, want rotated rt-2/at-2", onDisk[key])
	}

	// No refresh_token in the response: the rotated one stays.
	rotate = false
	rec, _ := store.get(key)
	rec.ExpiresAt = time.Now().Add(10 * time.Second).UnixMilli()
	rec.ObtainedAt = time.Now().Add(-time.Hour).UnixMilli()
	seedMCPRecord(t, store, key, rec)
	tok, err := store.Token(context.Background(), key)
	if err != nil || tok.AccessToken != "at-3" {
		t.Fatalf("second refresh = %v, %v", tok, err)
	}
	data, _ = os.ReadFile(path)
	_ = json.Unmarshal(data, &onDisk)
	if onDisk[key].RefreshToken != "rt-2" {
		t.Fatalf("refresh token = %q, want rt-2 kept", onDisk[key].RefreshToken)
	}
}

// A refresh that the AS rejects as invalid_grant persists needs_reauth and
// drops the access token; a transient 5xx leaves the tokens untouched.
func TestMCPOAuthRefreshFailureClassification(t *testing.T) {
	f := newFakeMCPAuth(t)
	status := http.StatusServiceUnavailable
	f.tokenResponse = func(w http.ResponseWriter, form url.Values) {
		writeTestJSON(w, status, map[string]any{"error": "temporarily_unavailable"})
	}
	store, _ := newTestMCPStore(t)
	key := MCPOAuthKey(f.serverURL())
	seedMCPRecord(t, store, key, seededRecord(f, "at-1", "rt-1", time.Hour))

	err := store.RefreshIfCurrent(context.Background(), key, "at-1")
	if err == nil || errors.Is(err, ErrMCPAuthRequired) {
		t.Fatalf("transient failure err = %v, want a non-auth error", err)
	}
	if rec, _ := store.get(key); rec.AccessToken != "at-1" || rec.NeedsReauth {
		t.Fatalf("transient failure changed the record: %+v", rec)
	}

	// Someone else already rotated: nothing to do.
	if err := store.RefreshIfCurrent(context.Background(), key, "older-token"); err != nil {
		t.Fatalf("stale rejection err = %v, want nil", err)
	}

	status = http.StatusBadRequest
	f.tokenResponse = func(w http.ResponseWriter, form url.Values) {
		writeTestJSON(w, status, map[string]any{"error": "invalid_grant"})
	}
	if err := store.RefreshIfCurrent(context.Background(), key, "at-1"); !errors.Is(err, ErrMCPAuthRequired) {
		t.Fatalf("invalid_grant err = %v, want ErrMCPAuthRequired", err)
	}
	if exists, needs := store.Has(key); !exists || !needs {
		t.Fatalf("Has = %v,%v, want needs reauth", exists, needs)
	}
	if _, err := store.Token(context.Background(), key); !errors.Is(err, ErrMCPAuthRequired) {
		t.Fatalf("Token err = %v, want ErrMCPAuthRequired", err)
	}
}

// A token the server rejects right after it was issued is not refreshed again
// (that would loop): it goes straight to needs_reauth.
func TestMCPOAuthFreshTokenRejectedNeedsReauth(t *testing.T) {
	f := newFakeMCPAuth(t)
	store, _ := newTestMCPStore(t)
	key := MCPOAuthKey(f.serverURL())
	rec := seededRecord(f, "at-1", "rt-1", time.Hour)
	rec.ObtainedAt = time.Now().UnixMilli()
	seedMCPRecord(t, store, key, rec)

	if err := store.RefreshIfCurrent(context.Background(), key, "at-1"); !errors.Is(err, ErrMCPAuthRequired) {
		t.Fatalf("err = %v, want ErrMCPAuthRequired", err)
	}
	if c := f.refreshes.Load(); c != 0 {
		t.Fatalf("refreshes = %d, want 0", c)
	}
}

// Test 6: pasted URLs that don't match the in-flight authorization.
func TestMCPOAuthCompleteRejectsMismatchedCallbacks(t *testing.T) {
	f := newFakeMCPAuth(t)
	ctx := context.Background()

	begin := func(t *testing.T, store *MCPOAuthStore) (string, url.Values) {
		t.Helper()
		authorizeURL, err := store.Begin(ctx, f.serverURL())
		if err != nil {
			t.Fatal(err)
		}
		return callbackFor(t, authorizeURL, nil)
	}
	mutate := func(t *testing.T, pasted string, fn func(u *url.URL)) string {
		t.Helper()
		u, _ := url.Parse(pasted)
		fn(u)
		return u.String()
	}

	t.Run("wrong state does not consume the real one", func(t *testing.T) {
		store, _ := newTestMCPStore(t)
		pasted, _ := begin(t, store)
		bad := mutate(t, pasted, func(u *url.URL) {
			q := u.Query()
			q.Set("state", "not-the-state")
			u.RawQuery = q.Encode()
		})
		if err := store.Complete(ctx, f.serverURL(), bad); !errors.Is(err, ErrMCPOAuthNoPending) {
			t.Fatalf("err = %v, want ErrMCPOAuthNoPending", err)
		}
		if err := store.Complete(ctx, f.serverURL(), pasted); err != nil {
			t.Fatalf("real callback after a wrong one: %v", err)
		}
	})

	t.Run("other server", func(t *testing.T) {
		store, _ := newTestMCPStore(t)
		pasted, _ := begin(t, store)
		if err := store.Complete(ctx, "https://other.example/mcp", pasted); !errors.Is(err, ErrMCPOAuthNoPending) {
			t.Fatalf("err = %v, want ErrMCPOAuthNoPending", err)
		}
		if err := store.Complete(ctx, f.serverURL(), pasted); err != nil {
			t.Fatalf("pending was consumed by another server's paste: %v", err)
		}
	})

	for _, tc := range []struct {
		name string
		fn   func(u *url.URL)
	}{
		{"wrong host", func(u *url.URL) { u.Host = "localhost:" + u.Port() }},
		{"wrong port", func(u *url.URL) { u.Host = "127.0.0.1:1" }},
		{"wrong path", func(u *url.URL) { u.Path = "/other" }},
		{"iss mismatch", func(u *url.URL) {
			q := u.Query()
			q.Set("iss", "https://evil.example")
			u.RawQuery = q.Encode()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := newTestMCPStore(t)
			pasted, _ := begin(t, store)
			if err := store.Complete(ctx, f.serverURL(), mutate(t, pasted, tc.fn)); !errors.Is(err, ErrMCPOAuthNoPending) {
				t.Fatalf("err = %v, want ErrMCPOAuthNoPending", err)
			}
			if exists, _ := store.Has(MCPOAuthKey(f.serverURL())); exists {
				t.Fatal("a rejected callback stored credentials")
			}
		})
	}

	t.Run("access denied", func(t *testing.T) {
		store, _ := newTestMCPStore(t)
		pasted, _ := begin(t, store)
		denied := mutate(t, pasted, func(u *url.URL) {
			q := u.Query()
			q.Del("code")
			q.Set("error", "access_denied")
			q.Set("error_description", "user said no")
			u.RawQuery = q.Encode()
		})
		err := store.Complete(ctx, f.serverURL(), denied)
		if !errors.Is(err, ErrMCPOAuthDenied) || !strings.Contains(err.Error(), "access_denied") || strings.Contains(err.Error(), "user said no") {
			t.Fatalf("err = %v, want denied with only the code", err)
		}
	})

	t.Run("expired pending", func(t *testing.T) {
		store, _ := newTestMCPStore(t)
		pasted, q := begin(t, store)
		store.mu.Lock()
		store.pending[q.Get("state")].created = time.Now().Add(-mcpPendingTTL - time.Minute)
		store.mu.Unlock()
		if err := store.Complete(ctx, f.serverURL(), pasted); !errors.Is(err, ErrMCPOAuthNoPending) {
			t.Fatalf("err = %v, want ErrMCPOAuthNoPending", err)
		}
	})

	t.Run("replay", func(t *testing.T) {
		store, _ := newTestMCPStore(t)
		pasted, _ := begin(t, store)
		if err := store.Complete(ctx, f.serverURL(), pasted); err != nil {
			t.Fatal(err)
		}
		if err := store.Complete(ctx, f.serverURL(), pasted); !errors.Is(err, ErrMCPOAuthNoPending) {
			t.Fatalf("replay err = %v, want ErrMCPOAuthNoPending", err)
		}
	})

	t.Run("new begin supersedes the previous one", func(t *testing.T) {
		store, _ := newTestMCPStore(t)
		first, _ := begin(t, store)
		second, _ := begin(t, store)
		if err := store.Complete(ctx, f.serverURL(), first); !errors.Is(err, ErrMCPOAuthNoPending) {
			t.Fatalf("superseded err = %v, want ErrMCPOAuthNoPending", err)
		}
		if err := store.Complete(ctx, f.serverURL(), second); err != nil {
			t.Fatal(err)
		}
	})
}

// Test 10: errors never carry tokens, codes, verifiers, secrets or raw bodies.
func TestMCPOAuthErrorsAreRedacted(t *testing.T) {
	const secret = "SUPERSECRET-body-value"
	ctx := context.Background()

	t.Run("exchange", func(t *testing.T) {
		f := newFakeMCPAuth(t)
		f.tokenResponse = func(w http.ResponseWriter, form url.Values) {
			writeTestJSON(w, http.StatusBadRequest, map[string]any{
				"error":             "invalid_request",
				"error_description": "bad code " + form.Get("code") + " verifier " + form.Get("code_verifier") + " " + secret,
			})
		}
		store, _ := newTestMCPStore(t)
		authorizeURL, err := store.Begin(ctx, f.serverURL())
		if err != nil {
			t.Fatal(err)
		}
		pasted, _ := callbackFor(t, authorizeURL, nil)
		err = store.Complete(ctx, f.serverURL(), pasted)
		if err == nil {
			t.Fatal("expected an exchange error")
		}
		verifier := f.lastTokenForm().Get("code_verifier")
		for _, leak := range []string{secret, "code-123", verifier} {
			if strings.Contains(err.Error(), leak) {
				t.Fatalf("error %q leaks %q", err, leak)
			}
		}
		if !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "invalid_request") {
			t.Fatalf("error %q should keep status and OAuth code", err)
		}
	})

	t.Run("refresh", func(t *testing.T) {
		f := newFakeMCPAuth(t)
		f.tokenResponse = func(w http.ResponseWriter, form url.Values) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, "refresh "+form.Get("refresh_token")+" "+secret)
		}
		store, _ := newTestMCPStore(t)
		key := MCPOAuthKey(f.serverURL())
		seedMCPRecord(t, store, key, seededRecord(f, "at-secret-access", "rt-secret-refresh", time.Second))
		_, err := store.Token(ctx, key)
		if err == nil {
			t.Fatal("expected a refresh error")
		}
		for _, leak := range []string{secret, "rt-secret-refresh", "at-secret-access"} {
			if strings.Contains(err.Error(), leak) {
				t.Fatalf("error %q leaks %q", err, leak)
			}
		}
	})

	t.Run("registration", func(t *testing.T) {
		f := newFakeMCPAuth(t)
		f.registerResponse = func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"client_secret":"`+secret+`"}`)
		}
		store, _ := newTestMCPStore(t)
		_, err := store.Begin(ctx, f.serverURL())
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("registration error = %v, want one without the body", err)
		}
	})
}

func TestMCPOAuthKeyNormalization(t *testing.T) {
	for in, want := range map[string]string{
		"HTTPS://MCP.Example.com:443/":        "https://mcp.example.com",
		"https://mcp.example.com/mcp#frag":    "https://mcp.example.com/mcp",
		"http://Host:80/a/?x=1":               "http://host/a/?x=1",
		"http://127.0.0.1:8080/mcp":           "http://127.0.0.1:8080/mcp",
		"https://mcp.example.com/Path/Casing": "https://mcp.example.com/Path/Casing",
	} {
		if got := MCPOAuthKey(in); got != want {
			t.Errorf("MCPOAuthKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// With no config directory the store holds nothing and refuses to write.
func TestMCPOAuthStoreWithoutPathFailsClosed(t *testing.T) {
	store := MCPOAuthStoreAt("")
	if tok, err := store.Token(context.Background(), "https://x.example"); tok != nil || err != nil {
		t.Fatalf("Token = %v, %v, want nil, nil", tok, err)
	}
	if err := store.withFileLock(context.Background(), func() error { return nil }); err == nil {
		t.Fatal("expected an error without a config directory")
	}
}

// A public MCP server's metadata must not steer OAuth requests at internal
// addresses (SSRF): discovered endpoints must be https and every dialed
// address public. The fake servers are all on 127.0.0.1, so the test marks
// every port except the internal target's as "public".
func TestMCPOAuthDiscoveryRefusesInternalTargets(t *testing.T) {
	var internalHits atomic.Int32
	internal := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		internalHits.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(internal.Close)
	internalHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		internalHits.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(internalHTTP.Close)
	internalPort := func(ap netip.AddrPort) bool {
		for _, s := range []*httptest.Server{internal, internalHTTP} {
			if strings.HasSuffix(s.URL, ":"+strconv.Itoa(int(ap.Port()))) {
				return true
			}
		}
		return false
	}

	// as is a cross-origin authorization server (Lovable: resource
	// mcp.lovable.dev, AS lovable.dev); it must keep working.
	var as *httptest.Server
	as = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			writeTestJSON(w, http.StatusOK, map[string]any{
				"issuer":                           as.URL,
				"authorization_endpoint":           as.URL + "/authorize",
				"token_endpoint":                   as.URL + "/token",
				"registration_endpoint":            as.URL + "/register",
				"code_challenge_methods_supported": []string{"S256"},
			})
		case "/register":
			writeTestJSON(w, http.StatusCreated, map[string]any{"client_id": "c", "token_endpoint_auth_method": "none"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(as.Close)

	var resource *httptest.Server
	var prmAS, challengeMeta string
	resource = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/oauth-protected-resource/mcp" && prmAS != "" {
			writeTestJSON(w, http.StatusOK, map[string]any{
				"resource":              resource.URL + "/mcp",
				"authorization_servers": []string{prmAS},
			})
			return
		}
		if r.URL.Path == "/mcp" && challengeMeta != "" {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+challengeMeta+`"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(resource.Close)

	newStore := func(t *testing.T) *MCPOAuthStore {
		store, _ := newTestMCPStore(t)
		store.addrPublic = func(ap netip.AddrPort) bool { return !internalPort(ap) }
		tlsBase := resource.Client().Transport.(*http.Transport)
		store.strictClient = newMCPHTTPClient(tlsBase, func(ap netip.AddrPort) bool { return store.addrPublic(ap) })
		return store
	}
	serverURL := resource.URL + "/mcp"

	for _, tc := range []struct {
		name, prmAS, challengeMeta string
		wantOK                     bool
	}{
		{name: "public cross-origin AS works", prmAS: as.URL, wantOK: true},
		{name: "PRM names an internal AS", prmAS: internal.URL},
		{name: "PRM names an http AS", prmAS: internalHTTP.URL},
		{name: "challenge names internal metadata", challengeMeta: internal.URL + "/prm"},
		{name: "challenge names http metadata", challengeMeta: internalHTTP.URL + "/prm"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prmAS, challengeMeta = tc.prmAS, tc.challengeMeta
			_, err := newStore(t).Begin(context.Background(), serverURL)
			if tc.wantOK != (err == nil) {
				t.Fatalf("Begin err = %v, want ok=%v", err, tc.wantOK)
			}
			if n := internalHits.Load(); n != 0 {
				t.Fatalf("internal target was hit %d times", n)
			}
		})
	}
}

func TestIsPublicAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8:443": true, "[2606:4700::1]:443": true,
		"127.0.0.1:443": false, "10.1.2.3:443": false, "172.16.0.1:443": false, "192.168.1.1:443": false,
		"169.254.169.254:80": false, "0.0.0.0:443": false, "100.64.0.1:443": false, "100.127.255.254:443": false,
		"224.0.0.1:443": false, "[::1]:443": false, "[fd00::1]:443": false, "[fe80::1]:443": false,
		"[::]:443": false, "[::ffff:127.0.0.1]:443": false, "[ff02::1]:443": false,
		"198.18.0.1:443": false, "240.0.0.1:443": false, "192.0.2.1:443": false, "[64:ff9b::a00:1]:443": false, "[2002:a00:1::1]:443": false,
	} {
		if got := isPublicAddr(netip.MustParseAddrPort(addr)); got != want {
			t.Errorf("isPublicAddr(%s) = %v, want %v", addr, got, want)
		}
	}
}

// A credential file that cannot be decoded is never overwritten: Complete
// fails and the file stays byte for byte as it was.
func TestMCPOAuthCorruptFileIsNotOverwritten(t *testing.T) {
	f := newFakeMCPAuth(t)
	path := filepath.Join(t.TempDir(), "mcp-oauth.json")
	corrupt := []byte(`{"https://x.example": {"access_token": "keep-me"`)
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	store := MCPOAuthStoreAt(path)

	authorizeURL, err := store.Begin(context.Background(), f.serverURL())
	if err != nil {
		t.Fatal(err)
	}
	pasted, _ := callbackFor(t, authorizeURL, nil)
	err = store.Complete(context.Background(), f.serverURL(), pasted)
	if err == nil || !strings.Contains(err.Error(), "corrupt") || strings.Contains(err.Error(), "keep-me") {
		t.Fatalf("Complete err = %v, want a redacted corrupt-file error", err)
	}
	if got, _ := os.ReadFile(path); string(got) != string(corrupt) {
		t.Fatalf("file was rewritten:\n%s", got)
	}

	// The refresh path refuses to save as well.
	store.put("k", seededRecord(f, "at-1", "rt-1", time.Second))
	if _, err := store.Token(context.Background(), "k"); err == nil {
		t.Fatal("refresh over a corrupt file must fail")
	}
	if got, _ := os.ReadFile(path); string(got) != string(corrupt) {
		t.Fatalf("file was rewritten by refresh:\n%s", got)
	}
}

// A short-lived token (60s) is not refreshed on every request: the skew is a
// tenth of its lifetime, not the fixed 2 minutes.
func TestMCPOAuthShortLivedTokenIsNotRefreshedEachCall(t *testing.T) {
	f := newFakeMCPAuth(t)
	f.tokenResponse = func(w http.ResponseWriter, form url.Values) {
		writeTestJSON(w, http.StatusOK, map[string]any{
			"access_token": "at-1", "refresh_token": "rt-1", "token_type": "Bearer", "expires_in": 60,
		})
	}
	store, _ := newTestMCPStore(t)
	authorizeURL, err := store.Begin(context.Background(), f.serverURL())
	if err != nil {
		t.Fatal(err)
	}
	pasted, _ := callbackFor(t, authorizeURL, nil)
	if err := store.Complete(context.Background(), f.serverURL(), pasted); err != nil {
		t.Fatal(err)
	}
	key := MCPOAuthKey(f.serverURL())
	for range 5 {
		if tok, err := store.Token(context.Background(), key); err != nil || tok.AccessToken != "at-1" {
			t.Fatalf("Token = %v, %v", tok, err)
		}
	}
	f.mu.Lock()
	exchanges := len(f.tokenForms)
	f.mu.Unlock()
	if exchanges != 1 || f.refreshes.Load() != 0 {
		t.Fatalf("token requests = %d, refreshes = %d; want 1 exchange and no refresh", exchanges, f.refreshes.Load())
	}
	rec, _ := store.get(key)
	if rec.nearExpiry(time.UnixMilli(rec.ExpiresAt - 7000)) {
		t.Fatal("7s before expiry of a 60s token should not be near expiry")
	}
	if !rec.nearExpiry(time.UnixMilli(rec.ExpiresAt - 5000)) {
		t.Fatal("5s before expiry should be near expiry")
	}

	// A token living only 1s is still usable right after it is issued.
	now := time.Now()
	tiny := MCPOAuthRecord{ObtainedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Second).UnixMilli()}
	if tiny.nearExpiry(now) {
		t.Fatal("a freshly issued 1s token should not be near expiry")
	}
}

// Only the URL decides whether a server is local: a hostname is never
// resolved, so DNS cannot lift the restrictions for a public server.
func TestMCPOAuthServerIsLocalIgnoresDNS(t *testing.T) {
	store := MCPOAuthStoreAt(filepath.Join(t.TempDir(), mcpOAuthFileName))
	for raw, want := range map[string]bool{
		"http://localhost:3000/mcp":       true,
		"http://api.localhost/mcp":        true,
		"http://127.0.0.1:3000/mcp":       true,
		"http://10.0.0.5/mcp":             true,
		"https://localhost.evil.test/mcp": false,
		"https://mcp.lovable.dev/?src=x":  false,
		"https://127.0.0.1.nip.io/mcp":    false,
	} {
		if got := store.serverIsLocal(raw); got != want {
			t.Errorf("serverIsLocal(%s) = %v, want %v", raw, got, want)
		}
	}
}

// A sibling process holding the file lock makes a refresh fail with a
// transient error once ctx ends, instead of blocking without bound.
func TestMCPOAuthRefreshLockWaitIsBounded(t *testing.T) {
	f := newFakeMCPAuth(t)
	store, path := newTestMCPStore(t)
	key := MCPOAuthKey(f.serverURL())
	seedMCPRecord(t, store, key, seededRecord(f, "at-1", "rt-1", time.Second))

	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- withPlatformFileLock(path+".lock", func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := store.Token(ctx, key)
	if !errors.Is(err, errMCPLockBusy) || time.Since(start) > 2*time.Second {
		t.Fatalf("Token err = %v after %v, want errMCPLockBusy promptly", err, time.Since(start))
	}
	if f.refreshes.Load() != 0 {
		t.Fatal("refreshed without the lock")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if tok, err := store.Token(context.Background(), key); err != nil || tok.AccessToken != "at-1" {
		t.Fatalf("Token after release = %v, %v", tok, err)
	}
}

// Only the client authentication methods moa can perform are accepted.
func TestMCPOAuthRejectsUnsupportedClientAuthMethod(t *testing.T) {
	for method, wantOK := range map[string]bool{
		"none": true, "client_secret_post": true, "client_secret_basic": true, "": true,
		"private_key_jwt": false, "tls_client_auth": false,
	} {
		t.Run("method "+method, func(t *testing.T) {
			f := newFakeMCPAuth(t)
			f.registerResponse = func(w http.ResponseWriter) {
				body := map[string]any{"client_id": "c", "client_secret": "SECRET-xyz"}
				if method != "" {
					body["token_endpoint_auth_method"] = method
				}
				writeTestJSON(w, http.StatusCreated, body)
			}
			store, _ := newTestMCPStore(t)
			_, err := store.Begin(context.Background(), f.serverURL())
			if wantOK != (err == nil) {
				t.Fatalf("Begin err = %v, want ok=%v", err, wantOK)
			}
			if err != nil && (!strings.Contains(err.Error(), "unsupported client authentication method") || strings.Contains(err.Error(), method) || strings.Contains(err.Error(), "SECRET")) {
				t.Fatalf("err = %q, want a redacted unsupported-method error", err)
			}
		})
	}
}

// A discovered endpoint becomes a browser link, so even a local server cannot
// hand out a non-http(s) URL such as javascript:.
func TestRequireHTTPSRejectsNonHTTPSchemes(t *testing.T) {
	for _, tc := range []struct {
		strict bool
		raw    string
		ok     bool
	}{
		{false, "http://127.0.0.1:3000/authorize", true},
		{false, "https://auth.example/authorize", true},
		{false, "javascript:alert(1)//x", false},
		{false, "data:text/html,x", false},
		{true, "http://auth.example/authorize", false},
		{true, "https://auth.example/authorize", true},
	} {
		if err := requireHTTPS(tc.strict, "authorization endpoint", tc.raw); (err == nil) != tc.ok {
			t.Errorf("requireHTTPS(%v, %q) = %v, want ok=%v", tc.strict, tc.raw, err, tc.ok)
		}
	}
}
