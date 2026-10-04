package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/serve"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureOutput redirects slog (and through it the log package) and
// os.Stderr until the returned function is called.
func captureOutput(t *testing.T) func() string {
	t.Helper()
	logs := &syncBuffer{}
	prevLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	prevErr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	stderr := &syncBuffer{}
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(stderr, r)
		close(done)
	}()
	var once sync.Once
	stop := func() string {
		once.Do(func() {
			os.Stderr = prevErr
			slog.SetDefault(prevLog)
			_ = w.Close()
			<-done
		})
		return logs.String() + stderr.String()
	}
	t.Cleanup(func() { stop() })
	return stop
}

type sessionHarness struct {
	mgr  *serve.Manager
	srv  *httptest.Server
	base string
}

func newSessionHarness(t *testing.T, f *runtimeFixture) *sessionHarness {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cfg := core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"}
	base := filepath.Join(t.TempDir(), "state", "sessions")
	mgr := serve.NewManager(ctx, serve.ManagerConfig{
		ProviderFactory: func(m core.Model) (core.Provider, error) {
			built, err := buildProviderWithClient(m, f.store, f.u.client())
			if err != nil {
				return nil, err
			}
			return built.Provider, nil
		},
		DefaultModel:   openaiModel,
		WorkspaceRoot:  t.TempDir(),
		MoaCfg:         cfg,
		ConfigLoader:   func(string) core.MoaConfig { return cfg },
		SessionBaseDir: base,
		SchedulePath:   filepath.Join(t.TempDir(), "schedules.json"),
	})
	srv := httptest.NewServer(serve.NewServer(mgr))
	t.Cleanup(func() {
		srv.Close()
		for _, s := range mgr.List() {
			_ = mgr.Delete(s.ID)
		}
		mgr.Shutdown()
		cancel()
	})
	return &sessionHarness{mgr: mgr, srv: srv, base: base}
}

func (h *sessionHarness) dial(t *testing.T, ctx context.Context, id string) (*websocket.Conn, json.RawMessage) {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, h.srv.URL+"/api/sessions/"+id+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.SetReadLimit(-1)
	var init json.RawMessage
	if err := wsjson.Read(ctx, conn, &init); err != nil {
		t.Fatal(err)
	}
	return conn, init
}

func (h *sessionHarness) get(t *testing.T, path string) []byte {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, h.srv.URL+path, nil)
	req.Header.Set("X-Moa-Request", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d %s", path, resp.StatusCode, body)
	}
	return body
}

