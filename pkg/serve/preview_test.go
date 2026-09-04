package serve

import (
	"bytes"
	"compress/gzip"
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
	grant := httptest.NewRecorder()
	req := httptest.NewRequest("GET", p.PreviewURL(), nil)
	req.Host = "preview.test"
	protected.ServeHTTP(grant, req)
	grantCookie := findCookie(grant.Result().Cookies(), previewAuthCookie)
	if grant.Code != http.StatusFound || grantCookie == nil || strings.Contains(grant.Header().Get("Location"), "preview_token") {
		t.Fatalf("capability exchange failed: %d %q", grant.Code, grant.Header().Get("Location"))
	}
	allowed := httptest.NewRecorder()
	req = httptest.NewRequest("GET", "https://preview.test/", nil)
	req.Host = "preview.test"
	req.AddCookie(grantCookie)
	protected.ServeHTTP(allowed, req)
	if allowed.Code == http.StatusUnauthorized {
		t.Fatal("capability cookie rejected")
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
