package serve

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newPreviewFor(t *testing.T, upstream string) *PreviewProxy {
	t.Helper()
	p := NewPreviewProxy("https://node.ts.net:7492", 7392, 7492)
	if err := p.SetTarget(upstream, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func TestPreviewRewritesInjectsAndNamespacesCookies(t *testing.T) {
	var upURL string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization reached upstream: %q", got)
		}
		if got := r.Header.Get("Cookie"); got != "app=y" {
			t.Errorf("cookies upstream = %q", got)
		}
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Location", upURL+"/login")
		w.Header().Add("Set-Cookie", "app=x; Domain=tailnet.ts.net; SameSite=Lax")
		w.Header().Add("Set-Cookie", "moa_auth=nope")
		w.Header().Set("X-Frame-Options", "DENY")
		_, _ = io.WriteString(w, `<head></head><meta http-equiv="Content-Security-Policy" content="default-src 'none'"><a href="`+upURL+`/a">x</a>`)
	}))
	defer up.Close()
	upURL = up.URL
	p := newPreviewFor(t, up.URL)
	r := httptest.NewRequest("GET", "http://node.ts.net:7492/", nil)
	r.Header.Set("Authorization", "Bearer owner")
	r.Header.Set("Cookie", previewCookiePrefix(1)+"app=y; moa_auth=x; app=z")
	w := httptest.NewRecorder()
	p.Handler().ServeHTTP(w, r)
	if got := w.Header().Get("Location"); got != "https://node.ts.net:7492/login" {
		t.Fatalf("Location=%q", got)
	}
	if w.Header().Get("X-Frame-Options") != "" {
		t.Fatal("frame options survived")
	}
	cookies := strings.Join(w.Header().Values("Set-Cookie"), ";")
	if !strings.Contains(cookies, previewCookiePrefix(1)+"app=x") || !strings.Contains(cookies, "SameSite=Lax") || !strings.Contains(cookies, "Secure") || strings.Contains(cookies, "Domain=") || strings.Contains(cookies, "moa_auth") {
		t.Fatalf("cookies=%s", cookies)
	}
	body := w.Body.String()
	if !strings.Contains(body, "/__moa/inspector.js") || !strings.Contains(body, "https://node.ts.net:7492/a") {
		t.Fatalf("body %q", body)
	}
	if strings.Contains(strings.ToLower(body), "content-security-policy") {
		t.Fatalf("meta CSP survived: %q", body)
	}
}