func (h *sessionHarness) transcripts(t *testing.T, want string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var all strings.Builder
		_ = filepath.WalkDir(filepath.Dir(h.base), func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				if data, rerr := os.ReadFile(path); rerr == nil {
					all.Write(data)
				}
			}
			return nil
		})
		if strings.Contains(all.String(), want) || time.Now().After(deadline) {
			return all.String()
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type sessionSurfaces struct {
	stateChange json.RawMessage // the live error state_change event
	init        json.RawMessage // WS init after reconnecting
	roster      []byte
	transcript  string
	history     string // the agent's model-visible history
	logs        string
}

func (s sessionSurfaces) all() map[string]string {
	return map[string]string{
		"state_change": string(s.stateChange),
		"ws_init":      string(s.init),
		"roster":       string(s.roster),
		"transcript":   s.transcript,
		"history":      s.history,
		"logs/stderr":  s.logs,
	}
}

// runFailingTurn sends one message through the real session path and
// collects every output surface once the session settled in error.
func runFailingTurn(t *testing.T, f *runtimeFixture) sessionSurfaces {
	t.Helper()
	stop := captureOutput(t)
	h := newSessionHarness(t, f)
	sess, err := h.mgr.CreateSession(serve.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, _ := h.dial(t, ctx, sess.ID)
	defer conn.CloseNow() //nolint:errcheck

	if _, _, _, err := h.mgr.Send(sess.ID, "hello", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	var out sessionSurfaces
	for out.stateChange == nil {
		var raw json.RawMessage
		if err := wsjson.Read(ctx, conn, &raw); err != nil {
			t.Fatalf("waiting for the error state: %v", err)
		}
		var ev struct {
			Type string `json:"type"`
			Data struct {
				State string `json:"state"`
			} `json:"data"`
		}
		_ = json.Unmarshal(raw, &ev)
		if ev.Type == "state_change" && ev.Data.State == "error" {
			out.stateChange = raw
		}
	}
	conn2, init := h.dial(t, ctx, sess.ID)
	defer conn2.CloseNow() //nolint:errcheck
	out.init = init
	out.roster = h.get(t, "/api/sessions")
	out.transcript = h.transcripts(t, "(stopped")
	msgs, _ := json.Marshal(sess.History())
	out.history = string(msgs)
	out.logs = stop()
	return out
}

func assertNoSecrets(t *testing.T, s sessionSurfaces, secrets ...string) {
	t.Helper()
	for surface, text := range s.all() {
		for _, secret := range secrets {
			if strings.Contains(text, secret) {
				t.Errorf("%s contains secret %q", surface, secret)
			}
		}
	}
	if !strings.Contains(s.history, "(stopped") || !strings.Contains(s.transcript, "(stopped") {
		t.Errorf("no stopped marker reached history/transcript; the scan saw nothing")
	}
}

func errorDetail(t *testing.T, surface string, raw []byte, path ...string) map[string]any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s: %v", surface, err)
	}
	for _, p := range path {
		switch x := v.(type) {
		case map[string]any:
			v = x[p]
		case []any:
			if len(x) == 0 {
				return nil
			}
			v = x[0]
			if m, ok := v.(map[string]any); ok {
				v = m[p]
			}
		}
	}
	m, _ := v.(map[string]any)
	return m
}

// R15/R16: a classified credential failure reaches the session's state, the
// live and reconnect WebSocket payloads and the roster as structured detail,
// and no output surface carries a token, refresh token, key or upstream body.
func TestRuntimeSession_CredentialErrorIsStructuredAndSafe(t *testing.T) {
	f := newRuntimeFixture(t)
	access1 := openaiAccess("acct-a", "ACCESS-SENTINEL-1")
	access2 := openaiAccess("acct-a", "ACCESS-SENTINEL-2")
	gen := f.commit(t, f.store, "openai", oauthCred(access1, "REFRESH-SENTINEL-1", "acct-a"))
	f.u.token = func(w http.ResponseWriter, _ upstreamCall, _ int) {
		writeTokens(w, access2, "REFRESH-SENTINEL-2")
	}
	f.u.inference = func(w http.ResponseWriter, c upstreamCall, _ int) {
		writeStatus(w, http.StatusUnauthorized, fmt.Sprintf(`{"error":{"message":"bad token %s code CODE-SENTINEL-1 BODY-SENTINEL"}}`, c.Bearer))
	}
	s := runFailingTurn(t, f)
	assertNoSecrets(t, s, "ACCESS-SENTINEL", "REFRESH-SENTINEL", "CODE-SENTINEL", "BODY-SENTINEL", access1, access2)

	want := map[string]any{"provider": "openai", "source": "store", "credential_generation": gen, "class": "reconnect", "action": "reconnect"}
	for surface, d := range map[string]map[string]any{
		"state_change": errorDetail(t, "state_change", s.stateChange, "data", "error_detail"),
		"ws_init":      errorDetail(t, "ws_init", s.init, "data", "error_detail"),
		"roster":       errorDetail(t, "roster", s.roster, "error_detail"),
	} {
		if fmt.Sprint(d) != fmt.Sprint(want) {
			t.Errorf("%s error_detail = %v, want %v", surface, d, want)
		}
	}
	if d := errorDetail(t, "ws_init", s.init, "data"); d["error"] == nil || d["error"] == "" {
		t.Errorf("ws_init has no session error: %v", d["error"])
	}
	// The rotation itself is durable: the stored secrets are where they belong.
	if cred, _ := auth.NewStore(f.path).Get("openai"); cred.Access != access2 || cred.Refresh != "REFRESH-SENTINEL-2" {
		t.Errorf("stored credential = %+v", cred)
	}
}

// R16: errors that are not credential failures are not rewritten, but the
// values of the credential the request used never reach an output surface,
// whether they come back in an HTTP error body or a stream error frame.
func TestRuntimeSession_UpstreamErrorsNeverEchoTheCredential(t *testing.T) {
	access := openaiAccess("acct-a", "ACCESS-SENTINEL-3")
	echo400 := func(w http.ResponseWriter, c upstreamCall, _ int) {
		writeStatus(w, http.StatusBadRequest, fmt.Sprintf(`{"error":{"message":"invalid request for %s"}}`, c.Bearer))
	}
	echoFrame := func(w http.ResponseWriter, c upstreamCall, _ int) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"error\",\"message\":\"rejected %s\"}\n\n", c.Bearer)
	}
	cases := []struct {
		name      string
		cred      auth.Credential
		inference func(http.ResponseWriter, upstreamCall, int)
		secrets   []string
	}{
		{"oauth_http_body", oauthCred(access, "REFRESH-SENTINEL-3", "acct-a"), echo400, []string{"ACCESS-SENTINEL", "REFRESH-SENTINEL", access}},
		{"api_key_http_body", apiKeyCred("sk-" + "proj-KEY-SENTINEL-4"), echo400, []string{"KEY-SENTINEL"}},
		{"oauth_stream_frame", oauthCred(access, "REFRESH-SENTINEL-3", "acct-a"), echoFrame, []string{"ACCESS-SENTINEL", "REFRESH-SENTINEL", access}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRuntimeFixture(t)
			f.commit(t, f.store, "openai", tc.cred)
			f.u.inference = tc.inference
			s := runFailingTurn(t, f)
			assertNoSecrets(t, s, tc.secrets...)
			if d := errorDetail(t, "state_change", s.stateChange, "data", "error_detail"); d != nil {
				t.Errorf("non-credential error got error_detail %v", d)
			}
		})
	}
}

