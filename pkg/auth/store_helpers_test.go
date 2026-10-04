package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// rotatingTokenServer is an httptest OAuth token endpoint with real rotation
// semantics: every refresh token is single-use, and reusing one answers
// invalid_grant like the providers do.
type rotatingTokenServer struct {
	*httptest.Server
	t *testing.T

	mu      sync.Mutex
	valid   map[string]bool
	calls   int
	next    int
	account string // OpenAI account claim for issued access tokens
	// gate, when set, blocks each refresh until the test releases it.
	gate chan struct{}
	// respond, when set, overrides the response for every request.
	respond func(w http.ResponseWriter)
}

func newRotatingTokenServer(t *testing.T, validRefresh ...string) *rotatingTokenServer {
	t.Helper()
	ts := &rotatingTokenServer{t: t, valid: map[string]bool{}}
	for _, r := range validRefresh {
		ts.valid[r] = true
	}
	ts.Server = httptest.NewServer(http.HandlerFunc(ts.handle))
	t.Cleanup(ts.Close)
	return ts
}

func (ts *rotatingTokenServer) handle(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	ts.mu.Lock()
	ts.calls++
	gate, respond := ts.gate, ts.respond
	ts.mu.Unlock()
	if gate != nil {
		<-gate
	}
	if respond != nil {
		respond(w)
		return
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	refresh := r.Form.Get("refresh_token")
	if r.Form.Get("grant_type") != "refresh_token" || !ts.valid[refresh] {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":"invalid_grant"}`)
		return
	}
	delete(ts.valid, refresh)
	ts.next++
	newRefresh := fmt.Sprintf("refresh-%d", ts.next)
	ts.valid[newRefresh] = true
	access := fmt.Sprintf("access-%d", ts.next)
	if ts.account != "" {
		access = testOpenAIJWT(ts.account, ts.next)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": newRefresh, "expires_in": 3600})
}

func (ts *rotatingTokenServer) callCount() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.calls
}

func (ts *rotatingTokenServer) endpoints() tokenEndpoints {
	return tokenEndpoints{client: ts.Client(), anthropic: ts.URL, openai: ts.URL}
}

// testOpenAIJWT builds an unsigned JWT carrying the ChatGPT account claim.
func testOpenAIJWT(account string, n int) string {
	enc := base64.RawURLEncoding
	payload, _ := json.Marshal(map[string]any{
		openaiJWTClaimPath: map[string]any{"chatgpt_account_id": account},
		"n":                n,
	})
	return enc.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + enc.EncodeToString(payload) + ".sig"
}

func newServedStore(t *testing.T, path string, ts *rotatingTokenServer) *Store {
	t.Helper()
	s := NewStore(path)
	s.refresh = ts.endpoints().refresh
	return s
}

func writeAuthFile(t *testing.T, path string, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func seedAuthFile(t *testing.T, path string, creds map[string]Credential) {
	t.Helper()
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeAuthFile(t, path, string(data))
}

func readAuthFile(t *testing.T, path string) map[string]Credential {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]Credential{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func fileBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// makeReadOnlyDir makes real writes into dir fail with EACCES (temp file
// creation), while existing files stay readable and the existing lock file can
// still be opened. Permissions are restored on cleanup.
func makeReadOnlyDir(t *testing.T, dir string) (restore func()) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Fatal("running as root: directory permissions cannot inject write failures")
	}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	restored := false
	restore = func() {
		if !restored {
			restored = true
			_ = os.Chmod(dir, 0700)
		}
	}
	t.Cleanup(restore)
	return restore
}

func expired() int64 { return time.Now().Add(-time.Minute).UnixMilli() }
func fresh() int64   { return time.Now().Add(time.Hour).UnixMilli() }

// wantCredClass returns a copy so callers may ignore it (a pointer would read
// as an unchecked error).
func wantCredClass(t *testing.T, err error, class string) core.ProviderCredentialError {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, want class %s", class)
	}
	pe, ok := core.AsProviderCredentialError(err)
	if !ok {
		t.Fatalf("err = %v (%T), want *core.ProviderCredentialError class %s", err, err, class)
	}
	if pe.Class != class {
		t.Fatalf("class = %s (%v), want %s", pe.Class, err, class)
	}
	return *pe
}

// refreshOAuthIfCurrent is the retired token-only reactive refresh, kept for
// the store tests that exercise the reactive rotation path through it: if the
// rejected token is no longer current it answers with whatever is current,
// which is why production callers use RefreshOAuthIfGeneration instead.
func refreshOAuthIfCurrent(s *Store, provider, rejected string) (string, error) {
	if os.Getenv(envKeyForProvider(provider)) != "" {
		return "", core.NewProviderCredentialError(provider, core.CredentialSourceEnv, "refresh", core.CredentialReconnect)
	}
	snap, err := s.PeekSnapshot(provider)
	if err != nil {
		return "", err
	}
	if snap.Kind != "oauth" {
		return "", core.NewProviderCredentialError(provider, core.CredentialSourceStore, "refresh", core.CredentialMissing)
	}
	if snap.Token != rejected {
		return snap.Token, nil
	}
	next, err := s.RefreshOAuthIfGeneration(context.Background(), snap, rejected)
	if err != nil {
		return "", err
	}
	return next.Token, nil
}
