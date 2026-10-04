package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// TestEnvOpenAIOAuth_NeverBorrowsStore pins the store part of R14: an OpenAI
// OAuth token from the environment takes its account from its own JWT and has
// no store refresh, even when a different login is saved.
func TestEnvOpenAIOAuth_NeverBorrowsStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	ts := newRotatingTokenServer(t, "refresh-store")
	store := newServedStore(t, path, ts)
	if _, err := store.CommitLogin("openai", "", Credential{Type: "oauth", Access: testOpenAIJWT("acct-store", 0), Refresh: "refresh-store", Expires: fresh(), AccountID: "acct-store"}); err != nil {
		t.Fatal(err)
	}
	envToken := testOpenAIJWT("acct-env", 0)
	t.Setenv("OPENAI_API_KEY", envToken)

	if got := store.GetAccountID("openai"); got != "acct-env" {
		t.Fatalf("GetAccountID = %q, want the env JWT account", got)
	}
	for name, get := range map[string]func() (CredentialSnapshot, error){
		"resolve": func() (CredentialSnapshot, error) { return store.ResolveSnapshot(context.Background(), "openai") },
		"peek":    func() (CredentialSnapshot, error) { return store.PeekSnapshot("openai") },
	} {
		snap, err := get()
		if err != nil || snap.Source != core.CredentialSourceEnv || snap.Kind != "oauth" || snap.Token != envToken || snap.AccountID != "acct-env" {
			t.Fatalf("%s snapshot = source %q kind %q account %q, %v", name, snap.Source, snap.Kind, snap.AccountID, err)
		}
	}

	token, err := refreshOAuthIfCurrent(store, "openai", envToken)
	if token != "" {
		t.Fatalf("rejected env token answered with a stored token")
	}
	wantCredClass(t, err, core.CredentialReconnect)
	snap, _ := store.ResolveSnapshot(context.Background(), "openai")
	if _, err := store.RefreshOAuthIfGeneration(context.Background(), snap, envToken); err == nil {
		t.Fatal("env snapshot refreshed through the store")
	}
	if n := ts.callCount(); n != 0 {
		t.Fatalf("store refresh calls = %d, want 0", n)
	}
}

func TestEnvOpenAIOAuth_MalformedAccountDoesNotFallBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	store := NewStore(path)
	if _, err := store.CommitLogin("openai", "", Credential{Type: "oauth", Access: "a", Refresh: "r", Expires: fresh(), AccountID: "acct-store"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "eyJ"+"hbGciOiJub25lIn0.bm90LWpzb24.sig")
	if got := store.GetAccountID("openai"); got != "" {
		t.Fatalf("GetAccountID fell back to %q", got)
	}
	_, err := store.ResolveSnapshot(context.Background(), "openai")
	pe := wantCredClass(t, err, core.CredentialMissing)
	if pe.Source != core.CredentialSourceEnv || pe.Action != "manage_environment" {
		t.Fatalf("error = %+v", pe)
	}
}

func TestEnvSnapshotKinds(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "auth.json"))
	cases := []struct{ provider, env, value, kind string }{
		{"anthropic", "ANTHROPIC_API_KEY", "sk-" + "ant-oat-env", "oauth"},
		{"anthropic", "ANTHROPIC_API_KEY", "sk-" + "ant-api03-env", "api_key"},
		{"openai", "OPENAI_API_KEY", "sk-" + "proj-env", "api_key"},
		{"xai", "XAI_API_KEY", "verylongjwtxx.payload.signature", "api_key"},
		{"meta", "META_API_KEY", "verylongjwtxx.payload.signature", "api_key"},
	}
	for _, c := range cases {
		t.Setenv(c.env, c.value)
		snap, err := store.PeekSnapshot(c.provider)
		if err != nil || snap.Kind != c.kind || snap.Source != core.CredentialSourceEnv || snap.Token != c.value || snap.Generation != "" {
			t.Fatalf("%s %s: %+v, %v", c.provider, c.value, snap, err)
		}
	}
}

