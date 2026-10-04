package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

const (
	browserA = "browser-binding-A-0123456789abcdef"
	browserB = "browser-binding-B-0123456789abcdef"
)

// fakeClock is a manually advanced clock shared by the manager and the xAI
// device flow.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Now().UTC().Truncate(time.Second)} }
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// clearProviderEnv makes the environment not select any provider, whatever
// the developer's shell exports.
func clearProviderEnv(t *testing.T) {
	t.Helper()
	for _, p := range []string{"anthropic", "openai", "xai"} {
		t.Setenv(envKeyForProvider(p), "")
	}
}

type loginFixture struct {
	path  string
	store *Store
	ts    *codeTokenServer
	m     *ProviderLoginManager
	clock *fakeClock
}

func newLoginFixture(t *testing.T) *loginFixture {
	t.Helper()
	clearProviderEnv(t)
	f := &loginFixture{path: filepath.Join(t.TempDir(), "auth.json"), clock: newFakeClock()}
	seedAuthFile(t, f.path, map[string]Credential{
		"anthropic": {Type: "api_key", Key: "sk-" + "ant-api-OLD", Generation: "00000000000000000000000000000001"},
		"openai":    {Type: "api_key", Key: "sk-" + "OLD", Generation: "00000000000000000000000000000002"},
	})
	f.store = NewStore(f.path)
	f.ts = newCodeTokenServer(t)
	f.ts.account = "acct-new"
	f.m = NewProviderLoginManager(context.Background(), f.store)
	f.m.endpoints = f.ts.endpoints()
	f.m.now = f.clock.Now
	t.Cleanup(f.m.Close)
	return f
}

func (f *loginFixture) gen(t *testing.T, provider string) string {
	t.Helper()
	g, err := f.store.StoredGeneration(provider)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func (f *loginFixture) begin(t *testing.T, provider, binding string) LoginAttemptView {
	t.Helper()
	v, err := f.m.Begin(context.Background(), provider, binding, f.gen(t, provider))
	if err != nil {
		t.Fatalf("Begin(%s): %v", provider, err)
	}
	return v
}

func pasteFor(t *testing.T, v LoginAttemptView) string {
	t.Helper()
	state := attemptState(t, v.AuthorizeURL)
	if v.Provider == "anthropic" {
		return anthropicPaste(state)
	}
	return openaiPaste(state)
}

// loginClass returns the safe class of a manager error.
func loginClass(err error) string {
	var le *LoginError
	if errors.As(err, &le) {
		return le.Class
	}
	if pe, ok := core.AsProviderCredentialError(err); ok {
		return pe.Class
	}
	return ""
}

func wantLoginClass(t *testing.T, err error, class string) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, want class %s", class)
	}
	if got := loginClass(err); got != class {
		t.Fatalf("class = %q (%v), want %s", got, err, class)
	}
	noEcho(t, err)
}

func TestLoginManager_CodeFlowsSaveWithNewGeneration(t *testing.T) {
	for _, provider := range []string{"anthropic", "openai"} {
		t.Run(provider, func(t *testing.T) {
			f := newLoginFixture(t)
			before := f.gen(t, provider)
			v := f.begin(t, provider, browserA)
			wantFlow := map[string]string{"anthropic": "paste_code_state", "openai": "paste_url"}[provider]
			if v.Flow != wantFlow || v.Provider != provider || len(v.AttemptID) < 43 {
				t.Fatalf("view = %+v", v)
			}
			if !v.ExpiresAt.Equal(f.clock.Now().Add(15 * time.Minute)) {
				t.Fatalf("expires_at = %v, want now+15m", v.ExpiresAt)
			}
			if err := f.m.Complete(context.Background(), provider, v.AttemptID, browserA, pasteFor(t, v)); err != nil {
				t.Fatalf("Complete: %v", err)
			}
			snap, err := f.store.PeekSnapshot(provider)
			if err != nil || snap.Kind != "oauth" || snap.Credential.Refresh != refreshSentinel {
				t.Fatalf("saved snapshot %+v, %v", snap, err)
			}
			if snap.Generation == "" || snap.Generation == before {
				t.Fatalf("generation %q not renewed from %q", snap.Generation, before)
			}
			if provider == "openai" && snap.AccountID != "acct-new" {
				t.Fatalf("account = %q", snap.AccountID)
			}
			p, err := f.m.Progress(provider, v.AttemptID, browserA)
			if err != nil || p.State != "saved" {
				t.Fatalf("progress %+v, %v", p, err)
			}
			// The verifier the server received never reached the browser.
			verifier := f.ts.lastRequest()["code_verifier"]
			out, _ := json.Marshal(struct {
				V LoginAttemptView
				P LoginProgress
			}{v, p})
			if verifier == "" || strings.Contains(string(out), verifier) {
				t.Fatalf("verifier %q exposed in %s", verifier, out)
			}
			for _, secret := range []string{codeSentinel, refreshSentinel, snap.Token} {
				if strings.Contains(string(out), secret) {
					t.Fatalf("secret %q in DTOs", secret)
				}
			}
		})
	}
}