func TestPreviewCompressionAndInjectionIdempotence(t *testing.T) {
	var upURL string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/zip" {
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("Content-Encoding", "gzip")
			z := gzip.NewWriter(w)
			_, _ = z.Write([]byte("<head>" + upURL))
			_ = z.Close()
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<head><script src="/__moa/inspector.js"></script>`)
	}))
	defer up.Close()
	upURL = up.URL
	p := newPreviewFor(t, up.URL)
	for _, path := range []string{"/", "/zip"} {
		w := httptest.NewRecorder()
		p.Handler().ServeHTTP(w, httptest.NewRequest("GET", "http://x"+path, nil))
		if path == "/" && strings.Count(w.Body.String(), "/__moa/inspector.js") != 1 {
			t.Fatalf("not idempotent: %q", w.Body.String())
		}
		if path == "/zip" {
			if got := w.Header().Get("Content-Encoding"); got != "gzip" {
				t.Fatalf("encoding=%q", got)
			}
			z, err := gzip.NewReader(bytes.NewReader(w.Body.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(z)
			_ = z.Close()
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != "<head>"+upURL {
				t.Fatalf("gzip body changed: %q", body)
			}
		}
	}
}

func TestPreviewLargeBodyPassesThroughIntact(t *testing.T) {
	body := bytes.Repeat([]byte("0123456789abcdef"), previewMaxBody/16+1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(body)
	}))
	defer up.Close()
	p := newPreviewFor(t, up.URL)
	w := httptest.NewRecorder()
	p.Handler().ServeHTTP(w, httptest.NewRequest("GET", "http://x/", nil))
	if len(w.Body.Bytes()) != len(body) || sha256.Sum256(w.Body.Bytes()) != sha256.Sum256(body) {
		t.Fatalf("large body was truncated or altered: got=%d want=%d", w.Body.Len(), len(body))
	}
}

func TestPreviewAllowlistPinsDNSAndBlocksBypasses(t *testing.T) {
	p := NewPreviewProxy("https://p", 7392, 7492)
	p.resolve = func(host string) ([]net.IP, error) {
		if ip := net.ParseIP(host); ip != nil {
			return []net.IP{ip}, nil
		}
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}
	for _, raw := range []string{"http://127.0.0.1:5173", "http://[::1]:5173"} {
		if err := p.SetTarget(raw, nil); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
	for _, raw := range []string{"http://127.0.0.1.nip.io:7392", "http://localhost.:7492", "http://127.0.0.1:07492", "http://[::ffff:127.0.0.1]:7392"} {
		if err := p.SetTarget(raw, nil); err == nil {
			t.Errorf("accepted loopback bypass %s", raw)
		}
	}
	for _, raw := range []string{"http://0.0.0.0:5173", "http://169.254.169.254", "http://[::ffff:127.0.0.1]:5173"} {
		if err := p.SetTarget(raw, nil); err == nil {
			t.Errorf("accepted forbidden IP %s", raw)
		}
	}
	// The dial plan contains the validated address. Changing resolution later
	// cannot redirect an already configured target to another host.
	var reached atomic.Bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true) }))
	defer up.Close()
	u, _ := url.Parse(up.URL)
	port, _ := strconv.Atoi(u.Port())
	p = NewPreviewProxy("https://p", 1, 2)
	p.resolve = func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("127.0.0.1")}, nil }
	if err := p.SetTarget("http://rebind.test:"+strconv.Itoa(port), nil); err != nil {
		t.Fatal(err)
	}
	p.resolve = func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("169.254.169.254")}, nil }
	w := httptest.NewRecorder()
	p.Handler().ServeHTTP(w, httptest.NewRequest("GET", "http://x/", nil))
	if !reached.Load() || w.Code != http.StatusOK {
		t.Fatalf("pinned target was not reached: code=%d", w.Code)
	}
	p.Close()
}

func TestPreviewRejectsLocalInterfaceOnMoaPort(t *testing.T) {
	p := NewPreviewProxy("https://preview.test", 7392, 7492)
	p.resolve = func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("10.10.10.4")}, nil }
	p.localIPs = func() ([]net.IP, error) { return []net.IP{net.ParseIP("10.10.10.4")}, nil }
	if err := p.SetTarget("http://10.10.10.4:7392", nil); err == nil {
		t.Fatal("accepted an address assigned to this machine on moa's port")
	}
}

func TestPreviewRejectsRedirectOutsideValidatedTarget(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://169.254.169.254/latest")
		w.WriteHeader(http.StatusFound)
	}))
	defer up.Close()
	p := newPreviewFor(t, up.URL)
	w := httptest.NewRecorder()
	p.Handler().ServeHTTP(w, httptest.NewRequest("GET", "http://x/", nil))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("redirect escaped target: %d", w.Code)
	}
}

func TestPreviewProtectedHandlerRequiresCapability(t *testing.T) {
	p := NewPreviewProxy("https://preview.test", 1, 2)
	protected := p.ProtectedHandler([]string{"preview.test"})
	denied := httptest.NewRecorder()
	protected.ServeHTTP(denied, httptest.NewRequest("GET", "https://preview.test/", nil))
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", denied.Code)
	}
	capabilityURL := p.PreviewURL()
	grant := httptest.NewRecorder()
	req := httptest.NewRequest("GET", capabilityURL, nil)
	req.Host = "preview.test"
	protected.ServeHTTP(grant, req)
	grantCookie := findCookie(grant.Result().Cookies(), previewAuthCookie)
	if grant.Code != http.StatusFound || grantCookie == nil || strings.Contains(grant.Header().Get("Location"), "preview_token") || grant.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("capability exchange failed: %d %q", grant.Code, grant.Header().Get("Location"))
	}
	replay := httptest.NewRecorder()
	req = httptest.NewRequest("GET", capabilityURL, nil)
	req.Host = "preview.test"
	protected.ServeHTTP(replay, req)
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("replayed capability = %d", replay.Code)
	}
	allowed := httptest.NewRecorder()
	req = httptest.NewRequest("GET", "https://preview.test/", nil)
	req.Host = "preview.test"
	req.AddCookie(grantCookie)
	protected.ServeHTTP(allowed, req)
	if allowed.Code == http.StatusUnauthorized {
		t.Fatal("capability cookie rejected")
	}
	clean := httptest.NewRecorder()
	req = httptest.NewRequest("GET", capabilityURL, nil)
	req.Host = "preview.test"
	req.AddCookie(grantCookie)
	protected.ServeHTTP(clean, req)
	if clean.Code != http.StatusFound || strings.Contains(clean.Header().Get("Location"), "preview_token") {
		t.Fatalf("authenticated bootstrap URL was not cleaned: %d %q", clean.Code, clean.Header().Get("Location"))
	}
}

func TestPreviewRotatesCapabilityWhenTargetChanges(t *testing.T) {
	p := NewPreviewProxy("https://preview.test", 7392, 7492)
	t.Cleanup(p.Close)
	if err := p.SetTarget("http://127.0.0.1:5173", nil); err != nil {
		t.Fatal(err)
	}
	oldURL := p.PreviewURL()
	protected := p.ProtectedHandler([]string{"preview.test"})
	grant := httptest.NewRecorder()
	request := httptest.NewRequest("GET", oldURL, nil)
	request.Host = "preview.test"
	protected.ServeHTTP(grant, request)
	oldCookie := findCookie(grant.Result().Cookies(), previewAuthCookie)
	if oldCookie == nil {
		t.Fatal("did not issue the original preview cookie")
	}
	if err := p.SetTarget("http://127.0.0.1:5174", nil); err != nil {
		t.Fatal(err)
	}
	newURL := p.PreviewURL()
	if newURL == oldURL {
		t.Fatal("target change did not create a new capability")
	}
	for _, request := range []*http.Request{
		httptest.NewRequest("GET", oldURL, nil),
		httptest.NewRequest("GET", "https://preview.test/", nil),
	} {
		request.Host = "preview.test"
		if request.URL.String() == "https://preview.test/" {
			request.AddCookie(oldCookie)
		}
		response := httptest.NewRecorder()
		protected.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("old target credential accepted: %d", response.Code)
		}
	}
	grant = httptest.NewRecorder()
	request = httptest.NewRequest("GET", newURL, nil)
	request.Host = "preview.test"
	protected.ServeHTTP(grant, request)
	if grant.Code != http.StatusFound {
		t.Fatalf("new capability = %d", grant.Code)
	}
}

func TestPreviewRejectsLateDialFromPreviousGeneration(t *testing.T) {
	p := NewPreviewProxy("https://preview.test", 7392, 7492)
	t.Cleanup(p.Close)
	if err := p.SetTarget("http://127.0.0.1:5173", nil); err != nil {
		t.Fatal(err)
	}
	old := p.targetSnapshot()
	client, peer := net.Pipe()
	defer peer.Close()
	p.dial = func(context.Context, string, string) (net.Conn, error) { return client, nil }
	if err := p.SetTarget("http://127.0.0.1:5174", nil); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), previewDialPlanKey{}, previewDialPlan{ips: old.ips, port: old.port, gen: old.generation})
	if conn, err := p.dialContext(ctx, "tcp", "ignored"); err == nil || conn != nil {
		t.Fatalf("late old-generation dial survived: conn=%v err=%v", conn, err)
	}
	_ = peer.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := peer.Write([]byte("x")); err == nil {
		t.Fatal("late dial connection remained open")
	}
}

type previewDeadlineWriter struct {
	header   http.Header
	deadline time.Time
}

func (w *previewDeadlineWriter) Header() http.Header         { return w.header }
func (w *previewDeadlineWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *previewDeadlineWriter) WriteHeader(int)             {}
func (w *previewDeadlineWriter) SetReadDeadline(t time.Time) error {
	w.deadline = t
	return nil
}

func TestBodyTimeoutRejectsFalseWebSocketUpgrade(t *testing.T) {
	handler := bodyTimeoutMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	falseUpgrade := httptest.NewRequest(http.MethodPost, "http://preview.test/", nil)
	falseUpgrade.Header.Set("Upgrade", "websocket")
	falseWriter := &previewDeadlineWriter{header: make(http.Header)}
	handler.ServeHTTP(falseWriter, falseUpgrade)
	if falseWriter.deadline.IsZero() {
		t.Fatal("false websocket upgrade bypassed the body deadline")
	}

	websocket := httptest.NewRequest(http.MethodGet, "http://preview.test/", nil)
	websocket.Header.Set("Upgrade", "websocket")
	websocket.Header.Set("Connection", "keep-alive, Upgrade")
	websocket.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	websocket.Header.Set("Sec-WebSocket-Version", "13")
	websocketWriter := &previewDeadlineWriter{header: make(http.Header)}
	handler.ServeHTTP(websocketWriter, websocket)
	if !websocketWriter.deadline.IsZero() {
		t.Fatal("valid websocket upgrade received a body deadline")
	}
}

func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func TestPreviewReusesUpstreamConnections(t *testing.T) {
	var connections atomic.Int32
	up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	up.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	up.Start()
	defer up.Close()
	p := newPreviewFor(t, up.URL)
	for i := 0; i < 40; i++ {
		w := httptest.NewRecorder()
		p.Handler().ServeHTTP(w, httptest.NewRequest("GET", "http://x/", nil))
		if w.Code != http.StatusOK {
			t.Fatal(w.Code)
		}
	}
	if got := connections.Load(); got > 2 {
		t.Fatalf("connections grew per request: %d", got)
	}
}