// R16 at the cut boundaries: an upstream echo that a bounded read or a
// malformed-frame diagnostic cuts would split the credential, so whole-value
// redaction alone misses its prefix. A key shorter than the input minimum
// (stored by an older binary) is redacted all the same.
func TestRuntimeSession_CutUpstreamErrorsNeverLeakACredentialPrefix(t *testing.T) {
	long := "sk-" + "proj-PARTIAL-KEY-SECRET-" + strings.Repeat("K", 80)
	short := "K-7abcd"
	cases := []struct {
		name, key, secret string
		inference         func(http.ResponseWriter, upstreamCall, int)
	}{
		{"cut_http_body", long, "PARTIAL-KEY-SECRET", func(w http.ResponseWriter, c upstreamCall, _ int) {
			writeStatus(w, http.StatusBadRequest, strings.Repeat(".", 4050)+c.Bearer)
		}},
		{"short_stored_key", short, short, func(w http.ResponseWriter, c upstreamCall, _ int) {
			writeStatus(w, http.StatusBadRequest, c.Bearer)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRuntimeFixture(t)
			f.commit(t, f.store, "openai", apiKeyCred(tc.key))
			f.u.inference = tc.inference
			s := runFailingTurn(t, f)
			assertNoSecrets(t, s, tc.secret)
			if !strings.Contains(s.history, "400") {
				t.Errorf("the status code was lost: %s", s.history)
			}
		})
	}
}

func TestRuntime_AnthropicCutDiagnosticsNeverLeakAPrefix(t *testing.T) {
	key := "sk-" + "ant-api03-FRAME-KEY-SECRET-" + strings.Repeat("K", 80)
	cases := map[string]func(http.ResponseWriter, upstreamCall, int){
		"cut_http_body": func(w http.ResponseWriter, c upstreamCall, _ int) {
			writeStatus(w, http.StatusBadRequest, strings.Repeat(".", 4050)+c.APIKey)
		},
		"malformed_message_start": func(w http.ResponseWriter, c upstreamCall, _ int) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "event: message_start\ndata: %s%s\n\n", strings.Repeat(".", 40), c.APIKey)
		},
		"malformed_error_frame": func(w http.ResponseWriter, c upstreamCall, _ int) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "event: error\ndata: %s%s\n\n", strings.Repeat(".", 150), c.APIKey)
		},
	}
	for name, inference := range cases {
		t.Run(name, func(t *testing.T) {
			f := newRuntimeFixture(t)
			f.commit(t, f.store, "anthropic", apiKeyCred(key))
			f.u.inference = inference
			_, err := streamText(context.Background(), f.build(t, anthropicModel), anthropicModel)
			if err == nil {
				t.Fatal("the upstream failure did not fail the request")
			}
			if strings.Contains(err.Error(), "FRAME-KEY-SECRET") {
				t.Fatalf("a cut diagnostic leaked a credential prefix: %.200q", err.Error())
			}
		})
	}
}

// Redacting a short registered value first must not break the match of a
// longer one that contains it: "ey" is a synthetic legacy refresh token that
// also starts every JWT, here the access token issued by the rotation.
func TestRuntimeSession_ShortSecretCannotSplitALongerOne(t *testing.T) {
	f := newRuntimeFixture(t)
	first := openaiAccess("acct-a", "FIRST-ACCESS")
	next := openaiAccess("acct-a", "AFTER-ROTATION-ACCESS-SENTINEL")
	f.commit(t, f.store, "openai", oauthCred(first, "ey", "acct-a"))
	f.u.token = func(w http.ResponseWriter, _ upstreamCall, _ int) {
		writeTokens(w, next, "rotated-refresh-token")
	}
	f.u.inference = func(w http.ResponseWriter, c upstreamCall, n int) {
		if n == 0 {
			writeStatus(w, http.StatusUnauthorized, `{}`)
			return
		}
		writeStatus(w, http.StatusBadRequest, fmt.Sprintf(`{"error":{"message":%q}}`, c.Bearer))
	}
	assertNoSecrets(t, runFailingTurn(t, f), "AFTER-ROTATION-ACCESS-SENTINEL")
}

// A registered value that starts before a longer one and overlaps its
// beginning must not consume that start and leave the rest in the text.
func TestRuntimeSession_OverlappingSecretCannotSplitALongerOne(t *testing.T) {
	f := newRuntimeFixture(t)
	first := openaiAccess("acct-a", "FIRST-ACCESS")
	next := openaiAccess("acct-a", "STRADDLING-ACCESS-SENTINEL")
	const prefix = "reason:"
	refresh := prefix + next[:24]
	f.commit(t, f.store, "openai", oauthCred(first, refresh, "acct-a"))
	f.u.token = func(w http.ResponseWriter, _ upstreamCall, _ int) {
		writeTokens(w, next, "rotated-refresh-token")
	}
	f.u.inference = func(w http.ResponseWriter, c upstreamCall, n int) {
		if n == 0 {
			writeStatus(w, http.StatusUnauthorized, `{}`)
			return
		}
		writeStatus(w, http.StatusBadRequest, fmt.Sprintf(`{"error":{"message":%q}}`, prefix+c.Bearer))
	}
	assertNoSecrets(t, runFailingTurn(t, f), "STRADDLING-ACCESS-SENTINEL")
}