// R04: an attempt is bound to provider, attempt id and browser. A mismatch
// consumes nothing and makes no token request; the right paste still works.
func TestLoginManager_BindingMismatchConsumesNothing(t *testing.T) {
	f := newLoginFixture(t)
	authBefore := fileBytes(t, f.path)
	v := f.begin(t, "anthropic", browserA)
	ov := f.begin(t, "openai", browserA)
	paste := pasteFor(t, v)

	checks := []struct {
		name                      string
		provider, id, binding, in string
	}{
		{"other browser", "anthropic", v.AttemptID, browserB, paste},
		{"empty browser", "anthropic", v.AttemptID, "", paste},
		{"other provider", "openai", v.AttemptID, browserA, openaiPaste(attemptState(t, v.AuthorizeURL))},
		{"other attempt id", "anthropic", ov.AttemptID, browserA, paste},
		{"unknown attempt id", "anthropic", strings.Repeat("A", 43), browserA, paste},
		{"wrong state", "anthropic", v.AttemptID, browserA, codeSentinel + "#wrong-state"},
		{"invalid paste", "anthropic", v.AttemptID, browserA, codeSentinel},
		{"xai has no paste", "xai", v.AttemptID, browserA, paste},
	}
	for _, c := range checks {
		err := f.m.Complete(context.Background(), c.provider, c.id, c.binding, c.in)
		if err == nil {
			t.Fatalf("%s: accepted", c.name)
		}
		noEcho(t, err, "wrong-state")
		if n := f.ts.callCount(); n != 0 {
			t.Fatalf("%s: %d token requests", c.name, n)
		}
	}
	if _, err := f.m.Progress("anthropic", v.AttemptID, browserB); err == nil {
		t.Fatal("progress readable from another browser")
	}
	if err := f.m.Cancel("anthropic", v.AttemptID, browserB); err == nil {
		t.Fatal("cancel accepted from another browser")
	}
	if !bytes.Equal(fileBytes(t, f.path), authBefore) {
		t.Fatal("auth.json changed")
	}
	if err := f.m.Complete(context.Background(), "anthropic", v.AttemptID, browserA, paste); err != nil {
		t.Fatalf("matching completion after mismatches: %v", err)
	}
	if n := f.ts.callCount(); n != 1 {
		t.Fatalf("token requests = %d, want 1", n)
	}
}

// R04: two simultaneous completions of the same attempt exchange the code
// exactly once.
func TestLoginManager_ConcurrentCompleteExchangesOnce(t *testing.T) {
	f := newLoginFixture(t)
	f.ts.gate = make(chan struct{})
	v := f.begin(t, "openai", browserA)
	paste := pasteFor(t, v)
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { results <- f.m.Complete(context.Background(), "openai", v.AttemptID, browserA, paste) }()
	}
	<-f.ts.started
	var errs []error
	select {
	case <-f.ts.started:
		close(f.ts.gate)
		t.Fatal("second completion reached the token endpoint")
	case err := <-results:
		errs = append(errs, err)
	case <-time.After(5 * time.Second):
		close(f.ts.gate)
		t.Fatal("second completion neither refused nor exchanged")
	}
	close(f.ts.gate)
	errs = append(errs, <-results)
	if errs[0] == nil {
		t.Fatal("the refused completion reported success")
	}
	wantLoginClass(t, errs[0], "not_found")
	if errs[1] != nil {
		t.Fatalf("claimed completion failed: %v", errs[1])
	}
	if n := f.ts.callCount(); n != 1 {
		t.Fatalf("token requests = %d", n)
	}
	// Used once: a replay of the same paste is refused without a request.
	wantLoginClass(t, f.m.Complete(context.Background(), "openai", v.AttemptID, browserA, paste), "not_found")
	if n := f.ts.callCount(); n != 1 {
		t.Fatalf("replay made a token request (%d)", n)
	}
}