func TestSnapshots_StoreKindsAndPeekNeverRefreshes(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("XAI_API_KEY", "")
	path := filepath.Join(t.TempDir(), "auth.json")
	ts := newRotatingTokenServer(t, "refresh-0")
	store := newServedStore(t, path, ts)
	keyGen, err := store.CommitLogin("xai", "", Credential{Type: "api_key", Key: "xai-" + "key"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitLogin("anthropic", "", Credential{Type: "oauth", Access: "access-0", Refresh: "refresh-0", Expires: expired()}); err != nil {
		t.Fatal(err)
	}
	snap, err := store.ResolveSnapshot(context.Background(), "xai")
	if err != nil || snap.Kind != "api_key" || snap.Token != "xai-"+"key" || snap.Generation != keyGen || snap.Source != core.CredentialSourceStore {
		t.Fatalf("xai snapshot = %+v, %v", snap, err)
	}
	peek, err := store.PeekSnapshot("anthropic")
	if err != nil || peek.Kind != "oauth" || peek.Token != "access-0" || !credentialExpired(peek.Credential) {
		t.Fatalf("peek = %+v, %v", peek, err)
	}
	if n := ts.callCount(); n != 0 {
		t.Fatalf("PeekSnapshot refreshed (%d calls)", n)
	}
	_, err = store.ResolveSnapshot(context.Background(), "nobody")
	wantCredClass(t, err, core.CredentialMissing)
}

const upstreamSecret = "SECRET-UPSTREAM-BODY-7f3a"

// TestRefreshErrors_ClassifiedWithoutUpstreamBody: refresh failures carry a
// class and allowlisted metadata, never the upstream body.
func TestRefreshErrors_ClassifiedWithoutUpstreamBody(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		class  string
		code   string
	}{
		{"invalid_grant", 400, `{"error":"invalid_grant","error_description":"` + upstreamSecret + `"}`, core.CredentialReconnect, "invalid_grant"},
		{"server error", 503, upstreamSecret, core.CredentialTemporary, ""},
		{"rate limited", 429, `{"error":"` + upstreamSecret + `"}`, core.CredentialTemporary, "other"},
		{"invalid_client", 401, `{"error":"invalid_client","error_description":"` + upstreamSecret + `"}`, core.CredentialProviderUnavailable, "invalid_client"},
		{"unknown 4xx", 403, upstreamSecret, core.CredentialProviderUnavailable, ""},
		{"empty access", 200, `{"access_token":"","refresh_token":"` + upstreamSecret + `","expires_in":3600}`, core.CredentialProviderUnavailable, ""},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			_, _ = fmt.Fprint(w, c.body)
		}))
		xaiSrv := newXAIRefreshServer(t, c.status, c.body)
		meta := metaEndpoints{TokenURL: srv.URL, MintURL: srv.URL}
		for _, provider := range []string{"anthropic", "openai", "xai", "meta"} {
			t.Run(c.name+"/"+provider, func(t *testing.T) {
				e := tokenEndpoints{client: srv.Client(), anthropic: srv.URL, openai: srv.URL, xai: testXAIEndpoints(xaiSrv), meta: meta}
				_, err := e.refresh(context.Background(), provider, "refresh-0")
				pe := wantCredClass(t, err, c.class)
				if strings.Contains(err.Error(), upstreamSecret) || strings.Contains(fmt.Sprintf("%+v", pe), upstreamSecret) {
					t.Fatalf("upstream body leaked: %v", err)
				}
				if c.status != 200 && (pe.Status != c.status || pe.OAuthCode != c.code) {
					t.Fatalf("metadata = status %d code %q", pe.Status, pe.OAuthCode)
				}
				if pe.Provider != provider || pe.Operation != "refresh" {
					t.Fatalf("error = %+v", pe)
				}
			})
		}
		srv.Close()
	}

	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	_, err := tokenEndpoints{anthropic: dead.URL}.refresh(context.Background(), "anthropic", "r")
	wantCredClass(t, err, core.CredentialTemporary)
}

func newXAIRefreshServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/discovery" {
			_, _ = fmt.Fprintf(w, `{"issuer":%q,"token_endpoint":%q,"device_authorization_endpoint":%q}`, srv.URL, srv.URL+"/token", srv.URL+"/device")
			return
		}
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}