// R04: a new Begin for the same provider replaces the previous attempt.
func TestLoginManager_NewBeginSupersedes(t *testing.T) {
	f := newLoginFixture(t)
	old := f.begin(t, "anthropic", browserA)
	cur := f.begin(t, "anthropic", browserA)
	wantLoginClass(t, f.m.Complete(context.Background(), "anthropic", old.AttemptID, browserA, pasteFor(t, old)), "not_found")
	if n := f.ts.callCount(); n != 0 {
		t.Fatalf("superseded attempt exchanged (%d)", n)
	}
	if p, err := f.m.Progress("anthropic", old.AttemptID, browserA); err == nil && p.State != "superseded" {
		t.Fatalf("old progress = %+v", p)
	}
	if err := f.m.Complete(context.Background(), "anthropic", cur.AttemptID, browserA, pasteFor(t, cur)); err != nil {
		t.Fatal(err)
	}
}

// R04: cancel, a new Begin or Close while the exchange is in flight leave the
// previous credential untouched.
func TestLoginManager_InterruptedExchangeDoesNotCommit(t *testing.T) {
	for _, how := range []string{"cancel", "new begin", "close"} {
		t.Run(how, func(t *testing.T) {
			f := newLoginFixture(t)
			f.ts.gate = make(chan struct{})
			// The token response still arrives after the interruption, so the
			// commit guard (not just request cancellation) is what is tested.
			f.m.endpoints.client = &http.Client{Transport: ctxIgnoringTransport{f.ts.Client().Transport}}
			v := f.begin(t, "anthropic", browserA)
			before := fileBytes(t, f.path)
			done := make(chan error, 1)
			go func() {
				done <- f.m.Complete(context.Background(), "anthropic", v.AttemptID, browserA, pasteFor(t, v))
			}()
			<-f.ts.started
			switch how {
			case "cancel":
				if err := f.m.Cancel("anthropic", v.AttemptID, browserA); err != nil {
					t.Fatal(err)
				}
			case "new begin":
				f.begin(t, "anthropic", browserA)
			case "close":
				f.m.Close()
			}
			close(f.ts.gate)
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("interrupted completion reported success")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("completion did not return")
			}
			if !bytes.Equal(fileBytes(t, f.path), before) {
				t.Fatal("interrupted attempt changed auth.json")
			}
		})
	}
}

// R04: attempts expire 15 minutes after Begin; an expired one exchanges
// nothing.
func TestLoginManager_ExpiredAttemptExchangesNothing(t *testing.T) {
	f := newLoginFixture(t)
	v := f.begin(t, "openai", browserA)
	f.clock.Advance(15*time.Minute + time.Second)
	wantLoginClass(t, f.m.Complete(context.Background(), "openai", v.AttemptID, browserA, pasteFor(t, v)), "expired")
	if n := f.ts.callCount(); n != 0 {
		t.Fatalf("expired attempt exchanged (%d)", n)
	}
	if p, err := f.m.Progress("openai", v.AttemptID, browserA); err != nil || p.State != "expired" {
		t.Fatalf("progress %+v, %v", p, err)
	}
}

// R04: a denial in the pasted callback consumes the attempt without a token
// request.
func TestLoginManager_DenialConsumesWithoutExchange(t *testing.T) {
	f := newLoginFixture(t)
	v := f.begin(t, "openai", browserA)
	state := attemptState(t, v.AuthorizeURL)
	denied := openaiCallback + "?error=access_denied&error_description=" + bodySentinel + "&state=" + state
	wantLoginClass(t, f.m.Complete(context.Background(), "openai", v.AttemptID, browserA, denied), "denied")
	if p, err := f.m.Progress("openai", v.AttemptID, browserA); err != nil || p.State != "denied" {
		t.Fatalf("progress %+v, %v", p, err)
	}
	wantLoginClass(t, f.m.Complete(context.Background(), "openai", v.AttemptID, browserA, pasteFor(t, v)), "not_found")
	if n := f.ts.callCount(); n != 0 {
		t.Fatalf("token requests = %d", n)
	}
}

// R04/R11: the commit is guarded by the generation seen at Begin: a key or
// login saved meanwhile is not overwritten by an older attempt.
func TestLoginManager_StaleAttemptCannotOverwriteNewerLogin(t *testing.T) {
	f := newLoginFixture(t)
	if _, err := f.m.Begin(context.Background(), "anthropic", browserA, "stale-page-generation"); loginClass(err) != core.CredentialChanged {
		t.Fatalf("Begin from a stale page: %v", err)
	}
	v := f.begin(t, "anthropic", browserA)
	newer, err := f.store.CommitLogin("anthropic", f.gen(t, "anthropic"), Credential{Type: "api_key", Key: "sk-" + "ant-api-NEWER"})
	if err != nil {
		t.Fatal(err)
	}
	wantLoginClass(t, f.m.Complete(context.Background(), "anthropic", v.AttemptID, browserA, pasteFor(t, v)), core.CredentialChanged)
	got := readAuthFile(t, f.path)["anthropic"]
	if got.Key != "sk-"+"ant-api-NEWER" || got.Generation != newer {
		t.Fatalf("newer login overwritten: %+v", got)
	}
	if p, err := f.m.Progress("anthropic", v.AttemptID, browserA); err != nil || p.State != "superseded" {
		t.Fatalf("progress %+v, %v", p, err)
	}
}

// R14/R04: environment-managed providers cannot be signed in from here, and
// an environment credential appearing mid-attempt blocks the commit.
func TestLoginManager_EnvironmentManagedRefused(t *testing.T) {
	f := newLoginFixture(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-"+"ant-api-ENV")
	if _, err := f.m.Begin(context.Background(), "anthropic", browserA, f.gen(t, "anthropic")); loginClass(err) != "env_managed" {
		t.Fatalf("Begin with env: %v", err)
	}
	if _, err := f.m.SaveAPIKey("anthropic", f.gen(t, "anthropic"), "sk-"+"ant-api-NEW"); loginClass(err) != "env_managed" {
		t.Fatalf("SaveAPIKey with env: %v", err)
	}
	v := f.begin(t, "openai", browserA)
	before := fileBytes(t, f.path)
	t.Setenv("OPENAI_API_KEY", "sk-"+"ENV")
	wantLoginClass(t, f.m.Complete(context.Background(), "openai", v.AttemptID, browserA, pasteFor(t, v)), "env_managed")
	if !bytes.Equal(fileBytes(t, f.path), before) {
		t.Fatal("auth.json changed under an environment credential")
	}
}

// R04: attempts are memory-only. After a restart the old attempt id is
// unusable and the previous credential is intact.
func TestLoginManager_RestartLosesOnlyTheAttempt(t *testing.T) {
	f := newLoginFixture(t)
	v := f.begin(t, "anthropic", browserA)
	before := fileBytes(t, f.path)
	f.m.Close()
	m2 := NewProviderLoginManager(context.Background(), NewStore(f.path))
	m2.endpoints = f.ts.endpoints()
	defer m2.Close()
	wantLoginClass(t, m2.Complete(context.Background(), "anthropic", v.AttemptID, browserA, pasteFor(t, v)), "not_found")
	if n := f.ts.callCount(); n != 0 {
		t.Fatalf("token requests = %d", n)
	}
	if !bytes.Equal(fileBytes(t, f.path), before) {
		t.Fatal("previous credential changed")
	}
	if _, err := f.m.Begin(context.Background(), "anthropic", browserA, f.gen(t, "anthropic")); err == nil {
		t.Fatal("closed manager accepted Begin")
	}
}

func TestLoginManager_BeginValidatesInput(t *testing.T) {
	f := newLoginFixture(t)
	for _, p := range []string{"meta", "openai-transcribe", "", "ANTHROPIC"} {
		if _, err := f.m.Begin(context.Background(), p, browserA, ""); loginClass(err) != "unsupported" {
			t.Fatalf("Begin(%q): %v", p, err)
		}
	}
	if _, err := f.m.Begin(context.Background(), "anthropic", "", f.gen(t, "anthropic")); err == nil {
		t.Fatal("Begin without a browser binding")
	}
}

// API keys are write-only: validated with fixed copy, never echoed, committed
// under the generation guard as type api_key.
func TestLoginManager_SaveAPIKey(t *testing.T) {
	f := newLoginFixture(t)
	gen, err := f.m.SaveAPIKey("openai", f.gen(t, "openai"), "  sk-"+"proj-NEWKEY-0123456789 \n")
	if err != nil {
		t.Fatal(err)
	}
	got := readAuthFile(t, f.path)["openai"]
	if got.Type != "api_key" || got.Key != "sk-"+"proj-NEWKEY-0123456789" || got.Generation != gen || gen == "" {
		t.Fatalf("saved %+v gen %q", got, gen)
	}
	before := fileBytes(t, f.path)
	bad := []struct {
		provider, key, class string
	}{
		{"openai", "", "invalid_key"},
		{"openai", "   ", "invalid_key"},
		{"openai", "sk-" + "short-key-123", "invalid_key"},
		{"xai", "xai-" + "fragment", "invalid_key"},
		{"openai", "sk-" + strings.Repeat("k", 8<<10), "invalid_key"},
		{"openai", "sk-" + "abc\x00" + codeSentinel, "invalid_key"},
		{"openai", "sk-" + "abc\x1b[31m" + codeSentinel, "invalid_key"},
		{"openai", "sk-" + "abc " + codeSentinel, "invalid_key"},
		{"anthropic", "sk-" + "ant-oat01-" + codeSentinel, "not_api_key"},
		{"meta", "key-" + codeSentinel, "unsupported"},
		{"openai-transcribe", "sk-" + codeSentinel, "unsupported"},
	}
	for _, b := range bad {
		_, err := f.m.SaveAPIKey(b.provider, f.gen(t, b.provider), b.key)
		wantLoginClass(t, err, b.class)
	}
	_, err = f.m.SaveAPIKey("openai", "stale-page-generation", "sk-"+"proj-OTHER-0123456789")
	wantLoginClass(t, err, core.CredentialChanged)
	if !bytes.Equal(fileBytes(t, f.path), before) {
		t.Fatal("rejected keys changed auth.json")
	}
}

// --- xAI device flow under the manager (R07) ---

type xaiDeviceServer struct {
	*httptest.Server
	mu sync.Mutex
	// script answers successive token polls per device code; the last entry
	// repeats.
	script  map[string][]string
	polls   map[string]int
	issued  int
	started chan string
	gates   map[string]chan struct{}
	expires int
}

func newXAIDeviceServer(t *testing.T) *xaiDeviceServer {
	t.Helper()
	s := &xaiDeviceServer{script: map[string][]string{}, polls: map[string]int{}, started: make(chan string, 64), gates: map[string]chan struct{}{}, expires: 600}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/discovery":
			_, _ = w.Write([]byte(discovery(r, serverURL(r)+"/token", serverURL(r)+"/device")))
		case "/device":
			s.mu.Lock()
			s.issued++
			code := fmt.Sprintf("DEVICE-CODE-SENTINEL-%d", s.issued)
			expires := s.expires
			s.mu.Unlock()
			_, _ = fmt.Fprintf(w, `{"device_code":%q,"user_code":"USER-%d","verification_uri":"https://accounts.x.ai/device","verification_uri_complete":"https://accounts.x.ai/device?user_code=USER-%d","expires_in":%d,"interval":5}`, code, s.issued, s.issued, expires)
		case "/token":
			_ = r.ParseForm()
			code := r.Form.Get("device_code")
			s.mu.Lock()
			n := s.polls[code]
			s.polls[code]++
			script := s.script[code]
			gate := s.gates[code]
			s.mu.Unlock()
			s.started <- code
			if gate != nil {
				<-gate
			}
			answer := "authorization_pending"
			if len(script) > 0 {
				answer = script[min(n, len(script)-1)]
			}
			if answer == "success" {
				_, _ = fmt.Fprintf(w, `{"access_token":"XAI-ACCESS-%s","refresh_token":%q,"expires_in":3600}`, code, refreshSentinel)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, `{"error":%q}`, answer)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *xaiDeviceServer) pollCount(code string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.polls[code]
}

// ctxIgnoringTransport completes requests even after their context is
// canceled, to model a provider response that arrives after cancel/replace.
type ctxIgnoringTransport struct{ base http.RoundTripper }

func (c ctxIgnoringTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return c.base.RoundTrip(r.WithContext(context.Background()))
}

type xaiFixture struct {
	*loginFixture
	srv     *xaiDeviceServer
	waits   chan time.Duration
	step    chan struct{}
	settled chan string
}

// newXAIFixture drives polling through the Wait seam: each wait reports its
// interval on waits, advances the fake clock by it, and returns only when
// the test feeds step (or the poll context ends).
func newXAIFixture(t *testing.T, transport func(http.RoundTripper) http.RoundTripper) *xaiFixture {
	t.Helper()
	f := &xaiFixture{loginFixture: newLoginFixture(t), srv: newXAIDeviceServer(t),
		waits: make(chan time.Duration, 64), step: make(chan struct{}), settled: make(chan string, 16)}
	client := f.srv.Client()
	if transport != nil {
		client = &http.Client{Transport: transport(client.Transport)}
	}
	ep := f.ts.endpoints()
	ep.client = client
	ep.xai = testXAIEndpoints(f.srv.Server)
	ep.xai.Now = f.clock.Now
	ep.xai.Wait = func(ctx context.Context, d time.Duration) error {
		f.waits <- d
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-f.step:
			f.clock.Advance(d)
			return nil
		}
	}
	f.m.endpoints = ep
	f.m.onSettled = func(provider, state string) { f.settled <- provider + ":" + state }
	return f
}

func (f *xaiFixture) waitSettled(t *testing.T, want string) {
	t.Helper()
	select {
	case got := <-f.settled:
		if got != want {
			t.Fatalf("settled %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("not settled, want %q", want)
	}
}

func (f *xaiFixture) release(t *testing.T) {
	t.Helper()
	select {
	case f.step <- struct{}{}:
	case <-time.After(5 * time.Second):
		t.Fatal("poller stopped waiting")
	}
}

func (f *xaiFixture) nextWait(t *testing.T) time.Duration {
	t.Helper()
	select {
	case d := <-f.waits:
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("poller is not waiting")
		return 0
	}
}

// R07: polling runs on the manager lifetime, not the Begin request; it honors
// the interval and slow_down, and the saved login never exposes device_code.
func TestLoginManager_XAIDeviceSurvivesRequest(t *testing.T) {
	f := newXAIFixture(t, nil)
	f.srv.script["DEVICE-CODE-SENTINEL-1"] = []string{"authorization_pending", "slow_down", "success"}
	reqCtx, cancelReq := context.WithCancel(context.Background())
	v, err := f.m.Begin(reqCtx, "xai", browserA, f.gen(t, "xai"))
	cancelReq() // the HTTP request that started it is gone
	if err != nil {
		t.Fatal(err)
	}
	if v.Flow != "device" || v.UserCode != "USER-1" || v.VerificationURI != "https://accounts.x.ai/device" || v.State != "waiting" {
		t.Fatalf("view %+v", v)
	}
	var intervals []time.Duration
	for i := 0; i < 3; i++ {
		intervals = append(intervals, f.nextWait(t))
		f.release(t)
	}
	f.waitSettled(t, "xai:saved")
	if want := []time.Duration{5 * time.Second, 5 * time.Second, 10 * time.Second}; fmt.Sprint(intervals) != fmt.Sprint(want) {
		t.Fatalf("intervals %v, want %v", intervals, want)
	}
	snap, err := f.store.PeekSnapshot("xai")
	if err != nil || snap.Kind != "oauth" || snap.Token != "XAI-ACCESS-DEVICE-CODE-SENTINEL-1" || snap.Generation == "" {
		t.Fatalf("snapshot %+v, %v", snap, err)
	}
	p, err := f.m.Progress("xai", v.AttemptID, browserA)
	if err != nil || p.State != "saved" {
		t.Fatalf("progress %+v %v", p, err)
	}
	out, _ := json.Marshal([]any{v, p})
	for _, s := range []string{"DEVICE-CODE-SENTINEL", refreshSentinel, "XAI-ACCESS"} {
		if strings.Contains(string(out), s) {
			t.Fatalf("%q in DTOs: %s", s, out)
		}
	}
	if err := f.m.Complete(context.Background(), "xai", v.AttemptID, browserA, "x"); err == nil {
		t.Fatal("xAI accepted a pasted completion")
	}
}

// R07: cancel, a new Begin and Close stop polling, and a provider answer that
// arrives afterwards cannot save.
func TestLoginManager_XAIStopsAndLateResultCannotSave(t *testing.T) {
	for _, how := range []string{"cancel", "new begin", "close"} {
		t.Run(how, func(t *testing.T) {
			f := newXAIFixture(t, func(rt http.RoundTripper) http.RoundTripper { return ctxIgnoringTransport{rt} })
			first := "DEVICE-CODE-SENTINEL-1"
			f.srv.script[first] = []string{"success"}
			gate := make(chan struct{})
			f.srv.gates[first] = gate
			v, err := f.m.Begin(context.Background(), "xai", browserA, f.gen(t, "xai"))
			if err != nil {
				t.Fatal(err)
			}
			before := fileBytes(t, f.path)
			f.nextWait(t)
			f.release(t)
			<-f.srv.started // the poll is at the provider
			switch how {
			case "cancel":
				if err := f.m.Cancel("xai", v.AttemptID, browserA); err != nil {
					t.Fatal(err)
				}
			case "new begin":
				if _, err := f.m.Begin(context.Background(), "xai", browserA, f.gen(t, "xai")); err != nil {
					t.Fatal(err)
				}
			case "close":
				go f.m.Close()
			}
			close(gate) // the provider now approves the old attempt
			want := map[string]string{"cancel": "xai:canceled", "new begin": "xai:superseded", "close": "xai:canceled"}[how]
			f.waitSettled(t, want)
			f.m.Close() // waits for the old poller to finish with its late result
			if !bytes.Equal(fileBytes(t, f.path), before) {
				t.Fatal("a late device result was saved")
			}
			if n := f.srv.pollCount(first); n != 1 {
				t.Fatalf("old device code polled %d times", n)
			}
		})
	}
}

// R07: the attempt deadline is the provider's device expiry capped at 15
// minutes from Begin, and polling stops there without saving.
func TestLoginManager_XAIDeadlineCapped(t *testing.T) {
	f := newXAIFixture(t, nil)
	f.srv.expires = 1800
	v, err := f.m.Begin(context.Background(), "xai", browserA, f.gen(t, "xai"))
	if err != nil {
		t.Fatal(err)
	}
	if want := f.clock.Now().Add(15 * time.Minute); !v.ExpiresAt.Equal(want) {
		t.Fatalf("expires_at %v, want %v", v.ExpiresAt, want)
	}
	before := fileBytes(t, f.path)
	go func() {
		for {
			select {
			case <-f.waits:
				f.clock.Advance(time.Minute) // plus the interval on step
				select {
				case f.step <- struct{}{}:
				case <-time.After(5 * time.Second):
					return
				}
			case <-time.After(5 * time.Second):
				return
			}
		}
	}()
	f.waitSettled(t, "xai:expired")
	if p, err := f.m.Progress("xai", v.AttemptID, browserA); err != nil || p.State != "expired" {
		t.Fatalf("progress %+v, %v", p, err)
	}
	if !bytes.Equal(fileBytes(t, f.path), before) {
		t.Fatal("expired attempt changed auth.json")
	}
}

func TestLoginManager_XAIDeniedStops(t *testing.T) {
	f := newXAIFixture(t, nil)
	f.srv.script["DEVICE-CODE-SENTINEL-1"] = []string{"access_denied"}
	v, err := f.m.Begin(context.Background(), "xai", browserA, f.gen(t, "xai"))
	if err != nil {
		t.Fatal(err)
	}
	f.nextWait(t)
	f.release(t)
	f.waitSettled(t, "xai:denied")
	if p, err := f.m.Progress("xai", v.AttemptID, browserA); err != nil || p.State != "denied" {
		t.Fatalf("progress %+v, %v", p, err)
	}
}
